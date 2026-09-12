package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/byteporter/ssh-tunnel/internal/config"
)

func TestConfigTransferEndpointsRequireAuthentication(t *testing.T) {
	_, s := snapshotTestServer(t)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/config/export"}, {http.MethodGet, "/api/config/backups"},
		{http.MethodGet, "/api/config/backups/example"}, {http.MethodPost, "/api/config/preview"},
		{http.MethodPost, "/api/config/import"},
	} {
		res := httptest.NewRecorder()
		s.routes().ServeHTTP(res, httptest.NewRequest(route.method, route.path, nil))
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", route.path, res.Code)
		}
	}
}

func TestImportAPIRevisionAndApplicationState(t *testing.T) {
	a, s := snapshotTestServer(t)
	request := func(path string, input any) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("X-Auth-Token", snapshotTestToken)
		res := httptest.NewRecorder()
		s.routes().ServeHTTP(res, req)
		return res
	}
	input := config.ImportRequest{Mode: "merge", Content: "log_level = 'debug'"}
	if res := request("/api/config/import", input); res.Code != http.StatusConflict {
		t.Fatalf("missing revision: %d", res.Code)
	}
	preview := func() string {
		t.Helper()
		res := request("/api/config/preview", input)
		if res.Code != http.StatusOK {
			t.Fatalf("preview: %d %s", res.Code, res.Body)
		}
		var result config.ImportPreview
		if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Revision
	}
	input.Revision = preview()
	settings := a.GetGlobalSettings()
	settings.LogLevel = "warn"
	if err := a.SetGlobalSettings(settings); err != nil {
		t.Fatal(err)
	}
	if res := request("/api/config/import", input); res.Code != http.StatusConflict || a.GetGlobalSettings().LogLevel != "warn" {
		t.Fatal("stale import changed application state")
	}
	input.Revision = preview()
	res := request("/api/config/import", input)
	if res.Code != http.StatusOK || a.GetGlobalSettings().LogLevel != "debug" {
		t.Fatalf("import: %d %s", res.Code, res.Body)
	}
	var backup config.ConfigBackup
	if err := json.Unmarshal(res.Body.Bytes(), &backup); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReadBackup(backup.Name); err != nil {
		t.Fatalf("missing backup: %v", err)
	}
}
