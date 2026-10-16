package portscan

import (
	"testing"
)

func TestScopeOf(t *testing.T) {
	cases := []struct {
		addr string
		want string
	}{
		{"0.0.0.0", ScopeAll},
		{"::", ScopeAll},
		{"127.0.0.1", ScopeLoopback},
		{"::1", ScopeLoopback},
		{"127.0.0.2", ScopeLoopback},
		{"192.168.1.5", ScopeSpecific},
		{"10.0.0.1", ScopeSpecific},
		{"fe80::1", ScopeSpecific},
		{"", ScopeAll},
	}
	for _, c := range cases {
		if got := scopeOf(c.addr); got != c.want {
			t.Errorf("scopeOf(%q) = %q, want %q", c.addr, got, c.want)
		}
	}
}

func TestScopeOfHostPort(t *testing.T) {
	// 带端口的输入也要能正确推导
	if got := scopeOf("127.0.0.1:8080"); got != ScopeLoopback {
		t.Errorf("scopeOf with port = %q, want %q", got, ScopeLoopback)
	}
	if got := scopeOf("[::1]:8080"); got != ScopeLoopback {
		t.Errorf("scopeOf IPv6 with port = %q, want %q", got, ScopeLoopback)
	}
}

func TestProtected(t *testing.T) {
	// 保存并恢复，避免影响其它测试
	saved := selfPID
	defer func() { selfPID = saved }()
	selfPID = 4242

	cases := []struct {
		pid      int
		name     string
		rejected bool
	}{
		{0, "anything", true},
		{4, "System", true},
		{4242, "portway", true},
		{1000, "smss", true},
		{1000, "lsass", true},
		{1000, "services", true},
		{1000, "csrss", true},
		{1000, "SYSTEM", true}, // 大小写不敏感
		{1000, "systemd", true},
		{1000, "svchost", false}, // 承载普通服务，允许结束
		{1000, "chrome", false},
		{1000, "", false}, // 名字读不到时仍允许（由 OS 兜底）
	}
	for _, c := range cases {
		got := protected(c.pid, c.name) != ""
		if got != c.rejected {
			t.Errorf("protected(%d, %q) rejected = %v, want %v", c.pid, c.name, got, c.rejected)
		}
	}
}

func TestKillRejectsProtected(t *testing.T) {
	saved := selfPID
	defer func() { selfPID = saved }()
	selfPID = 4242

	result := Kill(4, "System", false)
	if result.OK {
		t.Fatal("Kill(4) should be rejected")
	}
	if result.Code != CodeProtected {
		t.Errorf("Kill(4) code = %q, want %q", result.Code, CodeProtected)
	}
	if result.Error == "" {
		t.Error("Kill(4) should carry a reason")
	}
}

func TestKillRejectsSelf(t *testing.T) {
	saved := selfPID
	defer func() { selfPID = saved }()
	selfPID = 4242

	result := Kill(4242, "portway", true)
	if result.OK || result.Code != CodeProtected {
		t.Errorf("Kill(self) = %+v, want protected rejection", result)
	}
}

func TestMatchesText(t *testing.T) {
	entry := PortEntry{
		Protocol:    ProtocolTCP,
		LocalAddr:   "0.0.0.0",
		LocalPort:   3306,
		PID:         1234,
		ProcessName: "mysqld",
		Executable:  `C:\Program Files\MySQL\bin\mysqld.exe`,
	}

	cases := []struct {
		text string
		want bool
	}{
		{"", true},
		{"3306", true},   // 端口号精确
		{"1234", true},   // PID 精确
		{"330", true},    // 端口号子串
		{"mysql", true},  // 进程名子串
		{"MYSQLD", true}, // 大小写不敏感
		{"Program Files", true},
		{"0.0.0.0", true},
		{"9999", false},
		{"nginx", false},
	}
	for _, c := range cases {
		if got := matches(entry, Query{Text: c.text}); got != c.want {
			t.Errorf("matches(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestMatchesFilters(t *testing.T) {
	tcp := PortEntry{Protocol: ProtocolTCP, Scope: ScopeAll, System: false, LocalPort: 80}
	udpSystem := PortEntry{Protocol: ProtocolUDP, Scope: ScopeLoopback, System: true, LocalPort: 53}

	if matches(tcp, Query{Protocol: ProtocolUDP}) {
		t.Error("tcp entry should not match protocol=udp")
	}
	if !matches(udpSystem, Query{Protocol: ProtocolUDP}) {
		t.Error("udp entry should match protocol=udp")
	}
	if matches(tcp, Query{Scope: ScopeLoopback}) {
		t.Error("scope=all entry should not match scope=loopback")
	}
	if !matches(udpSystem, Query{Scope: ScopeLoopback}) {
		t.Error("scope=loopback entry should match scope=loopback")
	}

	onlySystem := true
	onlyUser := false
	if !matches(udpSystem, Query{System: &onlySystem}) {
		t.Error("system entry should match system=true")
	}
	if matches(udpSystem, Query{System: &onlyUser}) {
		t.Error("system entry should not match system=false")
	}
	if !matches(tcp, Query{System: &onlyUser}) {
		t.Error("non-system entry should match system=false")
	}
	if !matches(udpSystem, Query{}) {
		t.Error("nil system filter should not filter anything")
	}
}

func TestMatchesFiltersAreAnded(t *testing.T) {
	entry := PortEntry{Protocol: ProtocolTCP, Scope: ScopeAll, System: false, LocalPort: 8080, ProcessName: "nginx"}

	// 三个条件全部满足
	if !matches(entry, Query{Text: "nginx", Protocol: ProtocolTCP, Scope: ScopeAll}) {
		t.Error("all conditions satisfied should match")
	}
	// 任一条件不满足即不匹配
	if matches(entry, Query{Text: "nginx", Protocol: ProtocolUDP}) {
		t.Error("one failing condition should reject")
	}
	if matches(entry, Query{Text: "apache", Protocol: ProtocolTCP}) {
		t.Error("failing text should reject")
	}
}

func TestFilterSortsByPort(t *testing.T) {
	entries := []PortEntry{
		{Protocol: ProtocolTCP, LocalPort: 8080, PID: 2},
		{Protocol: ProtocolTCP, LocalPort: 80, PID: 1},
		{Protocol: ProtocolTCP, LocalPort: 3306, PID: 3},
	}
	got := Filter(entries, Query{})
	want := []int{80, 3306, 8080}
	for i, port := range want {
		if got[i].LocalPort != port {
			t.Errorf("position %d = port %d, want %d", i, got[i].LocalPort, port)
		}
	}
}

func TestFilterPrefersTCPOverUDP(t *testing.T) {
	entries := []PortEntry{
		{Protocol: ProtocolUDP, LocalPort: 53, PID: 2},
		{Protocol: ProtocolTCP, LocalPort: 53, PID: 1},
	}
	got := Filter(entries, Query{})
	if got[0].Protocol != ProtocolTCP {
		t.Errorf("first entry protocol = %q, want tcp", got[0].Protocol)
	}
}

func TestFilterRanksExactMatchFirst(t *testing.T) {
	entries := []PortEntry{
		{Protocol: ProtocolTCP, LocalPort: 33060, PID: 1, ProcessName: "a"},
		{Protocol: ProtocolTCP, LocalPort: 13306, PID: 2, ProcessName: "b"},
		{Protocol: ProtocolTCP, LocalPort: 3306, PID: 3, ProcessName: "c"},
	}
	got := Filter(entries, Query{Text: "3306"})
	if got[0].LocalPort != 3306 {
		t.Errorf("exact match should rank first, got port %d", got[0].LocalPort)
	}
	// 其余两条：33060 是前缀命中（rank 1），13306 只是子串命中（rank 2），
	// 因此 33060 排在 13306 前面——这正是「前缀匹配优先于子串匹配」的体现。
	if got[1].LocalPort != 33060 || got[2].LocalPort != 13306 {
		t.Errorf("remaining order = %d, %d, want 33060, 13306", got[1].LocalPort, got[2].LocalPort)
	}
}

func TestFilterSubstringMatchesSortByPort(t *testing.T) {
	// 两条都是子串命中（rank 相同）时，退化为按端口号数值升序。
	// 用 "3306" 查 13306 与 23306：两者都不是前缀，rank 都是 2。
	entries := []PortEntry{
		{Protocol: ProtocolTCP, LocalPort: 23306, PID: 1},
		{Protocol: ProtocolTCP, LocalPort: 13306, PID: 2},
	}
	got := Filter(entries, Query{Text: "3306"})
	if got[0].LocalPort != 13306 || got[1].LocalPort != 23306 {
		t.Errorf("order = %d, %d, want 13306, 23306", got[0].LocalPort, got[1].LocalPort)
	}
}

func TestFilterRanksPIDMatchFirst(t *testing.T) {
	entries := []PortEntry{
		{Protocol: ProtocolTCP, LocalPort: 80, PID: 1},
		{Protocol: ProtocolTCP, LocalPort: 443, PID: 1234},
	}
	got := Filter(entries, Query{Text: "1234"})
	if got[0].PID != 1234 {
		t.Errorf("PID exact match should rank first, got PID %d", got[0].PID)
	}
}

func TestFilterDoesNotMutateInput(t *testing.T) {
	entries := []PortEntry{
		{Protocol: ProtocolTCP, LocalPort: 8080},
		{Protocol: ProtocolTCP, LocalPort: 80},
	}
	_ = Filter(entries, Query{})
	if entries[0].LocalPort != 8080 {
		t.Error("Filter should not reorder the input slice")
	}
}

func TestFilterEmptyResultIsValid(t *testing.T) {
	entries := []PortEntry{{Protocol: ProtocolTCP, LocalPort: 80, System: true}}
	onlyUser := false
	got := Filter(entries, Query{System: &onlyUser})
	if len(got) != 0 {
		t.Errorf("expected empty result, got %d entries", len(got))
	}
	if got == nil {
		t.Error("Filter should return an empty slice, not nil")
	}
}

func TestCheckPort(t *testing.T) {
	entries := []PortEntry{
		{Protocol: ProtocolTCP, LocalPort: 3306, PID: 1, ProcessName: "mysqld"},
		{Protocol: ProtocolUDP, LocalPort: 3306, PID: 2, ProcessName: "other"},
		{Protocol: ProtocolTCP, LocalPort: 8080, PID: 3},
	}

	// 不指定协议：两侧都查
	both := CheckPort(entries, 3306, "")
	if !both.InUse || len(both.Entries) != 2 {
		t.Errorf("CheckPort(3306, \"\") = %+v, want 2 entries", both)
	}

	// 只查 TCP：隧道编辑器要的语义
	tcpOnly := CheckPort(entries, 3306, ProtocolTCP)
	if !tcpOnly.InUse || len(tcpOnly.Entries) != 1 {
		t.Errorf("CheckPort(3306, tcp) = %+v, want 1 entry", tcpOnly)
	}
	if tcpOnly.Entries[0].Protocol != ProtocolTCP {
		t.Errorf("entry protocol = %q, want tcp", tcpOnly.Entries[0].Protocol)
	}

	// 只查 UDP
	udpOnly := CheckPort(entries, 3306, ProtocolUDP)
	if len(udpOnly.Entries) != 1 || udpOnly.Entries[0].Protocol != ProtocolUDP {
		t.Errorf("CheckPort(3306, udp) = %+v, want 1 udp entry", udpOnly)
	}

	// 空闲端口
	free := CheckPort(entries, 9999, "")
	if free.InUse || len(free.Entries) != 0 {
		t.Errorf("CheckPort(9999) = %+v, want free", free)
	}
	if free.Entries == nil {
		t.Error("Entries should be an empty slice, not nil")
	}
}

func TestAnnotate(t *testing.T) {
	entries := []PortEntry{
		{Protocol: ProtocolTCP, LocalAddr: "127.0.0.1", LocalPort: 13306},
		{Protocol: ProtocolTCP, LocalAddr: "0.0.0.0", LocalPort: 13306},
		{Protocol: ProtocolTCP, LocalAddr: "127.0.0.1", LocalPort: 9999},
	}
	tunnels := []TunnelPort{
		{Name: "mysql-prod", Host: "127.0.0.1", Port: 13306, Proto: ProtocolTCP},
	}
	Annotate(entries, tunnels)

	if entries[0].Tunnel != "mysql-prod" {
		t.Errorf("loopback entry tunnel = %q, want mysql-prod", entries[0].Tunnel)
	}
	if entries[1].Tunnel != "" {
		t.Errorf("0.0.0.0 entry should not match a 127.0.0.1 tunnel, got %q", entries[1].Tunnel)
	}
	if entries[2].Tunnel != "" {
		t.Errorf("unrelated port should stay unannotated, got %q", entries[2].Tunnel)
	}
}

func TestAnnotateDefaultsToLoopbackTCP(t *testing.T) {
	entries := []PortEntry{{Protocol: ProtocolTCP, LocalAddr: "127.0.0.1", LocalPort: 8080}}
	Annotate(entries, []TunnelPort{{Name: "web", Port: 8080}})
	if entries[0].Tunnel != "web" {
		t.Errorf("empty host/proto should default to 127.0.0.1/tcp, got %q", entries[0].Tunnel)
	}
}

func TestAnnotateNoTunnels(t *testing.T) {
	entries := []PortEntry{{Protocol: ProtocolTCP, LocalAddr: "127.0.0.1", LocalPort: 8080}}
	Annotate(entries, nil)
	if entries[0].Tunnel != "" {
		t.Error("no tunnels should leave entries untouched")
	}
}

func TestResolvePathRejectsInvalidPID(t *testing.T) {
	if _, err := ResolvePath(0); err == nil {
		t.Error("ResolvePath(0) should fail")
	}
	if _, err := ResolvePath(-1); err == nil {
		t.Error("ResolvePath(-1) should fail")
	}
}

func TestStateFor(t *testing.T) {
	if got := stateFor(ProtocolTCP); got != StateListen {
		t.Errorf("stateFor(tcp) = %q, want %q", got, StateListen)
	}
	if got := stateFor(ProtocolUDP); got != StateBound {
		t.Errorf("stateFor(udp) = %q, want %q", got, StateBound)
	}
}

func TestProcessCacheDeduplicates(t *testing.T) {
	cache := newProcessCache()
	// 同一个 PID 查两次，第二次应命中缓存（无法直接断言调用次数，
	// 但两次结果必须一致，且不会 panic）
	first, ok1 := cache.lookup(0)
	second, ok2 := cache.lookup(0)
	if ok1 != ok2 {
		t.Error("cache should return consistent results for the same PID")
	}
	if first != second {
		t.Error("cache should return identical values for the same PID")
	}
}
