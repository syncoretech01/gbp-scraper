package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
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
		Info    map[string]any             `json:"info"`
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

// The document must describe the whole local API, not a hand-picked sample, so
// its path set has to match the generated route catalogue exactly.
func TestOpenAPIDocumentCoversTheWholeRouteCatalogue(t *testing.T) {
	t.Parallel()

	server, err := New(NewService(&openAPIRouteRepository{}, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/openapi.json", http.NoBody))

	var document struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Summary     string `json:"summary"`
			Samples     []struct {
				Lang   string `json:"lang"`
				Source string `json:"source"`
			} `json:"x-codeSamples"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}

	seen := make(map[string]struct{}, len(document.Paths))
	for path, item := range document.Paths {
		for method := range item {
			seen[strings.ToUpper(method)+" "+path] = struct{}{}
		}
	}
	for _, operation := range localAPICatalogue() {
		key := operation.Method + " " + operation.Path
		if _, ok := seen[key]; !ok {
			t.Fatalf("openapi.json omits catalogued operation %s", key)
		}
		item := document.Paths[operation.Path][strings.ToLower(operation.Method)]
		if item.OperationID == "" || item.Summary == "" {
			t.Fatalf("operation %s misses an operationId or summary: %+v", key, item)
		}
		languages := make([]string, 0, len(item.Samples))
		for _, sample := range item.Samples {
			if sample.Source == "" {
				t.Fatalf("operation %s has an empty %s sample", key, sample.Lang)
			}
			languages = append(languages, sample.Lang)
		}
		if want := []string{"shell", "python", "javascript", "go"}; !slices.Equal(languages, want) {
			t.Fatalf("operation %s sample languages = %v, want %v", key, languages, want)
		}
	}
	if len(seen) != len(localAPICatalogue()) {
		t.Fatalf("openapi.json documents %d operations, catalogue has %d", len(seen), len(localAPICatalogue()))
	}
}

// The generated catalogue is only trustworthy while it matches the routes the
// package actually registers, so the package's own source is the fixture.
func TestRouteCatalogueCoversEveryRegisteredAPIRoute(t *testing.T) {
	t.Parallel()

	catalogued := make(map[string]struct{}, len(localAPICatalogue()))
	prefixes := make([]string, 0, len(localAPICatalogue()))
	for _, operation := range localAPICatalogue() {
		catalogued[operation.Path] = struct{}{}
		prefixes = append(prefixes, operation.Path)
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	pattern := regexp.MustCompile(`HandleFunc\("([^"]+)"`)
	registered := 0

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(source), -1) {
			route := match[1]
			if !strings.Contains(route, "/api/") {
				continue
			}
			path := route
			if index := strings.Index(route, " "); index >= 0 {
				path = route[index+1:]
			}
			registered++
			if _, ok := catalogued[path]; ok {
				continue
			}
			// A registration built by concatenation (the lifecycle controls)
			// appears as a subtree prefix; the catalogue must document at least
			// one operation beneath it.
			covered := false
			for _, candidate := range prefixes {
				if strings.HasPrefix(candidate, path) && candidate != path {
					covered = true

					break
				}
			}
			if !covered {
				t.Fatalf("%s registers %q but openapi_catalogue.go does not document it", name, route)
			}
		}
	}

	if registered == 0 {
		t.Fatal("no API route registrations were found; the scan is not testing anything")
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
