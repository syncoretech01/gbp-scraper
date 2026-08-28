package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// One Export must contain exactly the businesses its scope names, and the
// operator must be able to read that number off the control before pressing
// it. These tests pin the two halves of that promise: the scope resolution
// that decides which rows leave, and the preview that says how many.

// scopeParityRepository is a result store that records every query it is
// asked, so a test can prove Results and Export handed it the same one. Its
// filter engine is deliberately tiny: it only understands the fields these
// tests use, because the point being proved is that one query reaches the
// store, not how SQLite executes it.
type scopeParityRepository struct {
	openAPIRouteRepository
	businesses []BusinessResult
	jobs       map[string][]string
	seen       []ResultSearch
}

func (repository *scopeParityRepository) ImportLegacyCSV(context.Context, Job, string) (ResultFileImport, error) {
	return ResultFileImport{}, nil
}

func (repository *scopeParityRepository) ResultOverview(context.Context) (ResultOverview, error) {
	return ResultOverview{UniqueBusinesses: int64(len(repository.businesses))}, nil
}

func (repository *scopeParityRepository) GetBusiness(_ context.Context, id string) (BusinessDetail, error) {
	for _, business := range repository.businesses {
		if business.ID == id {
			return BusinessDetail{Business: business}, nil
		}
	}

	return BusinessDetail{}, ErrBusinessNotFound
}

func (repository *scopeParityRepository) SearchBusinesses(_ context.Context, search ResultSearch) (ResultPage, error) {
	repository.seen = append(repository.seen, search)
	matched := make([]BusinessResult, 0, len(repository.businesses))
	for _, business := range repository.businesses {
		if !repository.matches(business, search) {
			continue
		}
		matched = append(matched, business)
	}
	limit := search.Limit
	if limit <= 0 {
		limit = 25
	}
	page := matched
	if search.Offset < len(page) {
		page = page[search.Offset:]
	} else {
		page = nil
	}
	if len(page) > limit {
		page = page[:limit]
	}

	return ResultPage{Results: page, Total: int64(len(matched)), Limit: limit, Offset: search.Offset}, nil
}

func (repository *scopeParityRepository) matches(business BusinessResult, search ResultSearch) bool {
	if job := strings.TrimSpace(search.JobID); job != "" {
		found := false
		for _, id := range repository.jobs[job] {
			if id == business.ID {
				found = true

				break
			}
		}
		if !found {
			return false
		}
	}
	if query := strings.TrimSpace(search.Query); query != "" &&
		!strings.Contains(strings.ToLower(business.Name), strings.ToLower(query)) {
		return false
	}
	for _, filter := range search.Filters {
		if !repository.matchesFilter(business, filter) {
			return false
		}
	}
	if search.FilterGroup != nil && !repository.matchesGroup(business, *search.FilterGroup) {
		return false
	}

	return true
}

func (repository *scopeParityRepository) matchesGroup(business BusinessResult, group ResultFilterGroup) bool {
	logic := strings.ToLower(strings.TrimSpace(group.Logic))
	result := logic != "or"
	for _, filter := range group.Filters {
		matched := repository.matchesFilter(business, filter)
		if logic == "or" {
			result = result || matched
		} else {
			result = result && matched
		}
	}
	if group.Not {
		return !result
	}

	return result
}

func (repository *scopeParityRepository) matchesFilter(business BusinessResult, filter ResultFilter) bool {
	switch filter.Field {
	case "id":
		for _, id := range strings.Split(filter.Value, ",") {
			if strings.TrimSpace(id) == business.ID {
				return true
			}
		}

		return false
	case "no_website":
		return business.NoWebsite() == (filter.Value == "true")
	case "contactable":
		return business.Contactable() == (filter.Value == "true")
	case "prospect_tier":
		for _, tier := range strings.Split(filter.Value, ",") {
			if strings.EqualFold(strings.TrimSpace(tier), business.ProspectTier) {
				return true
			}
		}

		return false
	}

	return true
}

func newScopeParityServer(t *testing.T) (*Server, *scopeParityRepository) {
	t.Helper()

	repository := &scopeParityRepository{
		businesses: []BusinessResult{
			{ID: "biz-one", Name: "Ink One", ProspectTier: "A", Phone: "+1 555 0100"},
			{ID: "biz-two", Name: "Ink Two", ProspectTier: "B", Website: "https://two.example", Phone: "+1 555 0101"},
			{ID: "biz-three", Name: "Ink Three", ProspectTier: "C"},
			{ID: "biz-four", Name: "Dental Four", ProspectTier: "A", Phone: "+1 555 0103"},
		},
		jobs: map[string][]string{
			"job-tattoo": {"biz-one", "biz-two", "biz-three"},
			"job-dental": {"biz-four"},
		},
	}
	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return server, repository
}

func exportForm(t *testing.T, values url.Values) *http.Request {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/exports", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := request.ParseForm(); err != nil {
		t.Fatal(err)
	}

	return request
}

// A "current filtered view" export with no filter is the whole workspace under
// a narrower name. That is exactly how one export produced a file mixing two
// unrelated jobs, so it must be refused, and the refusal must say how many
// businesses the operator nearly exported.
func TestFilteredExportWithoutAnyFilterIsRefusedWithTheWorkspaceCount(t *testing.T) {
	t.Parallel()

	server, _ := newScopeParityServer(t)
	request := exportForm(t, url.Values{
		"format": {"csv"}, "name": {"Everything"}, "source_scope": {"filtered"},
	})
	_, err := server.resolveExportCreation(request)
	if err == nil {
		t.Fatal("an unfiltered filtered-view export was accepted")
	}
	if !strings.Contains(err.Error(), "all 4 businesses") {
		t.Fatalf("refusal did not state the workspace count: %v", err)
	}
	if !strings.Contains(err.Error(), "Entire workspace") {
		t.Fatalf("refusal did not name the scope to choose instead: %v", err)
	}
}

// A scope must never silently discard a narrowing input that is still on the
// form. Choosing "Entire workspace" while a source job is selected is the
// exact shape of the defect, so it is refused rather than widened.
func TestExportScopeRefusesInputsItWouldSilentlyIgnore(t *testing.T) {
	t.Parallel()

	server, _ := newScopeParityServer(t)
	tests := []struct {
		name   string
		values url.Values
		want   string
	}{
		{
			name: "workspace with a job",
			values: url.Values{
				"format": {"csv"}, "name": {"All"}, "source_scope": {"all"}, "job_id": {"job-tattoo"},
			},
			want: "a source job",
		},
		{
			name: "job scope with table filters",
			values: url.Values{
				"format": {"csv"}, "name": {"Job"}, "source_scope": {"job"}, "job_id": {"job-tattoo"},
				"filter_field": {"no_website"}, "filter_operator": {"eq"}, "filter_value": {"true"},
			},
			want: "table filters",
		},
		{
			name: "selected scope with a search text",
			values: url.Values{
				"format": {"csv"}, "name": {"Selected"}, "source_scope": {"selected"},
				"selected_ids": {"biz-one"}, "q": {"ink"},
			},
			want: "a search text",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := server.resolveExportCreation(exportForm(t, test.values))
			if err == nil {
				t.Fatal("a scope silently ignored a submitted narrowing input")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("refusal = %v, want it to name %q", err, test.want)
			}
		})
	}
}

// The source-job scope is its own explicit choice, not a filtered view that
// happens to carry a job.
func TestExportJobScopeResolvesToThatJobAlone(t *testing.T) {
	t.Parallel()

	server, _ := newScopeParityServer(t)
	creation, err := server.resolveExportCreation(exportForm(t, url.Values{
		"format": {"csv"}, "name": {"Job"}, "source_scope": {"job"}, "job_id": {"job-tattoo"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if creation.SourceType != "results_job" || creation.Scope != ExportScopeJob {
		t.Fatalf("source type = %q scope = %q", creation.SourceType, creation.Scope)
	}
	if creation.Search.JobID != "job-tattoo" || len(creation.Search.Filters) != 0 {
		t.Fatalf("job scope search = %+v", creation.Search)
	}
}

// The number on the control and the number of rows in the file come from one
// query. This is the guarantee the whole scope model exists for.
func TestExportScopePreviewCountsMatchTheResultsCount(t *testing.T) {
	t.Parallel()

	server, _ := newScopeParityServer(t)
	query := url.Values{
		"job_id":          {"job-tattoo"},
		"filter_field":    {"no_website"},
		"filter_operator": {"eq"},
		"filter_value":    {"true"},
	}

	results := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(results, httptest.NewRequest(http.MethodGet, "/api/v1/results?"+query.Encode(), nil))
	if results.Code != http.StatusOK {
		t.Fatalf("results status = %d", results.Code)
	}
	var resultsPayload struct {
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(results.Body.Bytes(), &resultsPayload); err != nil {
		t.Fatal(err)
	}

	preview := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(preview, httptest.NewRequest(http.MethodGet, "/api/v1/exports/scopes?"+query.Encode(), nil))
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status = %d body = %s", preview.Code, preview.Body.String())
	}
	var previewPayload struct {
		Data []ExportScopeOption `json:"data"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &previewPayload); err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int64, len(previewPayload.Data))
	for _, scope := range previewPayload.Data {
		counts[scope.Key] = scope.Count
	}
	if counts[ExportScopeFiltered] != resultsPayload.Meta.Total {
		t.Fatalf("filtered preview = %d, results total = %d", counts[ExportScopeFiltered], resultsPayload.Meta.Total)
	}
	if counts[ExportScopeJob] != 3 {
		t.Fatalf("job preview = %d, want 3", counts[ExportScopeJob])
	}
	if counts[ExportScopeWorkspace] != 4 {
		t.Fatalf("workspace preview = %d, want 4", counts[ExportScopeWorkspace])
	}
	// The four scopes must be genuinely different numbers here; a preview that
	// reported the same count everywhere would hide the very ambiguity it
	// exists to remove.
	if counts[ExportScopeFiltered] == counts[ExportScopeWorkspace] {
		t.Fatalf("filtered and workspace previews are indistinguishable at %d", counts[ExportScopeFiltered])
	}
}

// Results and Export must hand the store the same query. A second filter
// engine behind exports is what makes a file drift away from the table.
func TestExportAndResultsIssueTheSameQueryForTheSameFilters(t *testing.T) {
	t.Parallel()

	server, repository := newScopeParityServer(t)
	query := url.Values{
		"job_id":          {"job-tattoo"},
		"q":               {"ink"},
		"filter_field":    {"no_website", "contactable"},
		"filter_operator": {"eq", "eq"},
		"filter_value":    {"true", "true"},
	}

	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/results?"+query.Encode(), nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("results status = %d", recorder.Code)
	}
	tableSearch := repository.seen[len(repository.seen)-1]

	form := url.Values{"format": {"csv"}, "name": {"Parity"}, "source_scope": {"filtered"}}
	for key, values := range query {
		form[key] = values
	}
	creation, err := server.resolveExportCreation(exportForm(t, form))
	if err != nil {
		t.Fatal(err)
	}

	if creation.Search.JobID != tableSearch.JobID || creation.Search.Query != tableSearch.Query {
		t.Fatalf("export search = %+v, table search = %+v", creation.Search, tableSearch)
	}
	if len(creation.Search.Filters) != len(tableSearch.Filters) {
		t.Fatalf("export carried %d filters, the table used %d", len(creation.Search.Filters), len(tableSearch.Filters))
	}
	for index := range tableSearch.Filters {
		if creation.Search.Filters[index] != tableSearch.Filters[index] {
			t.Fatalf("filter %d differs: export %+v table %+v",
				index, creation.Search.Filters[index], tableSearch.Filters[index])
		}
	}
}

// Export history must describe a scope in the same words the builder used, so
// a file created months ago can still be read for what it contains.
func TestExportHistoryNamesTheScopeItWasCreatedWith(t *testing.T) {
	t.Parallel()

	tests := []struct {
		record ExportRecord
		want   string
	}{
		{record: ExportRecord{SourceType: "results_all"}, want: "Entire workspace"},
		{record: ExportRecord{SourceType: "results_filtered"}, want: "Current filtered view"},
		{record: ExportRecord{SourceType: "results_selected"}, want: "Selected businesses"},
		{record: ExportRecord{SourceType: "results_job", SourceID: "job-tattoo"}, want: "Current source job"},
		{record: ExportRecord{SourceType: "results_saved_view", SavedViewID: "view-1"}, want: "Saved view"},
	}
	for _, test := range tests {
		if got := exportRecordSource(test.record); !strings.Contains(got, test.want) {
			t.Fatalf("exportRecordSource(%+v) = %q, want it to contain %q", test.record, got, test.want)
		}
	}
}

// Every prospecting signal the Results table shows as a chip must also be
// available as a machine-readable export column, because a spreadsheet cannot
// filter on the text of a badge.
func TestProspectingSignalsAreAvailableAsExportColumns(t *testing.T) {
	t.Parallel()

	required := []string{
		"prospect_tier", "prospect_score", "prospect_reasons",
		"website_url", "website_kind", "website_state", "website_status",
		"website_audit_status", "website_health_score", "website_health_grade",
		"website_health_version", "website_confidence", "website_last_checked_at",
		"website_http_status", "website_error_reason",
		"no_website", "social_only", "weak_website", "contactable",
		"has_phone", "has_email", "email_count", "social_profiles",
		"record_quality_score", "record_confidence", "scoring_rule_version",
		"source_job_id", "source_query", "source_cell", "postal_code",
		"source_job_ids", "source_queries", "source_cells",
		"observation_count", "first_observed_at", "last_observed_at",
		"first_seen_at", "last_seen_at", "export_scope",
	}
	for _, key := range required {
		if _, ok := exportColumnDefinitionFor(key); !ok {
			t.Fatalf("export column %q is missing", key)
		}
	}

	// Selecting them must not change the default export shape.
	columns, legacy, err := parseExportColumnSpec("", ExportBuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !legacy || !equalExportColumns(columns, defaultExportColumns()) {
		t.Fatal("the default export column set changed")
	}
}

// The derived prospecting flags must be values, not rendered text, and they
// must agree with the filters the Results chips use.
func TestExportColumnValuesReportDerivedProspectingFlags(t *testing.T) {
	t.Parallel()

	business := BusinessResult{
		ID: "biz-one", Name: "Ink One", Phone: "+1 555 0100",
		ProspectStatus: "SOCIAL_ONLY", Website: "https://facebook.com/inkone",
		EmailCount: 2, PhoneCount: 1, SocialCount: 1, ObservationCount: 3,
		SourceJobIDs:  []string{"job-tattoo", "job-dental"},
		SourceQueries: []string{"tattoo artist"},
	}
	row := exportDataRow{Business: business, Scope: "Current source job"}
	expected := map[string]any{
		"no_website":        false,
		"social_only":       true,
		"weak_website":      true,
		"contactable":       true,
		"has_phone":         true,
		"has_email":         true,
		"email_count":       int64(2),
		"observation_count": int64(3),
		"website_kind":      "social",
		"source_job_ids":    "job-tattoo; job-dental",
		"export_scope":      "Current source job",
	}
	for key, want := range expected {
		got, err := exportColumnValue(row, key)
		if err != nil {
			t.Fatalf("exportColumnValue(%q) error = %v", key, err)
		}
		if got != want {
			t.Fatalf("exportColumnValue(%q) = %#v, want %#v", key, got, want)
		}
	}

	// A business whose site was never reached has no health score at all. An
	// absent score is information; a zero would read as "graded badly".
	health, err := exportColumnValue(row, "website_health_score")
	if err != nil {
		t.Fatal(err)
	}
	if health != nil {
		t.Fatalf("website_health_score = %#v for an unaudited business, want nil", health)
	}
}

// The scope preview only helps if the interface actually asks for it and
// renders the number. These are the DOM and asset hooks the two pages depend
// on; losing one silently returns the interface to an unlabelled Export
// button, which is the defect this lane exists to remove.
func TestExportScopeInterfaceCarriesItsCountHooks(t *testing.T) {
	t.Parallel()

	exportsPage, err := static.ReadFile("static/templates/app/pages/exports.html")
	if err != nil {
		t.Fatalf("read exports.html: %v", err)
	}
	for _, want := range []string{
		"/static/js/app-exports.js",
		"data-export-scope-form",
		`data-scope-endpoint="/api/v1/exports/scopes"`,
		"data-scope-summary",
		"data-scope-counts",
		"data-scope-submit",
		`value="job"`,
		"Current filtered view",
		"Entire workspace",
	} {
		if !strings.Contains(string(exportsPage), want) {
			t.Fatalf("exports.html misses %q", want)
		}
	}

	resultsPage, err := static.ReadFile("static/templates/app/pages/results.html")
	if err != nil {
		t.Fatalf("read results.html: %v", err)
	}
	for _, want := range []string{
		"data-export-scope-menu",
		`data-export-scope="filtered"`,
		`data-export-scope="selected"`,
		`data-export-scope="job"`,
		`data-export-scope="all"`,
		"data-scope-count",
	} {
		if !strings.Contains(string(resultsPage), want) {
			t.Fatalf("results.html misses %q", want)
		}
	}

	builder, err := static.ReadFile("static/js/app-exports.js")
	if err != nil {
		t.Fatalf("read app-exports.js: %v", err)
	}
	for _, want := range []string{"/api/v1/exports/scopes", "applyScopeInputs", "renderSummary"} {
		if !strings.Contains(string(builder), want) {
			t.Fatalf("app-exports.js misses %q", want)
		}
	}

	explorer, err := static.ReadFile("static/js/app-results.js")
	if err != nil {
		t.Fatalf("read app-results.js: %v", err)
	}
	for _, want := range []string{"data-export-scope-menu", "loadExportScopeCounts", "refreshSelectedScopeLink"} {
		if !strings.Contains(string(explorer), want) {
			t.Fatalf("app-results.js misses %q", want)
		}
	}
}
