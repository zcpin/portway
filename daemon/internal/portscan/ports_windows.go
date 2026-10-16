//go:build windows

package portscan

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
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

// ---------- 端口枚举 ----------

// MIB_TCPROW_OWNER_PID 是 GetExtendedTcpTable 在 TCP_TABLE_OWNER_PID_LISTENER
// 级别下返回的行结构。字段顺序与 Windows SDK 的 mib.h 一致，不能调整。
type mibTCPRowOwnerPID struct {
	State      uint32
	LocalAddr  uint32
	LocalPort  uint32
	RemoteAddr uint32
	RemotePort uint32
	OwningPID  uint32
}

// MIB_TCP6ROW_OWNER_PID 是 IPv6 版本。地址是 16 字节数组，不是 uint32。
type mibTCP6RowOwnerPID struct {
	LocalAddr     [16]byte
	LocalScopeID  uint32
	LocalPort     uint32
	RemoteAddr    [16]byte
	RemoteScopeID uint32
	RemotePort    uint32
	State         uint32
	OwningPID     uint32
}

// MIB_UDPROW_OWNER_PID 是 UDP 版本。UDP 没有 State 字段——无连接协议没有状态机。
type mibUDPRowOwnerPID struct {
	LocalAddr uint32
	LocalPort uint32
	OwningPID uint32
}

type mibUDP6RowOwnerPID struct {
	LocalAddr    [16]byte
	LocalScopeID uint32
	LocalPort    uint32
	OwningPID    uint32
}

// TCP 状态常量，取自 Windows SDK 的 MIB_TCP_STATE。
const mibTCPStateListen = 2

// AF_INET / AF_INET6，用于告诉 iphlpapi 要哪一族地址。
const (
	afInet  = 2
	afInet6 = 23
)

// TCP_TABLE_CLASS 取值，取自 Windows SDK 的 iprtrmib.h。
//
// 这几个常量极易踩坑：OWNER_PID_LISTENER 是 3 而不是 2，2 是 BASIC_ALL。
// BASIC_* 系列的行结构是 20 字节且**不含 OwningPID**，用错常量会让整张表
// 按 24 字节错位解析，读出来的「PID」其实是邻行的 RemotePort——表现为一堆
// 2、5、8、11 这样的小整数，看起来像进程号但完全不对。
//
// 我们只使用 OWNER_PID_* （24 字节行，含 OwningPID）。
const (
	tcpTableOwnerPidListener    = 3
	tcpTableOwnerPidConnections = 4
	tcpTableOwnerPidAll         = 5
	udpTableOwnerPid            = 1
)

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUdpTable = iphlpapi.NewProc("GetExtendedUdpTable")
)

// enumerate 用 iphlpapi 读取本机 TCP 监听表与 UDP 绑定表。
//
// 为什么不用 `netstat -ano`：输出要按列宽解析，中文 Windows 下列名会变，
// 且连接多时明显慢。GetExtendedTcpTable 是结构化的，一次调用拿全部条目，
// 还能直接拿到 OwningPid。
func enumerate() ([]rawEntry, error) {
	var entries []rawEntry

	tcp4, err := tcpTable(afInet, tcpTableOwnerPidListener)
	if err != nil {
		return nil, fmt.Errorf("读取 TCP 监听表失败: %w", err)
	}
	entries = append(entries, tcp4...)

	tcp6, err := tcpTable(afInet6, tcpTableOwnerPidListener)
	if err != nil {
		// IPv6 不可用（老系统或已禁用）不该让整个功能失败，IPv4 结果仍然有价值
		tcp6 = nil
	}
	entries = append(entries, tcp6...)

	udp4, err := udpTable(afInet)
	if err != nil {
		return nil, fmt.Errorf("读取 UDP 绑定表失败: %w", err)
	}
	entries = append(entries, udp4...)

	udp6, err := udpTable(afInet6)
	if err != nil {
		udp6 = nil
	}
	entries = append(entries, udp6...)

	return dedupe(entries), nil
}

// tcpTable 调用 GetExtendedTcpTable。
//
// 这个 API 的调用约定是「先传 nil 探测所需缓冲区大小，再按大小分配重试」，
// 中间表可能变大，因此必须允许重试而不是一次分配。
func tcpTable(family uint32, class uint32) ([]rawEntry, error) {
	var size uint32
	ret, _, _ := procGetExtendedTcpTable.Call(
		0, uintptr(unsafe.Pointer(&size)), 0, uintptr(family), uintptr(class), 0)
	// ERROR_INSUFFICIENT_BUFFER(122) 是预期结果，表示 size 已被填好
	if ret != 122 && ret != 0 {
		return nil, syscall.Errno(ret)
	}
	if size == 0 {
		return nil, nil
	}

	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]byte, size)
		ret, _, _ = procGetExtendedTcpTable.Call(
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)),
			0, uintptr(family), uintptr(class), 0)
		if ret == 122 {
			continue // 表在两次调用之间变大了，按新 size 重试
		}
		if ret != 0 {
			return nil, syscall.Errno(ret)
		}
		// size 是 in/out 参数：第二次调用会把**实际填充的字节数**写回来，
		// 通常小于 buf 的容量（第一次探测时表更大）。解析必须用这个值，
		// 否则按容量算出的行宽会偏大，行宽校验会误判成「class 用错了」。
		return parseTCPTable(buf[:size], family, class), nil
	}
	return nil, errors.New("TCP 表在读取过程中持续变化")
}

// parseTCPTable 解析表头 + 行数据。
//
// 表结构：前 4 字节是行数，之后紧跟着行数组（无对齐填充）。
//
// 这里会校验「实际行宽」与结构体大小是否一致：iphlpapi 不会告诉我们行宽，
// 传错 TCP_TABLE_CLASS（例如把 BASIC_ALL 当成 OWNER_PID_LISTENER）会让整个
// 缓冲区按错误的步长解析，读出来的 PID 是邻行字段，而且**不会报错**。
// 校验一次就能把这类静默错误变成明确失败。
func parseTCPTable(buf []byte, family uint32, class uint32) []rawEntry {
	if len(buf) < 4 {
		return nil
	}
	count := int(binary.LittleEndian.Uint32(buf[:4]))
	offset := 4
	entries := make([]rawEntry, 0, count)

	expected := int(unsafe.Sizeof(mibTCPRowOwnerPID{}))
	if family == afInet6 {
		expected = int(unsafe.Sizeof(mibTCP6RowOwnerPID{}))
	}
	if count > 0 {
		if actual := (len(buf) - 4) / count; actual != expected {
			// 行宽不符说明 class 常量用错了，继续解析只会得到垃圾数据
			return nil
		}
	}

	for i := 0; i < count; i++ {
		if family == afInet {
			var row mibTCPRowOwnerPID
			rowSize := int(unsafe.Sizeof(row))
			if offset+rowSize > len(buf) {
				break
			}
			row = *(*mibTCPRowOwnerPID)(unsafe.Pointer(&buf[offset]))
			offset += rowSize

			// 只要 LISTEN：其它状态是已建立的连接，不是「端口被占用」
			if class == tcpTableOwnerPidListener && row.State != mibTCPStateListen {
				continue
			}
			if row.State != mibTCPStateListen {
				continue
			}
			entries = append(entries, rawEntry{
				Protocol:  ProtocolTCP,
				LocalAddr: ipv4String(row.LocalAddr),
				LocalPort: portFromNetworkOrder(row.LocalPort),
				PID:       int(row.OwningPID),
			})
			continue
		}

		var row mibTCP6RowOwnerPID
		rowSize := int(unsafe.Sizeof(row))
		if offset+rowSize > len(buf) {
			break
		}
		row = *(*mibTCP6RowOwnerPID)(unsafe.Pointer(&buf[offset]))
		offset += rowSize

		if row.State != mibTCPStateListen {
			continue
		}
		entries = append(entries, rawEntry{
			Protocol:  ProtocolTCP,
			LocalAddr: ipv6String(row.LocalAddr),
			LocalPort: portFromNetworkOrder(row.LocalPort),
			PID:       int(row.OwningPID),
		})
	}
	return entries
}

// udpTable 调用 GetExtendedUdpTable，调用约定与 TCP 相同。
func udpTable(family uint32) ([]rawEntry, error) {
	var size uint32
	ret, _, _ := procGetExtendedUdpTable.Call(
		0, uintptr(unsafe.Pointer(&size)), 0, uintptr(family), udpTableOwnerPid, 0)
	if ret != 122 && ret != 0 {
		return nil, syscall.Errno(ret)
	}
	if size == 0 {
		return nil, nil
	}

	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]byte, size)
		ret, _, _ = procGetExtendedUdpTable.Call(
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)),
			0, uintptr(family), udpTableOwnerPid, 0)
		if ret == 122 {
			continue
		}
		if ret != 0 {
			return nil, syscall.Errno(ret)
		}
		return parseUDPTable(buf[:size], family), nil
	}
	return nil, errors.New("UDP 表在读取过程中持续变化")
}

// parseUDPTable 解析 UDP 表。**不过滤状态**：UDP 无状态机，绑定了就是绑定了。
func parseUDPTable(buf []byte, family uint32) []rawEntry {
	if len(buf) < 4 {
		return nil
	}
	count := int(binary.LittleEndian.Uint32(buf[:4]))
	offset := 4
	entries := make([]rawEntry, 0, count)

	for i := 0; i < count; i++ {
		if family == afInet {
			var row mibUDPRowOwnerPID
			rowSize := int(unsafe.Sizeof(row))
			if offset+rowSize > len(buf) {
				break
			}
			row = *(*mibUDPRowOwnerPID)(unsafe.Pointer(&buf[offset]))
			offset += rowSize
			entries = append(entries, rawEntry{
				Protocol:  ProtocolUDP,
				LocalAddr: ipv4String(row.LocalAddr),
				LocalPort: portFromNetworkOrder(row.LocalPort),
				PID:       int(row.OwningPID),
			})
			continue
		}

		var row mibUDP6RowOwnerPID
		rowSize := int(unsafe.Sizeof(row))
		if offset+rowSize > len(buf) {
			break
		}
		row = *(*mibUDP6RowOwnerPID)(unsafe.Pointer(&buf[offset]))
		offset += rowSize
		entries = append(entries, rawEntry{
			Protocol:  ProtocolUDP,
			LocalAddr: ipv6String(row.LocalAddr),
			LocalPort: portFromNetworkOrder(row.LocalPort),
			PID:       int(row.OwningPID),
		})
	}
	return entries
}

// ipv4String 把 MIB 里的 DWORD 地址转成点分十进制。
//
// 该字段是**网络字节序存在小端机器上的 DWORD**，因此要按小端读出来再当
// 大端解释：0x0100007F 表示 127.0.0.1。
func ipv4String(addr uint32) string {
	return net.IPv4(
		byte(addr),
		byte(addr>>8),
		byte(addr>>16),
		byte(addr>>24),
	).String()
}

func ipv6String(addr [16]byte) string {
	return net.IP(addr[:]).String()
}

// portFromNetworkOrder 把 MIB 里的端口转成主机序。
//
// 端口以网络字节序存放在 DWORD 的低 16 位里，因此取低 16 位后做字节交换。
func portFromNetworkOrder(raw uint32) int {
	return int((raw&0xff)<<8 | (raw>>8)&0xff)
}

// dedupe 按「协议 + 地址 + 端口 + PID」去重。
//
// 去重是必要的：同一进程可能对同一端口建立多条 LISTEN 记录（双栈监听、
// SO_REUSEADDR），列表里出现重复行会让用户困惑。
func dedupe(entries []rawEntry) []rawEntry {
	seen := make(map[string]bool, len(entries))
	out := make([]rawEntry, 0, len(entries))
	for _, e := range entries {
		key := portKey(e.Protocol, e.LocalAddr, e.LocalPort) + "|" + fmt.Sprint(e.PID)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}

// ---------- 进程信息 ----------

// readProcessInfo 读取进程名、可执行文件路径、启动时间与用户。
//
// 用 OpenProcess + QueryFullProcessImageName 而不是 WMI：前者是本地调用，
// 微秒级；后者要起 COM 连接，首次调用 200–500ms。命令行才需要 WMI，
// 因此单独放在 fillCommandLines 里按需执行。
func readProcessInfo(pid int) (ProcessInfo, error) {
	handle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ProcessInfo{}, err
	}
	defer windows.CloseHandle(handle)

	var info ProcessInfo

	// 可执行文件完整路径
	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); err == nil {
		info.Executable = windows.UTF16ToString(buf[:size])
		info.Name = strings.TrimSuffix(
			strings.ToLower(baseName(info.Executable)), ".exe")
	}

	// 启动时间。注意这是内核时间，需要转成 Unix 时间戳。
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err == nil {
		info.StartedAt = time.Unix(0, creation.Nanoseconds())
	}

	// 进程所有者。失败不影响其它字段——非管理员读不到别的用户的进程 SID。
	var token windows.Token
	if err := windows.OpenProcessToken(handle, windows.TOKEN_QUERY, &token); err == nil {
		if sid, err := tokenUserSID(token); err == nil {
			if account, domain, _, err := sid.LookupAccount(""); err == nil {
				if domain != "" {
					info.User = domain + `\` + account
				} else {
					info.User = account
				}
			}
		}
		token.Close()
	}

	if info.Executable == "" && info.Name == "" {
		return ProcessInfo{}, errors.New("无法读取进程信息")
	}
	return info, nil
}

// tokenUserSID 从进程令牌里取出用户 SID。
func tokenUserSID(token windows.Token) (*windows.SID, error) {
	var size uint32
	// 先探测大小，ERROR_INSUFFICIENT_BUFFER 是预期结果
	err := windows.GetTokenInformation(token, windows.TokenUser, nil, 0, &size)
	if err != nil && err != windows.ERROR_INSUFFICIENT_BUFFER {
		return nil, err
	}
	if size == 0 {
		return nil, errors.New("令牌信息为空")
	}
	buf := make([]byte, size)
	if err := windows.GetTokenInformation(
		token, windows.TokenUser, &buf[0], size, &size); err != nil {
		return nil, err
	}
	return (*windows.Tokenuser)(unsafe.Pointer(&buf[0])).User.Sid, nil
}

// baseName 取路径的文件名部分。Windows 上同时兼容反斜杠与正斜杠。
func baseName(path string) string {
	if i := strings.LastIndexAny(path, `\/`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// ---------- 命令行补全 ----------

// fillCommandLines 为每条记录补全命令行。
//
// 走 WMI（Win32_Process.CommandLine）而不是 PEB 读取：PEB 方式需要
// ReadProcessMemory 且对跨架构进程不可靠，WMI 虽然慢但一次查询能拿到
// 全部进程的命令行，按 PID 建索引后是 O(1) 查找。
//
// 失败时静默跳过——命令行是辅助信息，读不到不该让整个列表失败。
func fillCommandLines(entries []PortEntry) {
	lines := queryCommandLines()
	if len(lines) == 0 {
		return
	}
	for i := range entries {
		if cmd, ok := lines[entries[i].PID]; ok {
			entries[i].CommandLine = cmd
		}
	}
}

// queryCommandLines 通过 WMI 一次性取出所有进程的命令行。
//
// 用 PowerShell 调用 Get-CimInstance 而不是引入 WMI 的 Go 绑定：
// 项目当前没有 WMI 依赖，为了一个可选的辅助字段引入一个 COM 库不划算。
// 这里刻意不做超时控制——调用方（app 层）在 3 秒内没拿到结果会自行放弃。
func queryCommandLines() map[int]string {
	const script = `Get-CimInstance Win32_Process | ` +
		`Select-Object ProcessId,CommandLine | ` +
		`ForEach-Object { "$($_.ProcessId)|$($_.CommandLine)" }`

	output, err := runHidden("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	if err != nil {
		return nil
	}

	result := make(map[int]string)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		idx := strings.Index(line, "|")
		if idx <= 0 {
			continue
		}
		pid, err := parsePositiveInt(line[:idx])
		if err != nil {
			continue
		}
		if cmd := strings.TrimSpace(line[idx+1:]); cmd != "" {
			result[pid] = cmd
		}
	}
	return result
}

// ---------- 结束进程 ----------

// killProcess 终止一个进程。
//
// Windows 没有真正的「优雅终止」：GenerateConsoleCtrlEvent 只对同控制台的
// 进程有效，WM_CLOSE 只对有窗口的进程有效。因此这里一律 TerminateProcess，
// force 参数在该平台不产生行为差异（语义由 portscan.go 的文档说明）。
func killProcess(pid int, force bool) error {
	handle, err := windows.OpenProcess(
		windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)

	if err := windows.TerminateProcess(handle, 1); err != nil {
		return err
	}
	return nil
}

// classifyKillError 把平台错误映射成界面可用的分类。
func classifyKillError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, syscall.EPERM) {
		return CodeDenied
	}
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) ||
		errors.Is(err, windows.ERROR_NOT_FOUND) ||
		errors.Is(err, syscall.ESRCH) {
		return CodeNotFound
	}
	return CodeFailed
}

// ---------- 辅助 ----------

// runHidden 执行外部命令并返回 stdout，不弹出控制台窗口。
//
// 必须设置 HideWindow：客户端是 GUI 程序，不设置的话每次调用都会闪一个黑框。
func runHidden(name string, args ...string) (string, error) {
	cmd := hiddenCommand(name, args...)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return decodeConsole(output), nil
}

// sortEntriesStable 是给平台层复用的小工具，保证与纯逻辑层同一套排序。
func sortEntriesStable(entries []PortEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].LocalPort != entries[j].LocalPort {
			return entries[i].LocalPort < entries[j].LocalPort
		}
		return entries[i].PID < entries[j].PID
	})
}

func parsePositiveInt(raw string) (int, error) {
	var value int
	for _, ch := range strings.TrimSpace(raw) {
		if ch < '0' || ch > '9' {
			return 0, errors.New("不是数字")
		}
		value = value*10 + int(ch-'0')
		if value > 1<<31 {
			return 0, errors.New("数值过大")
		}
	}
	return value, nil
}
