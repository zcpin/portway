package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/byteporter/ssh-tunnel/internal/tunnel"
)

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
