package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// The wizard's step-3 markup and web/job_fields.go must never drift: a
// checkbox the backend does not know would be silently dropped, and a
// catalogue entry with no control would be unreachable.
func TestWizardFieldStepMatchesFieldCatalogue(t *testing.T) {
	t.Parallel()

	markup := readWizardTemplate(t)
	pattern := regexp.MustCompile(`<input name="fields" type="checkbox" value="([a-z_]+)"`)
	matches := pattern.FindAllStringSubmatch(markup, -1)
	if len(matches) == 0 {
		t.Fatal("the wizard renders no data-field checkboxes")
	}

	rendered := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		rendered[match[1]] = struct{}{}
	}

	for _, field := range JobFieldCatalogue() {
		if _, ok := rendered[field.Key]; !ok {
			t.Errorf("catalogue field %q has no wizard checkbox", field.Key)
		}
		delete(rendered, field.Key)
	}
	for key := range rendered {
		t.Errorf("wizard checkbox %q is not in the field catalogue", key)
	}
}

// Step 5 must state, in the markup an operator actually reads, that Google
// applied none of these filters.
func TestWizardFilterStepStatesFiltersArePostCollection(t *testing.T) {
	t.Parallel()

	markup := readWizardTemplate(t)
	for _, needle := range []string{
		"Applied after collection, not by Google.",
		`name="filter_rating_min"`,
		`name="filter_reviews_min"`,
		`name="filter_include_categories"`,
		`name="filter_exclude_categories"`,
		`value="temporarily_closed"`,
		`value="permanently_closed"`,
		`name="filter_claimed"`,
		`name="filter_name_contains"`,
		`name="filter_name_excludes"`,
		`value="volatile_fields"`,
		`value="stale_contacts"`,
	} {
		if !strings.Contains(markup, needle) {
			t.Errorf("wizard step 5 is missing %q", needle)
		}
	}
}

func TestNormalizeJobFieldKeysValidatesAndCanonicalises(t *testing.T) {
	t.Parallel()

	if _, err := NormalizeJobFieldKeys([]string{"not_a_field"}); err == nil {
		t.Fatal("NormalizeJobFieldKeys accepted an unknown field")
	}

	// A partial selection always regains the required fields, in catalogue
	// order, without duplicates.
	keys, err := NormalizeJobFieldKeys([]string{"reviews", "rating", "rating"})
	if err != nil {
		t.Fatalf("NormalizeJobFieldKeys() error = %v", err)
	}
	want := []string{"name", "rating", "reviews"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("NormalizeJobFieldKeys() = %v, want %v", keys, want)
	}

	// A complete selection stores nothing, which keeps the saved job
	// identical to one created before the step existed.
	full, err := NormalizeJobFieldKeys(DefaultJobFieldKeys())
	if err != nil {
		t.Fatalf("NormalizeJobFieldKeys(all) error = %v", err)
	}
	if full != nil {
		t.Fatalf("a complete selection stored %v, want nil", full)
	}
}

func TestJobFieldExportColumnsFollowTheSelection(t *testing.T) {
	t.Parallel()

	columns := JobFieldExportColumnKeys([]string{"name", "rating", "reviews"})
	joined := strings.Join(columns, ",")
	for _, needed := range []string{"id", "name", "rating", "review_count", "source_job_id", "scraped_at"} {
		if !strings.Contains(joined, needed) {
			t.Errorf("export columns %v miss %q", columns, needed)
		}
	}
	if strings.Contains(joined, "menu") || strings.Contains(joined, "raw_json") {
		t.Errorf("a core-only selection exported extended columns: %v", columns)
	}

	// A raw-only extended field is exported through raw_json rather than
	// pretending a dedicated column exists.
	withMenus := strings.Join(JobFieldExportColumnKeys([]string{"name", "menus"}), ",")
	if !strings.Contains(withMenus, "raw_json") {
		t.Errorf("selecting menus did not add raw_json: %v", withMenus)
	}
}

func TestJobResultFiltersRejectImpossibleBoundsAndBuildFilterGroups(t *testing.T) {
	t.Parallel()

	high, low := 4.5, 2.0
	invalid := JobResultFilters{RatingMin: &high, RatingMax: &low}
	if err := invalid.Validate(); err == nil {
		t.Fatal("an inverted rating range validated")
	}

	unknownStatus := JobResultFilters{Statuses: []string{"closed_forever"}}
	if err := unknownStatus.Validate(); err == nil {
		t.Fatal("an unknown business status validated")
	}

	claimed := true
	filters := JobResultFilters{
		RatingMin:         &low,
		IncludeCategories: []string{"Dentist", "dentist", " Dental clinic "},
		ExcludeCategories: []string{"Orthodontist"},
		Statuses:          []string{JobStatusOperational},
		Claimed:           &claimed,
		NameContains:      "dental",
		NameExcludes:      "laboratory",
	}
	if err := filters.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	normalized := filters.Normalized()
	if normalized == nil {
		t.Fatal("Normalized() dropped a populated filter set")
	}
	if len(normalized.IncludeCategories) != 2 {
		t.Fatalf("include categories = %v, want the case-insensitive duplicate removed", normalized.IncludeCategories)
	}

	group := filters.FilterGroup()
	if group == nil || group.Logic != "and" {
		t.Fatalf("FilterGroup() = %+v, want an AND group", group)
	}
	encoded, err := json.Marshal(group)
	if err != nil {
		t.Fatalf("marshal filter group: %v", err)
	}
	for _, needed := range []string{`"rating"`, `"category_member"`, `"business_status"`, `"claimed"`, `"not_contains"`} {
		if !strings.Contains(string(encoded), needed) {
			t.Errorf("filter group %s misses %s", encoded, needed)
		}
	}

	// An empty set must never produce a group, or every job would carry a
	// no-op filter into its saved view.
	var empty *JobResultFilters
	if empty.FilterGroup() != nil {
		t.Fatal("an empty filter set produced a filter group")
	}
}

func TestBuildJobCollectionPlanExpressesRescanModesAsLineageFilters(t *testing.T) {
	t.Parallel()

	cases := []struct {
		mode  string
		needs []string
	}{
		{IncrementalModeNewOnly, []string{`"first_seen_job"`}},
		{IncrementalModeNewChanged, []string{`"first_seen_job"`, `"changed_by_job"`}},
		{IncrementalModeVolatile, []string{`"changed_by_job"`, `"changed_field"`, `"review_count"`}},
	}
	for _, testCase := range cases {
		plan := BuildJobCollectionPlan("job-1", JobData{IncrementalMode: testCase.mode})
		if plan.Search.FilterGroup == nil {
			t.Fatalf("mode %q produced no filter group", testCase.mode)
		}
		encoded, err := json.Marshal(plan.Search.FilterGroup)
		if err != nil {
			t.Fatalf("marshal plan filters: %v", err)
		}
		for _, needle := range testCase.needs {
			if !strings.Contains(string(encoded), needle) {
				t.Errorf("mode %q filters %s miss %s", testCase.mode, encoded, needle)
			}
		}
		if len(plan.Notices) == 0 {
			t.Errorf("mode %q carries no honesty notice", testCase.mode)
		}
	}

	// A full collection with no filters keeps the historical shape: the plan
	// narrows nothing and produces no saved view.
	plain := BuildJobCollectionPlan("job-1", JobData{})
	if plain.Search.FilterGroup != nil {
		t.Fatalf("a plain job produced filters: %+v", plain.Search.FilterGroup)
	}
	if !plain.AllFields || len(plain.Fields) != len(JobFieldCatalogue()) {
		t.Fatalf("a plain job did not retain every field: %+v", plain.AllFields)
	}
}

func TestJobCollectionPlanResultsURLIsAValidResultQuery(t *testing.T) {
	t.Parallel()

	minimum := 4.0
	plan := BuildJobCollectionPlan("job-42", JobData{
		IncrementalMode: IncrementalModeNewOnly,
		ResultFilters:   &JobResultFilters{RatingMin: &minimum, NameExcludes: "closed"},
	})
	if !strings.HasPrefix(plan.ResultsURL, "/app/results?") {
		t.Fatalf("results URL = %q", plan.ResultsURL)
	}

	// The generated URL must parse back through the very parser the Results
	// page uses, or the link would be a dead end.
	request := httptest.NewRequest(http.MethodGet, plan.ResultsURL, http.NoBody)
	search, err := parseResultSearch(request)
	if err != nil {
		t.Fatalf("parseResultSearch(%q) error = %v", plan.ResultsURL, err)
	}
	if search.JobID != "job-42" {
		t.Fatalf("parsed job ID = %q, want job-42", search.JobID)
	}
	if search.FilterGroup == nil {
		t.Fatal("the generated URL carried no structured filter group")
	}
	query, err := url.ParseQuery(strings.TrimPrefix(plan.ResultsURL, "/app/results?"))
	if err != nil {
		t.Fatalf("parse results query: %v", err)
	}
	if query.Get("filter_json") == "" {
		t.Fatal("the generated URL has no filter_json")
	}
}

func TestScrapeFieldAndCategoryRoutesAreReachable(t *testing.T) {
	t.Parallel()

	server, err := New(NewService(&openAPIRouteRepository{}, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	for _, path := range []string{"/api/v1/scrape-fields", "/api/v1/business-categories"} {
		recorder := httptest.NewRecorder()
		server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, http.NoBody))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, body = %s", path, recorder.Code, recorder.Body.String())
		}
		var payload struct {
			Data []map[string]any `json:"data"`
			Meta map[string]any   `json:"meta"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("GET %s is not JSON: %v", path, err)
		}
		if len(payload.Data) == 0 {
			t.Fatalf("GET %s returned no entries", path)
		}
		if notice, _ := payload.Meta["notice"].(string); notice == "" {
			t.Fatalf("GET %s carries no honesty notice", path)
		}
	}
}

func readWizardTemplate(t *testing.T) string {
	t.Helper()

	raw, err := static.ReadFile("static/templates/app/pages/new_scrape.html")
	if err != nil {
		t.Fatalf("read wizard template: %v", err)
	}

	return string(raw)
}
