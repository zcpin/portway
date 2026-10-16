//go:build darwin

package portscan

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func init() {
	selfPID = os.Getpid()
}

// rawEntry 是平台层枚举出的一条原始记录，交给 portscan.go 补全进程信息。
type rawEntry struct {
	Protocol  string
	LocalAddr string
	LocalPort int
	PID       int
}

// enumerate 用 lsof 枚举本机监听端口。
//
// **为什么不用 sysctl 的 pcblist**：解析 `net.inet.tcp.pcblist_n` 需要按 C 结构体
// 布局读取 xinpgen/xsocket 链表，而这些结构体只在 C 头文件里定义，Go 侧要复刻
// 就必须引入 CGO。项目当前 daemon 不依赖 C 工具链（只有进程内引擎的 c-shared
// 需要），为了一个功能引入 CGO 会让构建矩阵复杂化，因此选择 lsof 回退。
//
// lsof 的代价是每次要起两个子进程（TCP + UDP），总耗时约 100–200ms，
// 比 Windows 的 iphlpapi 慢，但可接受——列表默认不自动刷新。
func enumerate() ([]rawEntry, error) {
	if _, err := exec.LookPath("lsof"); err != nil {
		return nil, errors.New(
			"未找到 lsof 命令，无法查询端口占用（macOS 上通常自带，若缺失可通过 Homebrew 安装）")
	}

	// TCP 只取 LISTEN；UDP 没有状态机，全部保留
	tcp, err := runLsof("-nP", "-iTCP", "-sTCP:LISTEN")
	if err != nil {
		return nil, fmt.Errorf("读取 TCP 监听列表失败: %w", err)
	}
	udp, err := runLsof("-nP", "-iUDP")
	if err != nil {
		// UDP 失败不该让整个功能不可用，TCP 结果仍然有价值
		udp = nil
	}

	entries := append(parseLsof(string(tcp), ProtocolTCP), parseLsof(string(udp), ProtocolUDP)...)
	return dedupe(entries), nil
}

// runLsof 执行 lsof 并返回输出。
//
// -n 禁止解析主机名、-P 禁止解析端口名：两者都会触发 DNS/服务名查询，
// 明显拖慢速度且结果不稳定（localhost 可能变成 127.0.0.1 或 ::1）。
func runLsof(args ...string) ([]byte, error) {
	cmd := exec.Command("lsof", args...)
	out, err := cmd.Output()
	if err != nil {
		// lsof 在「没有匹配项」时返回退出码 1，这不是错误
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return out, nil
		}
		return nil, err
	}
	return out, nil
}

// parseLsof 解析 lsof 的输出。
//
// 格式（列以空白分隔，COMMAND 可能含空格所以要按列位置处理）：
//
//	COMMAND   PID   USER   FD   TYPE   DEVICE SIZE/OFF NODE NAME
//	nginx    1234   root    6u  IPv4 0x1234      0t0  TCP 127.0.0.1:8080 (LISTEN)
//	node     5678   user   20u  IPv6 0x5678      0t0  TCP *:3000 (LISTEN)
//	mDNSRes  9012   _mdns   4u  IPv4 0x9012      0t0  UDP *:5353
//
// 关键点：NAME 列里是 `地址:端口`，IPv6 带方括号 `[::1]:8080`。
// 协议既可以从 TYPE 列（IPv4/IPv6）判断，也可以从 NAME 前缀（TCP/UDP）判断——
// 后者更直接，因为我们要区分的是 TCP 与 UDP。
func parseLsof(content, wantProtocol string) []rawEntry {
	var entries []rawEntry
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue // 表头或残缺行
		}
		// 第 0 列是 COMMAND，第 1 列是 PID
		if fields[0] == "COMMAND" {
			continue // 表头
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}

		// 从第 7 列开始找协议标记（TCP/UDP），跳过 DEVICE/SIZE/OFF/NODE
		protoIdx := -1
		for i := 7; i < len(fields); i++ {
			up := strings.ToUpper(fields[i])
			if up == "TCP" || up == "UDP" {
				protoIdx = i
				break
			}
		}
		if protoIdx < 0 || protoIdx+1 >= len(fields) {
			continue
		}
		if strings.ToUpper(fields[protoIdx]) != strings.ToUpper(wantProtocol) {
			continue
		}

		// 地址列后面可能跟 "(LISTEN)" 之类的状态说明，只取地址部分
		addrField := fields[protoIdx+1]
		addr, port, err := splitLsofAddr(addrField)
		if err != nil {
			continue
		}

		entries = append(entries, rawEntry{
			Protocol:  wantProtocol,
			LocalAddr: addr,
			LocalPort: port,
			PID:       pid,
		})
	}
	return entries
}

// splitLsofAddr 解析 lsof 的 NAME 列，如 `127.0.0.1:8080`、`*:3000`、`[::1]:8080`。
func splitLsofAddr(s string) (string, int, error) {
	host, portStr, err := netSplitHostPort(s)
	if err != nil {
		return "", 0, err
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

// netSplitHostPort 是 net.SplitHostPort 的宽松版本，额外接受不带方括号的 IPv6。
func netSplitHostPort(s string) (string, string, error) {
	// 带方括号的 IPv6：`[::1]:8080`
	if strings.HasPrefix(s, "[") {
		end := strings.LastIndex(s, "]")
		if end < 0 || end+1 >= len(s) || s[end+1] != ':' {
			return "", "", errors.New("地址格式无效")
		}
		return s[1:end], s[end+2:], nil
	}
	// 其余情况按最后一个冒号切分
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", "", errors.New("地址缺少端口")
	}
	return s[:i], s[i+1:], nil
}

// dedupe 按「协议 + 地址 + 端口 + PID」去重。
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

// readProcessInfo 用 ps 读取进程信息。
//
// macOS 上读进程信息有两条路：
//   - libproc 的 proc_pidpath / proc_pidinfo：需要 CGO
//   - ps 命令：无 CGO 依赖，一次调用能拿多个字段
//
// 选择 ps。为了减少子进程开销，一次查询只针对单个 PID（列表里同一 PID 通常
// 只出现少数几次，且 portscan.go 的 processCache 保证每个 PID 只查一次）。
func readProcessInfo(pid int) (ProcessInfo, error) {
	if pid <= 0 {
		return ProcessInfo{}, errors.New("无效的进程号")
	}

	// comm= 是进程名，args= 是完整命令行，lstart= 是启动时间
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid),
		"-o", "comm=,args=,lstart=,user=")
	out, err := cmd.Output()
	if err != nil {
		return ProcessInfo{}, err
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return ProcessInfo{}, errors.New("进程不存在")
	}

	info := ProcessInfo{}
	// comm 是第一个字段（路径），args 是剩余部分直到 lstart 的固定格式
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ProcessInfo{}, errors.New("无法解析进程信息")
	}
	info.Executable = fields[0]
	info.Name = baseName(info.Executable)

	// lstart 的格式是 "Wed Oct 15 09:41:30 2026"，共 5 个字段，位于末尾；
	// 用户是它的前一个字段。args 夹在中间。
	if len(fields) >= 7 {
		userIdx := len(fields) - 6
		info.User = fields[userIdx]
		lstart := strings.Join(fields[len(fields)-5:], " ")
		if t, err := time.Parse("Mon Jan _2 15:04:05 2006", lstart); err == nil {
			info.StartedAt = t
		}
		// args 是 comm 之后、user 之前的部分
		if userIdx > 1 {
			info.CommandLine = strings.Join(fields[1:userIdx], " ")
		}
	}

	if info.Name == "" && info.Executable == "" {
		return ProcessInfo{}, errors.New("无法读取进程信息")
	}
	return info, nil
}

// baseName 取路径的文件名部分。
func baseName(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// fillCommandLines 在 macOS 上无需额外工作。
//
// 命令行随 readProcessInfo 一起由 ps 读出，没有 Windows 上「WMI 慢所以要按需
// 加载」的问题，因此这里是空实现。
func fillCommandLines(entries []PortEntry) {
	// 命令行已在 readProcessInfo 中填充，无需补全。
}

// ---------- 结束进程 ----------

// killProcess 结束进程。
//
// force 为 false 时先发 SIGTERM 并等待宽限期，让进程有机会保存数据；
// 超时后再 SIGKILL。与 Linux 行为一致。
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
			return killError(err)
		}
		return nil
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return killError(err)
	}
	const grace = 3 * time.Second
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		return killError(err)
	}
	return nil
}

// processAlive 判断进程是否还活着。
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// killError 把 signal 错误包装成带分类的 KillError。
func killError(err error) error {
	return &KillError{Code: classifyKillError(err), Message: killMessage(err)}
}

// killMessage 给出面向用户的中文提示。
func killMessage(err error) string {
	switch classifyKillError(err) {
	case CodeNotFound:
		return "进程不存在或已退出"
	case CodeDenied:
		return "权限不足，需要管理员权限才能结束该进程"
	}
	return err.Error()
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
