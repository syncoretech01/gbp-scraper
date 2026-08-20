package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gosom/google-maps-scraper/web/enrichment"
)

func TestPreclassifyOptionsCoerceLightweightProfile(t *testing.T) {
	t.Parallel()

	options, err := (EnrichmentOptions{
		Preclassify:           true,
		Scope:                 string(enrichment.ScopeContactAbout),
		MaxPages:              8,
		TimeoutSeconds:        45,
		MaxBodyBytes:          5 << 20,
		MaxRedirects:          15,
		MaxInternalLinkChecks: 50,
		CheckMX:               true,
		CaptureScreenshot:     true,
	}).normalized()
	if err != nil {
		t.Fatalf("normalized() error = %v", err)
	}

	if !options.Preclassify || options.Scope != string(enrichment.ScopeHomepage) || options.MaxPages != 1 ||
		!options.DisableInternalChecks || options.MaxInternalLinkChecks != 0 ||
		options.CheckMX || options.CaptureScreenshot {
		t.Fatalf("coerced profile = %+v", options)
	}

	if options.TimeoutSeconds != preclassifyMaximumTimeoutSeconds ||
		options.MaxBodyBytes != preclassifyMaximumBodyBytes ||
		options.MaxRedirects != preclassifyMaximumRedirects {
		t.Fatalf("coerced caps = %+v", options)
	}

	defaults, err := (EnrichmentOptions{Preclassify: true}).normalized()
	if err != nil {
		t.Fatalf("normalized() defaults error = %v", err)
	}

	if defaults.TimeoutSeconds != preclassifyDefaultTimeoutSeconds ||
		defaults.MaxBodyBytes != preclassifyDefaultBodyBytes ||
		defaults.MaxRedirects != preclassifyMaximumRedirects {
		t.Fatalf("default caps = %+v", defaults)
	}

	if profile := PreclassifyProfile(); profile != defaults {
		t.Fatalf("PreclassifyProfile() = %+v, want %+v", profile, defaults)
	}
}

func TestPreclassifyOptionJSONRoundTripAndStrictDecode(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(EnrichmentOptions{Preclassify: true})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	if !strings.Contains(string(encoded), `"preclassify":true`) {
		t.Fatalf("encoded options = %s", encoded)
	}

	var decoded EnrichmentOptions
	if err := json.Unmarshal(encoded, &decoded); err != nil || !decoded.Preclassify {
		t.Fatalf("Unmarshal() = %+v, %v", decoded, err)
	}

	// POST /api/v1/results/enrich decodes with DisallowUnknownFields, so the
	// new option must be a known field of the strict request payload.
	request := httptest.NewRequest(
		"POST",
		"/api/v1/results/enrich",
		strings.NewReader(`{"ids":["business-1"],"options":{"preclassify":true,"timeout_seconds":5}}`),
	)
	request.Header.Set("Content-Type", "application/json")

	apiRequest, err := decodeEnrichmentRequest(httptest.NewRecorder(), request)
	if err != nil {
		t.Fatalf("decodeEnrichmentRequest() error = %v", err)
	}

	if len(apiRequest.IDs) != 1 || !apiRequest.Options.Preclassify || apiRequest.Options.TimeoutSeconds != 5 {
		t.Fatalf("decoded API request = %+v", apiRequest)
	}
}

func TestEnrichmentOptionsForJobPassesPreclassifyThrough(t *testing.T) {
	t.Parallel()

	options, enabled, err := EnrichmentOptionsForJob(JobData{Enrichment: &JobEnrichmentOptions{
		Website:     true,
		Preclassify: true,
		MaxPages:    9,
		CheckMX:     true,
		Emails:      true,
	}})
	if err != nil || !enabled {
		t.Fatalf("EnrichmentOptionsForJob() = %v, %v", enabled, err)
	}

	if !options.Preclassify || options.MaxPages != 1 ||
		options.Scope != string(enrichment.ScopeHomepage) || options.CheckMX {
		t.Fatalf("job options = %+v", options)
	}

	if err := (JobEnrichmentOptions{Preclassify: true, MaxPages: 9}).Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestDefaultWebsiteAnalyzerSelectsPreclassifyProbe(t *testing.T) {
	t.Parallel()

	analyzer, err := defaultWebsiteAnalyzer(PreclassifyProfile())
	if err != nil {
		t.Fatalf("defaultWebsiteAnalyzer(preclassify) error = %v", err)
	}

	if _, ok := analyzer.(preclassifyAnalyzer); !ok {
		t.Fatalf("analyzer = %T, want preclassifyAnalyzer", analyzer)
	}

	analyzer, err = defaultWebsiteAnalyzer(EnrichmentOptions{})
	if err != nil {
		t.Fatalf("defaultWebsiteAnalyzer(full) error = %v", err)
	}

	if _, ok := analyzer.(*enrichment.Crawler); !ok {
		t.Fatalf("analyzer = %T, want *enrichment.Crawler", analyzer)
	}
}
