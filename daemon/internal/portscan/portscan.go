// Package portscan 枚举本机正在占用端口的进程，并支持结束进程与定位可执行文件。
//
// 与 tunnel 包的区别：隧道诊断针对某条已配置的隧道做连通性检查，回答的是
// 「这条隧道通不通」；这里回答的是「这个端口现在被谁占着」，与隧道配置无关。
//
// 平台相关的系统调用集中在 ports_<goos>.go 与 process_<goos>.go，本文件只保留
// 与平台无关的推导、判定与筛选逻辑，因此可以脱离真机做完整单元测试。
package portscan

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 协议取值。
const (
	ProtocolTCP = "tcp"
	ProtocolUDP = "udp"
)

// 端口状态。TCP 只有 listen 会进入列表，UDP 没有状态机、绑定了就是 bound。
//
// 保留这个字段而不是让调用方按 Protocol 推导，是为了将来若要纳入 TCP 的
// ESTABLISHED（当前明确不做）时不必改动结构。
const (
	StateListen = "listen"
	StateBound  = "bound"
)

// 监听地址范围。
const (
	ScopeAll      = "all"      // 0.0.0.0 / :: —— 绑定所有网卡
	ScopeLoopback = "loopback" // 127.0.0.1 / ::1 —— 仅本机可达
	ScopeSpecific = "specific" // 其它具体地址 —— 绑定到指定网卡
)

// PortEntry 是一个正在占用端口的进程视角条目。
//
// 进程信息字段（Executable / CommandLine / User / StartedAt）在权限不足时为空，
// 调用方必须把「空」理解为「读不到」而不是「没有」——见 Snapshot.Partial。
type PortEntry struct {
	Protocol    string `json:"protocol"`   // tcp / udp
	State       string `json:"state"`      // listen / bound
	LocalAddr   string `json:"local_addr"` // 0.0.0.0 / 127.0.0.1 / :: / ::1 / 具体网卡地址
	LocalPort   int    `json:"local_port"`
	Scope       string `json:"scope"`        // all / loopback / specific
	PID         int    `json:"pid"`          // 0 表示无法归属到进程
	ProcessName string `json:"process_name"` // 不含扩展名
	Executable  string `json:"executable"`
	CommandLine string `json:"command_line"`
	User        string `json:"user"`
	StartedAt   string `json:"started_at"` // RFC3339
	System      bool   `json:"system"`     // 系统关键进程，不允许结束
	Tunnel      string `json:"tunnel,omitempty"`
}

// Snapshot 是一次扫描的完整结果。
type Snapshot struct {
	Ports     []PortEntry `json:"ports"`
	ScannedAt string      `json:"scanned_at"`
	ElapsedMS int64       `json:"elapsed_ms"`
	// Partial 为 true 表示部分进程信息因权限不足而缺失。
	// 界面据此提示「以管理员身份运行可查看完整信息」。
	Partial  bool     `json:"partial"`
	Warnings []string `json:"warnings,omitempty"`
}

// Query 是列表筛选条件，各字段之间是 AND 关系。
type Query struct {
	Text     string // 模糊匹配：端口号 / PID / 进程名 / 路径 / 地址
	Protocol string // tcp / udp / 空表示全部
	Scope    string // all / loopback / specific / 空表示全部
	// System 为 nil 表示不过滤；否则按 System 字段过滤（true 只看系统进程）。
	System *bool
}

// KillResult 是一次结束进程操作的结果。
//
// 结束进程是异步的（进程退出需要时间），因此调用方应在操作后重新扫描一次列表，
// 用实际结果确认，而不是只信这个返回值。
type KillResult struct {
	PID   int    `json:"pid"`
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Code 取值：denied / not_found / protected / failed
	Code string `json:"code,omitempty"`
}

// 结束进程的失败分类。界面据此给出不同的提示文案。
const (
	CodeDenied    = "denied"    // 权限不足
	CodeNotFound  = "not_found" // 进程不存在（可能已经退出）
	CodeProtected = "protected" // 命中保护规则，拒绝操作
	CodeFailed    = "failed"    // 其它失败
)

// KillError 是带分类的结束进程错误。
type KillError struct {
	Code    string
	Message string
}

func (e *KillError) Error() string { return e.Message }

// isSystemProcess 判断一条记录是否属于系统关键进程。
//
// 不能只看进程名：Windows 上 PID 4（System）和 PID 0（Idle）是内核伪进程，
// 查询进程信息会失败，拿不到名字；而 PID 2 是 System 的别名，名字同样为空。
// 只按名字判定会让这些内核占用（445、135、5040 等）在「仅显示非系统进程」
// 筛选下漏出来——恰恰是最不该让用户去结束的一批。
func isSystemProcess(pid int, name string) bool {
	switch pid {
	case 0, 4:
		return true
	}
	return name != "" && systemProcessNames[strings.ToLower(name)]
}

// protected 返回拒绝结束该进程的原因；返回空字符串表示允许操作。
//
// 这是**尽力而为的白名单**：真正的权限边界由操作系统决定（非管理员结束系统进程
// 本来就会失败）。白名单的价值是给出清晰的提示，而不是「防止」——防止靠 OS。
//
// 判定放在这一层而不是界面层，因为这是安全边界，不能依赖调用方自觉。
func protected(pid int, name string) string {
	switch pid {
	case 0:
		return "PID 0 是系统空闲进程，不能结束"
	case 4:
		return "PID 4 是 Windows 系统进程，结束会导致系统崩溃"
	}
	if pid == selfPID {
		return "不能结束 Portway 自身进程"
	}
	if name != "" && systemProcessNames[strings.ToLower(name)] {
		return fmt.Sprintf("%s 是系统关键进程，结束会导致系统不稳定", name)
	}
	return ""
}

// selfPID 由平台文件填充（os.Getpid 在纯逻辑测试里不便于打桩，因此单独留一个变量）。
var selfPID = 0

// systemProcessNames 是各平台公认的系统关键进程白名单，键为小写进程名。
//
// 刻意保守：只收录结束之后会立刻让系统失去响应或崩溃的进程。像 svchost 这类
// 承载普通服务的进程不在其中——它同样可能是用户自己启动的服务，一刀切会让
// 功能失去意义。
var systemProcessNames = map[string]bool{
	// Windows
	"system":             true,
	"smss":               true,
	"csrss":              true,
	"wininit":            true,
	"winlogon":           true,
	"services":           true,
	"lsass":              true,
	"registry":           true,
	"memory compression": true,
	// Linux / macOS
	"init":     true,
	"systemd":  true,
	"kthreadd": true,
	"launchd":  true,
}

// scopeOf 由监听地址推导出可达范围。
//
// IPv6 不单列 scope：:: 归入 all、::1 归入 loopback，这样界面上的「仅本机」
// 筛选对 IPv6 同样有效；具体网卡地址归入 specific。
func scopeOf(addr string) string {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "", "0.0.0.0", "::":
		return ScopeAll
	case "127.0.0.1", "::1":
		return ScopeLoopback
	}
	// 127.0.0.0/8 的其它地址同样只在本机可达
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return ScopeLoopback
	}
	return ScopeSpecific
}

// matches 判断一条记录是否满足查询条件。
//
// 匹配规则（服务端与客户端共用同一套语义，避免行为不一致）：
//   - 纯数字：优先按端口号精确匹配，同时按 PID 精确匹配，两者都命中时都返回
//   - 其它：对进程名、路径、命令行、地址做不区分大小写的子串匹配
//   - 端口号也参与子串匹配（输入 330 能匹配到 3306），但精确命中的排在前面
func matches(entry PortEntry, query Query) bool {
	if query.Protocol != "" && entry.Protocol != query.Protocol {
		return false
	}
	if query.Scope != "" && entry.Scope != query.Scope {
		return false
	}
	if query.System != nil && entry.System != *query.System {
		return false
	}

	text := strings.TrimSpace(query.Text)
	if text == "" {
		return true
	}

	if _, err := strconv.Atoi(text); err == nil {
		// 纯数字：端口号或 PID 精确命中即算匹配
		if strconv.Itoa(entry.LocalPort) == text || strconv.Itoa(entry.PID) == text {
			return true
		}
	}

	lower := strings.ToLower(text)
	port := strconv.Itoa(entry.LocalPort)
	return strings.Contains(port, lower) ||
		strings.Contains(strings.ToLower(entry.ProcessName), lower) ||
		strings.Contains(strings.ToLower(entry.Executable), lower) ||
		strings.Contains(strings.ToLower(entry.CommandLine), lower) ||
		strings.Contains(strings.ToLower(entry.LocalAddr), lower)
}

// rank 给出排序权重，越小越靠前。
//
// 有查询词时按「精确匹配 → 前缀匹配 → 子串匹配」分组；无查询词时全部同级，
// 于是排序退化为「端口号升序、TCP 优先」。
func rank(entry PortEntry, query Query) int {
	text := strings.TrimSpace(query.Text)
	if text == "" {
		return 0
	}
	lower := strings.ToLower(text)

	if strconv.Itoa(entry.LocalPort) == text || strconv.Itoa(entry.PID) == text {
		return 0
	}
	if strings.HasPrefix(strconv.Itoa(entry.LocalPort), lower) ||
		strings.HasPrefix(strings.ToLower(entry.ProcessName), lower) {
		return 1
	}
	return 2
}

// sortEntries 就地排序：先按匹配权重，再按端口号升序，同端口号 TCP 优先，
// 最后按 PID 升序保证结果稳定。
//
// TCP 优先是因为隧道场景的主角是 TCP：同一端口同时被 TCP 和 UDP 占用时，
// 用户关心的几乎总是 TCP 那一条。
//
// 注意「端口号升序」是**数值**比较而不是字符串比较：字符串比较会把 33060
// 排在 3306 前面，与用户预期相反。
func sortEntries(entries []PortEntry, query Query) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if ra, rb := rank(a, query), rank(b, query); ra != rb {
			return ra < rb
		}
		if a.LocalPort != b.LocalPort {
			return a.LocalPort < b.LocalPort
		}
		if a.Protocol != b.Protocol {
			return a.Protocol == ProtocolTCP
		}
		return a.PID < b.PID
	})
}

// Filter 按查询条件筛选并排序，返回新切片（不修改入参）。
func Filter(entries []PortEntry, query Query) []PortEntry {
	out := make([]PortEntry, 0, len(entries))
	for _, entry := range entries {
		if matches(entry, query) {
			out = append(out, entry)
		}
	}
	sortEntries(out, query)
	return out
}

// Check 是单端口检测的结果。
type Check struct {
	Port    int         `json:"port"`
	InUse   bool        `json:"in_use"`
	Entries []PortEntry `json:"entries"`
}

// CheckPort 检测一个端口是否被占用。
//
// protocol 为空时同时检查 TCP 与 UDP；指定时只查一侧。隧道编辑器必须传 "tcp"：
// 隧道不涉及 UDP，把 UDP 冲突报成「端口不可用」会误导用户。
func CheckPort(entries []PortEntry, port int, protocol string) Check {
	result := Check{Port: port, Entries: []PortEntry{}}
	for _, entry := range entries {
		if entry.LocalPort != port {
			continue
		}
		if protocol != "" && entry.Protocol != protocol {
			continue
		}
		result.Entries = append(result.Entries, entry)
	}
	sortEntries(result.Entries, Query{})
	result.InUse = len(result.Entries) > 0
	return result
}

// TunnelPort 描述一条隧道占用的本地端口，用于把扫描结果标记上隧道名。
type TunnelPort struct {
	Name  string
	Host  string
	Port  int
	Proto string
}

// Annotate 把扫描结果里属于本机隧道的条目标上隧道名。
//
// 匹配是展示性的，不影响任何行为。价值在于：用户看到 13306 被占用时能立刻
// 知道「这是我自己的 mysql-prod 隧道占的，不用管」，而不是去杀自己的隧道。
//
// 只匹配本地监听：remote 模式（反向转发）的端口在远端，本机列表里看不到。
func Annotate(entries []PortEntry, tunnels []TunnelPort) {
	if len(tunnels) == 0 {
		return
	}
	index := make(map[string]string, len(tunnels))
	for _, t := range tunnels {
		host := t.Host
		if host == "" {
			host = "127.0.0.1"
		}
		proto := t.Proto
		if proto == "" {
			proto = ProtocolTCP
		}
		index[portKey(proto, host, t.Port)] = t.Name
	}
	for i := range entries {
		if name, ok := index[portKey(entries[i].Protocol, entries[i].LocalAddr, entries[i].LocalPort)]; ok {
			entries[i].Tunnel = name
		}
	}
}

func portKey(protocol, host string, port int) string {
	return protocol + "|" + strings.ToLower(host) + "|" + strconv.Itoa(port)
}

// ---------- 进程信息缓存 ----------

// processCache 缓存一次扫描内的进程信息。
//
// 一个进程可能监听多个端口（例如 svchost 同时绑十几个 UDP），补全进程信息
// 是整条链路上最贵的一步，因此按 PID 去重，同一个 PID 只查一次。
type processCache struct {
	mu      sync.Mutex
	entries map[int]*ProcessInfo
	missed  map[int]bool
}

// ProcessInfo 是一个进程的补充信息，字段为空表示读不到（通常是权限不足）。
type ProcessInfo struct {
	Name        string
	Executable  string
	CommandLine string
	User        string
	StartedAt   time.Time
}

func newProcessCache() *processCache {
	return &processCache{
		entries: make(map[int]*ProcessInfo),
		missed:  make(map[int]bool),
	}
}

// lookup 返回 PID 对应的进程信息；第二个返回值表示是否读到了信息。
func (c *processCache) lookup(pid int) (ProcessInfo, bool) {
	if pid <= 0 {
		return ProcessInfo{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if info, ok := c.entries[pid]; ok {
		return *info, true
	}
	if c.missed[pid] {
		return ProcessInfo{}, false
	}
	info, err := readProcessInfo(pid)
	if err != nil {
		c.missed[pid] = true
		return ProcessInfo{}, false
	}
	c.entries[pid] = &info
	return info, true
}

// ---------- 扫描入口 ----------

// Scan 枚举本机端口占用并补全进程信息。
//
// withCommandLine 为 false 时跳过命令行补全：Windows 上命令行走 WMI 查询，
// 首次调用约 200–500ms，而列表默认不展示命令行，没必要每次都付这个代价。
func Scan(withCommandLine bool) (Snapshot, error) {
	started := time.Now()
	raw, err := enumerate()
	if err != nil {
		return Snapshot{}, err
	}

	cache := newProcessCache()
	entries := make([]PortEntry, 0, len(raw))
	partial := false

	for _, item := range raw {
		entry := PortEntry{
			Protocol:  item.Protocol,
			State:     stateFor(item.Protocol),
			LocalAddr: item.LocalAddr,
			LocalPort: item.LocalPort,
			Scope:     scopeOf(item.LocalAddr),
			PID:       item.PID,
		}
		// System 必须在补全进程信息之前无条件判定：PID 0/4 这类内核伪进程
		// 读不到进程信息，如果只在 lookup 成功分支里赋值，它们会被误判成
		// 非系统进程，从而出现在「仅显示非系统进程」的结果里。
		entry.System = isSystemProcess(item.PID, "")
		if info, ok := cache.lookup(item.PID); ok {
			entry.ProcessName = info.Name
			entry.Executable = info.Executable
			entry.CommandLine = info.CommandLine
			entry.User = info.User
			if !info.StartedAt.IsZero() {
				entry.StartedAt = info.StartedAt.Format(time.RFC3339)
			}
			// 拿到进程名后重新判定一次：白名单里多数条目（smss、lsass 等）
			// 只能靠名字识别。
			entry.System = isSystemProcess(item.PID, info.Name)
			if entry.Executable == "" && !entry.System {
				// 进程存在但路径读不到，典型原因是权限不足。
				// 系统内核伪进程（PID 0/4）本来就没有用户态路径，不算「信息缺失」，
				// 否则每次扫描都会误报 partial。
				partial = true
			}
		} else if item.PID > 0 && !entry.System {
			// 端口能读到、进程读不到：同样是权限问题（内核伪进程除外）
			partial = true
		}
		entries = append(entries, entry)
	}

	sortEntries(entries, Query{})

	snapshot := Snapshot{
		Ports:     entries,
		ScannedAt: time.Now().Format(time.RFC3339Nano),
		ElapsedMS: time.Since(started).Milliseconds(),
		Partial:   partial,
	}
	if partial {
		snapshot.Warnings = append(snapshot.Warnings,
			"部分进程信息需要管理员权限才能查看")
	}
	if withCommandLine {
		// 命令行补全在平台层按需执行，这里只负责把结果写回。
		fillCommandLines(snapshot.Ports)
	}
	return snapshot, nil
}

func stateFor(protocol string) string {
	if protocol == ProtocolUDP {
		return StateBound
	}
	return StateListen
}

// ---------- 结束进程 ----------

// Kill 结束一个进程。
//
// force 为 true 时跳过优雅退出。注意 Windows 上没有真正的优雅终止手段
// （GenerateConsoleCtrlEvent 只对同控制台进程有效），因此该平台一律走
// TerminateProcess，force 不产生行为差异；Linux / macOS 上 force 为 false
// 时先发 SIGTERM 并等待宽限期。
//
// 保护规则在调用平台实现之前判定，命中即返回 CodeProtected。
func Kill(pid int, name string, force bool) KillResult {
	result := KillResult{PID: pid, Name: name}
	if reason := protected(pid, name); reason != "" {
		result.Code = CodeProtected
		result.Error = reason
		return result
	}
	if err := killProcess(pid, force); err != nil {
		result.Code = classifyKillError(err)
		result.Error = err.Error()
		return result
	}
	result.OK = true
	return result
}

// ResolvePath 把 PID 解析成可执行文件的绝对路径。
//
// 真正打开文件管理器由客户端执行，不由本包负责：daemon 可能是系统服务，
// 以 LocalSystem 身份 explorer.exe /select, 打开的窗口不属于用户桌面会话，
// 用户看不到。
func ResolvePath(pid int) (string, error) {
	if pid <= 0 {
		return "", &KillError{Code: CodeNotFound, Message: "无效的进程号"}
	}
	info, err := readProcessInfo(pid)
	if err != nil {
		return "", &KillError{Code: classifyKillError(err), Message: err.Error()}
	}
	if info.Executable == "" {
		return "", &KillError{Code: CodeDenied, Message: "无法读取该进程的可执行文件路径，可能需要管理员权限"}
	}
	return info.Executable, nil
}

// ReadProcessInfo 读取单个进程的信息（进程名、路径、用户、启动时间）。
//
// 单独导出是为了让调用方能在结束进程**之前**取到进程名：kill 之后进程就没了，
// 名字查不到，而界面要用它拼「已结束 xxx」的提示。
//
// 与扫描不同，这里只查一个 PID，开销很小（Windows 上走
// QueryFullProcessImageName，微秒级）。
func ReadProcessInfo(pid int) (ProcessInfo, error) {
	if pid <= 0 {
		return ProcessInfo{}, &KillError{Code: CodeNotFound, Message: "无效的进程号"}
	}
	return readProcessInfo(pid)
}
