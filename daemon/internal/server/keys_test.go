package server

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestKeySecurityEndpointsAndSessionUnlock(t *testing.T) {
	a, s := snapshotTestServer(t)
	for _, path := range []string{"/api/keys/unlock", "/api/keys/lock", "/api/ssh-connections/host-key", "/api/ssh-connections/trust"} {
		res := httptest.NewRecorder()
		s.routes().ServeHTTP(res, httptest.NewRequest(http.MethodPost, path, nil))
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("%s is not protected", path)
		}
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret := "fixture-passphrase-never-exported"
	block, err := ssh.MarshalPrivateKeyWithPassphrase(key, "fixture", []byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(a.ConfigPath()), "encrypted.key")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	request := func(route string, payload any) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, route, bytes.NewReader(body))
		req.Header.Set("X-Auth-Token", snapshotTestToken)
		res := httptest.NewRecorder()
		s.routes().ServeHTTP(res, req)
		return res
	}
	bad := request("/api/keys/unlock", map[string]string{"path": path, "passphrase": "do-not-echo"})
	if bad.Code != http.StatusBadRequest || strings.Contains(bad.Body.String(), "do-not-echo") {
		t.Fatal("unlock error disclosed request")
	}
	res := request("/api/keys/unlock", map[string]string{"path": path, "passphrase": secret})
	if res.Code != http.StatusOK {
		t.Fatalf("unlock: %s", res.Body)
	}
	info, err := a.StatKey(path)
	if err != nil || !info.Encrypted || !info.Unlocked {
		t.Fatalf("key info: %+v %v", info, err)
	}
	exported, err := a.ExportConfig()
	if err != nil {
		t.Fatal(err)
	}
	logs, err := json.Marshal(a.GetLogs())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(exported.Content+string(logs)+res.Body.String(), secret) {
		t.Fatal("passphrase leaked")
	}
	res = request("/api/keys/lock", map[string]string{"path": path})
	if res.Code != http.StatusOK {
		t.Fatalf("lock: %s", res.Body)
	}
	info, err = a.StatKey(path)
	if err != nil || info.Unlocked {
		t.Fatal("key remains unlocked")
	}
}
