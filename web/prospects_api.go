package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gosom/google-maps-scraper/web/prospect"
)

const (
	maximumProspectJSONBytes = 64 << 10
	maximumProspectFormBytes = 2 << 20
)

type prospectRecomputeInput struct {
	IDs []string `json:"ids"`
	// AuditFirst queues the website audits the score depends on before the
	// pass runs. It defaults to true: an explicit scoring request for a
	// never-checked website should produce evidence, not a guess. Send false
	// to score from whatever evidence already exists.
	AuditFirst *bool `json:"audit_first,omitempty"`
}

// registerProspectRoutes wires the GBP-prospecting API. Every GET works with
// the same API-key auth as the other read-only endpoints; every mutation is
// CSRF-gated.
func (s *Server) registerProspectRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/prospects/summary", s.apiProspectSummary)
	mux.HandleFunc("POST /api/v1/prospects/recompute", s.apiProspectRecompute)
	mux.HandleFunc("GET /api/v1/prospects/scoring", s.apiProspectScoring)
	mux.HandleFunc("PUT /api/v1/prospects/scoring", s.apiProspectScoring)
	mux.HandleFunc("GET /api/v1/prospects/openers", s.apiProspectOpeners)
	mux.HandleFunc("PUT /api/v1/prospects/openers", s.apiProspectOpeners)
	mux.HandleFunc("GET /api/v1/prospects/integrations", s.apiProspectIntegrations)
	mux.HandleFunc("PUT /api/v1/prospects/integrations", s.apiProspectIntegrations)
	mux.HandleFunc("GET /api/v1/prospects/discovered", s.apiDiscoveredCompanies)
	mux.HandleFunc("POST /api/v1/prospects/queries", s.apiProspectQueries)
	// The canonical website-state surface is registered here rather than in
	// the shared route table: it is the same prospecting area, and keeping
	// the registration next to its owner keeps web.go untouched.
	s.registerWebsiteStateRoutes(mux)
}

func (s *Server) apiProspectSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := s.svc.ProspectSummaryData(r.Context())
	if err != nil {
		renderProspectAPIError(w, err)
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: summary})
}

func (s *Server) apiProspectRecompute(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	var input prospectRecomputeInput
	if strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		for _, line := range strings.Split(strings.ReplaceAll(r.FormValue("ids"), "\r\n", "\n"), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				input.IDs = append(input.IDs, line)
			}
		}
	} else if err := decodeBoundedProspectJSON(w, r, &input); err != nil {
		renderProspectAPIError(w, err)
		return
	}
	auditFirst := true
	if input.AuditFirst != nil {
		auditFirst = *input.AuditFirst
	}
	result, err := s.svc.RecomputeProspectsWithAudit(r.Context(), input.IDs, auditFirst)
	if err != nil {
		renderProspectAPIError(w, err)
		return
	}
	payload := map[string]any{"processed": result.Processed}
	if result.Prerequisite != nil {
		payload["website_audit_prerequisite"] = result.Prerequisite
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: payload})
}

// apiProspectScoring reads and writes the worth-calling score weights. The
// PUT body is JSON only; the settings form posts JSON via fetch.
func (s *Server) apiProspectScoring(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		weights, err := s.svc.ProspectScoreWeights(r.Context())
		if err != nil {
			renderProspectAPIError(w, err)
			return
		}
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: weights})
		return
	}
	if !s.requireCSRF(w, r) {
		return
	}
	weights := prospect.DefaultScoreWeights()
	if err := decodeBoundedProspectJSON(w, r, &weights); err != nil {
		renderProspectAPIError(w, err)
		return
	}
	if err := s.svc.SaveProspectScoreWeights(r.Context(), weights); err != nil {
		renderProspectAPIError(w, err)
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: weights})
}

func (s *Server) apiProspectOpeners(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		templates, err := s.svc.ProspectOpenerTemplates(r.Context())
		if err != nil {
			renderProspectAPIError(w, err)
			return
		}
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: templates})
		return
	}
	if !s.requireCSRF(w, r) {
		return
	}
	var templates map[string]string
	if err := decodeBoundedProspectJSON(w, r, &templates); err != nil {
		renderProspectAPIError(w, err)
		return
	}
	if err := s.svc.SaveProspectOpenerTemplates(r.Context(), templates); err != nil {
		renderProspectAPIError(w, err)
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: templates})
}

func (s *Server) apiProspectIntegrations(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		settings, err := s.svc.ProspectIntegrationConfiguration(r.Context())
		if err != nil {
			renderProspectAPIError(w, err)
			return
		}
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: settings})
		return
	}
	if !s.requireCSRF(w, r) {
		return
	}
	var settings ProspectIntegrationConfig
	if err := decodeBoundedProspectJSON(w, r, &settings); err != nil {
		renderProspectAPIError(w, err)
		return
	}
	if err := s.svc.SaveProspectIntegrationConfiguration(r.Context(), settings); err != nil {
		renderProspectAPIError(w, err)
		return
	}
	saved, err := s.svc.ProspectIntegrationConfiguration(r.Context())
	if err != nil {
		renderProspectAPIError(w, err)
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: saved})
}

// apiDiscoveredCompanies is the Lead-Engine boundary: a read-only pull of one
// job's businesses in the DiscoveredCompany contract shape. It stays dormant
// behind the explicit future-integrations toggle.
func (s *Server) apiDiscoveredCompanies(w http.ResponseWriter, r *http.Request) {
	if !s.requireFutureIntegrations(w, r) {
		return
	}
	jobID := strings.TrimSpace(r.URL.Query().Get("job_id"))
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maximumDiscoveredCompanyLimit {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_prospect_request",
				fmt.Sprintf("Limit must be between 1 and %d", maximumDiscoveredCompanyLimit))
			return
		}
		limit = value
	}
	companies, err := s.svc.DiscoveredCompanies(r.Context(), jobID, limit)
	if err != nil {
		renderProspectAPIError(w, err)
		return
	}
	if companies == nil {
		companies = []DiscoveredCompany{}
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: companies})
}

// apiProspectQueries generates seed queries for the wizard from a state and
// city filter, category synonyms (one per line), and an optional ZIP CSV.
func (s *Server) apiProspectQueries(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	if err := parseBoundedRequestForm(w, r, maximumProspectFormBytes); err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_prospect_request", "Request form could not be parsed")
		return
	}
	topN := 0
	if raw := strings.TrimSpace(r.FormValue("top_n")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maximumQueryTopN {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_prospect_request",
				fmt.Sprintf("top_n must be between 1 and %d", maximumQueryTopN))
			return
		}
		topN = value
	}
	synonyms := strings.Split(strings.ReplaceAll(r.FormValue("synonyms"), "\r\n", "\n"), "\n")

	var zipCSV io.Reader
	if file, _, err := r.FormFile("zips_csv"); err == nil {
		defer file.Close()
		zipCSV = file
	}

	plan, err := s.svc.GenerateProspectQueries(r.FormValue("state"), r.FormValue("city"), topN, synonyms, zipCSV)
	if err != nil {
		renderProspectAPIError(w, err)
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: plan})
}

func decodeBoundedProspectJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maximumProspectJSONBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("%w: request body is too large", ErrInvalidProspectConfig)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("%w: request body is empty", ErrInvalidProspectConfig)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid JSON: %v", ErrInvalidProspectConfig, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: request must contain one JSON document", ErrInvalidProspectConfig)
	}
	return nil
}

func renderProspectAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidProspectConfig), errors.Is(err, ErrInvalidResultQuery):
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_prospect_request", err.Error())
	case errors.Is(err, ErrProspectsUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "prospects_unavailable", "Prospect scoring is unavailable")
	case errors.Is(err, ErrSettingsUnsupported), errors.Is(err, ErrResultStoreUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "prospects_unavailable", "Prospect storage is unavailable")
	default:
		renderLocalAPIError(w, http.StatusInternalServerError, "prospect_failed", "Could not process the prospect request")
	}
}
