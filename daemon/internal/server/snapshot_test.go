package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/byteporter/ssh-tunnel/internal/app"
	"github.com/byteporter/ssh-tunnel/internal/config"
	"github.com/byteporter/ssh-tunnel/internal/logger"
	"github.com/coder/websocket"
)

const snapshotTestToken = "snapshot-test-token"

func snapshotTestServer(t *testing.T) (*app.App, *Server) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("log_level = \"info\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := app.New(path)
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	a.SetEmitter(hub)
	t.Cleanup(func() {
		logger.SetGlobalLogHook(nil)
		a.Stop()
	})
	return a, New(a, hub, "127.0.0.1:0", snapshotTestToken)
}

func snapshotTestTunnel(name string, port int) config.Tunnel {
	return config.Tunnel{
		Name: name, LocalPort: port, RemoteHost: "127.0.0.1", RemotePort: 5432,
		SSHHost: "127.0.0.1:1", SSHUser: "tester", KeyFile: "keys/missing",
		HostKeyCheck: config.HostKeyCheckKnownHosts, KnownHostsFile: "keys/known_hosts",
	}
}

func TestTunnelAPIEditPreservesRawSecurityFields(t *testing.T) {
	a, s := snapshotTestServer(t)
	original := snapshotTestTunnel("database", 15432)
	if err := a.AddTunnel(original); err != nil {
		t.Fatal(err)
	}
	handler := s.routes()
	get := httptest.NewRequest(http.MethodGet, "/api/tunnels", nil)
	get.Header.Set("X-Auth-Token", snapshotTestToken)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, get)
	if res.Code != http.StatusOK {
		t.Fatalf("GET tunnels: %d %s", res.Code, res.Body)
	}
	var tunnels []config.Tunnel
	if err := json.Unmarshal(res.Body.Bytes(), &tunnels); err != nil {
		t.Fatal(err)
	}
	if len(tunnels) != 1 || !reflect.DeepEqual(tunnels[0], original) {
		t.Fatalf("GET did not preserve raw configuration: %+v", tunnels)
	}

	// 模拟客户端仅修改远端端口后，把 GET 返回的配置提交回来。
	tunnels[0].RemotePort = 6432
	body, err := json.Marshal(tunnels[0])
	if err != nil {
		t.Fatal(err)
	}
	put := httptest.NewRequest(http.MethodPut, "/api/tunnels/database", bytes.NewReader(body))
	put.Header.Set("X-Auth-Token", snapshotTestToken)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, put)
	if res.Code != http.StatusOK {
		t.Fatalf("PUT tunnel: %d %s", res.Code, res.Body)
	}
	persisted, err := config.Load(a.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Tunnels) != 1 || !reflect.DeepEqual(persisted.Tunnels[0], tunnels[0]) {
		t.Fatalf("edit changed unrelated fields: %+v", persisted.Tunnels)
	}
}

type tunnelEvent struct {
	Type     string           `json:"type"`
	Snapshot []app.TunnelInfo `json:"snapshot"`
	Status   map[string]bool  `json:"status"`
}

func readTunnelEvent(t *testing.T, conn *websocket.Conn, eventType string) tunnelEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("no %s event on connection: %v", eventType, err)
		}
		var event tunnelEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == eventType {
			return event
		}
	}
}

func TestWebSocketSnapshotResyncsOfflineChanges(t *testing.T) {
	a, s := snapshotTestServer(t)
	kept := snapshotTestTunnel("kept", 15432)
	removed := snapshotTestTunnel("removed", 15433)
	for _, tunnel := range []config.Tunnel{kept, removed} {
		if err := a.AddTunnel(tunnel); err != nil {
			t.Fatal(err)
		}
		// 使用不存在的私钥，不访问外部 SSH 服务；运行状态仍可启停。
		if err := a.StartTunnel(tunnel.Name); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(s.routes())
	t.Cleanup(ts.Close)
	dial := func() *websocket.Conn {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", &websocket.DialOptions{
			HTTPHeader: http.Header{"X-Auth-Token": []string{snapshotTestToken}},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.CloseNow() })
		return conn
	}

	first := dial()
	initial := readTunnelEvent(t, first, "snapshot").Snapshot
	if len(initial) != 2 || !initial[0].IsRunning || !initial[1].IsRunning {
		t.Fatalf("incorrect initial snapshot: %+v", initial)
	}
	initialStatus := readTunnelEvent(t, first, "status").Status
	if !reflect.DeepEqual(initialStatus, map[string]bool{"kept": true, "removed": true}) {
		t.Fatalf("legacy status event differs from snapshot: %v", initialStatus)
	}
	if err := first.Close(websocket.StatusNormalClosure, "offline"); err != nil {
		t.Fatal(err)
	}

	if err := a.StopTunnel("kept"); err != nil {
		t.Fatal(err)
	}
	kept.RemotePort = 6432
	if err := a.UpdateTunnel("kept", kept); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteTunnel("removed"); err != nil {
		t.Fatal(err)
	}
	if err := a.AddTunnel(snapshotTestTunnel("added", 15434)); err != nil {
		t.Fatal(err)
	}

	// 没有周期广播；所有变更已在离线期间发出，重连必须主动补发。
	second := dial()
	snapshot := readTunnelEvent(t, second, "snapshot").Snapshot
	if !reflect.DeepEqual(snapshot, a.GetTunnels()) {
		t.Fatalf("reconnected snapshot is stale: %+v", snapshot)
	}
	if len(snapshot) != 2 || snapshot[0].Name != "kept" || snapshot[0].RemotePort != 6432 ||
		snapshot[0].IsRunning || snapshot[1].Name != "added" || snapshot[1].IsRunning {
		t.Fatalf("offline changes were not restored: %+v", snapshot)
	}
	status := readTunnelEvent(t, second, "status").Status
	if !reflect.DeepEqual(status, map[string]bool{"kept": false, "added": false}) {
		t.Fatalf("reconnected status is stale: %v", status)
	}

	if err := a.DeleteTunnel("kept"); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteTunnel("added"); err != nil {
		t.Fatal(err)
	}
	third := dial()
	if empty := readTunnelEvent(t, third, "snapshot").Snapshot; empty == nil || len(empty) != 0 {
		t.Fatalf("empty configuration must be sent as []: %+v", empty)
	}
}
