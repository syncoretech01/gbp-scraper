package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const maximumEnrichmentRequestBytes = 64 << 10

type enrichmentAPIRequest struct {
	IDs     []string          `json:"ids,omitempty"`
	Options EnrichmentOptions `json:"options,omitempty"`
}

func (s *Server) registerEnrichmentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/results/enrich", s.apiQueueBusinessEnrichment)
	mux.HandleFunc("POST /api/v1/results/{id}/enrich", s.apiQueueSingleBusinessEnrichment)
	mux.HandleFunc("GET /api/v1/results/{id}/enrichment", s.apiBusinessEnrichmentHistory)
	mux.HandleFunc("GET /api/v1/enrichment/tasks", s.apiEnrichmentTasks)
}

func (s *Server) enrichmentAvailable() bool {
	return s != nil && s.svc != nil && s.svc.EnrichmentAvailable()
}

func (s *Server) apiQueueBusinessEnrichment(w http.ResponseWriter, r *http.Request) {
	request, err := decodeEnrichmentRequest(w, r)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_enrichment_request", err.Error())
		return
	}

	s.queueBusinessEnrichment(w, r, request.IDs, request.Options)
}

func (s *Server) apiQueueSingleBusinessEnrichment(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" || len(id) > 128 {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_business_id", "A valid business ID is required")
		return
	}
	request, err := decodeEnrichmentOptions(w, r)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_enrichment_request", err.Error())
		return
	}

	s.queueBusinessEnrichment(w, r, []string{id}, request)
}

func (s *Server) queueBusinessEnrichment(
	w http.ResponseWriter,
	r *http.Request,
	ids []string,
	options EnrichmentOptions,
) {
	batch, err := s.svc.QueueBusinessEnrichment(r.Context(), ids, options, "local_api")
	if err != nil {
		renderEnrichmentError(w, err)
		return
	}

	renderJSON(w, http.StatusAccepted, localAPIEnvelope{
		Data: batch.Tasks,
		Meta: map[string]any{"queued": batch.Queued, "skipped": batch.Skipped},
	})
}

func (s *Server) apiBusinessEnrichmentHistory(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	limit, err := boundedPositiveInt(r.URL.Query().Get("limit"), 20, 100)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_limit", err.Error())
		return
	}
	audits, err := s.svc.WebsiteAuditHistory(r.Context(), id, limit)
	if err != nil {
		renderEnrichmentError(w, err)
		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: audits, Meta: map[string]any{"count": len(audits)}})
}

func (s *Server) apiEnrichmentTasks(w http.ResponseWriter, r *http.Request) {
	limit, err := boundedPositiveInt(r.URL.Query().Get("limit"), 100, 500)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_limit", err.Error())
		return
	}
	tasks, err := s.svc.ListEnrichmentTasks(r.Context(), limit)
	if err != nil {
		renderEnrichmentError(w, err)
		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: tasks, Meta: map[string]any{"count": len(tasks)}})
}

func decodeEnrichmentRequest(w http.ResponseWriter, r *http.Request) (enrichmentAPIRequest, error) {
	if strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		var request enrichmentAPIRequest
		if err := decodeStrictEnrichmentJSON(w, r, &request); err != nil {
			return enrichmentAPIRequest{}, err
		}

		return request, nil
	}
	if err := r.ParseForm(); err != nil {
		return enrichmentAPIRequest{}, fmt.Errorf("parse form: %w", err)
	}

	return enrichmentAPIRequest{
		IDs:     append([]string(nil), r.Form["result_ids"]...),
		Options: enrichmentOptionsFromForm(r),
	}, nil
}

func decodeEnrichmentOptions(w http.ResponseWriter, r *http.Request) (EnrichmentOptions, error) {
	if r.Body == nil || r.ContentLength == 0 {
		return EnrichmentOptions{}, nil
	}
	if strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		var request enrichmentAPIRequest
		if err := decodeStrictEnrichmentJSON(w, r, &request); err != nil {
			return EnrichmentOptions{}, err
		}

		return request.Options, nil
	}
	if err := r.ParseForm(); err != nil {
		return EnrichmentOptions{}, fmt.Errorf("parse form: %w", err)
	}

	return enrichmentOptionsFromForm(r), nil
}

func decodeStrictEnrichmentJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maximumEnrichmentRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request must contain one JSON object")
	}

	return nil
}

func enrichmentOptionsFromForm(r *http.Request) EnrichmentOptions {
	return EnrichmentOptions{
		Scope:                 r.FormValue("enrichment_scope"),
		MaxPages:              formInteger(r.FormValue("enrichment_max_pages")),
		TimeoutSeconds:        formInteger(r.FormValue("enrichment_timeout_seconds")),
		MaxBodyBytes:          int64(formInteger(r.FormValue("enrichment_max_body_bytes"))),
		MaxRedirects:          formInteger(r.FormValue("enrichment_max_redirects")),
		MaxInternalLinkChecks: formInteger(r.FormValue("enrichment_internal_links")),
		DisableInternalChecks: r.FormValue("enrichment_check_links") == "off",
		CheckMX:               r.FormValue("enrichment_check_mx") == "on",
		CaptureScreenshot:     r.FormValue("enrichment_capture_screenshot") == "on",
		Force:                 r.FormValue("enrichment_force") == "on",
		StaleAfterHours:       formInteger(r.FormValue("enrichment_stale_hours")),
	}
}

func formInteger(value string) int {
	parsed, _ := strconv.Atoi(strings.TrimSpace(value))

	return parsed
}

func boundedPositiveInt(value string, fallback, maximum int) (int, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 || parsed > maximum {
		return 0, fmt.Errorf("value must be between 1 and %d", maximum)
	}

	return parsed, nil
}

func renderEnrichmentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidEnrichment):
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_enrichment_request", err.Error())
	case errors.Is(err, ErrBusinessNotFound), errors.Is(err, ErrEnrichmentTaskNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "enrichment_target_not_found", err.Error())
	case errors.Is(err, ErrEnrichmentUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "enrichment_unavailable", "Durable website enrichment is unavailable")
	default:
		renderLocalAPIError(w, http.StatusInternalServerError, "enrichment_failed", "Could not queue or read local website enrichment")
	}
}
