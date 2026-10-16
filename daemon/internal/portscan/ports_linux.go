//go:build linux

package portscan

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func init() {
	selfPID = os.Getpid()
}

// rawEntry 是平台层枚举出的一条原始记录，交给 portscan.go 补全进程信息。
//
// inode 只在 Linux 上有意义：/proc/net/* 不提供 PID，只能靠 socket inode 反查。
// 反查在 enumerate 内部完成，返回时 inode 已可丢弃（保留是为了便于测试断言）。
type rawEntry struct {
	Protocol  string
	LocalAddr string
	LocalPort int
	PID       int
	inode     string
}

// TCP 状态码：/proc/net/tcp 的 st 字段是十六进制。
// 0A = TCP_LISTEN，这是唯一算「端口被占用」的状态。
const procTCPStateListen = "0A"

// procNetFiles 列出了要读取的四个表。
//
// 顺序刻意是「IPv4 在前」：结果按端口号排序时，同一端口若同时有 IPv4 与 IPv6
// 监听，IPv4 那条会先出现，符合用户直觉。
var procNetFiles = []struct {
	path     string
	protocol string
}{
	{"/proc/net/tcp", ProtocolTCP},
	{"/proc/net/tcp6", ProtocolTCP},
	{"/proc/net/udp", ProtocolUDP},
	{"/proc/net/udp6", ProtocolUDP},
}

// enumerate 读取 /proc/net/{tcp,tcp6,udp,udp6} 并反查 PID。
//
// 为什么不用 `ss -tulpnH`：/proc 是内核直接暴露的结构化数据，不需要起子进程，
// 也不依赖 iproute2 是否安装；`ss` 的输出还要按列宽解析，进程名带空格时会错位。
// 只有在 /proc 不可读（极少见，如某些容器）时才回退到 `ss`。
func enumerate() ([]rawEntry, error) {
	var entries []rawEntry
	var firstErr error

	for _, f := range procNetFiles {
		raw, err := os.ReadFile(f.path)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		entries = append(entries, parseProcNet(string(raw), f.protocol, f.path)...)
	}

	if len(entries) > 0 {
		resolvePIDs(entries)
		return dedupe(entries), nil
	}

	// /proc 一条都读不到，尝试回退
	if fallback, err := enumerateViaSS(); err == nil && len(fallback) > 0 {
		return dedupe(fallback), nil
	}
	if firstErr != nil {
		return nil, fmt.Errorf("读取 /proc/net 失败: %w", firstErr)
	}
	return nil, nil
}

// resolvePIDs 用 socket inode 反查每条记录所属的 PID，就地写回。
//
// 一次扫描可能涉及几百个 inode，因此先收集去重再统一做一次 /proc 遍历，
// 而不是每条记录各扫一遍。
func resolvePIDs(entries []rawEntry) {
	want := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.inode != "" && e.inode != "0" {
			want[e.inode] = true
		}
	}
	if len(want) == 0 {
		return
	}
	owners := resolveInodes(want)
	for i := range entries {
		if pid, ok := owners[entries[i].inode]; ok {
			entries[i].PID = pid
		}
	}
}

// parseProcNet 解析 /proc/net/tcp 系列文件。
//
// 格式（每行一条记录，字段以空格分隔）：
//
//	sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
//	 0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 ...
//
// 关键点：
//   - 地址是**小端**十六进制。IPv4 是 8 位十六进制（0100007F = 127.0.0.1），
//     IPv6 是 32 位，且按 4 字节一组各自小端。
//   - 端口是**大端**十六进制（1F90 = 8080），与地址的字节序相反。
//   - PID 不在这个文件里，要靠 inode 去 /proc/<pid>/fd 反查（见 resolveInodes）。
func parseProcNet(content, protocol, path string) []rawEntry {
	isV6 := strings.HasSuffix(path, "6")
	lines := strings.Split(content, "\n")
	entries := make([]rawEntry, 0, len(lines))

	for i, line := range lines {
		if i == 0 {
			continue // 表头
		}
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}

		// st 字段（第 4 个，索引 3）
		if protocol == ProtocolTCP && !strings.EqualFold(fields[3], procTCPStateListen) {
			continue // 非 LISTEN 的是已建立连接，不算端口占用
		}

		// local_address 形如 0100007F:1F90
		addrHex, portHex, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		port, err := strconv.ParseInt(portHex, 16, 32)
		if err != nil {
			continue
		}
		addr := parseProcAddr(addrHex, isV6)
		if addr == "" {
			continue
		}

		// inode 是第 10 个字段（索引 9）。它是反查 PID 的唯一线索。
		inode := fields[9]

		entries = append(entries, rawEntry{
			Protocol:  protocol,
			LocalAddr: addr,
			LocalPort: int(port),
			PID:       0, // 稍后由 resolveInodes 填充
			inode:     inode,
		})
	}
	return entries
}

// parseProcAddr 把 /proc 里的十六进制地址转成可读形式。
//
// IPv4：8 位十六进制，按小端读 4 字节 → 0100007F = 127.0.0.1
// IPv6：32 位十六进制，**每 4 字节一组**各自小端 → 00000000000000000000000001000000 = ::1
//
// IPv6 的分组小端是最容易写错的地方：它不是整体反转，而是 4 个 32 位字
// 各自反转字节序。
func parseProcAddr(hexAddr string, isV6 bool) string {
	raw, err := hex.DecodeString(hexAddr)
	if err != nil {
		return ""
	}

	if !isV6 {
		if len(raw) != 4 {
			return ""
		}
		// 小端 → 反转后是网络字节序
		return net.IPv4(raw[3], raw[2], raw[1], raw[0]).String()
	}

	if len(raw) != 16 {
		return ""
	}
	// 每 4 字节一组反转
	ip := make(net.IP, 16)
	for g := 0; g < 4; g++ {
		ip[g*4+0] = raw[g*4+3]
		ip[g*4+1] = raw[g*4+2]
		ip[g*4+2] = raw[g*4+1]
		ip[g*4+3] = raw[g*4+0]
	}
	return ip.String()
}

// resolveInodes 通过 inode 反查 PID。
//
// /proc/net/* 不提供 PID，只能拿 inode 去遍历 /proc/<pid>/fd/*，找到
// 「socket:[<inode>]」这样的软链接。这是 O(进程数) 的扫描，但只在扫描时做一次，
// 且用 map 索引后是 O(1) 查找。
//
// **非 root 只能看到自己进程的 fd**，因此别人的端口会查不到 PID（保持 0）。
// 这不是 bug，界面会显示「无法确定占用进程」。
func resolveInodes(want map[string]bool) map[string]int {
	result := make(map[string]int)
	if len(want) == 0 {
		return result
	}

	procs, err := os.ReadDir("/proc")
	if err != nil {
		return result
	}

	for _, p := range procs {
		if !p.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(p.Name())
		if err != nil {
			continue // 非数字目录（self、sys 等）
		}

		fdDir := filepath.Join("/proc", p.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // 权限不足，跳过
		}

		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			// 形如 socket:[12345]
			if !strings.HasPrefix(target, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
			if want[inode] {
				result[inode] = pid
			}
		}
	}
	return result
}

// enumerateViaSS 是 /proc 不可读时的回退路径。
//
// `ss -tulpnH` 的 -H 去掉表头，-p 显示进程，-l 只要监听，-n 不做名称解析。
// 输出形如：
//
//	tcp   LISTEN 0      128    127.0.0.1:8080  0.0.0.0:*  users:(("nginx",pid=1234,fd=6))
func enumerateViaSS() ([]rawEntry, error) {
	cmd := exec.Command("ss", "-tulpnH")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseSSOutput(string(out)), nil
}

// parseSSOutput 解析 `ss -tulpnH` 的输出。
func parseSSOutput(content string) []rawEntry {
	var entries []rawEntry
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		protocol := ProtocolTCP
		if strings.HasPrefix(fields[0], "udp") {
			protocol = ProtocolUDP
		}

		addr, port, err := splitHostPortLoose(fields[4])
		if err != nil {
			continue
		}

		// users:(("name",pid=1234,fd=6)) —— 可能有多组，取第一个
		pid := 0
		if len(fields) > 5 {
			rest := strings.Join(fields[5:], " ")
			pid = extractSSPID(rest)
		}

		entries = append(entries, rawEntry{
			Protocol:  protocol,
			LocalAddr: addr,
			LocalPort: port,
			PID:       pid,
		})
	}
	return entries
}

// splitHostPortLoose 解析 ss 输出的地址列，兼容 `*:8080`、`[::]:8080`、`127.0.0.1:8080`。
func splitHostPortLoose(s string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		// 没有方括号的 IPv6 或 `*:port` 形式，手工切最后一个冒号
		i := strings.LastIndex(s, ":")
		if i < 0 {
			return "", 0, err
		}
		host, portStr = s[:i], s[i+1:]
	}
	if host == "*" || host == "" {
		host = "0.0.0.0"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}

// extractSSPID 从 `users:(("nginx",pid=1234,fd=6))` 里取出 pid。
func extractSSPID(s string) int {
	const marker = "pid="
	i := strings.Index(s, marker)
	if i < 0 {
		return 0
	}
	rest := s[i+len(marker):]
	end := strings.IndexAny(rest, ",)")
	if end < 0 {
		end = len(rest)
	}
	pid, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0
	}
	return pid
}

// dedupe 按「协议 + 地址 + 端口 + PID」去重。
//
// 去重是必要的：同一进程可能对同一端口建立多条记录（双栈监听、SO_REUSEADDR），
// 列表里出现重复行会让用户困惑。
func dedupe(entries []rawEntry) []rawEntry {
	seen := make(map[string]bool, len(entries))
	out := make([]rawEntry, 0, len(entries))
	for _, e := range entries {
		key := portKey(e.Protocol, e.LocalAddr, e.LocalPort) + "|" + strconv.Itoa(e.PID)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}

// ---------- 进程信息 ----------

// readProcessInfo 从 /proc/<pid> 读取进程信息。
//
// 全部字段都尽力而为：非 root 读不到别的用户的进程信息，此时返回零值而不是
// 错误——端口本身仍然是有效的，只是归属未知。
func readProcessInfo(pid int) (ProcessInfo, error) {
	if pid <= 0 {
		return ProcessInfo{}, errors.New("无效的进程号")
	}
	base := filepath.Join("/proc", strconv.Itoa(pid))

	var info ProcessInfo

	// 进程名：comm 比 cmdline 更可靠（cmdline 可能为空，如内核线程）
	if raw, err := os.ReadFile(filepath.Join(base, "comm")); err == nil {
		info.Name = strings.TrimSpace(string(raw))
	}

	// 可执行文件路径
	if target, err := os.Readlink(filepath.Join(base, "exe")); err == nil {
		info.Executable = strings.TrimSuffix(target, " (deleted)")
		if info.Name == "" {
			info.Name = filepath.Base(info.Executable)
		}
	}

	// 命令行：NUL 分隔
	if raw, err := os.ReadFile(filepath.Join(base, "cmdline")); err == nil {
		info.CommandLine = strings.TrimSpace(
			strings.ReplaceAll(string(raw), "\x00", " "))
	}

	// 用户 + 启动时间
	if raw, err := os.ReadFile(filepath.Join(base, "status")); err == nil {
		info.User = userFromStatus(string(raw))
	}
	if raw, err := os.ReadFile(filepath.Join(base, "stat")); err == nil {
		info.StartedAt = startTimeFromStat(string(raw))
	}

	if info.Name == "" && info.Executable == "" {
		return ProcessInfo{}, errors.New("无法读取进程信息")
	}
	return info, nil
}

// userFromStatus 从 /proc/<pid>/status 的 Uid 行解析用户名。
//
// 格式：`Uid:\t1000\t1000\t1000\t1000`（真实、有效、保存、文件系统）。
// 取第一个（真实 UID），再查 /etc/passwd 拿名字；查不到就用数字 UID。
func userFromStatus(status string) string {
	for _, line := range strings.Split(status, "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return ""
		}
		uid := fields[1]
		if name := lookupUID(uid); name != "" {
			return name
		}
		return uid
	}
	return ""
}

// lookupUID 在 /etc/passwd 里查 UID 对应的用户名。
func lookupUID(uid string) string {
	raw, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 3 && fields[2] == uid {
			return fields[0]
		}
	}
	return ""
}

// startTimeFromStat 从 /proc/<pid>/stat 计算进程启动时间。
//
// 第 22 项是启动时刻（自系统启动以来的时钟滴答数）。要转成绝对时间，
// 需要加上 /proc/stat 的 btime（系统启动的 Unix 时间戳），再除以
// sysconf(_SC_CLK_TCK)（Linux 上固定为 100）。
//
// 注意 comm 字段可能含空格和括号，所以不能直接按空格切分——要从最后一个
// ')' 之后开始切。
func startTimeFromStat(stat string) time.Time {
	close := strings.LastIndex(stat, ")")
	if close < 0 {
		return time.Time{}
	}
	fields := strings.Fields(stat[close+1:])
	// 去掉 ") " 后，字段索引整体前移：原第 22 项变成索引 19
	const starttimeIdx = 19
	if len(fields) <= starttimeIdx {
		return time.Time{}
	}
	ticks, err := strconv.ParseInt(fields[starttimeIdx], 10, 64)
	if err != nil {
		return time.Time{}
	}

	btime := bootTime()
	if btime.IsZero() {
		return time.Time{}
	}
	// 内核时钟频率在 Linux 上恒为 100
	return btime.Add(time.Duration(ticks) * (time.Second / 100))
}

// bootTime 从 /proc/stat 读取系统启动时间。
func bootTime() time.Time {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "btime ") {
			continue
		}
		sec, err := strconv.ParseInt(strings.TrimSpace(line[6:]), 10, 64)
		if err != nil {
			return time.Time{}
		}
		return time.Unix(sec, 0)
	}
	return time.Time{}
}

// ---------- 结束进程 ----------

// killProcess 结束进程。
//
// force 为 false 时先发 SIGTERM 并等待宽限期，让进程有机会保存数据；
// 超时后再 SIGKILL。这是 Linux 上与 Windows 最大的行为差异——Windows 上
// 没有可靠的优雅终止手段，一律强杀。
func killProcess(pid int, force bool) error {
	if pid <= 0 {
		return &KillError{Code: CodeNotFound, Message: "无效的进程号"}
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return &KillError{Code: CodeNotFound, Message: "进程不存在"}
	}

	if force {
		if err := proc.Signal(syscall.SIGKILL); err != nil {
			return classifySignalErr(err)
		}
		return nil
	}

	// 优雅终止：SIGTERM + 宽限期
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return classifySignalErr(err)
	}
	const grace = 3 * time.Second
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	// 宽限期内没退出，升级为 SIGKILL
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		return classifySignalErr(err)
	}
	return nil
}

// processAlive 判断进程是否还活着。
//
// 用 kill(pid, 0) 而不是读 /proc/<pid>：前者是原子的，且不需要读文件系统。
// 注意 PID 复用：进程退出后 PID 可能立刻被复用，这里只做尽力判断。
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// classifySignalErr 把 signal 错误映射成稳定的错误码。
func classifySignalErr(err error) error {
	if errors.Is(err, os.ErrProcessDone) {
		return &KillError{Code: CodeNotFound, Message: "进程已退出"}
	}
	if errors.Is(err, syscall.EPERM) {
		return &KillError{
			Code:    CodeDenied,
			Message: "权限不足，需要管理员权限才能结束该进程",
		}
	}
	if errors.Is(err, syscall.ESRCH) {
		return &KillError{Code: CodeNotFound, Message: "进程不存在"}
	}
	return &KillError{Code: CodeFailed, Message: err.Error()}
}

// classifyKillError 把 signal 错误映射成稳定的错误码。
func classifyKillError(err error) string {
	switch {
	case errors.Is(err, os.ErrProcessDone), errors.Is(err, syscall.ESRCH):
		return CodeNotFound
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EACCES):
		return CodeDenied
	}
	return CodeFailed
}

// fillCommandLines 在 Linux 上无需额外工作。
//
// 命令行随 readProcessInfo 一起从 /proc/<pid>/cmdline 读到，没有 Windows 上
// 「WMI 查询慢所以要按需加载」的问题，因此这里是空实现——保留函数是为了让
// portscan.go 的平台无关代码不必区分平台。
func fillCommandLines(entries []PortEntry) {
	// 命令行已在 readProcessInfo 中填充，无需补全。
}
