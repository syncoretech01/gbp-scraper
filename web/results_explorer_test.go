package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// These tests cover the Results explorer promises that only the rendered page
// and its script can prove: the specification's core columns reach the table,
// the table machinery (resize, reorder, hide, freeze, group, named layouts) is
// wired to real controls, and inline editing posts through the audited
// manual-edit route with a reason and an undo.

func coreColumnResultRow() BusinessResult {
	now := time.Unix(1_700_000_000, 0).UTC()
	latitude, longitude := 37.7749, -122.4194
	rating := 4.7
	reviews := int64(128)
	checked := now.Add(-time.Hour)

	return BusinessResult{
		ID: "biz_abcde", Name: "Bay Smile Dental", PrimaryCategory: "Dentist",
		AdditionalCategories: "Cosmetic dentist", BusinessStatus: "operational", Claimed: true,
		Description: "Family dentistry in SoMa.",
		Address:     "123 Market St, San Francisco, CA 94105", Street: "123 Market St",
		City: "San Francisco", State: "CA", PostalCode: "94105", Country: "United States",
		Latitude: &latitude, Longitude: &longitude, PlusCode: "849VQH48+92",
		Phone: "+1 415-555-0199", NormalizedPhone: "+14155550199", PhoneType: "landline",
		PrimaryEmail: "info@example.test", Emails: []string{"info@example.test", "hi@example.test"},
		EmailType: "role", EmailStatus: "unverified",
		Website: "https://example.test", Domain: "example.test", WebsiteStatus: "active",
		Rating: &rating, ReviewCount: &reviews,
		ReviewsPerRating: `{"5":100,"4":20,"3":5,"2":2,"1":1}`,
		UserReviews:      `[{"Name":"A"},{"Name":"B"},{"Name":"C"}]`,
		PopularTimes:     `{"monday":[1],"tuesday":[2]}`,
		Social: BusinessSocial{
			Facebook: "https://facebook.com/baysmile",
			LinkedIn: "https://linkedin.com/company/baysmile",
		},
		Technologies: []string{"WordPress", "nginx"},
		QualityScore: 85, Confidence: .9, Notes: "Called on Tuesday.",
		ChangeStatus: "updated", Tags: []string{"priority"},
		MapsURL: "https://maps.google.com/?cid=1", PlaceID: "core-1", CID: "1", DataID: "0x1",
		InputID: "seed-42", SourceQuery: "dentists in San Francisco", SourceCell: "3,4",
		LastCheckedAt: &checked, FirstSeenAt: now.Add(-72 * time.Hour), LastSeenAt: now,
		ScrapedAt: now, UpdatedAt: now,
	}
}

func newResultsExplorerServer(t *testing.T, editable bool) *Server {
	t.Helper()

	row := coreColumnResultRow()
	base := &fixedResultRepository{
		fixedJobRepository: &fixedJobRepository{job: Job{
			ID: "ba78441f-a048-4c9d-a8de-d0589e66f132", Name: "San Francisco dentists",
			Status: StatusOK, Date: row.UpdatedAt,
		}},
		page:     ResultPage{Total: 1, Limit: 25, Results: []BusinessResult{row}},
		overview: ResultOverview{UniqueBusinesses: 1, RawRecords: 1},
	}
	base.detail = BusinessDetail{Business: row, RawJSON: `{"title":"Bay Smile Dental"}`}

	var repository JobRepository = base
	if editable {
		repository = &manualEditCapableRepository{fixedResultRepository: base}
	}

	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return server
}

func TestResultsTableRendersEverySpecificationCoreColumn(t *testing.T) {
	t.Parallel()

	server := newResultsExplorerServer(t, false)
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app/results", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("results status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	for _, column := range []string{
		"name", "category", "description", "address", "street", "postal", "coords",
		"phone", "contacts", "emails", "social", "website", "rating", "reviews",
		"ratings", "userreviews", "populartimes", "identifiers", "technology",
		"checked", "quality", "workflow", "notes", "change", "seen", "source", "updated",
	} {
		if !strings.Contains(body, `data-column="`+column+`"`) {
			t.Errorf("results table is missing the %q column", column)
		}
	}

	for _, value := range []string{
		"Family dentistry in SoMa.", "849VQH48&#43;92", "37.774900, -122.419400",
		"landline", "hi@example.test", "LinkedIn", "5★ 100", "2 day profile",
		"seed-42", "WordPress, nginx", "Called on Tuesday.",
	} {
		if !strings.Contains(body, value) {
			t.Errorf("results table is missing the rendered value %q", value)
		}
	}
}

func TestResultsTableExposesColumnAndSelectionMachinery(t *testing.T) {
	t.Parallel()

	server := newResultsExplorerServer(t, false)
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app/results", nil))

	body := recorder.Body.String()
	for _, hook := range []string{
		"data-column-list", "data-layout-select", "data-layout-group", "data-column-profile",
		"data-layout-density", "data-results-mode", "data-select-all", "data-copy-value",
		"data-results-workspace-view", "data-view-columns", "data-view-group",
	} {
		if !strings.Contains(body, hook) {
			t.Errorf("results page is missing the %q hook", hook)
		}
	}

	script := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(script, httptest.NewRequest(http.MethodGet, "/static/js/app-results.js", nil))

	if script.Code != http.StatusOK {
		t.Fatalf("results script status = %d", script.Code)
	}

	source := script.Body.String()
	for _, symbol := range []string{
		"startColumnResize", "reorderColumns", "applyColumnVisibilityAndWidths",
		"applyFrozenColumns", "groupRows", "saveNamedLayout", "loadNamedLayout",
		"handleTableKeydown", "copySelected", "savedViewLayout", "syncSavedViewLayout",
	} {
		if !strings.Contains(source, symbol) {
			t.Errorf("results script is missing %q", symbol)
		}
	}
}

func TestResultsTableOffersInlineEditingOnlyWhenTheRepositorySupportsIt(t *testing.T) {
	t.Parallel()

	plain := httptest.NewRecorder()
	newResultsExplorerServer(t, false).srv.Handler.ServeHTTP(
		plain, httptest.NewRequest(http.MethodGet, "/app/results", nil),
	)

	if strings.Contains(plain.Body.String(), "data-edit-field") {
		t.Fatal("results page offered inline editing without a repository that can store it")
	}

	editable := httptest.NewRecorder()
	newResultsExplorerServer(t, true).srv.Handler.ServeHTTP(
		editable, httptest.NewRequest(http.MethodGet, "/app/results", nil),
	)

	body := editable.Body.String()
	for _, hook := range []string{
		`data-edit-field="name"`, `data-edit-field="category"`,
		`data-edit-field="website"`, `data-edit-field="phone"`,
		`id="inline-edit-template"`, "data-inline-edit-reason", "data-inline-edit-undo",
	} {
		if !strings.Contains(body, hook) {
			t.Errorf("editable results page is missing the %q hook", hook)
		}
	}
}

func TestInlineEditScriptPostsThroughTheAuditedRouteWithAnUndo(t *testing.T) {
	t.Parallel()

	server := newResultsExplorerServer(t, true)
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/static/js/app-results.js", nil))

	source := recorder.Body.String()
	for _, symbol := range []string{
		"beginInlineEdit", "saveInlineEdit", "cancelInlineEdit", "undoInlineEdit",
		"validateInlineEdit", `"/api/v1/results/" + encodeURIComponent(businessId) + "/fields"`,
		"Undo of: ",
	} {
		if !strings.Contains(source, symbol) {
			t.Errorf("results script is missing the inline-edit symbol %q", symbol)
		}
	}
}

func TestInlineEditRouteRecordsTheCorrectionAndItsUndo(t *testing.T) {
	t.Parallel()

	row := coreColumnResultRow()
	base := &fixedResultRepository{
		fixedJobRepository: &fixedJobRepository{},
		page:               ResultPage{Total: 1, Limit: 25, Results: []BusinessResult{row}},
	}
	base.detail = BusinessDetail{Business: row}
	repository := &manualEditCapableRepository{fixedResultRepository: base}
	server := newManualEditTestServer(t, repository)

	post := func(value, reason string) *httptest.ResponseRecorder {
		form := url.Values{}
		form.Set("field", "phone")
		form.Set("value", value)
		form.Set("reason", reason)
		form.Set("csrf_token", server.csrfToken)

		request := httptest.NewRequest(http.MethodPost, "/api/v1/results/biz_abcde/fields",
			strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Accept", "application/json")

		recorder := httptest.NewRecorder()
		server.srv.Handler.ServeHTTP(recorder, request)

		return recorder
	}

	if recorder := post("+1 415-555-0100", "corrected from the website footer"); recorder.Code != http.StatusOK {
		t.Fatalf("inline edit = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	if repository.lastEdit.Reason != "corrected from the website footer" {
		t.Fatalf("stored reason = %q, want the operator's reason", repository.lastEdit.Reason)
	}

	// The undo is a second, equally audited correction rather than a delete.
	if recorder := post("+1 415-555-0199", "Undo of: corrected from the website footer"); recorder.Code != http.StatusOK {
		t.Fatalf("inline undo = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	if repository.edits != 2 {
		t.Fatalf("recorded edits = %d, want 2 (the correction and its undo)", repository.edits)
	}

	if !strings.HasPrefix(repository.lastEdit.Reason, "Undo of:") {
		t.Fatalf("undo reason = %q, want it to name the correction it reverses", repository.lastEdit.Reason)
	}

	// A correction without a reason is refused, so nothing enters the record
	// without an explanation.
	form := url.Values{}
	form.Set("field", "phone")
	form.Set("value", "+1 415-555-0000")
	form.Set("csrf_token", server.csrfToken)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/results/biz_abcde/fields",
		strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("reasonless edit = %d, want 422", recorder.Code)
	}

	if repository.edits != 2 {
		t.Fatalf("recorded edits = %d, want the reasonless edit refused", repository.edits)
	}
}
