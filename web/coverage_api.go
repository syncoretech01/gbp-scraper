package web

import (
	"errors"
	"net/http"
)

// registerCoverageRoutes exposes the adaptive discovery readback: the
// per-query coverage table, saturation evidence, and the discovery trend for
// one job.
func (s *Server) registerCoverageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/jobs/{id}/coverage", func(w http.ResponseWriter, r *http.Request) {
		r = requestWithID(r)

		id, ok := getIDFromRequest(r)
		if !ok {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_job_id", "Invalid job ID")

			return
		}

		if _, err := s.svc.Get(r.Context(), id.String()); err != nil {
			renderLocalAPIError(w, http.StatusNotFound, "job_not_found", "Job not found")

			return
		}

		report, err := s.svc.JobCoverage(r.Context(), id.String())
		if err != nil {
			if errors.Is(err, ErrCoverageUnsupported) {
				renderLocalAPIError(w, http.StatusNotImplemented, "coverage_unavailable", "Job coverage is unavailable")

				return
			}

			renderLocalAPIError(w, http.StatusInternalServerError, "coverage_failed", "Could not load job coverage")

			return
		}

		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: report})
	})
}
