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

// These tests drive the manual edit route through its own mux. The real SQLite
// implementation cannot be imported from package-internal web tests (import
// cycle through web/sqlite); its single-transaction behavior is proven by the
// repository tests in web/sqlite/manual_edits_test.go, while the in-file fake
// below proves routing, CSRF gating, validation, and both response modes.

func newManualEditTestServer(t *testing.T, repository JobRepository) *Server {
	t.Helper()

	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return server
}

func TestManualEditRouteRequiresCSRFAndCapability(t *testing.T) {
	t.Parallel()

	server := newManualEditTestServer(t, &fixedJobRepository{})
	mux := http.NewServeMux()
	server.registerManualEditRoutes(mux)

	// A mutation without the CSRF token is refused before any repository work.
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/results/biz_12345/fields",
		strings.NewReader("field=name&value=New+Name&reason=fixing+a+typo"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("edit without CSRF = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	// A repository without the capability answers 501, not a panic or a 500.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/results/biz_12345/fields",
		strings.NewReader("field=name&value=New+Name&reason=fixing+a+typo&csrf_token="+url.QueryEscape(server.csrfToken)))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("edit without capability = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestManualEditRouteValidatesAndAppliesFormEdits(t *testing.T) {
	t.Parallel()

	repository := &manualEditCapableRepository{
		fixedResultRepository: &fixedResultRepository{fixedJobRepository: &fixedJobRepository{}},
	}
	server := newManualEditTestServer(t, repository)
	mux := http.NewServeMux()
	server.registerManualEditRoutes(mux)

	postForm := func(form url.Values) *httptest.ResponseRecorder {
		form.Set("csrf_token", server.csrfToken)
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/results/biz_12345/fields",
			strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(recorder, request)

		return recorder
	}

	// Missing reason.
	recorder := postForm(url.Values{"field": {"name"}, "value": {"Corrected Name"}})
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("edit without reason = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	// Unknown field.
	recorder = postForm(url.Values{
		"field": {"rating"}, "value": {"5"}, "reason": {"should be rejected"},
	})
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("edit with bad field = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	// Malformed website value.
	recorder = postForm(url.Values{
		"field": {"website"}, "value": {"not a url"}, "reason": {"should be rejected"},
	})
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("edit with bad website = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	if repository.edits != 0 {
		t.Fatalf("invalid edits reached the repository: %d", repository.edits)
	}

	// A native urlencoded form post succeeds end-to-end and redirects back to
	// the record like the other drawer forms do.
	recorder = postForm(url.Values{
		"field": {"phone"}, "value": {"+1 415 555 0142"},
		"reason": {"verified with the office"}, "operator": {"reviewer-3"},
	})
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("valid form edit = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if location := recorder.Header().Get("Location"); !strings.Contains(location, "/app/results/biz_12345") {
		t.Fatalf("redirect location = %q", location)
	}

	want := ManualFieldEdit{
		BusinessID: "biz_12345", Field: "phone", Value: "+1 415 555 0142",
		Reason: "verified with the office", Operator: "reviewer-3",
	}
	if repository.lastEdit != want {
		t.Fatalf("stored edit = %#v, want %#v", repository.lastEdit, want)
	}

	// A JSON client receives the envelope with the updated value instead.
	body := `{"field":"name","value":"Corrected Name","reason":"typo in the sign"}`
	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/results/biz_12345/fields", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("valid JSON edit = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data ManualFieldEditResult `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Data.Field != "name" || envelope.Data.Value != "Corrected Name" {
		t.Fatalf("envelope = %#v", envelope.Data)
	}

	// Unknown JSON keys are refused so typos cannot silently drop information.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/results/biz_12345/fields",
		strings.NewReader(`{"field":"name","value":"x","reason":"abc","surprise":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("edit with unknown JSON key = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestManualEditFormRendersInDrawerWhenSupported(t *testing.T) {
	t.Parallel()

	repository := &manualEditCapableRepository{
		fixedResultRepository: &fixedResultRepository{
			fixedJobRepository: &fixedJobRepository{},
			detail: BusinessDetail{Business: BusinessResult{
				ID: "biz_12345", Name: "Harbor Dental", PrimaryCategory: "Dentist",
			}},
		},
	}
	server := newManualEditTestServer(t, repository)

	request := httptest.NewRequest(http.MethodGet, "/app/results/biz_12345/drawer", nil)
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("drawer status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	drawer := recorder.Body.String()
	for _, expected := range []string{
		"Edit field", `action="/api/v1/results/biz_12345/fields"`,
		`name="reason"`, `name="operator"`, `<option value="category">`,
	} {
		if !strings.Contains(drawer, expected) {
			t.Fatalf("drawer missing %q", expected)
		}
	}
}

// manualEditCapableRepository records the last applied edit.
type manualEditCapableRepository struct {
	*fixedResultRepository
	lastEdit ManualFieldEdit
	edits    int
}

func (r *manualEditCapableRepository) ApplyManualFieldEdit(
	_ context.Context,
	edit ManualFieldEdit,
) (ManualFieldEditResult, error) {
	r.edits++
	r.lastEdit = edit

	return ManualFieldEditResult{
		BusinessID: edit.BusinessID, Field: edit.Field,
		Value: edit.Value, PreviousValue: "previous",
	}, nil
}
