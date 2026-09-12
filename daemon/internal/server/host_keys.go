package server

import (
	"net/http"

	"github.com/byteporter/ssh-tunnel/internal/config"
)

func (s *Server) handleInspectHostKey(w http.ResponseWriter, r *http.Request) {
	var conn config.SSHConnection
	if err := decodeJSON(w, r, &conn); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.app.InspectHostKey(r.Context(), conn)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleTrustHostKey(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Connection  config.SSHConnection `json:"connection"`
		Fingerprint string               `json:"fingerprint"`
		Replace     bool                 `json:"replace"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.app.TrustHostKey(r.Context(), input.Connection, input.Fingerprint, input.Replace)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
