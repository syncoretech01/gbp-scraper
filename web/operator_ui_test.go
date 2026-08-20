package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Every app page must execute against its own page-data type. A template that
// only parses is not enough: a renamed field surfaces as a 500 at request
// time, and several pages (job monitor, map, jobs) have no other render
// coverage. Zero values are used deliberately so empty states are exercised.
func TestEveryAppPageRendersWithZeroValuePageData(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, t.TempDir())

	pages := map[string]any{
		"api":            apiWorkspacePageData{},
		"dashboard":      dashboardPageData{},
		"exports":        exportsPageData{},
		"job_monitor":    jobMonitorPageData{},
		"jobs":           jobsPageData{},
		"map":            mapPageData{},
		"new_scrape":     newScrapePageData{},
		"onboarding":     onboardingPageData{},
		"proxies":        proxiesPageData{},
		"result_detail":  appBusinessDetail{},
		"results":        resultsPageData{},
		"saved_searches": reusablePageData{},
		"schedules":      schedulesPageData{},
		"settings":       settingsPageData{},
		"system":         systemPageData{},
	}

	for key, page := range pages {
		recorder := httptest.NewRecorder()
		server.renderAppPage(recorder, key, appPageData{
			Title: "Smoke", ActiveNav: key, Theme: "system", Page: page,
		})
		if recorder.Code != 200 {
			t.Fatalf("page %q rendered %d: %s", key, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), `<main class="app-main"`) {
			t.Fatalf("page %q did not render the app shell", key)
		}
	}
}

// The adaptive-coverage fieldset ships with the create form and uses the exact
// field names the server-side mapping reads. Renaming any of them silently
// drops an operator's saturation or expansion choice, so the contract is
// asserted here rather than trusted.
func TestNewScrapeWizardSubmitsAdaptiveCoverageFields(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, t.TempDir())

	recorder := httptest.NewRecorder()
	server.renderAppPage(recorder, "new_scrape", appPageData{
		Title: "New scrape", ActiveNav: "new-scrape", Theme: "system",
		Page: newScrapePageData{ProspectQueriesSupported: true},
	})
	if recorder.Code != 200 {
		t.Fatalf("new scrape render = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	for _, want := range []string{
		`name="coverage_auto_stop" type="checkbox" value="on"`,
		`name="coverage_saturation_window" type="number" min="3" max="50" value="8"`,
		`name="coverage_min_new_ratio" type="number" min="0" max="1" step="0.01" value="0.10"`,
		`name="coverage_max_expansions" type="number" min="0" max="500" value="0"`,
		`name="coverage_expansion_min_new" type="number" min="0" max="10000" value="10"`,
		"Stop automatically when new results dry up",
		"Expand into neighbouring ZIPs while yield is high",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("new scrape page misses %q", want)
		}
	}

	// The fieldset lives inside the form that posts to /app/scrapes, so the
	// values reach the create handler with the rest of the wizard.
	form := body[strings.Index(body, `action="/app/scrapes"`):]
	if index := strings.Index(form, "</form>"); index >= 0 {
		form = form[:index]
	}
	if !strings.Contains(form, `name="coverage_auto_stop"`) {
		t.Fatal("adaptive coverage fields are rendered outside the create form")
	}
}

// Without prospect support the adaptive-coverage controls must not render at
// all: their server mapping belongs to the GBP pipeline.
func TestNewScrapeWizardHidesAdaptiveCoverageWithoutProspects(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, t.TempDir())

	recorder := httptest.NewRecorder()
	server.renderAppPage(recorder, "new_scrape", appPageData{
		Title: "New scrape", ActiveNav: "new-scrape", Theme: "system",
		Page: newScrapePageData{},
	})
	if strings.Contains(recorder.Body.String(), "coverage_auto_stop") {
		t.Fatal("new scrape page shows adaptive coverage without prospect support")
	}
}

// The three wizard modes are progressive disclosure over one form: every step
// stays in the DOM so hidden values still submit. The markup must therefore
// carry the mode switch and the per-step mode markers app-wizard.js reads.
func TestNewScrapeWizardExposesProgressiveDisclosureModes(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, t.TempDir())

	recorder := httptest.NewRecorder()
	server.renderAppPage(recorder, "new_scrape", appPageData{
		Title: "New scrape", ActiveNav: "new-scrape", Theme: "system",
		Page: newScrapePageData{ProspectQueriesSupported: true},
	})

	body := recorder.Body.String()
	for _, want := range []string{
		"data-wizard-mode-switch", `value="basic"`, `value="advanced"`, `value="gbp"`,
		`data-wizard-panel="6" data-wizard-advanced`,
		"data-wizard-gbp",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("new scrape page misses %q", want)
		}
	}

	script, err := static.ReadFile("static/js/app-wizard.js")
	if err != nil {
		t.Fatalf("read app-wizard.js: %v", err)
	}
	for _, want := range []string{"data-wizard-mode-input", "gmaps-wizard-mode", "data-wizard-gbp"} {
		if !strings.Contains(string(script), want) {
			t.Fatalf("app-wizard.js misses %q", want)
		}
	}
}

// The page-heading eyebrow names the sidebar group of the current page so an
// operator always sees where they are, not a constant "Local workspace".
func TestPageHeadingEyebrowNamesTheNavigationGroup(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, t.TempDir())

	for nav, want := range map[string]string{
		"results":   "Results &amp; prospects",
		"jobs":      "Scrape",
		"settings":  "System",
		"dashboard": "Local workspace",
	} {
		recorder := httptest.NewRecorder()
		server.renderAppPage(recorder, "dashboard", appPageData{
			Title: "Any page", ActiveNav: nav, Theme: "system",
			Page: dashboardPageData{},
		})
		if !strings.Contains(recorder.Body.String(), `<p class="eyebrow">`+want+`</p>`) {
			t.Fatalf("eyebrow for %q is not %q", nav, want)
		}
	}
}

// The coverage-yield card is composed from data the dashboard already loads;
// with no raw records it must render its empty state rather than 0%.
func TestDashboardCoverageYieldCardStates(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, t.TempDir())

	recorder := httptest.NewRecorder()
	server.renderAppPage(recorder, "dashboard", appPageData{
		Title: "Dashboard", ActiveNav: "dashboard", Theme: "system",
		Page: dashboardPageData{},
	})
	body := recorder.Body.String()
	if !strings.Contains(body, "No coverage evidence yet") {
		t.Fatal("dashboard misses the coverage-yield empty state")
	}
	if strings.Contains(body, "view=data-gaps") {
		t.Fatal("dashboard still links to the saved-view name that answers 404")
	}

	recorder = httptest.NewRecorder()
	server.renderAppPage(recorder, "dashboard", appPageData{
		Title: "Dashboard", ActiveNav: "dashboard", Theme: "system",
		Page: dashboardPageData{Yield: dashboardYield{
			Jobs: 2, RawRecords: 500, UniqueRecords: 310, Duplicates: 190, Emails: 120,
			UniqueYield: "62.0%", DuplicateShare: "38.0%", EmailShare: "38.7%",
			UniquePercent: 62, DuplicatePercent: 38, EmailPercent: 39,
			BestJobID: "job-1", BestJobName: "SF dentists", BestJobYield: "70.0%",
		}},
	})
	body = recorder.Body.String()
	for _, want := range []string{"Coverage yield", "62.0% unique yield", "38.0% duplicate work", "SF dentists"} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard misses %q", want)
		}
	}
}

// accumulateDashboardYield must ignore jobs that produced nothing, and
// finaliseDashboardYield must collapse to the empty state instead of
// reporting a share computed from zero.
func TestDashboardYieldAccumulation(t *testing.T) {
	t.Parallel()

	var yield dashboardYield
	accumulateDashboardYield(&yield, Job{ID: "empty", Name: "Empty"}, 0, ResultStats{})
	finaliseDashboardYield(&yield)
	if yield.Jobs != 0 || yield.UniqueYield != "" {
		t.Fatalf("empty job contributed to the yield summary: %+v", yield)
	}

	yield = dashboardYield{}
	accumulateDashboardYield(&yield, Job{ID: "a", Name: "Wide"}, 100, ResultStats{Rows: 100, UniqueBusinesses: 80, Duplicates: 20, WithEmail: 40})
	accumulateDashboardYield(&yield, Job{ID: "b", Name: "Thin"}, 100, ResultStats{Rows: 100, UniqueBusinesses: 20, Duplicates: 80, WithEmail: 5})
	finaliseDashboardYield(&yield)

	if yield.Jobs != 2 || yield.RawRecords != 200 || yield.UniqueRecords != 100 {
		t.Fatalf("unexpected totals: %+v", yield)
	}
	if yield.UniqueYield != "50.0%" || yield.DuplicateShare != "50.0%" {
		t.Fatalf("unexpected shares: %+v", yield)
	}
	if yield.BestJobName != "Wide" || yield.WorstJobName != "Thin" {
		t.Fatalf("best/worst jobs not identified: %+v", yield)
	}
}
