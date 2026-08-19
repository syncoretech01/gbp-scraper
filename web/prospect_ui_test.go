package web

import (
	"context"
	"github.com/gosom/google-maps-scraper/web/prospect"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newProspectUITestServer(t *testing.T, repository JobRepository) *Server {
	t.Helper()

	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return server
}

func prospectUIRepository(t *testing.T, detail BusinessDetail) *fixedResultRepository {
	t.Helper()

	now := time.Now().UTC()
	repository := &fixedResultRepository{
		fixedJobRepository: &fixedJobRepository{job: Job{
			ID: "ba78441f-a048-4c9d-a8de-d0589e66f132", Name: "San Francisco dentists",
			Status: StatusOK, Date: now,
		}},
		page:     ResultPage{Total: 1, Limit: 25, Results: []BusinessResult{detail.Business}},
		overview: ResultOverview{UniqueBusinesses: 1, RawRecords: 1},
	}
	repository.detail = detail

	return repository
}

func prospectUIBusiness() BusinessResult {
	now := time.Now().UTC()
	score := 90.0

	return BusinessResult{
		ID: "biz_abcde", Name: "Bay Smile Dental", PrimaryCategory: "Dentist",
		Address: "123 Main St", City: "San Francisco", State: "CA",
		WebsiteStatus: "active", QualityScore: 85, Confidence: .9,
		ProspectStatus: "NO_WEBSITE", ProspectScore: &score, ProspectTier: "A",
		ScrapedAt: now, UpdatedAt: now,
	}
}

// The results page advertises the prospect filter fields, table columns, and
// the bulk recompute action only when the repository can serve prospect
// signals; the placeholder capability keys off normalized result storage.
func TestResultsPageRendersProspectFiltersColumnsAndBulkAction(t *testing.T) {
	t.Parallel()

	server := newProspectUITestServer(t, prospectUIRepository(t, BusinessDetail{Business: prospectUIBusiness()}))

	recorder := httptest.NewRecorder()
	server.resultsPage(recorder, httptest.NewRequest(http.MethodGet, "/app/results", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("results page = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	for _, want := range []string{
		`value="prospect_status"`, `value="prospect_tier"`, `value="prospect_score"`,
		"NO_WEBSITE, SOCIAL_ONLY, DEAD, PARKED, SSL_BROKEN, FREE_BUILDER, NO_HTTPS, LIVE",
		`data-column="prospect"`, `data-column="tier"`,
		"Recompute prospect score", "/api/v1/prospects/recompute",
		`class="prospect-badge prospect-no-website"`, "Tier A",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("results page misses %q", want)
		}
	}
}

// Without prospect support the recompute action must disappear while the
// remaining bulk actions stay untouched.
func TestResultsPageHidesProspectBulkActionWithoutSupport(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, t.TempDir())

	recorder := httptest.NewRecorder()
	server.renderAppPage(recorder, "results", appPageData{
		Title: "Results", ActiveNav: "results", Theme: "system",
		Page: resultsPageData{Capabilities: appResultCapabilities{CanSelect: true, CanTag: true}},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("results page render = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	if strings.Contains(body, "Recompute prospect score") || strings.Contains(body, "/api/v1/prospects/recompute") {
		t.Fatal("results page shows the prospect bulk action without prospect support")
	}
	if !strings.Contains(body, `value="prospect_status"`) {
		t.Fatal("results page misses the prospect filter field option")
	}
}

// The drawer's Prospecting section renders the badge, score, tier, the
// explainable reasons, and the server-rendered call opener through the real
// template path.
func TestBusinessDrawerRendersProspectingSection(t *testing.T) {
	t.Parallel()

	detail := BusinessDetail{
		Business: prospectUIBusiness(),
		RawJSON:  `{"title":"Bay Smile Dental"}`,
		ProspectReasons: `[{"signal":"NO_WEBSITE","contribution":40,"detail":` +
			`"the listing links no website"}]`,
	}
	server := newProspectUITestServer(t, prospectUIRepository(t, detail))

	request := httptest.NewRequest(http.MethodGet, "/app/results/biz_abcde/drawer", nil)
	request.SetPathValue("id", "biz_abcde")
	recorder := httptest.NewRecorder()
	server.businessDetailDrawer(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("drawer = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	for _, want := range []string{
		"Prospecting",
		`class="prospect-badge prospect-no-website"`, "NO_WEBSITE",
		"score <strong>90</strong>", "Tier A",
		// html/template escapes "+" to &#43; inside the contribution cell.
		"&#43;40.0", "the listing links no website",
		"Call opener", "prospect-opener-biz_abcde", "readonly",
		"Hi Bay Smile Dental, I searched for Dentist in San Francisco",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("drawer misses %q in: %s", want, body)
		}
	}
}

// A business without a stored prospect status renders no Prospecting section
// instead of an empty shell.
func TestBusinessDrawerSkipsProspectingWithoutStatus(t *testing.T) {
	t.Parallel()

	business := prospectUIBusiness()
	business.ProspectStatus = ""
	business.ProspectScore = nil
	business.ProspectTier = ""
	server := newProspectUITestServer(t, prospectUIRepository(t, BusinessDetail{
		Business: business, RawJSON: `{}`,
	}))

	request := httptest.NewRequest(http.MethodGet, "/app/results/biz_abcde/drawer", nil)
	request.SetPathValue("id", "biz_abcde")
	recorder := httptest.NewRecorder()
	server.businessDetailDrawer(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("drawer = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	if strings.Contains(body, "Prospecting") || strings.Contains(body, "prospect-badge") {
		t.Fatal("drawer renders a Prospecting section for an unscored business")
	}
}

// The dashboard card shows an honest empty state when supported but unscored,
// real counts when data exists, and nothing at all when unsupported.
func TestDashboardRendersProspectingCardStates(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, t.TempDir())

	recorder := httptest.NewRecorder()
	server.renderAppPage(recorder, "dashboard", appPageData{
		Title: "Dashboard", ActiveNav: "dashboard", Theme: "system",
		Page: dashboardPageData{Prospects: dashboardProspectSummary{Supported: true}},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("dashboard render = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `id="prospecting-heading"`) || !strings.Contains(body, "No prospect signals yet") {
		t.Fatal("dashboard misses the Prospecting card empty state")
	}

	recorder = httptest.NewRecorder()
	server.renderAppPage(recorder, "dashboard", appPageData{
		Title: "Dashboard", ActiveNav: "dashboard", Theme: "system",
		Page: dashboardPageData{Prospects: dashboardProspectSummary{
			Supported: true,
			Scored:    3,
			ByStatus:  []dashboardProspectPoint{{Label: "NO_WEBSITE", State: "no-website", Value: 2}},
			ByTier:    []dashboardProspectPoint{{Label: "A", State: "a", Value: 1}},
		}},
	})
	body = recorder.Body.String()
	for _, want := range []string{
		"prospect-badge prospect-no-website", "NO_WEBSITE",
		"prospect-badge prospect-tier-a", "Tier A",
		"3 businesses have a prospect score.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard misses %q", want)
		}
	}

	recorder = httptest.NewRecorder()
	server.renderAppPage(recorder, "dashboard", appPageData{
		Title: "Dashboard", ActiveNav: "dashboard", Theme: "system",
		Page: dashboardPageData{},
	})
	if strings.Contains(recorder.Body.String(), "prospecting-heading") {
		t.Fatal("dashboard renders the Prospecting card without prospect support")
	}
}

// The wizard's GBP coverage block is capability-gated and its script posts to
// the local prospect query endpoint with a CSRF header.
func TestNewScrapeWizardIncludesGBPCoverageBlock(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, t.TempDir())

	recorder := httptest.NewRecorder()
	server.renderAppPage(recorder, "new_scrape", appPageData{
		Title: "New scrape", ActiveNav: "new-scrape", Theme: "system",
		Page: newScrapePageData{ProspectQueriesSupported: true},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("new scrape render = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"GBP prospecting coverage",
		`data-action="generate-gbp-queries"`,
		"zip,city,state,latitude,longitude,population",
		"bundled ZIP list is a small sample",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("new scrape page misses %q", want)
		}
	}

	recorder = httptest.NewRecorder()
	server.renderAppPage(recorder, "new_scrape", appPageData{
		Title: "New scrape", ActiveNav: "new-scrape", Theme: "system",
		Page: newScrapePageData{},
	})
	if strings.Contains(recorder.Body.String(), "GBP prospecting coverage") {
		t.Fatal("new scrape page shows the GBP coverage block without prospect support")
	}

	script, err := static.ReadFile("static/js/app-wizard.js")
	if err != nil {
		t.Fatalf("read app-wizard.js: %v", err)
	}
	for _, want := range []string{"/api/v1/prospects/queries", "X-CSRF-Token", "zips_csv", "queries across"} {
		if !strings.Contains(string(script), want) {
			t.Fatalf("app-wizard.js misses %q", want)
		}
	}
}

// The settings page loads the dedicated prospect editors module and exposes
// the three JSON editor endpoints.
func TestSettingsPageLoadsProspectEditors(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, t.TempDir())

	recorder := httptest.NewRecorder()
	server.renderAppPage(recorder, "settings", appPageData{
		Title: "Settings", ActiveNav: "settings", Theme: "system",
		Page: settingsPageData{},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("settings render = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"/static/js/app-prospects.js", "data-prospect-settings",
		"/api/v1/prospects/scoring", "/api/v1/prospects/openers", "/api/v1/prospects/integrations",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("settings page misses %q", want)
		}
	}

	script, err := static.ReadFile("static/js/app-prospects.js")
	if err != nil {
		t.Fatalf("read app-prospects.js: %v", err)
	}
	for _, want := range []string{`method: "PUT"`, "X-CSRF-Token"} {
		if !strings.Contains(string(script), want) {
			t.Fatalf("app-prospects.js misses %q", want)
		}
	}
}

// prospectStateClass backs every badge class, so it must map the taxonomy
// (and tiers) onto CSS-safe suffixes and reject garbage.
func TestProspectStateClassAndReasonParsing(t *testing.T) {
	t.Parallel()

	states := map[string]string{
		"NO_WEBSITE": "no-website", "SSL_BROKEN": "ssl-broken", "LIVE": "live",
		"A": "a", "": "", "  ": "", "<script>": "script",
	}
	for input, want := range states {
		if got := prospectStateClass(input); got != want {
			t.Fatalf("prospectStateClass(%q) = %q, want %q", input, got, want)
		}
	}

	reasons := parseProspectReasons(`[{"signal":"NO_WEBSITE","contribution":40,"detail":"no site"},` +
		`{"signal":"","detail":""}]`)
	if len(reasons) != 1 || reasons[0].Signal != "NO_WEBSITE" ||
		reasons[0].ContributionLabel != "+40.0" || reasons[0].Detail != "no site" {
		t.Fatalf("parseProspectReasons = %+v", reasons)
	}
	if parseProspectReasons("not json") != nil {
		t.Fatal("parseProspectReasons accepted malformed JSON")
	}
	if parseProspectReasons("[]") == nil {
		// An empty array parses to an empty, non-nil slice; both render the
		// same honest fallback so either representation is acceptable.
		t.Log("empty reasons array parsed to nil")
	}
}

// The shared results stub gains prospect capability so these tests exercise
// the real SupportsProspects semantics (repository interface assertion).
func (r *fixedResultRepository) RecomputeProspects(context.Context, prospect.ScoreWeights, []string) (int64, error) {
	return 0, nil
}

func (r *fixedResultRepository) ProspectSummary(context.Context) (ProspectSummary, error) {
	return ProspectSummary{}, nil
}
