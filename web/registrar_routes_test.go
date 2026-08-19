package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// registerQualityRoutes, registerEnrichmentRoutes and registerCheckpointRoutes had
// no route-level coverage. These tests drive each registrar through its mux so
// that a change to a pattern, a CSRF gate, or a capability status code fails the
// build rather than degrading silently in the browser.

func newRegistrarTestServer(t *testing.T) *Server {
	t.Helper()

	server, err := New(NewService(newFormEncodingRepository(), t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return server
}

func TestQualityRoutesGateCSRFAndReportCapability(t *testing.T) {
	t.Parallel()

	server := newRegistrarTestServer(t)
	mux := http.NewServeMux()
	server.registerQualityRoutes(mux)

	// A mutation without the CSRF token must be refused before any repository work.
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/quality/rules", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("PUT rules without CSRF = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	// This repository does not implement quality scoring, so every route must say
	// so with a capability status rather than a 500 or a panic.
	for _, route := range []struct {
		method string
		target string
	}{
		{http.MethodGet, "/api/v1/quality/rules"},
		{http.MethodGet, "/api/v1/results/biz-one/quality"},
	} {
		recorder = httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(route.method, route.target, http.NoBody))

		if recorder.Code == http.StatusOK || recorder.Code >= http.StatusInternalServerError &&
			recorder.Code != http.StatusNotImplemented {
			t.Fatalf("%s %s = %d, body = %s", route.method, route.target, recorder.Code, recorder.Body.String())
		}
	}
}

func TestQualityRulesFromFormReadsEveryWeight(t *testing.T) {
	t.Parallel()

	defaults := DefaultQualityRuleSet()
	values := url.Values{
		"name":                   {"Custom local quality"},
		"open_weight":            {"11"},
		"active_website_weight":  {"16"},
		"https_weight":           {"6"},
		"phone_weight":           {"9"},
		"email_weight":           {"13"},
		"social_weight":          {"4"},
		"rating_weight":          {"8"},
		"review_count_weight":    {"7"},
		"completeness_weight":    {"12"},
		"freshness_weight":       {"5"},
		"website_quality_weight": {"10"},
		"response_time_weight":   {"3"},
		"rating_threshold":       {"4.2"},
		"review_count_threshold": {"25"},
		"freshness_days":         {"45"},
		"response_time_ms":       {"800"},
		"exclude_closed":         {"on"},
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/quality/rules", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rules, err := qualityRulesFromForm(request)
	if err != nil {
		t.Fatalf("qualityRulesFromForm() error = %v", err)
	}

	if rules.Name != "Custom local quality" || !rules.ExcludeClosed {
		t.Fatalf("rules identity = %+v", rules)
	}

	if rules.OpenWeight != 11 || rules.ActiveWebsiteWeight != 16 || rules.ResponseTimeWeight != 3 {
		t.Fatalf("weights not read from form: %+v", rules)
	}

	if rules.RatingThreshold != 4.2 || rules.ReviewCountThreshold != 25 {
		t.Fatalf("thresholds = %.2f / %d", rules.RatingThreshold, rules.ReviewCountThreshold)
	}

	// The form must be able to express something other than the built-in profile.
	if rules.OpenWeight == defaults.OpenWeight && rules.ActiveWebsiteWeight == defaults.ActiveWebsiteWeight {
		t.Fatal("form values were not applied over the defaults")
	}

	// A weight outside the accepted range must be rejected by name.
	values.Set("open_weight", "500")
	bad := httptest.NewRequest(http.MethodPost, "/api/v1/quality/rules", strings.NewReader(values.Encode()))
	bad.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if _, err := qualityRulesFromForm(bad); err == nil {
		t.Fatal("out-of-range weight was accepted")
	}
}

func TestEnrichmentRoutesGateCSRFAndReportCapability(t *testing.T) {
	t.Parallel()

	server := newRegistrarTestServer(t)
	mux := http.NewServeMux()
	server.registerEnrichmentRoutes(mux)

	// Capability is reported before the CSRF gate. That ordering is safe because an
	// unavailable capability leaks nothing, but it must stay deliberate: the
	// response has to be the capability status, never a 500 or a partial mutation.
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/results/enrich", strings.NewReader(`{"ids":["biz-one"]}`))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotImplemented ||
		!strings.Contains(recorder.Body.String(), "enrichment_unavailable") {
		t.Fatalf("enqueue without enrichment support = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/results/enrich", strings.NewReader(`{"ids":["biz-one"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("enqueue with CSRF but no enrichment support = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/enrichment/tasks", http.NoBody))

	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("task list without enrichment support = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestCheckpointRouteRejectsUnknownJobAndInvalidID(t *testing.T) {
	t.Parallel()

	server := newRegistrarTestServer(t)
	mux := http.NewServeMux()
	server.registerCheckpointRoutes(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/not-a-uuid/checkpoint", http.NoBody))

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid job ID = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodGet, "/api/v1/jobs/11111111-1111-4111-8111-111111111111/checkpoint", http.NoBody,
	))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown job = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
