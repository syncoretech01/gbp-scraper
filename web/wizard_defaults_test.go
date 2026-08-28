package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// inheritedJobContent is the exact content the shipped wizard used to open
// with. None of it may appear in a genuinely new scrape.
var inheritedJobContent = []string{
	"San Francisco dentists",
	"dentists in San Francisco",
	"dental clinics in San Francisco",
	"San Francisco, California, United States",
	"37.7749",
	"-122.4194",
}

// TestFreshWizardCarriesNoPriorJobContent is the issue-G regression.
//
// Reproduced against the deployed build: GET /app/scrapes/new returned a form
// whose name field was value="San Francisco dentists", whose query box held
// "dentists in San Francisco" and "dental clinics in San Francisco", and whose
// centre was 37.7749,-122.4194 -- three occurrences each of the coordinates in
// the rendered HTML. A new scrape must inherit none of it.
func TestFreshWizardCarriesNoPriorJobContent(t *testing.T) {
	t.Parallel()

	initial := freshWizardInitialValues(scrapeDefaults{})

	if initial.carriesJobContent() {
		t.Fatalf("a fresh wizard carries job content: %+v", initial)
	}

	if initial.Name != "" || initial.Keywords != "" {
		t.Fatalf("name = %q, keywords = %q; both must be empty", initial.Name, initial.Keywords)
	}

	if initial.LocationLabel != "" || initial.Latitude != "" || initial.Longitude != "" {
		t.Fatalf("geography = %q %q,%q; all must be empty without saved defaults",
			initial.LocationLabel, initial.Latitude, initial.Longitude)
	}

	if initial.GeographyMode != defaultWizardGeographyMode {
		t.Fatalf("geography mode = %q, want the %q default", initial.GeographyMode, defaultWizardGeographyMode)
	}
}

// TestFreshWizardAcceptsOnlySavedGlobalDefaults proves the one thing that MAY
// prefill a new scrape is the operator's own saved location default, and that
// it still brings no name and no query text with it.
func TestFreshWizardAcceptsOnlySavedGlobalDefaults(t *testing.T) {
	t.Parallel()

	initial := freshWizardInitialValues(scrapeDefaults{
		LocationLabel: "Austin, Texas",
		Lat:           "30.2672",
		Lon:           "-97.7431",
		Language:      "en",
		Zoom:          12,
	})

	if initial.LocationLabel != "Austin, Texas" || initial.Latitude != "30.2672" || initial.Longitude != "-97.7431" {
		t.Fatalf("saved defaults did not prefill the wizard: %+v", initial)
	}

	if initial.carriesJobContent() {
		t.Fatalf("saved defaults brought job content with them: %+v", initial)
	}
}

// TestFreshWizardIgnoresHalfACentre keeps a partially saved default from
// producing a wizard with a latitude and no longitude, which would submit an
// invalid job.
func TestFreshWizardIgnoresHalfACentre(t *testing.T) {
	t.Parallel()

	for _, defaults := range []scrapeDefaults{
		{Lat: "30.2672"},
		{Lon: "-97.7431"},
	} {
		initial := freshWizardInitialValues(defaults)
		if initial.Latitude != "" || initial.Longitude != "" {
			t.Fatalf("half a saved centre prefilled %q,%q", initial.Latitude, initial.Longitude)
		}
	}
}

// TestFreshWizardRendersNoInheritedContent renders the real wizard template
// with fresh initial values and proves not one of the inherited strings
// reaches the HTML -- including in a placeholder, where an example that names
// a real job's subject reads as state the operator has to clear.
func TestFreshWizardRendersNoInheritedContent(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, t.TempDir())

	recorder := httptest.NewRecorder()
	server.renderAppPage(recorder, "new_scrape", appPageData{
		Title: "New scrape", ActiveNav: "new-scrape", Theme: "system",
		Page: newScrapePageData{
			Initial:                  freshWizardInitialValues(scrapeDefaults{}),
			ProspectQueriesSupported: true,
			KeywordSetsSupported:     true,
		},
	})

	if recorder.Code != 200 {
		t.Fatalf("wizard rendered %d: %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()

	for _, content := range inheritedJobContent {
		// The San Francisco preset button is an explicit, labelled operator
		// action, so the strings it writes live in JavaScript, not in the
		// rendered form. Nothing in the HTML itself may carry them.
		if strings.Contains(body, content) {
			t.Fatalf("the fresh wizard HTML still carries %q", content)
		}
	}

	// The form still has to exist and still has to ask for the values.
	for _, required := range []string{`name="name"`, `name="keywords"`, `name="latitude"`, `name="longitude"`} {
		if !strings.Contains(body, required) {
			t.Fatalf("the wizard no longer submits %s", required)
		}
	}
}

// TestWizardFilterExamplesDoNotReadAsState is the G-2 regression: the category
// and name filter examples were HTML placeholders naming one real job's
// subject ("Dentist", "Dental clinic", "Orthodontist", "Dental laboratory",
// "clinic group"), which read as inherited values an operator had to clear.
func TestWizardFilterExamplesDoNotReadAsState(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, t.TempDir())

	recorder := httptest.NewRecorder()
	server.renderAppPage(recorder, "new_scrape", appPageData{
		Title: "New scrape", ActiveNav: "new-scrape", Theme: "system",
		Page: newScrapePageData{Initial: freshWizardInitialValues(scrapeDefaults{})},
	})

	body := recorder.Body.String()

	for _, placeholder := range []string{
		`placeholder="Dentist&#10;Dental clinic"`,
		`placeholder="Orthodontist&#10;Dental laboratory"`,
		`placeholder="dental"`,
		`placeholder="clinic group"`,
		`placeholder="San Francisco dental coverage"`,
		`placeholder="Bay Area dental queries"`,
		`placeholder="Dental practices"`,
	} {
		if strings.Contains(body, placeholder) {
			t.Fatalf("filter example %s still reads as inherited state", placeholder)
		}
	}
}

// TestWizardSubmitsCoverageTargets pins the issue-I contract on the form side:
// a ZIP coverage plan travels with the job as geography, so the field that
// carries it has to exist and has to keep its name.
func TestWizardSubmitsCoverageTargets(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, t.TempDir())

	recorder := httptest.NewRecorder()
	server.renderAppPage(recorder, "new_scrape", appPageData{
		Title: "New scrape", ActiveNav: "new-scrape", Theme: "system",
		Page: newScrapePageData{
			Initial:                  freshWizardInitialValues(scrapeDefaults{}),
			ProspectQueriesSupported: true,
		},
	})

	body := recorder.Body.String()

	for _, required := range []string{`name="query_targets"`, "data-query-targets", "data-hidden-narrowing"} {
		if !strings.Contains(body, required) {
			t.Fatalf("the wizard no longer carries %s", required)
		}
	}
}
