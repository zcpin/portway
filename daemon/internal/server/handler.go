package server

import (
	"encoding/json"
	"net/http"

	"github.com/byteporter/ssh-tunnel/internal/config"
)

// Version 由 main 在构建时注入。
var Version = "dev"

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "ok",
		"version":     Version,
		"config_path": s.app.ConfigPath(),
		"clients":     s.hub.ClientCount(),
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.app.GetStatus())
}

func (s *Server) handleListTunnels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.app.GetTunnels())
}

func (s *Server) handleCreateTunnel(w http.ResponseWriter, r *http.Request) {
	var t config.Tunnel
	if err := decodeJSON(w, r, &t); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.app.AddTunnel(t); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleUpdateTunnel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var t config.Tunnel
	if err := decodeJSON(w, r, &t); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.app.UpdateTunnel(name, t); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleDeleteTunnel(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteTunnel(r.PathValue("name")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleStartTunnel(w http.ResponseWriter, r *http.Request) {
	if err := s.app.StartTunnel(r.PathValue("name")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleStopTunnel(w http.ResponseWriter, r *http.Request) {
	if err := s.app.StopTunnel(r.PathValue("name")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleRestartTunnel(w http.ResponseWriter, r *http.Request) {
	if err := s.app.RestartTunnel(r.PathValue("name")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

// ---------- SSH 连接 ----------

func (s *Server) handleTestSSH(w http.ResponseWriter, r *http.Request) {
	var conn config.SSHConnection
	if err := decodeJSON(w, r, &conn); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.app.TestSSHConnection(r.Context(), conn)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleListSSH(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.app.GetSSHConnections())
}

func (s *Server) handleCreateSSH(w http.ResponseWriter, r *http.Request) {
	var c config.SSHConnection
	if err := decodeJSON(w, r, &c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.app.AddSSHConnection(c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleUpdateSSH(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var c config.SSHConnection
	if err := decodeJSON(w, r, &c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.app.UpdateSSHConnection(name, c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleDeleteSSH(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteSSHConnection(r.PathValue("name")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

// ---------- 密钥 ----------

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.app.ListKeys())
}

// handleStatKey 校验一个私钥路径，供客户端在用户选完文件后立即确认
// 该路径对守护进程可读（客户端与守护进程同机，路径通常一致）。
//
// 注意：这里刻意不对路径做白名单限制——守护进程以当前用户身份运行，
// 本就拥有该用户的文件读取权限，且接口仅监听回环地址并要求 token。
func (s *Server) handleStatKey(w http.ResponseWriter, r *http.Request) {
	info, err := s.app.StatKey(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// ---------- 日志与配置 ----------

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.app.GetLogs())
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if err := s.app.ReloadConfig(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

// handleGetConfig 返回全局配置项，供客户端「设置」页回显。
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.app.GetGlobalSettings())
}

// handleUpdateConfig 更新全局配置项（日志级别、重连默认值）。
//
// 请求体只需包含要修改的字段；未提供的字段按零值处理并套用默认值。
// 只改动顶层字段，不会触碰隧道 / SSH 连接。
func (s *Server) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var g config.GlobalSettings
	if err := decodeJSON(w, r, &g); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.app.SetGlobalSettings(g); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

// maxRequestBody 是 API 请求体上限，防止异常客户端把内存吃满。
const maxRequestBody = 1 << 20 // 1 MiB

// decodeJSON 解析请求体：限制大小并拒绝未知字段（字段名写错时能立刻发现）。
func decodeJSON(w http.ResponseWriter, r *http.Request, v interface{}) error {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
