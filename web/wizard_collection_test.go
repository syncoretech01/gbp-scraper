package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The step-3 and step-5 controls have to survive the real form round trip, or
// the wizard would present choices the job never receives.
func TestCreateScrapeFromWizardStoresFieldSelectionAndFilters(t *testing.T) {
	t.Parallel()

	repository := &lifecycleCaptureRepository{}
	server, err := New(NewService(repository, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	form := validWizardForm(server.csrfToken)
	form.Set("fields_selected", "on")
	form["fields"] = []string{"category", "rating", "reviews", "place_id"}
	form.Set("filter_rating_min", "4")
	form.Set("filter_reviews_min", "25")
	form.Set("filter_include_categories", "Dentist, Dental clinic")
	form.Set("filter_exclude_categories", "Orthodontist")
	form["filter_status"] = []string{"operational"}
	form.Set("filter_claimed", "unclaimed")
	form.Set("filter_name_contains", "dental")
	form.Set("filter_name_excludes", "laboratory")
	form.Set("incremental_mode", IncrementalModeNewChanged)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/app/scrapes", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	server.createScrapeFromWizard(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repository.job == nil {
		t.Fatal("no job was created")
	}

	data := repository.job.Data
	// Name is required and is added back even though the form omitted it.
	if len(data.Fields) != 5 || data.Fields[0] != "name" {
		t.Fatalf("stored fields = %v, want the required name plus the four chosen", data.Fields)
	}
	if data.ResultFilters == nil {
		t.Fatal("the step-5 filters were dropped")
	}
	if data.ResultFilters.RatingMin == nil || *data.ResultFilters.RatingMin != 4 {
		t.Fatalf("rating minimum = %+v", data.ResultFilters.RatingMin)
	}
	if data.ResultFilters.ReviewsMin == nil || *data.ResultFilters.ReviewsMin != 25 {
		t.Fatalf("review minimum = %+v", data.ResultFilters.ReviewsMin)
	}
	if len(data.ResultFilters.IncludeCategories) != 2 {
		t.Fatalf("included categories = %v, want the comma list split", data.ResultFilters.IncludeCategories)
	}
	if data.ResultFilters.Claimed == nil || *data.ResultFilters.Claimed {
		t.Fatalf("claim filter = %+v, want unclaimed", data.ResultFilters.Claimed)
	}
	if data.IncrementalMode != IncrementalModeNewChanged {
		t.Fatalf("incremental mode = %q", data.IncrementalMode)
	}

	// The plan the job resolves to must combine both, and must carry the
	// honesty notices the UI repeats.
	plan := BuildJobCollectionPlan(repository.job.ID, data)
	if plan.Search.FilterGroup == nil {
		t.Fatal("the collection plan resolved to no filters")
	}
	joined := strings.Join(plan.Notices, " ")
	if !strings.Contains(joined, "after collection") {
		t.Fatalf("plan notices do not state post-collection filtering: %v", plan.Notices)
	}
}

// An untouched step 3 must store nothing, so a job created without opening the
// step is byte-identical to one created before the step existed.
func TestCreateScrapeFromWizardKeepsEveryFieldByDefault(t *testing.T) {
	t.Parallel()

	repository := &lifecycleCaptureRepository{}
	server, err := New(NewService(repository, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	form := validWizardForm(server.csrfToken)
	// The checkboxes are rendered checked, so they post even when the toggle
	// is off. The toggle, not the checkboxes, is what decides.
	form["fields"] = []string{"category", "rating"}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/app/scrapes", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	server.createScrapeFromWizard(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repository.job == nil {
		t.Fatal("no job was created")
	}
	if len(repository.job.Data.Fields) != 0 {
		t.Fatalf("stored fields = %v, want none", repository.job.Data.Fields)
	}
	if repository.job.Data.ResultFilters != nil {
		t.Fatalf("stored filters = %+v, want none", repository.job.Data.ResultFilters)
	}

	plan := BuildJobCollectionPlan(repository.job.ID, repository.job.Data)
	if !plan.AllFields || plan.Search.FilterGroup != nil {
		t.Fatalf("a default job resolved to a narrowing plan: %+v", plan)
	}
}

// A parameterised configuration generates its query lines at creation time,
// which is what lets a template be reused across many cities.
func TestCreateScrapeFromWizardExpandsParameters(t *testing.T) {
	t.Parallel()

	repository := &lifecycleCaptureRepository{}
	server, err := New(NewService(repository, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	form := validWizardForm(server.csrfToken)
	form.Set("keywords", "")
	form.Set("parameter_categories", "dentist\northodontist")
	form.Set("parameter_locations", "San Francisco, Oakland")
	form.Set("parameter_pattern", "{category} near {location}")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/app/scrapes", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	server.createScrapeFromWizard(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repository.job == nil {
		t.Fatal("no job was created")
	}

	got := strings.Join(repository.job.Data.Keywords, "|")
	want := "dentist near San Francisco|dentist near Oakland|" +
		"orthodontist near San Francisco|orthodontist near Oakland"
	if got != want {
		t.Fatalf("expanded queries = %q, want %q", got, want)
	}
	if repository.job.Data.Parameters == nil {
		t.Fatal("the parameters were not stored with the job")
	}
}

// A configuration with neither typed queries nor parameters is still rejected.
func TestCreateScrapeFromWizardStillRequiresAQuery(t *testing.T) {
	t.Parallel()

	repository := &lifecycleCaptureRepository{}
	server, err := New(NewService(repository, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	form := validWizardForm(server.csrfToken)
	form.Set("keywords", "   ")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/app/scrapes", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	server.createScrapeFromWizard(recorder, request)

	if recorder.Code != http.StatusUnprocessableEntity || repository.job != nil {
		t.Fatalf("status = %d, job = %+v", recorder.Code, repository.job)
	}
}

// Impossible filter combinations are rejected before anything is stored.
func TestCreateScrapeFromWizardRejectsImpossibleFilters(t *testing.T) {
	t.Parallel()

	repository := &lifecycleCaptureRepository{}
	server, err := New(NewService(repository, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	form := validWizardForm(server.csrfToken)
	form.Set("filter_rating_min", "4.5")
	form.Set("filter_rating_max", "2")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/app/scrapes", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	server.createScrapeFromWizard(recorder, request)

	if recorder.Code != http.StatusUnprocessableEntity || repository.job != nil {
		t.Fatalf("status = %d, job = %+v", recorder.Code, repository.job)
	}
}

// The stale-contacts rescan mode narrows the LOCAL website audit, which is the
// one thing an incremental mode can genuinely change, and it must never force
// a full re-audit.
func TestStaleContactsModeNarrowsTheLocalAudit(t *testing.T) {
	t.Parallel()

	repository := &lifecycleCaptureRepository{}
	server, err := New(NewService(repository, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	form := validWizardForm(server.csrfToken)
	form.Set("email", "on")
	form.Set("incremental_mode", IncrementalModeStaleContacts)
	form.Set("enrichment_stale_hours", "168")
	form.Set("enrichment_force_reaudit", "on")

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/app/scrapes", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	server.createScrapeFromWizard(recorder, request)

	if recorder.Code != http.StatusSeeOther || repository.job == nil {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	enrichment := repository.job.Data.Enrichment
	if enrichment == nil {
		t.Fatal("website enrichment was not configured")
	}
	if enrichment.StaleAfterHours != 168 {
		t.Fatalf("stale window = %d hours, want 168", enrichment.StaleAfterHours)
	}
	if enrichment.ForceReaudit {
		t.Fatal("the stale-contacts mode left the force re-audit switch on")
	}

	options, enabled, err := EnrichmentOptionsForJob(repository.job.Data)
	if err != nil || !enabled {
		t.Fatalf("EnrichmentOptionsForJob() = %v, %v, %v", options, enabled, err)
	}
	if options.StaleAfterHours != 168 || options.Force {
		t.Fatalf("resolved audit options = %+v", options)
	}

	// An unset window keeps the historical 24 hours exactly.
	unset := repository.job.Data
	unset.Enrichment.StaleAfterHours = 0
	options, _, err = EnrichmentOptionsForJob(unset)
	if err != nil {
		t.Fatalf("EnrichmentOptionsForJob(unset) error = %v", err)
	}
	if options.StaleAfterHours != DefaultEnrichmentStaleHours {
		t.Fatalf("default stale window = %d, want %d", options.StaleAfterHours, DefaultEnrichmentStaleHours)
	}
}
