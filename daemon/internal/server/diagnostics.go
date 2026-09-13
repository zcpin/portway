package server

import "net/http"

func (s *Server) handleDiagnoseTunnel(w http.ResponseWriter, r *http.Request) {
	result, err := s.app.DiagnoseTunnel(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
