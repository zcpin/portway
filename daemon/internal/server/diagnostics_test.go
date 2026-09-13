package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/byteporter/ssh-tunnel/internal/tunnel"
)

func TestTunnelDiagnosticsAreAuthenticatedAndPreserveConfiguration(t *testing.T) {
	a, s := snapshotTestServer(t)
	name := "database / 开发"
	cfg := snapshotTestTunnel(name, 15489)
	manual := false
	cfg.AutoStart = &manual
	if err := a.AddTunnel(cfg); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(a.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	status := a.GetStatus()
	for _, token := range []string{"", "wrong-token", snapshotTestToken} {
		req := httptest.NewRequest(http.MethodPost, "/api/tunnels/"+url.PathEscape(name)+"/diagnose", nil)
		req.Header.Set("X-Auth-Token", token)
		res := httptest.NewRecorder()
		s.routes().ServeHTTP(res, req)
		if token != snapshotTestToken {
			if res.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated diagnosis: %d", res.Code)
			}
			continue
		}
		var result tunnel.TunnelDiagnostic
		if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &result) != nil {
			t.Fatalf("diagnostic response: %d %s", res.Code, res.Body)
		}
		if result.Name != name || result.OK || len(result.Checks) != 3 || result.Checks[1].Status != "failed" {
			t.Fatalf("missing key was not reported at SSH stage: %+v", result)
		}
	}
	after, err := os.ReadFile(a.ConfigPath())
	if err != nil || string(before) != string(after) || !reflect.DeepEqual(status, a.GetStatus()) {
		t.Fatalf("diagnosis changed config or running state: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/tunnels/missing/diagnose", nil)
	req.Header.Set("X-Auth-Token", snapshotTestToken)
	res := httptest.NewRecorder()
	s.routes().ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("unknown tunnel: %d", res.Code)
	}
}

func TestConnectionDiagnosticsAreAuthenticatedAndDoNotSave(t *testing.T) {
	a, s := snapshotTestServer(t)
	before, err := os.ReadFile(a.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	body := `{"host":"127.0.0.1:1","user":"test","key_file":"missing-test-key"}`
	for _, token := range []string{"", snapshotTestToken} {
		req := httptest.NewRequest(http.MethodPost, "/api/ssh-connections/test", strings.NewReader(body))
		req.Header.Set("X-Auth-Token", token)
		res := httptest.NewRecorder()
		s.routes().ServeHTTP(res, req)
		if token == "" {
			if res.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated response: %d", res.Code)
			}
			continue
		}
		if res.Code != http.StatusOK {
			t.Fatalf("diagnostic response: %d %s", res.Code, res.Body)
		}
		var result tunnel.Diagnostic
		if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.OK || result.Error == "" {
			t.Fatalf("missing key should be reported: %+v", result)
		}
	}
	after, err := os.ReadFile(a.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) || len(a.GetTunnels()) != 0 {
		t.Fatal("diagnostics changed configuration")
	}
}
