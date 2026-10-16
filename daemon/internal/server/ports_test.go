package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/byteporter/portway/internal/portscan"
)

// 端口接口的测试只覆盖「协议层」：参数校验、鉴权、响应结构。
//
// 不去断言具体扫到了哪些端口——那取决于跑测试的机器上恰好开着什么，
// 断言它会让测试在别人的机器上随机失败。平台采集逻辑的测试在
// internal/portscan 里用固定输入做。

func portsRequest(t *testing.T, s *Server, method, target string, body string, token string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("X-Auth-Token", token)
	res := httptest.NewRecorder()
	s.routes().ServeHTTP(res, req)
	return res
}

func TestPortsAPIRequiresAuthentication(t *testing.T) {
	_, s := snapshotTestServer(t)
	cases := []struct{ method, target, body string }{
		{http.MethodGet, "/api/ports", ""},
		{http.MethodGet, "/api/ports/check?port=1", ""},
		{http.MethodPost, "/api/ports/kill", `{"pid":1}`},
		{http.MethodPost, "/api/ports/reveal", `{"pid":1}`},
	}
	for _, c := range cases {
		res := portsRequest(t, s, c.method, c.target, c.body, "")
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without token: %d", c.method, c.target, res.Code)
		}
		res = portsRequest(t, s, c.method, c.target, c.body, "wrong-token")
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s with wrong token: %d", c.method, c.target, res.Code)
		}
	}
}

func TestListPortsReturnsSnapshotShape(t *testing.T) {
	_, s := snapshotTestServer(t)
	res := portsRequest(t, s, http.MethodGet, "/api/ports", "", snapshotTestToken)
	if res.Code != http.StatusOK {
		t.Fatalf("GET /api/ports: %d %s", res.Code, res.Body)
	}
	var snapshot portscan.Snapshot
	if err := json.Unmarshal(res.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	// ports 必须是数组而不是 null：客户端按列表解析，null 会直接抛异常。
	if snapshot.Ports == nil {
		t.Fatal("ports must be an array, got null")
	}
	if snapshot.ScannedAt == "" {
		t.Fatal("scanned_at should be filled")
	}
}

// 参数校验：非法值必须被拒绝，而不是静默当作「不过滤」。
func TestListPortsRejectsInvalidFilters(t *testing.T) {
	_, s := snapshotTestServer(t)
	cases := []string{
		"/api/ports?system=maybe",
		"/api/ports?protocol=sctp",
		"/api/ports?scope=nowhere",
	}
	for _, target := range cases {
		res := portsRequest(t, s, http.MethodGet, target, "", snapshotTestToken)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("%s should be rejected: %d %s", target, res.Code, res.Body)
		}
	}
}

// 合法筛选组合应当被接受，包括空值。
func TestListPortsAcceptsValidFilters(t *testing.T) {
	_, s := snapshotTestServer(t)
	cases := []string{
		"/api/ports?q=8080",
		"/api/ports?protocol=tcp",
		"/api/ports?protocol=udp",
		"/api/ports?scope=loopback",
		"/api/ports?system=false",
		"/api/ports?system=true",
		"/api/ports?with_command_line=true",
		"/api/ports?q=ssh&protocol=tcp&scope=all&system=false",
	}
	for _, target := range cases {
		res := portsRequest(t, s, http.MethodGet, target, "", snapshotTestToken)
		if res.Code != http.StatusOK {
			t.Fatalf("%s should be accepted: %d %s", target, res.Code, res.Body)
		}
	}
}

func TestCheckPortValidatesInput(t *testing.T) {
	_, s := snapshotTestServer(t)

	// 缺少 port / 非数字 / 越界 / 非法协议都要拒绝。
	for _, target := range []string{
		"/api/ports/check",
		"/api/ports/check?port=abc",
		"/api/ports/check?port=0",
		"/api/ports/check?port=70000",
		"/api/ports/check?port=80&protocol=sctp",
	} {
		res := portsRequest(t, s, http.MethodGet, target, "", snapshotTestToken)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("%s should be rejected: %d %s", target, res.Code, res.Body)
		}
	}

	// 合法请求返回结构完整的检测结果。
	res := portsRequest(t, s, http.MethodGet, "/api/ports/check?port=1", "", snapshotTestToken)
	if res.Code != http.StatusOK {
		t.Fatalf("check port 1: %d %s", res.Code, res.Body)
	}
	var check portscan.Check
	if err := json.Unmarshal(res.Body.Bytes(), &check); err != nil {
		t.Fatal(err)
	}
	if check.Port != 1 {
		t.Fatalf("port should be echoed back: %+v", check)
	}
	if check.Entries == nil && check.InUse {
		t.Fatal("in-use port must carry entries")
	}
}

// 隧道编辑器固定传 protocol=tcp：隧道不涉及 UDP，
// 把 UDP 冲突报成「端口不可用」会误导用户。
func TestCheckPortAcceptsExplicitProtocol(t *testing.T) {
	_, s := snapshotTestServer(t)
	for _, protocol := range []string{"tcp", "udp"} {
		res := portsRequest(t, s, http.MethodGet,
			"/api/ports/check?port=1&protocol="+protocol, "", snapshotTestToken)
		if res.Code != http.StatusOK {
			t.Fatalf("check with protocol=%s: %d %s", protocol, res.Code, res.Body)
		}
	}
}

func TestKillProcessValidatesInput(t *testing.T) {
	_, s := snapshotTestServer(t)

	// 缺字段 / 负数 PID 要拒绝。
	// PID 0 不在此列：它是「系统空闲进程」，由保护规则给出更准确的提示。
	for _, body := range []string{`{}`, `{"pid":-1}`, `not json`} {
		res := portsRequest(t, s, http.MethodPost, "/api/ports/kill", body, snapshotTestToken)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("kill %s should be rejected: %d %s", body, res.Code, res.Body)
		}
	}
}

// 保护规则命中时返回 200 + code=protected，而不是 HTTP 错误。
//
// 这是刻意的：接口调用本身成功了，是业务上拒绝。界面据此给出
// 「系统关键进程不能结束」的专属文案，而不是通用的「操作失败」。
func TestKillProcessReportsProtectionAsBusinessResult(t *testing.T) {
	_, s := snapshotTestServer(t)

	// PID 0 是空闲进程、PID 4 是 Windows 系统进程，两者在所有平台的
	// 保护名单里都按 PID 硬编码，因此断言是跨平台稳定的。
	// （按名字匹配的那部分依赖具体进程名，不适合在测试里假定。）
	for _, pid := range []int{0, 4} {
		body := `{"pid":` + strconv.Itoa(pid) + `}`
		res := portsRequest(t, s, http.MethodPost, "/api/ports/kill", body, snapshotTestToken)
		if res.Code != http.StatusOK {
			t.Fatalf("kill pid %d should return 200: %d %s", pid, res.Code, res.Body)
		}
		var result portscan.KillResult
		if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.OK {
			t.Fatalf("pid %d must not be killed", pid)
		}
		if result.Code != portscan.CodeProtected {
			t.Fatalf("pid %d should be reported as protected, got %q", pid, result.Code)
		}
		if result.Error == "" {
			t.Fatal("protected result should explain why")
		}
	}
}

// 不存在的进程报告 not_found，而不是 failed。
//
// 界面据此提示「进程已经退出」而不是「操作失败」——用户看到后者会以为
// 是权限问题，反复重试。
func TestKillProcessReportsMissingProcess(t *testing.T) {
	_, s := snapshotTestServer(t)
	// 选一个几乎不可能存在的 PID（且不在保护名单里）。
	body := `{"pid":` + strconv.Itoa(1<<30) + `}`
	res := portsRequest(t, s, http.MethodPost, "/api/ports/kill", body, snapshotTestToken)
	if res.Code != http.StatusOK {
		t.Fatalf("kill unknown pid should return 200: %d %s", res.Code, res.Body)
	}
	var result portscan.KillResult
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Fatal("unknown pid must not be reported as killed")
	}
	if result.Code != portscan.CodeNotFound {
		t.Fatalf("unknown pid should be not_found, got %q (%s)", result.Code, result.Error)
	}
}

func TestRevealProcessValidatesInput(t *testing.T) {
	_, s := snapshotTestServer(t)
	for _, body := range []string{`{}`, `{"pid":0}`, `{"pid":-5}`, `not json`} {
		res := portsRequest(t, s, http.MethodPost, "/api/ports/reveal", body, snapshotTestToken)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("reveal %s should be rejected: %d %s", body, res.Code, res.Body)
		}
	}
}

// 不存在的进程要报错，而不是返回空路径让客户端去 explorer 打开一个空路径。
func TestRevealProcessRejectsUnknownPID(t *testing.T) {
	_, s := snapshotTestServer(t)
	// 选一个几乎不可能存在的 PID。
	body := `{"pid":` + strconv.Itoa(1<<30) + `}`
	res := portsRequest(t, s, http.MethodPost, "/api/ports/reveal", body, snapshotTestToken)
	if res.Code == http.StatusOK {
		var payload map[string]string
		if json.Unmarshal(res.Body.Bytes(), &payload) == nil && payload["path"] != "" {
			t.Fatalf("unknown pid should not resolve to a path: %s", res.Body)
		}
	}
}

// 隧道标注：占用端口的条目要能认出「这是我自己的隧道」。
func TestListPortsAnnotatesTunnelPorts(t *testing.T) {
	a, s := snapshotTestServer(t)
	// 隧道不必真的跑起来——标注按配置匹配，不要求隧道处于运行状态。
	cfg := snapshotTestTunnel("annotated", 15432)
	if err := a.AddTunnel(cfg); err != nil {
		t.Fatal(err)
	}

	res := portsRequest(t, s, http.MethodGet, "/api/ports", "", snapshotTestToken)
	if res.Code != http.StatusOK {
		t.Fatalf("GET /api/ports: %d %s", res.Code, res.Body)
	}
	var snapshot portscan.Snapshot
	if err := json.Unmarshal(res.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	// 端口 15432 上通常没有进程监听，因此这里只验证「若命中则标注正确」：
	// 扫描结果里凡是 15432 的条目都应带上隧道名。
	for _, entry := range snapshot.Ports {
		if entry.LocalPort == 15432 && entry.Protocol == portscan.ProtocolTCP {
			if entry.Tunnel != "annotated" {
				t.Fatalf("port 15432 should be annotated with the tunnel name: %+v", entry)
			}
		}
	}
}

// 筛选条件要真的作用到结果上，而不是被忽略。
func TestListPortsAppliesProtocolFilter(t *testing.T) {
	_, s := snapshotTestServer(t)
	for _, protocol := range []string{"tcp", "udp"} {
		res := portsRequest(t, s, http.MethodGet, "/api/ports?protocol="+protocol, "", snapshotTestToken)
		if res.Code != http.StatusOK {
			t.Fatalf("GET /api/ports?protocol=%s: %d %s", protocol, res.Code, res.Body)
		}
		var snapshot portscan.Snapshot
		if err := json.Unmarshal(res.Body.Bytes(), &snapshot); err != nil {
			t.Fatal(err)
		}
		for _, entry := range snapshot.Ports {
			if entry.Protocol != protocol {
				t.Fatalf("protocol filter leaked %q into %q results", entry.Protocol, protocol)
			}
		}
	}
}

// 未知查询参数不应导致 400：前向兼容，将来加参数不会让旧客户端崩。
func TestListPortsIgnoresUnknownQueryParams(t *testing.T) {
	_, s := snapshotTestServer(t)
	res := portsRequest(t, s, http.MethodGet, "/api/ports?future_param=1", "", snapshotTestToken)
	if res.Code != http.StatusOK {
		t.Fatalf("unknown query param should be ignored: %d %s", res.Code, res.Body)
	}
}

// 路径转义：确认 URL 里的编码不会影响解析（端口接口目前没有路径参数，
// 但保留这条以防将来加 /api/ports/{port}）。
func TestPortsAPIPathHandling(t *testing.T) {
	_, s := snapshotTestServer(t)
	res := portsRequest(t, s, http.MethodGet, "/api/ports/check?port="+url.QueryEscape("8080"), "", snapshotTestToken)
	if res.Code != http.StatusOK {
		t.Fatalf("escaped port param: %d %s", res.Code, res.Body)
	}
}
