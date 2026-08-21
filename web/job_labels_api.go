package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maximumJobLabelBytes bounds a label request. Labels are a folder, a handful
// of tags, an owner, and a note, so this is generous by an order of magnitude.
const maximumJobLabelBytes = 16 << 10

// jobLabelInput accepts labels either as a JSON object or as the form the job
// pages submit. Tags arrive as a repeated field or as one comma-separated
// value, because a text input is the control that fits a table row.
type jobLabelInput struct {
	Folder string   `json:"folder,omitempty"`
	Owner  string   `json:"owner,omitempty"`
	Tags   []string `json:"tags,omitempty"`
	// Notes is applied through the existing job-organisation path so a single
	// operator action does not have to be split across two requests.
	Notes *string `json:"notes,omitempty"`
}

func (s *Server) registerJobLabelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/jobs/{id}/labels", func(w http.ResponseWriter, r *http.Request) {
		s.handleJobLabels(w, requestWithID(r))
	})
}

func (s *Server) handleJobLabels(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}

	id, ok := getIDFromRequest(r)
	if !ok {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_job_id", "Invalid job ID")

		return
	}

	jobID := id.String()

	input, err := decodeJobLabelInput(w, r)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_job_label", err.Error())

		return
	}

	if err := s.svc.SetJobLabels(r.Context(), jobID, JobLabels{
		Folder: input.Folder,
		Owner:  input.Owner,
		Tags:   input.Tags,
	}); err != nil {
		renderJobLabelError(w, err)

		return
	}

	// Notes already have durable storage of their own; applying them here keeps
	// one operator action to one request instead of two.
	if input.Notes != nil {
		if err := s.svc.SetJobNotes(r.Context(), jobID, *input.Notes); err != nil &&
			!errors.Is(err, ErrJobOrganisationUnsupported) {
			renderJobLabelError(w, err)

			return
		}
	}

	labels, err := s.svc.JobLabels(r.Context(), jobID)
	if err != nil {
		renderJobLabelError(w, err)

		return
	}

	if !strings.Contains(r.Header.Get("Accept"), "application/json") &&
		!strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		http.Redirect(w, r, "/app/jobs/"+jobID, http.StatusSeeOther)

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: labels})
}

func decodeJobLabelInput(w http.ResponseWriter, r *http.Request) (jobLabelInput, error) {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		r.Body = http.MaxBytesReader(w, r.Body, maximumJobLabelBytes)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()

		var input jobLabelInput
		if err := decoder.Decode(&input); err != nil {
			return jobLabelInput{}, fmt.Errorf("invalid JSON")
		}

		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return jobLabelInput{}, fmt.Errorf("request must contain exactly one JSON object")
		}

		return input, nil
	}

	if err := parseBoundedRequestForm(w, r, maximumJobLabelBytes); err != nil {
		return jobLabelInput{}, fmt.Errorf("invalid form")
	}

	input := jobLabelInput{
		Folder: strings.TrimSpace(r.FormValue("folder")),
		Owner:  strings.TrimSpace(r.FormValue("owner")),
		Tags:   ParseJobTagList(r.FormValue("tags")),
	}

	if r.Form.Has("notes") {
		notes := r.FormValue("notes")
		input.Notes = &notes
	}

	return input, nil
}

func renderJobLabelError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrJobLabelsUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "job_labels_unavailable", "Job labels require the upgraded local database")
	case errors.Is(err, ErrInvalidJobLabel), errors.Is(err, ErrInvalidJobOrganisation):
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_job_label", err.Error())
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrLifecycleNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "job_not_found", "Job not found")
	default:
		renderLocalAPIError(w, http.StatusInternalServerError, "job_label_failed", "Could not update job labels")
	}
}
