package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
)

func newCSRFToken() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("create CSRF token: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(token[:]), nil
}

func (s *Server) requireCSRF(w http.ResponseWriter, r *http.Request) bool {
	provided := r.Header.Get("X-CSRF-Token")
	if provided == "" {
		provided = r.FormValue("csrf_token")
	}

	if subtle.ConstantTimeCompare([]byte(provided), []byte(s.csrfToken)) != 1 {
		http.Error(w, "invalid request token", http.StatusForbidden)

		return false
	}

	return true
}
