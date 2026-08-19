package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maximumJobOrganisationBytes = 16 << 10

type jobOrganisationInput struct {
	Name     string `json:"name,omitempty"`
	Notes    string `json:"notes,omitempty"`
	Archived *bool  `json:"archived,omitempty"`
}

func (s *Server) registerJobOrganisationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/jobs/{id}/rename", func(w http.ResponseWriter, r *http.Request) {
		s.handleJobOrganisation(w, requestWithID(r), "rename")
	})
	mux.HandleFunc("POST /api/v1/jobs/{id}/archive", func(w http.ResponseWriter, r *http.Request) {
		s.handleJobOrganisation(w, requestWithID(r), "archive")
	})
	mux.HandleFunc("POST /api/v1/jobs/{id}/notes", func(w http.ResponseWriter, r *http.Request) {
		s.handleJobOrganisation(w, requestWithID(r), "notes")
	})
}

func (s *Server) handleJobOrganisation(w http.ResponseWriter, r *http.Request, action string) {
	if !s.requireCSRF(w, r) {
		return
	}

	id, ok := getIDFromRequest(r)
	if !ok {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_job_id", "Invalid job ID")

		return
	}

	jobID := id.String()

	input, err := decodeJobOrganisationInput(w, r)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_job_organisation", err.Error())

		return
	}

	switch action {
	case "rename":
		err = s.svc.RenameJob(r.Context(), jobID, input.Name)
	case "notes":
		err = s.svc.SetJobNotes(r.Context(), jobID, input.Notes)
	case "archive":
		archived := true
		if input.Archived != nil {
			archived = *input.Archived
		} else {
			// A form toggle sends no value, so read the current state and flip it.
			current, readErr := s.svc.GetJobOrganisation(r.Context(), jobID)
			if readErr != nil {
				renderJobOrganisationError(w, readErr)

				return
			}

			archived = !current.Archived
		}

		err = s.svc.SetJobArchived(r.Context(), jobID, archived)
	}

	if err != nil {
		renderJobOrganisationError(w, err)

		return
	}

	organisation, err := s.svc.GetJobOrganisation(r.Context(), jobID)
	if err != nil {
		renderJobOrganisationError(w, err)

		return
	}

	if !strings.Contains(r.Header.Get("Accept"), "application/json") &&
		!strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		http.Redirect(w, r, "/app/jobs?notice=Job+updated", http.StatusSeeOther)

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: organisation})
}

func decodeJobOrganisationInput(w http.ResponseWriter, r *http.Request) (jobOrganisationInput, error) {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		r.Body = http.MaxBytesReader(w, r.Body, maximumJobOrganisationBytes)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()

		var input jobOrganisationInput
		if err := decoder.Decode(&input); err != nil {
			return jobOrganisationInput{}, fmt.Errorf("invalid JSON")
		}

		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return jobOrganisationInput{}, fmt.Errorf("request must contain exactly one JSON object")
		}

		return input, nil
	}

	if err := parseBoundedRequestForm(w, r, maximumJobOrganisationBytes); err != nil {
		return jobOrganisationInput{}, fmt.Errorf("invalid form")
	}

	input := jobOrganisationInput{
		Name:  strings.TrimSpace(r.FormValue("name")),
		Notes: r.FormValue("notes"),
	}

	if raw := strings.TrimSpace(r.FormValue("archived")); raw != "" {
		archived := raw == "on" || raw == "true" || raw == "1"
		input.Archived = &archived
	}

	return input, nil
}

func renderJobOrganisationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrJobOrganisationUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "job_organisation_unavailable", "Job organisation is unavailable")
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrLifecycleNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "job_not_found", "Job not found")
	case errors.Is(err, ErrInvalidJobOrganisation):
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_job_organisation", err.Error())
	default:
		renderLocalAPIError(w, http.StatusInternalServerError, "job_organisation_failed", "Could not update the job")
	}
}
