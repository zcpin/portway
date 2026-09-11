package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/byteporter/ssh-tunnel/internal/app"
)

// newTestServer 用一份空配置启动只挂了路由的测试服务器。
func newTestServer(t *testing.T, token string) *httptest.Server {
	t.Helper()

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("log_level = \"info\"\n"), 0600); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}

	a, err := app.New(cfgPath)
	if err != nil {
		t.Fatalf("app.New() error = %v", err)
	}

	hub := NewHub()
	a.SetEmitter(hub)

	s := New(a, hub, "127.0.0.1:0", token)
	ts := httptest.NewServer(s.routes())
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, ts *httptest.Server, path string, headers map[string]string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", path, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestAuthMiddleware(t *testing.T) {
	ts := newTestServer(t, "secret")

	// 健康检查免认证
	if code := get(t, ts, "/api/health", nil); code != http.StatusOK {
		t.Errorf("/api/health 状态码 = %d，期望 200", code)
	}

	// 缺少 token
	if code := get(t, ts, "/api/tunnels", nil); code != http.StatusUnauthorized {
		t.Errorf("缺少 token 状态码 = %d，期望 401", code)
	}

	// 错误的 token
	if code := get(t, ts, "/api/tunnels", map[string]string{"X-Auth-Token": "wrong"}); code != http.StatusUnauthorized {
		t.Errorf("错误 token 状态码 = %d，期望 401", code)
	}

	// 正确的 token（两种携带方式）
	if code := get(t, ts, "/api/tunnels", map[string]string{"X-Auth-Token": "secret"}); code != http.StatusOK {
		t.Errorf("X-Auth-Token 状态码 = %d，期望 200", code)
	}
	if code := get(t, ts, "/api/status", map[string]string{"Authorization": "Bearer secret"}); code != http.StatusOK {
		t.Errorf("Authorization Bearer 状态码 = %d，期望 200", code)
	}
}

func TestCreateTunnelRejectsBadBody(t *testing.T) {
	ts := newTestServer(t, "secret")

	post := func(body string) int {
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/tunnels", strings.NewReader(body))
		if err != nil {
			t.Fatalf("构造请求失败: %v", err)
		}
		req.Header.Set("X-Auth-Token", "secret")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// 字段名写错时应立刻报错，而不是被静默忽略
	if code := post(`{"name":"x","local_prt":13306}`); code != http.StatusBadRequest {
		t.Errorf("未知字段状态码 = %d，期望 400", code)
	}

	// 超过上限的请求体应被拒绝
	big := `{"name":"x","padding":"` + strings.Repeat("a", maxRequestBody+1) + `"}`
	if code := post(big); code != http.StatusBadRequest {
		t.Errorf("超大请求体状态码 = %d，期望 400", code)
	}
}
