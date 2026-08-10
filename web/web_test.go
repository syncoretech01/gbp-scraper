//nolint:testpackage // tests unexported handlers (viewJob, requestWithID, securityHeaders) directly
package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServer(t *testing.T, dir string) *Server {
	t.Helper()

	srv, err := New(NewService(nil, dir), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return srv
}

func TestWildcardBind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		address string
		want    bool
	}{
		{address: ":8080", want: true},
		{address: "0.0.0.0:8080", want: true},
		{address: "[::]:8080", want: true},
		{address: "127.0.0.1:8080", want: false},
		{address: "localhost:8080", want: false},
	}

	for _, test := range tests {
		t.Run(test.address, func(t *testing.T) {
			t.Parallel()

			if got := wildcardBind(test.address); got != test.want {
				t.Fatalf("wildcardBind(%q) = %v, want %v", test.address, got, test.want)
			}
		})
	}
}

func TestViewJobRendersPlaces(t *testing.T) {
	dir := t.TempDir()
	id := "11111111-1111-1111-1111-111111111111"

	csv := "title,latitude,longitude\nPlace,1.5,2.5\n"
	if err := os.WriteFile(filepath.Join(dir, id+".csv"), []byte(csv), 0o600); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	srv := newTestServer(t, dir)

	req := requestWithID(httptest.NewRequest(http.MethodGet, "/view?id="+id, http.NoBody))
	rec := httptest.NewRecorder()
	srv.viewJob(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	for _, want := range []string{`id="map-modal"`, `initJobMap()`, `"title":"Place"`, `"latitude":1.5`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestViewJobEmptyState(t *testing.T) {
	srv := newTestServer(t, t.TempDir())

	id := "22222222-2222-2222-2222-222222222222"
	req := requestWithID(httptest.NewRequest(http.MethodGet, "/view?id="+id, http.NoBody))
	rec := httptest.NewRecorder()
	srv.viewJob(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "var places = [];") {
		t.Fatalf("expected empty places array, got:\n%s", body)
	}
}

func TestViewJobInvalidID(t *testing.T) {
	srv := newTestServer(t, t.TempDir())

	req := requestWithID(httptest.NewRequest(http.MethodGet, "/view?id=not-a-uuid", http.NoBody))
	rec := httptest.NewRecorder()
	srv.viewJob(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rec.Code)
	}
}

func TestSecurityHeadersSeparateLegacyAndLocalPolicies(t *testing.T) {
	handler := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/legacy", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"tile.openstreetmap.org", "cdnjs.cloudflare.com"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP missing %q: %s", want, csp)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/app/dashboard", http.NoBody)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	csp = rec.Header().Get("Content-Security-Policy")
	for _, forbidden := range []string{"tile.openstreetmap.org", "cdnjs.cloudflare.com", "unsafe-eval"} {
		if strings.Contains(csp, forbidden) {
			t.Fatalf("local CSP unexpectedly contains %q: %s", forbidden, csp)
		}
	}
	for header, want := range map[string]string{
		"Cache-Control":          "no-store",
		"Referrer-Policy":        "no-referrer",
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestScrapeAcceptsGridCoverage(t *testing.T) {
	t.Parallel()

	repo := &captureJobRepository{}

	srv, err := New(NewService(repo, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	form := url.Values{
		"name":         {"San Francisco dentists"},
		"keywords":     {"dentists"},
		"lang":         {"en"},
		"zoom":         {"14"},
		"radius":       {"10000"},
		"latitude":     {"37.7749"},
		"longitude":    {"-122.4194"},
		"depth":        {"5"},
		"maxtime":      {"45m"},
		"grid_bbox":    {"37.708,-122.515,37.833,-122.354"},
		"grid_cell_km": {"2.5"},
	}
	req := httptest.NewRequest(http.MethodPost, "/scrape", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	srv.scrape(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if repo.created == nil {
		t.Fatal("job was not created")
	}

	if repo.created.Data.GridBBox != form.Get("grid_bbox") || repo.created.Data.GridCellKM != 2.5 {
		t.Fatalf("grid config = %+v", repo.created.Data)
	}
}

type captureJobRepository struct {
	created *Job
}

func (r *captureJobRepository) Get(context.Context, string) (Job, error) {
	return Job{}, nil
}

func (r *captureJobRepository) Create(_ context.Context, job *Job) error {
	copy := *job
	r.created = &copy

	return nil
}

func (r *captureJobRepository) Delete(context.Context, string) error {
	return nil
}

func (r *captureJobRepository) Select(context.Context, SelectParams) ([]Job, error) {
	return nil, nil
}

func (r *captureJobRepository) Update(context.Context, *Job) error {
	return nil
}
