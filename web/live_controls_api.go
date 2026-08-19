package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) registerLiveControlRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/jobs/{id}/runtime", func(w http.ResponseWriter, r *http.Request) {
		s.handleLiveControl(w, requestWithID(r), "runtime")
	})
	mux.HandleFunc("POST /api/v1/jobs/{id}/concurrency", func(w http.ResponseWriter, r *http.Request) {
		s.handleLiveControl(w, requestWithID(r), "concurrency")
	})
	mux.HandleFunc("POST /api/v1/jobs/{id}/proxy-pool", func(w http.ResponseWriter, r *http.Request) {
		s.handleLiveControl(w, requestWithID(r), "proxy-pool")
	})
	mux.HandleFunc("POST /api/v1/jobs/{id}/retry-current", func(w http.ResponseWriter, r *http.Request) {
		s.handleLiveControl(w, requestWithID(r), "retry-current")
	})
}

// handleLiveControl stores one durable control request. The worker applies it
// at the next safe task boundary; the response says so rather than pretending
// the change was instant.
func (s *Server) handleLiveControl(w http.ResponseWriter, r *http.Request, control string) {
	if !s.requireCSRF(w, r) {
		return
	}

	id, ok := getIDFromRequest(r)
	if !ok {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_job_id", "Invalid job ID")

		return
	}

	jobID := id.String()

	if _, err := s.svc.Get(r.Context(), jobID); err != nil {
		renderLocalAPIError(w, http.StatusNotFound, "job_not_found", "Job not found")

		return
	}

	if err := parseBoundedRequestForm(w, r, 16<<10); err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_live_control", "The form could not be read")

		return
	}

	var err error

	switch control {
	case "runtime":
		duration, parseErr := time.ParseDuration(strings.TrimSpace(r.FormValue("duration")))
		if parseErr != nil {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_live_control",
				"Duration must be a value like 15m or 1h")

			return
		}

		err = s.svc.ExtendJobRuntime(r.Context(), jobID, duration)
	case "concurrency":
		concurrency, parseErr := strconv.Atoi(strings.TrimSpace(r.FormValue("concurrency")))
		if parseErr != nil {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_live_control",
				"Concurrency must be a whole number")

			return
		}

		err = s.svc.SetJobConcurrencyOverride(r.Context(), jobID, concurrency)
	case "proxy-pool":
		poolID := strings.TrimSpace(r.FormValue("proxy_pool_id"))
		if poolID == "" {
			poolID = DirectConnectionPool
		}

		err = s.svc.SetJobProxyPoolOverride(r.Context(), jobID, poolID)
	case "retry-current":
		err = s.svc.RequestJobRetryCurrent(r.Context(), jobID)
	}

	if err != nil {
		switch {
		case errors.Is(err, ErrLiveControlsUnsupported):
			renderLocalAPIError(w, http.StatusNotImplemented, "live_controls_unavailable", "Live controls are unavailable")
		case errors.Is(err, ErrLifecycleNotFound):
			renderLocalAPIError(w, http.StatusNotFound, "job_not_found", "Job not found")
		case errors.Is(err, ErrInvalidLiveControl):
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_live_control", err.Error())
		default:
			renderLocalAPIError(w, http.StatusInternalServerError, "live_control_failed", "Could not store the control request")
		}

		return
	}

	if !strings.Contains(r.Header.Get("Accept"), "application/json") {
		http.Redirect(w, r, "/app/jobs/"+jobID+"?notice=Applied+at+the+next+safe+task+boundary", http.StatusSeeOther)

		return
	}

	controls, readErr := s.svc.JobLiveControls(r.Context(), jobID)
	if readErr != nil {
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]any{"accepted": true}})

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: controls})
}
