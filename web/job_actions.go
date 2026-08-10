package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

func (s *Server) apiDuplicateJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		renderLocalAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")

		return
	}

	id, ok := getIDFromRequest(r)
	if !ok {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_job_id", "Invalid job ID")

		return
	}

	if r.Header.Get("Origin") != "" && !s.requireCSRF(w, r) {
		return
	}

	source, err := s.svc.Get(r.Context(), id.String())
	if err != nil {
		renderLocalAPIError(w, http.StatusNotFound, "job_not_found", "Job not found")

		return
	}

	duplicate := source
	duplicate.ID = uuid.NewString()
	duplicate.Name = strings.TrimSpace(source.Name) + " (copy)"
	duplicate.Date = time.Now().UTC()
	duplicate.Status = StatusPending

	if err := s.svc.CreateWithState(r.Context(), &duplicate, jobruntime.StateDraft); err != nil {
		if err == ErrLifecycleUnsupported {
			renderLocalAPIError(w, http.StatusNotImplemented, "lifecycle_unavailable", "Duplicating into a draft requires the upgraded local database")

			return
		}

		renderLocalAPIError(w, http.StatusInternalServerError, "duplicate_failed", "Could not duplicate job")

		return
	}

	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		renderJSON(w, http.StatusCreated, localAPIEnvelope{Data: map[string]string{
			"id":    duplicate.ID,
			"state": string(jobruntime.StateDraft),
		}})

		return
	}

	http.Redirect(w, r, "/app/jobs/"+duplicate.ID, http.StatusSeeOther)
}
