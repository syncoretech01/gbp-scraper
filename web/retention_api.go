package web

import (
	"errors"
	"net/http"
	"strings"
)

func (s *Server) registerRetentionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/system/retention/apply", s.apiApplyRetention)
}

// apiApplyRetention runs one retention pass on demand. The same pass also runs
// automatically when the local worker starts.
func (s *Server) apiApplyRetention(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}

	report, err := s.svc.ApplyRetentionPolicies(r.Context())
	if err != nil {
		if errors.Is(err, ErrRetentionUnsupported) {
			renderLocalAPIError(w, http.StatusNotImplemented, "retention_unavailable", "Retention is unavailable")

			return
		}

		renderLocalAPIError(w, http.StatusInternalServerError, "retention_failed", "Could not apply retention policies")

		return
	}

	if !strings.Contains(r.Header.Get("Accept"), "application/json") &&
		!strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		http.Redirect(w, r, "/app/system?notice=Retention+policies+applied", http.StatusSeeOther)

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: report})
}
