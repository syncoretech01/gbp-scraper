package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// maximumRerunRequestBytes bounds the rescan request body; the payload is a
// handful of short fields.
const maximumRerunRequestBytes = 8 << 10

// rerunAPIInput is the JSON body of POST /api/v1/jobs/{id}/rerun.
type rerunAPIInput struct {
	Mode           string `json:"mode"`
	Name           string `json:"name,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	Draft          bool   `json:"draft,omitempty"`
}

// registerRerunRoutes exposes rescan campaigns: creating a new run from an
// existing job's plan, and reading the lineage that links them.
func (s *Server) registerRerunRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/jobs/{id}/rerun", s.rerunJob)
	mux.HandleFunc("GET /api/v1/jobs/{id}/campaign", s.jobCampaign)
}

func (s *Server) rerunJob(w http.ResponseWriter, r *http.Request) {
	r = requestWithID(r)

	id, ok := getIDFromRequest(r)
	if !ok {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_job_id", "Invalid job ID")

		return
	}

	input, err := decodeRerunInput(w, r)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_rerun", err.Error())

		return
	}

	if !s.requireCSRF(w, r) {
		return
	}

	rerun, err := s.svc.RerunJob(r.Context(), RerunRequest{
		SourceJobID:    id.String(),
		Mode:           input.Mode,
		Name:           input.Name,
		IdempotencyKey: input.IdempotencyKey,
		Draft:          input.Draft,
	})
	if err != nil {
		s.renderRerunError(w, err)

		return
	}

	status := http.StatusCreated
	if rerun.Reused {
		status = http.StatusOK
	}

	renderJSON(w, status, localAPIEnvelope{Data: map[string]any{
		"job_id":        rerun.Job.ID,
		"name":          rerun.Job.Name,
		"state":         rerun.State,
		"mode":          rerun.Link.Mode,
		"campaign_id":   rerun.Link.CampaignID,
		"root_job_id":   rerun.Link.RootJobID,
		"source_job_id": rerun.Link.SourceJobID,
		"generation":    rerun.Link.Generation,
		"reused":        rerun.Reused,
	}})
}

func (s *Server) jobCampaign(w http.ResponseWriter, r *http.Request) {
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

	campaign, err := s.svc.JobCampaignOf(r.Context(), id.String())
	if err != nil {
		s.renderRerunError(w, err)

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: campaign})
}

// decodeRerunInput accepts either a JSON body or a posted form, so the API
// and a progressively enhanced page control share one handler.
func decodeRerunInput(w http.ResponseWriter, r *http.Request) (rerunAPIInput, error) {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		if err := parseBoundedRequestForm(w, r, maximumRerunRequestBytes); err != nil {
			return rerunAPIInput{}, err
		}

		return rerunAPIInput{
			Mode:           strings.TrimSpace(r.FormValue("mode")),
			Name:           strings.TrimSpace(r.FormValue("name")),
			IdempotencyKey: strings.TrimSpace(r.FormValue("idempotency_key")),
			Draft:          r.FormValue("draft") == "true" || r.FormValue("draft") == "on",
		}, nil
	}

	r.Body = http.MaxBytesReader(w, r.Body, maximumRerunRequestBytes)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var input rerunAPIInput
	if err := decoder.Decode(&input); err != nil {
		return rerunAPIInput{}, err
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return rerunAPIInput{}, errors.New("request must contain exactly one JSON object")
	}

	input.Mode = strings.TrimSpace(input.Mode)
	input.Name = strings.TrimSpace(input.Name)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)

	return input, nil
}

func (s *Server) renderRerunError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrCampaignUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "campaign_unavailable",
			"Rescan campaigns require the upgraded local database")
	case errors.Is(err, ErrInvalidRerun):
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_rerun", err.Error())
	case errors.Is(err, ErrLifecycleUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "lifecycle_unavailable",
			"Creating a rescan requires the upgraded local database")
	case errors.Is(err, ErrLifecycleNotFound), errors.Is(err, ErrNotFound), errors.Is(err, ErrPlacesNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "job_not_found", "Job not found")
	default:
		renderLocalAPIError(w, http.StatusInternalServerError, "rerun_failed", "Could not start the rescan")
	}
}
