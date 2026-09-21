package server

import (
	"errors"
	"net/http"

	"github.com/byteporter/portway/internal/config"
)

func (s *Server) handleExportConfig(w http.ResponseWriter, r *http.Request) {
	result, err := s.app.ExportConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handlePreviewImport(w http.ResponseWriter, r *http.Request) {
	var input config.ImportRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.app.PreviewImport(input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleImportConfig(w http.ResponseWriter, r *http.Request) {
	var input config.ImportRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.app.ImportConfig(input)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, config.ErrRevisionChanged) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	result, err := s.app.ListBackups()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleReadBackup(w http.ResponseWriter, r *http.Request) {
	content, err := s.app.ReadBackup(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": content})
}
