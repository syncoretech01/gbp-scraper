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
)

const maximumQualityRequestBytes = 64 << 10

type qualityRecalculationInput struct {
	IDs []string `json:"ids"`
}

func (s *Server) registerQualityRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/quality/rules", s.apiQualityRules)
	mux.HandleFunc("PUT /api/v1/quality/rules", s.apiQualityRules)
	mux.HandleFunc("POST /api/v1/quality/rules", s.apiQualityRules)
	mux.HandleFunc("POST /api/v1/quality/recalculate", s.apiRecalculateQuality)
	mux.HandleFunc("GET /api/v1/results/{id}/quality", s.apiBusinessQuality)
}

func (s *Server) apiQualityRules(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		rules, err := s.svc.ActiveQualityRules(r.Context())
		if err != nil {
			renderQualityAPIError(w, err)
			return
		}
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: rules})
		return
	}
	if !s.requireCSRF(w, r) {
		return
	}
	var rules QualityRuleSet
	if r.Method == http.MethodPost && strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		var err error
		rules, err = qualityRulesFromForm(r)
		if err != nil {
			renderQualityAPIError(w, err)
			return
		}
	} else {
		if err := decodeBoundedQualityJSON(w, r, &rules); err != nil {
			renderQualityAPIError(w, err)
			return
		}
	}
	saved, err := s.svc.SaveQualityRules(r.Context(), rules)
	if err != nil {
		renderQualityAPIError(w, err)
		return
	}
	if r.Method == http.MethodPost && !strings.Contains(r.Header.Get("Accept"), "application/json") {
		http.Redirect(w, r, "/app/settings?notice=Quality+scoring+rules+saved", http.StatusSeeOther)
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: saved})
}

func qualityRulesFromForm(r *http.Request) (QualityRuleSet, error) {
	parseFloat := func(name string) (float64, error) {
		value, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue(name)), 64)
		if err != nil || value < 0 || value > 100 {
			return 0, fmt.Errorf("%w: %s must be between 0 and 100", ErrInvalidQualityRules, strings.ReplaceAll(name, "_", " "))
		}
		return value, nil
	}
	parseInt := func(name string, minimum, maximum int64) (int64, error) {
		value, err := strconv.ParseInt(strings.TrimSpace(r.FormValue(name)), 10, 64)
		if err != nil || value < minimum || value > maximum {
			return 0, fmt.Errorf("%w: %s must be between %d and %d", ErrInvalidQualityRules, strings.ReplaceAll(name, "_", " "), minimum, maximum)
		}
		return value, nil
	}

	rules := QualityRuleSet{Name: strings.TrimSpace(r.FormValue("name")), ExcludeClosed: r.FormValue("exclude_closed") == "on"}
	weights := []struct {
		name   string
		target *float64
	}{
		{"open_weight", &rules.OpenWeight}, {"active_website_weight", &rules.ActiveWebsiteWeight},
		{"https_weight", &rules.HTTPSWeight}, {"phone_weight", &rules.PhoneWeight},
		{"email_weight", &rules.EmailWeight}, {"social_weight", &rules.SocialWeight},
		{"rating_weight", &rules.RatingWeight}, {"review_count_weight", &rules.ReviewCountWeight},
		{"completeness_weight", &rules.CompletenessWeight}, {"freshness_weight", &rules.FreshnessWeight},
		{"website_quality_weight", &rules.WebsiteQualityWeight}, {"response_time_weight", &rules.ResponseTimeWeight},
	}
	for _, weight := range weights {
		value, err := parseFloat(weight.name)
		if err != nil {
			return QualityRuleSet{}, err
		}
		*weight.target = value
	}
	rating, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("rating_threshold")), 64)
	if err != nil {
		return QualityRuleSet{}, fmt.Errorf("%w: invalid rating threshold", ErrInvalidQualityRules)
	}
	rules.RatingThreshold = rating
	reviews, err := parseInt("review_count_threshold", 1, 10_000_000)
	if err != nil {
		return QualityRuleSet{}, err
	}
	rules.ReviewCountThreshold = reviews
	freshness, err := parseInt("freshness_days", 1, 3650)
	if err != nil {
		return QualityRuleSet{}, err
	}
	rules.FreshnessDays = int(freshness)
	responseTime, err := parseInt("response_time_ms", 100, 120_000)
	if err != nil {
		return QualityRuleSet{}, err
	}
	rules.ResponseTimeMS = responseTime
	if err := ValidateQualityRuleSet(rules); err != nil {
		return QualityRuleSet{}, err
	}

	return rules, nil
}

func (s *Server) apiRecalculateQuality(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	var input qualityRecalculationInput
	if err := decodeBoundedQualityJSON(w, r, &input); err != nil {
		renderQualityAPIError(w, err)
		return
	}
	if len(input.IDs) > 100_000 {
		renderQualityAPIError(w, fmt.Errorf("%w: too many selected businesses", ErrInvalidResultMutation))
		return
	}
	count, err := s.svc.RecalculateQuality(r.Context(), input.IDs)
	if err != nil {
		renderQualityAPIError(w, err)
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]any{"recalculated": count}})
}

func (s *Server) apiBusinessQuality(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !validBusinessID(id) {
		renderQualityAPIError(w, fmt.Errorf("%w: invalid business ID", ErrInvalidResultQuery))
		return
	}
	report, err := s.svc.BusinessQuality(r.Context(), id)
	if err != nil {
		renderQualityAPIError(w, err)
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: report})
}

func decodeBoundedQualityJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maximumQualityRequestBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("%w: request body is too large", ErrInvalidQualityRules)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("%w: request body is empty", ErrInvalidQualityRules)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid JSON: %v", ErrInvalidQualityRules, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: request must contain one JSON object", ErrInvalidQualityRules)
	}

	return nil
}

func renderQualityAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrBusinessNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "business_not_found", "Business not found")
	case errors.Is(err, ErrInvalidQualityRules), errors.Is(err, ErrInvalidResultMutation), errors.Is(err, ErrInvalidResultQuery):
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_quality_request", err.Error())
	case errors.Is(err, ErrQualityScoringUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "quality_unavailable", "Quality scoring is unavailable")
	default:
		renderLocalAPIError(w, http.StatusInternalServerError, "quality_failed", "Could not process local quality scoring")
	}
}
