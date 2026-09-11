package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// dialHub 启动一个只挂了 Hub 的测试服务器并连上一个客户端。
func dialHub(t *testing.T) (*Hub, *websocket.Conn) {
	t.Helper()

	hub := NewHub()
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })

	deadline := time.Now().Add(2 * time.Second)
	for hub.ClientCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if hub.ClientCount() != 1 {
		t.Fatalf("连接数 = %d，期望 1", hub.ClientCount())
	}
	return hub, conn
}

func TestHubEmitDeliversEvent(t *testing.T) {
	hub, conn := dialHub(t)

	hub.Emit("status", map[string]bool{"mysql": true})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	var event map[string]any
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("解析事件失败: %v", err)
	}
	if event["type"] != "status" {
		t.Errorf("type = %v，期望 status", event["type"])
	}
	status, ok := event["status"].(map[string]any)
	if !ok || status["mysql"] != true {
		t.Errorf("status 字段不符: %v", event["status"])
	}
}

// TestHubEmitDoesNotBlockOnSlowClient 验证客户端不消费时 Emit 依然立即返回：
// 旧实现会对每个连接同步 Write（5 秒超时），一个慢客户端就能拖住日志与状态广播。
func TestHubEmitDoesNotBlockOnSlowClient(t *testing.T) {
	hub, _ := dialHub(t)

	// 客户端刻意不读取，直到写满内核缓冲区
	payload := map[string]string{"message": strings.Repeat("x", 32*1024)}

	start := time.Now()
	for i := 0; i < clientSendBuffer*2; i++ {
		hub.Emit("log", payload)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Emit 被慢客户端阻塞: %v", elapsed)
	}
}
