package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// maximumCampaignTemplateRequestBytes bounds the capture/instantiate bodies;
// both are a handful of short fields.
const maximumCampaignTemplateRequestBytes = 8 << 10

// campaignTemplateAPIInput is the JSON body of both campaign-template
// mutations. Unused fields are simply ignored by the handler that does not
// need them.
type campaignTemplateAPIInput struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Draft       bool   `json:"draft,omitempty"`
}

// registerCampaignTemplateRoutes exposes capturing a running job's complete
// campaign shape as a template and instantiating a template into a new run.
func (s *Server) registerCampaignTemplateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/jobs/{id}/template", s.captureCampaignTemplate)
	mux.HandleFunc("POST /api/v1/templates/{id}/instantiate", s.instantiateCampaignTemplate)
}

func (s *Server) captureCampaignTemplate(w http.ResponseWriter, r *http.Request) {
	r = requestWithID(r)

	id, ok := getIDFromRequest(r)
	if !ok {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_job_id", "Invalid job ID")

		return
	}

	input, err := decodeCampaignTemplateInput(w, r)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_template", err.Error())

		return
	}

	if !s.requireCSRF(w, r) {
		return
	}

	result, err := s.svc.CaptureCampaignTemplate(r.Context(), id.String(), input.Name, input.Description)
	if err != nil {
		s.renderCampaignTemplateError(w, err)

		return
	}

	renderJSON(w, http.StatusCreated, localAPIEnvelope{Data: result})
}

func (s *Server) instantiateCampaignTemplate(w http.ResponseWriter, r *http.Request) {
	templateID := strings.TrimSpace(r.PathValue("id"))
	if templateID == "" {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_template_id", "Invalid template ID")

		return
	}

	input, err := decodeCampaignTemplateInput(w, r)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_template", err.Error())

		return
	}

	if !s.requireCSRF(w, r) {
		return
	}

	result, err := s.svc.InstantiateCampaignTemplate(r.Context(), templateID, input.Name, input.Mode, input.Draft)
	if err != nil {
		s.renderCampaignTemplateError(w, err)

		return
	}

	renderJSON(w, http.StatusCreated, localAPIEnvelope{Data: result})
}

// decodeCampaignTemplateInput accepts a JSON body or a posted form, so the
// API and a progressively enhanced page control share one handler.
func decodeCampaignTemplateInput(w http.ResponseWriter, r *http.Request) (campaignTemplateAPIInput, error) {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		if err := parseBoundedRequestForm(w, r, maximumCampaignTemplateRequestBytes); err != nil {
			return campaignTemplateAPIInput{}, err
		}

		return campaignTemplateAPIInput{
			Name:        strings.TrimSpace(r.FormValue("name")),
			Description: strings.TrimSpace(r.FormValue("description")),
			Mode:        strings.TrimSpace(r.FormValue("mode")),
			Draft:       formBoolean(r, "draft", false),
		}, nil
	}

	r.Body = http.MaxBytesReader(w, r.Body, maximumCampaignTemplateRequestBytes)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var input campaignTemplateAPIInput
	if err := decoder.Decode(&input); err != nil {
		return campaignTemplateAPIInput{}, err
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return campaignTemplateAPIInput{}, errors.New("request must contain exactly one JSON object")
	}

	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.Mode = strings.TrimSpace(input.Mode)

	return input, nil
}

func (s *Server) renderCampaignTemplateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidCampaignTemplate):
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_template", err.Error())
	case errors.Is(err, ErrReusableStoreUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "templates_unavailable",
			"Campaign templates require the upgraded local database")
	case errors.Is(err, ErrLifecycleUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "lifecycle_unavailable",
			"Creating a run from a template requires the upgraded local database")
	case errors.Is(err, ErrReusableNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "template_not_found", "Template not found")
	case errors.Is(err, ErrLifecycleNotFound), errors.Is(err, ErrNotFound), errors.Is(err, ErrPlacesNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "job_not_found", "Job not found")
	default:
		renderLocalAPIError(w, http.StatusInternalServerError, "template_failed",
			"Could not complete the campaign template action")
	}
}
