// Package server 提供本地 HTTP API 与 WebSocket 事件流，是 UI 与业务层之间唯一的通道。
//
// 安全模型：daemon 只监听回环地址，且客户端必须携带 token。
// 刻意不设置 CORS 响应头 —— 这样浏览器中的恶意页面无法跨域访问本服务，
// 而原生客户端（Flutter 桌面端）不受同源策略限制，不受影响。
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/byteporter/ssh-tunnel/internal/app"
	"github.com/byteporter/ssh-tunnel/internal/logger"
)

// Server 对外提供 REST 接口与 WebSocket 事件流。
type Server struct {
	app      *app.App
	hub      *Hub
	token    string
	server   *http.Server
	listener net.Listener
}

// New 创建 Server，token 为空表示关闭认证（仅限回环地址下使用）。
func New(a *app.App, hub *Hub, addr, token string) *Server {
	hub.onConnect = func(c *client) {
		a.SendSnapshot(clientEvents{hub: hub, client: c})
	}
	s := &Server{
		app:   a,
		hub:   hub,
		token: token,
	}

	s.server = &http.Server{
		Addr:    addr,
		Handler: s.routes(),
		// WebSocket 连接在 hijack 时会被 net/http 清除 deadline，
		// 因此这些超时不会影响 /ws（详见 net/http conn.hijackLocked）
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	return s
}

// Listen 绑定监听地址。addr 端口为 0 时由系统分配空闲端口，
// 可通过 Addr() 获取实际地址，避免多实例端口冲突。
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		return err
	}
	s.listener = ln
	return nil
}

// Addr 返回实际监听地址，需在 Listen 之后调用。
func (s *Server) Addr() net.Addr {
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Serve 阻塞处理请求，直到服务关闭。
func (s *Server) Serve() error {
	if s.listener == nil {
		if err := s.Listen(); err != nil {
			return err
		}
	}
	logger.Info("API server listening on http://%s", s.listener.Addr().String())
	if err := s.server.Serve(s.listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown 优雅关闭。
func (s *Server) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// 健康检查免认证，便于客户端探测 daemon 是否已就绪
	mux.HandleFunc("GET /api/health", s.handleHealth)

	mux.Handle("GET /ws", s.auth(s.hub))

	mux.Handle("GET /api/tunnels", s.auth(http.HandlerFunc(s.handleListTunnels)))
	mux.Handle("POST /api/tunnels", s.auth(http.HandlerFunc(s.handleCreateTunnel)))
	mux.Handle("POST /api/tunnels/batch", s.auth(http.HandlerFunc(s.handleBatchTunnels)))
	mux.Handle("PUT /api/tunnels/{name}", s.auth(http.HandlerFunc(s.handleUpdateTunnel)))
	mux.Handle("DELETE /api/tunnels/{name}", s.auth(http.HandlerFunc(s.handleDeleteTunnel)))
	mux.Handle("POST /api/tunnels/{name}/start", s.auth(http.HandlerFunc(s.handleStartTunnel)))
	mux.Handle("POST /api/tunnels/{name}/stop", s.auth(http.HandlerFunc(s.handleStopTunnel)))
	mux.Handle("POST /api/tunnels/{name}/restart", s.auth(http.HandlerFunc(s.handleRestartTunnel)))

	mux.Handle("GET /api/ssh-connections", s.auth(http.HandlerFunc(s.handleListSSH)))
	mux.Handle("POST /api/ssh-connections/test", s.auth(http.HandlerFunc(s.handleTestSSH)))
	mux.Handle("POST /api/ssh-connections", s.auth(http.HandlerFunc(s.handleCreateSSH)))
	mux.Handle("PUT /api/ssh-connections/{name}", s.auth(http.HandlerFunc(s.handleUpdateSSH)))
	mux.Handle("DELETE /api/ssh-connections/{name}", s.auth(http.HandlerFunc(s.handleDeleteSSH)))

	mux.Handle("GET /api/keys", s.auth(http.HandlerFunc(s.handleListKeys)))
	mux.Handle("GET /api/keys/stat", s.auth(http.HandlerFunc(s.handleStatKey)))

	mux.Handle("GET /api/logs", s.auth(http.HandlerFunc(s.handleLogs)))
	mux.Handle("POST /api/reload", s.auth(http.HandlerFunc(s.handleReload)))
	mux.Handle("GET /api/status", s.auth(http.HandlerFunc(s.handleStatus)))
	mux.Handle("GET /api/config", s.auth(http.HandlerFunc(s.handleGetConfig)))
	mux.Handle("GET /api/config/export", s.auth(http.HandlerFunc(s.handleExportConfig)))
	mux.Handle("POST /api/config/preview", s.auth(http.HandlerFunc(s.handlePreviewImport)))
	mux.Handle("POST /api/config/import", s.auth(http.HandlerFunc(s.handleImportConfig)))
	mux.Handle("GET /api/config/backups", s.auth(http.HandlerFunc(s.handleListBackups)))
	mux.Handle("GET /api/config/backups/{name}", s.auth(http.HandlerFunc(s.handleReadBackup)))
	mux.Handle("PUT /api/config", s.auth(http.HandlerFunc(s.handleUpdateConfig)))

	return mux
}

// ---------- 中间件与辅助 ----------

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	if s.token == "" {
		return true
	}

	token := r.Header.Get("X-Auth-Token")
	if token == "" {
		token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}

	return subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) == 1
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Error("failed to write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}

// writeOK 返回 {"ok": true}，用于无返回体的操作。
func writeOK(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
