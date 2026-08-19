package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

func TestDashboardPageRendersRealLegacyResultMetrics(t *testing.T) {
	t.Parallel()

	const jobID = "55555555-5555-5555-5555-555555555555"

	dir := t.TempDir()
	writeCSV(t, dir, jobID, strings.Join([]string{
		"place_id,title,website,phone,emails",
		"one,Alpha,https://alpha.test,+1 555,[hello@alpha.test]",
		"two,Beta,,+1 556,[]",
	}, "\n"))

	repo := &fixedJobRepository{job: Job{
		ID: jobID, Name: "San Francisco dentists", Date: time.Now().UTC(), Status: StatusOK,
	}}
	srv, err := New(NewService(repo, dir), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.dashboardPage(rec, httptest.NewRequest(http.MethodGet, "/app/dashboard", http.NoBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	for _, expected := range []string{"Dashboard", "San Francisco dentists", "Unique businesses", ">2<", "50%"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("dashboard missing %q", expected)
		}
	}

	// One of the two fixture rows has a website, one has an email, and both have a
	// phone. Every availability meter must reflect its own field: a website
	// coverage of 0 here means the legacy CSV statistics were never accumulated.
	for _, meter := range []struct {
		label   string
		percent string
	}{
		{label: "Website", percent: "50"},
		{label: "Email", percent: "50"},
		{label: "Phone", percent: "100"},
	} {
		want := "<span>" + meter.label + "</span><strong>" + meter.percent + "%</strong>"
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard availability missing %q", want)
		}
	}
}

func TestNewScrapePageIsLocalAndExplainsSanFranciscoScope(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, t.TempDir())
	rec := httptest.NewRecorder()
	srv.newScrapePage(rec, httptest.NewRequest(http.MethodGet, "/app/scrapes/new", http.NoBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	for _, expected := range []string{"San Francisco", "Grid-cell size", "Maximum runtime", "/static/js/app-wizard.js"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("wizard missing %q", expected)
		}
	}

	if strings.Contains(body, "cdnjs") || strings.Contains(body, "https://unpkg") {
		t.Fatalf("wizard unexpectedly depends on a CDN")
	}
}

func TestCreateScrapeFromWizardQueuesGridJob(t *testing.T) {
	t.Parallel()

	repo := &lifecycleCaptureRepository{}
	srv, err := New(NewService(repo, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	form := validWizardForm(srv.csrfToken)
	form.Set("keywords", "dentists in San Francisco\nDentists in San Francisco\ndental clinics in San Francisco")
	req := httptest.NewRequest(http.MethodPost, "/app/scrapes", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	srv.createScrapeFromWizard(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if repo.state != jobruntime.StateQueued || repo.job == nil {
		t.Fatalf("created state = %q, job = %+v", repo.state, repo.job)
	}

	if len(repo.job.Data.Keywords) != 2 {
		t.Fatalf("keywords = %v", repo.job.Data.Keywords)
	}

	if repo.job.Data.GridBBox == "" || repo.job.Data.GridCellKM != 2.5 {
		t.Fatalf("grid config = %+v", repo.job.Data)
	}

	if repo.job.Data.Email {
		t.Fatal("email crawling should stay disabled when it was not selected")
	}
}

func TestCreateScrapeFromWizardSavesDraftWithoutAcknowledgement(t *testing.T) {
	t.Parallel()

	repo := &lifecycleCaptureRepository{}
	srv, err := New(NewService(repo, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	form := validWizardForm(srv.csrfToken)
	form.Set("_action", "draft")
	form.Del("responsible_use")
	req := httptest.NewRequest(http.MethodPost, "/app/scrapes", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	srv.createScrapeFromWizard(rec, req)

	if rec.Code != http.StatusSeeOther || repo.state != jobruntime.StateDraft {
		t.Fatalf("status = %d, state = %q, body = %s", rec.Code, repo.state, rec.Body.String())
	}
}

func TestCreateScrapeFromWizardRejectsMissingCSRF(t *testing.T) {
	t.Parallel()

	repo := &lifecycleCaptureRepository{}
	srv, err := New(NewService(repo, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	form := validWizardForm("")
	req := httptest.NewRequest(http.MethodPost, "/app/scrapes", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	srv.createScrapeFromWizard(rec, req)

	if rec.Code != http.StatusForbidden || repo.job != nil {
		t.Fatalf("status = %d, job = %+v", rec.Code, repo.job)
	}
}

func validWizardForm(csrfToken string) url.Values {
	return url.Values{
		"csrf_token":      {csrfToken},
		"_action":         {"start"},
		"responsible_use": {"accepted"},
		"name":            {"San Francisco dentists"},
		"keywords":        {"dentists in San Francisco"},
		"zoom":            {"14"},
		"radius":          {"10000"},
		"latitude":        {"37.7749"},
		"longitude":       {"-122.4194"},
		"grid_cell_km":    {"2.5"},
		"depth":           {"10"},
		"maxtime":         {"60m"},
	}
}

type lifecycleCaptureRepository struct {
	job   *Job
	state jobruntime.State
}

func (r *lifecycleCaptureRepository) CreateWithState(_ context.Context, job *Job, state jobruntime.State) error {
	copy := *job
	r.job = &copy
	r.state = state

	return nil
}

func (r *lifecycleCaptureRepository) Get(context.Context, string) (Job, error) { return Job{}, nil }
func (r *lifecycleCaptureRepository) Create(context.Context, *Job) error       { return nil }
func (r *lifecycleCaptureRepository) Delete(context.Context, string) error     { return nil }
func (r *lifecycleCaptureRepository) Update(context.Context, *Job) error       { return nil }

func (r *lifecycleCaptureRepository) Select(context.Context, SelectParams) ([]Job, error) {
	return nil, nil
}
