package web

import (
	"errors"
	"net/http"
)

func (s *Server) registerCheckpointRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/jobs/{id}/checkpoint", func(w http.ResponseWriter, r *http.Request) {
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
		snapshot, err := s.svc.GetJobExecution(r.Context(), id.String())
		if err != nil {
			if errors.Is(err, ErrCheckpointUnsupported) {
				renderLocalAPIError(w, http.StatusNotImplemented, "checkpoint_unavailable", "Job checkpoints are unavailable")

				return
			}
			renderLocalAPIError(w, http.StatusInternalServerError, "checkpoint_failed", "Could not load job checkpoint evidence")

			return
		}

		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: snapshot})
	})
}
