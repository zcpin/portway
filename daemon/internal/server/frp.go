package server

import (
	"net/http"

	"github.com/byteporter/portway/internal/frp"
)

// FRP 相关接口。结构与隧道接口保持一致：
// 列表返回完整视图（配置 + 运行状态），其余操作只回 {"ok":true}。

func (s *Server) handleListFrpClients(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.app.GetFrpClients())
}

func (s *Server) handleCreateFrpClient(w http.ResponseWriter, r *http.Request) {
	var payload frp.ClientPayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.app.AddFrpClient(payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleUpdateFrpClient(w http.ResponseWriter, r *http.Request) {
	var payload frp.ClientPayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.app.UpdateFrpClient(r.PathValue("name"), payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleDeleteFrpClient(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteFrpClient(r.PathValue("name")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleStartFrpClient(w http.ResponseWriter, r *http.Request) {
	if err := s.app.StartFrpClient(r.PathValue("name")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleStopFrpClient(w http.ResponseWriter, r *http.Request) {
	if err := s.app.StopFrpClient(r.PathValue("name")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleRestartFrpClient(w http.ResponseWriter, r *http.Request) {
	if err := s.app.RestartFrpClient(r.PathValue("name")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleCreateFrpProxy(w http.ResponseWriter, r *http.Request) {
	var payload frp.ProxyPayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.app.AddFrpProxy(r.PathValue("name"), payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleUpdateFrpProxy(w http.ResponseWriter, r *http.Request) {
	var payload frp.ProxyPayload
	if err := decodeJSON(w, r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.app.UpdateFrpProxy(r.PathValue("name"), r.PathValue("proxy"), payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleDeleteFrpProxy(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteFrpProxy(r.PathValue("name"), r.PathValue("proxy")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleToggleFrpProxy(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.app.ToggleFrpProxy(r.PathValue("name"), r.PathValue("proxy"), input.Enabled); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}
