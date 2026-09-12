package server

import "net/http"

func (s *Server) handleUnlockKey(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path       string `json:"path"`
		Passphrase string `json:"passphrase"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid unlock request")
		return
	}
	if input.Path == "" || len(input.Passphrase) > 4096 {
		writeError(w, http.StatusBadRequest, "invalid path or passphrase length")
		return
	}
	passphrase := []byte(input.Passphrase)
	input.Passphrase = ""
	defer clear(passphrase)
	if err := s.app.UnlockKey(input.Path, passphrase); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleLockKey(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if input.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	s.app.LockKey(input.Path)
	writeOK(w)
}
