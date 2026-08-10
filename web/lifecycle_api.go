package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

type jobControlResponse struct {
	Runtime  JobRuntime            `json:"runtime"`
	Decision jobControlDecisionDTO `json:"decision"`
}

type jobControlDecisionDTO struct {
	Control       string `json:"control"`
	Disposition   string `json:"disposition"`
	CurrentState  string `json:"current_state"`
	NextState     string `json:"next_state"`
	EventualState string `json:"eventual_state"`
	RequestedStop string `json:"requested_stop,omitempty"`
	Message       string `json:"message"`
}

func (s *Server) registerLifecycleRoutes(mux *http.ServeMux) {
	controls := []jobruntime.Control{
		jobruntime.ControlStart,
		jobruntime.ControlPause,
		jobruntime.ControlResume,
		jobruntime.ControlCancel,
		jobruntime.ControlRestart,
	}

	for _, control := range controls {
		mux.HandleFunc("/api/v1/jobs/{id}/"+string(control), func(w http.ResponseWriter, r *http.Request) {
			r = requestWithID(r)
			s.apiJobControl(w, r, control)
		})
	}

	mux.HandleFunc("/api/v1/jobs/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		r = requestWithID(r)
		s.apiJobControl(w, r, jobruntime.ControlRestart)
	})
	mux.HandleFunc("/api/v1/jobs/{id}/duplicate", func(w http.ResponseWriter, r *http.Request) {
		r = requestWithID(r)
		s.apiDuplicateJob(w, r)
	})
}

func (s *Server) apiJobControl(w http.ResponseWriter, r *http.Request, control jobruntime.Control) {
	if r.Method != http.MethodPost {
		renderLocalAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")

		return
	}

	id, ok := getIDFromRequest(r)
	if !ok {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_job_id", "Invalid job ID")

		return
	}

	// Browser-originated state changes require the per-process request token.
	// Local scripts and CLI clients do not send Origin and keep the historical
	// unauthenticated loopback behavior unless API-key auth is enabled later.
	if r.Header.Get("Origin") != "" && !s.requireCSRF(w, r) {
		return
	}

	runtime, decision, err := s.svc.ApplyControl(r.Context(), id.String(), control)
	if err != nil {
		switch {
		case errors.Is(err, ErrLifecycleUnsupported):
			renderLocalAPIError(w, http.StatusNotImplemented, "lifecycle_unavailable", "Job controls require the upgraded local database")
		case errors.Is(err, ErrLifecycleNotFound):
			renderLocalAPIError(w, http.StatusNotFound, "job_not_found", "Job not found")
		case errors.Is(err, jobruntime.ErrControlRejected), errors.Is(err, jobruntime.ErrInvalidTransition):
			renderLocalAPIError(w, http.StatusConflict, "invalid_transition", err.Error())
		case errors.Is(err, ErrLifecycleConflict):
			renderLocalAPIError(w, http.StatusConflict, "state_changed", "Job state changed; refresh and try again")
		default:
			renderLocalAPIError(w, http.StatusInternalServerError, "control_failed", "Could not apply job control")
		}

		return
	}

	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		renderJSON(w, http.StatusOK, localAPIEnvelope{
			Data: jobControlResponse{Runtime: runtime, Decision: newJobControlDecisionDTO(decision)},
		})

		return
	}

	http.Redirect(w, r, "/app/jobs/"+id.String(), http.StatusSeeOther)
}

func newJobControlDecisionDTO(decision jobruntime.ControlDecision) jobControlDecisionDTO {
	return jobControlDecisionDTO{
		Control:       string(decision.Control),
		Disposition:   string(decision.Disposition),
		CurrentState:  string(decision.CurrentState),
		NextState:     string(decision.NextState),
		EventualState: string(decision.EventualState),
		RequestedStop: string(decision.RequestedStop),
		Message:       decision.Message,
	}
}
