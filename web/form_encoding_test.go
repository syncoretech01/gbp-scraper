package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Browsers submit application/x-www-form-urlencoded unless a form declares a
// file input. http.Request.ParseMultipartForm rejects that encoding with
// ErrNotMultipart, so every handler that accepts both JSON and an HTML form must
// branch on the content type. These tests exercise the urlencoded path of each
// such handler through its registered route, which is the shape the local UI
// actually sends.

func urlencodedRequest(t *testing.T, method, target string, values url.Values, csrfToken string) *http.Request {
	t.Helper()

	request := httptest.NewRequest(method, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-CSRF-Token", csrfToken)

	return request
}

func TestParseBoundedRequestFormAcceptsURLEncodedAndMultipart(t *testing.T) {
	t.Parallel()

	values := url.Values{"name": {"Weekly leads"}, "format": {"csv"}}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/exports", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if err := parseBoundedRequestForm(httptest.NewRecorder(), request, 1<<20); err != nil {
		t.Fatalf("urlencoded form rejected: %v", err)
	}

	if got := request.FormValue("name"); got != "Weekly leads" {
		t.Fatalf("FormValue(name) = %q", got)
	}

	body := "--boundary\r\n" +
		"Content-Disposition: form-data; name=\"format\"\r\n\r\nxlsx\r\n" +
		"--boundary--\r\n"
	multipart := httptest.NewRequest(http.MethodPost, "/api/v1/exports", strings.NewReader(body))
	multipart.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")

	if err := parseBoundedRequestForm(httptest.NewRecorder(), multipart, 1<<20); err != nil {
		t.Fatalf("multipart form rejected: %v", err)
	}

	if got := multipart.FormValue("format"); got != "xlsx" {
		t.Fatalf("FormValue(format) = %q", got)
	}
}

func TestExportRouteAcceptsURLEncodedForm(t *testing.T) {
	t.Parallel()

	repository := newFormEncodingRepository()

	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	mux := http.NewServeMux()
	server.registerExportRoutes(mux)

	// An unknown source scope is rejected by name only once the body has been
	// parsed, so this response proves the urlencoded fields reached validation.
	values := url.Values{
		"name":         {"Weekly leads"},
		"format":       {"csv"},
		"source_scope": {"not-a-scope"},
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, urlencodedRequest(t, http.MethodPost, "/api/v1/exports", values, server.csrfToken))

	if recorder.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(recorder.Body.String(), "unsupported export source scope") {
		t.Fatalf("export form parsing = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	// A scope the handler understands must then get past scope validation. This
	// repository cannot persist an export, so a later failure is expected; a form
	// parsing failure is not.
	values.Set("source_scope", "all")
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, urlencodedRequest(t, http.MethodPost, "/api/v1/exports", values, server.csrfToken))

	if body := recorder.Body.String(); strings.Contains(body, "unsupported export source scope") ||
		strings.Contains(strings.ToLower(body), "multipart") {
		t.Fatalf("valid export form rejected: %d %s", recorder.Code, body)
	}
}

func TestAPIKeyRouteAcceptsURLEncodedForm(t *testing.T) {
	t.Parallel()

	repository := &apiAccessTestRepository{keys: map[string]APIKeyRecord{}}

	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	mux := http.NewServeMux()
	server.registerAPIAccessRoutes(mux)

	values := url.Values{"name": {"Local reader"}, "permission": {"read"}}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, urlencodedRequest(t, http.MethodPost, "/api/v1/api-keys", values, server.csrfToken))

	if recorder.Code != http.StatusCreated {
		t.Fatalf("create API key from urlencoded form = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	if !strings.Contains(recorder.Body.String(), `"token"`) {
		t.Fatalf("response missing generated token: %s", recorder.Body.String())
	}
}

func TestIntegrationRouteAcceptsURLEncodedForm(t *testing.T) {
	t.Parallel()

	repository := newFormEncodingRepository()

	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	mux := http.NewServeMux()
	server.registerIntegrationRoutes(mux)

	values := url.Values{
		"name":    {"Local n8n"},
		"kind":    {"webhook"},
		"url":     {"http://127.0.0.1:5678/webhook/leads"},
		"enabled": {"on"},
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, urlencodedRequest(t, http.MethodPost, "/api/v1/integrations", values, server.csrfToken))

	// This repository cannot persist an integration, so the save fails. That the
	// failure is a save failure rather than "invalid form" is the assertion: the
	// urlencoded body was decoded into a valid integration first.
	if recorder.Code != http.StatusInternalServerError ||
		!strings.Contains(recorder.Body.String(), "integration_save_failed") {
		t.Fatalf("integration form parsing = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	// A body the decoder cannot read must still be reported as an invalid form.
	broken := httptest.NewRequest(http.MethodPost, "/api/v1/integrations", strings.NewReader("%zz"))
	broken.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	broken.Header.Set("X-CSRF-Token", server.csrfToken)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, broken)

	if !strings.Contains(recorder.Body.String(), "invalid form") {
		t.Fatalf("malformed integration form = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

// formEncodingRepository is a minimal JobRepository. The tests above assert how a
// request body is parsed, so persistence only has to be well-behaved, not real.
type formEncodingRepository struct{}

func newFormEncodingRepository() *formEncodingRepository { return &formEncodingRepository{} }

func (repository *formEncodingRepository) Get(context.Context, string) (Job, error) {
	return Job{}, ErrNotFound
}

func (repository *formEncodingRepository) Create(context.Context, *Job) error   { return nil }
func (repository *formEncodingRepository) Delete(context.Context, string) error { return nil }
func (repository *formEncodingRepository) Update(context.Context, *Job) error   { return nil }

func (repository *formEncodingRepository) Select(context.Context, SelectParams) ([]Job, error) {
	return nil, nil
}
