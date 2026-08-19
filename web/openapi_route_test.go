package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The OpenAPI document and the docs alias are the only machine-readable map of
// the local API, so both must stay reachable through the real server handler
// chain rather than only existing as methods.

func TestOpenAPIDocumentIsServedAndListsJobsPath(t *testing.T) {
	t.Parallel()

	server, err := New(NewService(&openAPIRouteRepository{}, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/openapi.json", http.NoBody))

	if recorder.Code != http.StatusOK {
		t.Fatalf("openapi.json status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("openapi.json content type = %q", contentType)
	}

	var document struct {
		OpenAPI string                     `json:"openapi"`
		Info    map[string]string          `json:"info"`
		Paths   map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v; body = %s", err, recorder.Body.String())
	}
	if document.OpenAPI == "" || document.Info["title"] == "" {
		t.Fatalf("openapi.json misses version or title: %+v", document)
	}
	if _, ok := document.Paths["/api/v1/jobs"]; !ok {
		t.Fatalf("openapi.json paths miss /api/v1/jobs: %v", document.Paths)
	}
}

func TestAPIDocsRedirectsToRenderedWorkspacePage(t *testing.T) {
	t.Parallel()

	server, err := New(NewService(&openAPIRouteRepository{}, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// /api/docs is an alias: it hands the human over to the API workspace page.
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/docs", http.NoBody))

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("/api/docs status = %d, want %d", recorder.Code, http.StatusSeeOther)
	}
	location := recorder.Header().Get("Location")
	if location != "/app/api" {
		t.Fatalf("/api/docs redirect target = %q, want /app/api", location)
	}

	// Following the redirect must land on a page that actually renders.
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, location, http.NoBody))

	if recorder.Code != http.StatusOK {
		t.Fatalf("%s status = %d, body = %s", location, recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "/api/v1/jobs") {
		t.Fatalf("API workspace page does not document /api/v1/jobs")
	}
}

// openAPIRouteRepository is the smallest JobRepository these route tests need;
// the handlers under test never touch job persistence.
type openAPIRouteRepository struct{}

func (repository *openAPIRouteRepository) Get(context.Context, string) (Job, error) {
	return Job{}, ErrNotFound
}

func (repository *openAPIRouteRepository) Create(context.Context, *Job) error   { return nil }
func (repository *openAPIRouteRepository) Delete(context.Context, string) error { return nil }
func (repository *openAPIRouteRepository) Update(context.Context, *Job) error   { return nil }

func (repository *openAPIRouteRepository) Select(context.Context, SelectParams) ([]Job, error) {
	return nil, nil
}
