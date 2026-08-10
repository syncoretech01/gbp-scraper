package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestResultsPageAndDetailDrawerRenderRepositoryData(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	repository := &fixedResultRepository{
		fixedJobRepository: &fixedJobRepository{job: Job{
			ID: "ba78441f-a048-4c9d-a8de-d0589e66f132", Name: "San Francisco dentists",
			Status: StatusOK, Date: now,
		}},
		page: ResultPage{
			Total: 1, Limit: 25,
			Results: []BusinessResult{{
				ID: "biz_abcde", Name: "Bay Smile Dental", PrimaryCategory: "Dentist",
				Address: "123 Main St", City: "San Francisco", State: "CA", PostalCode: "94105",
				Phone: "+1 415-555-0199", NormalizedPhone: "+14155550199",
				PrimaryEmail: "info@example.test", Website: "https://example.test", Domain: "example.test",
				WebsiteStatus: "active", QualityScore: 85, Confidence: .9,
				SourceQuery: "dentists in San Francisco", ScrapedAt: now, UpdatedAt: now,
			}},
		},
		overview: ResultOverview{UniqueBusinesses: 1, RawRecords: 2, Websites: 1, Emails: 1},
	}
	repository.detail = BusinessDetail{
		Business: repository.page.Results[0], RawJSON: `{"title":"Bay Smile Dental"}`,
		Sources:  []BusinessSourceView{{ID: 1, SourceType: "google_maps_csv", SourceQuery: "dentists in San Francisco", ExtractedAt: now}},
		Versions: []BusinessVersionView{{ID: 1, Version: 1, ChangeType: "new", ObservedAt: now}},
	}

	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/app/results?q=Bay&page_size=25", nil)
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("results status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{"Bay Smile Dental", "2 source records", "dentists in San Francisco", "/app/results/biz_abcde/drawer"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("results body missing %q", expected)
		}
	}
	if strings.Contains(body, "/app/exports?source=results") {
		t.Fatal("results page advertised an export route that is not enabled")
	}

	request = httptest.NewRequest(http.MethodGet, "/app/results/biz_abcde/drawer", nil)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Raw source JSON") {
		t.Fatalf("drawer status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestResultsAPISeparatesValidationAndInternalErrors(t *testing.T) {
	t.Parallel()

	repository := &fixedResultRepository{
		fixedJobRepository: &fixedJobRepository{},
		searchErr:          errors.New("driver detail that must not be returned"),
	}
	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/results", nil)
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("internal result query status = %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "driver detail") {
		t.Fatalf("internal result query leaked repository detail: %s", recorder.Body.String())
	}

	repository.searchErr = ErrInvalidResultQuery
	request = httptest.NewRequest(http.MethodGet, "/api/v1/results", nil)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid result query status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestBusinessWorkflowHandlerRequiresCSRFAndBoundsRedirect(t *testing.T) {
	t.Parallel()

	repository := &fixedResultRepository{fixedJobRepository: &fixedJobRepository{}}
	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	form := "result_ids=biz_abcde&action=tag&tag=Priority&return_to=https%3A%2F%2Fevil.test"
	request := httptest.NewRequest(http.MethodPost, "/api/v1/results/bulk", strings.NewReader(form))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || len(repository.lastMutation.IDs) != 0 {
		t.Fatalf("missing CSRF status = %d, mutation = %+v", recorder.Code, repository.lastMutation)
	}

	form = "csrf_token=" + server.csrfToken + "&result_ids=biz_abcde&action=tag&tag=Priority&return_to=https%3A%2F%2Fevil.test"
	request = httptest.NewRequest(http.MethodPost, "/api/v1/results/bulk", strings.NewReader(form))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/app/results?notice=Workflow+updated" {
		t.Fatalf("mutation status = %d, location = %q, body = %s", recorder.Code, recorder.Header().Get("Location"), recorder.Body.String())
	}
	if repository.lastMutation.Action != "tag" || repository.lastMutation.Value != "Priority" ||
		!reflect.DeepEqual(repository.lastMutation.IDs, []string{"biz_abcde"}) {
		t.Fatalf("mutation = %+v", repository.lastMutation)
	}
}

func TestParseResultSearchBoundsInput(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/results?page=2&page_size=500&filter_field=city&filter_operator=eq&filter_value=San+Francisco", nil)
	search, err := parseResultSearch(request)
	if err != nil {
		t.Fatalf("parseResultSearch() error = %v", err)
	}
	if search.Limit != 250 || search.Offset != 250 || len(search.Filters) != 1 {
		t.Fatalf("parseResultSearch() = %+v", search)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/results?filter_field=city&filter_operator=eq", nil)
	if _, err := parseResultSearch(request); err == nil {
		t.Fatal("parseResultSearch() accepted an incomplete filter row")
	}
}

func TestBackfillLegacyResultsContinuesAfterOneBadFile(t *testing.T) {
	t.Parallel()

	dataFolder := t.TempDir()
	old := Job{ID: "old-job", Date: time.Unix(100, 0)}
	bad := Job{ID: "bad-job", Date: time.Unix(200, 0)}
	newest := Job{ID: "new-job", Date: time.Unix(300, 0)}
	missing := Job{ID: "missing-job", Date: time.Unix(50, 0)}
	for _, id := range []string{old.ID, bad.ID, newest.ID} {
		if err := os.WriteFile(filepath.Join(dataFolder, id+".csv"), []byte("title\nexample\n"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", id, err)
		}
	}
	repository := &backfillResultRepository{
		jobs: []Job{newest, bad, missing, old},
		fail: map[string]error{bad.ID: errors.New("malformed fixture")},
	}
	service := NewService(repository, dataFolder)

	imports, err := service.BackfillLegacyResults(context.Background())
	if err == nil || !strings.Contains(err.Error(), bad.ID) {
		t.Fatalf("BackfillLegacyResults() error = %v", err)
	}
	if len(imports) != 2 {
		t.Fatalf("BackfillLegacyResults() imports = %+v", imports)
	}
	if want := []string{old.ID, bad.ID, newest.ID}; !reflect.DeepEqual(repository.calls, want) {
		t.Fatalf("import calls = %v, want %v", repository.calls, want)
	}
}

type fixedResultRepository struct {
	*fixedJobRepository
	page         ResultPage
	detail       BusinessDetail
	overview     ResultOverview
	searchErr    error
	lastMutation ResultMutation
	mutateErr    error
}

func (r *fixedResultRepository) ImportLegacyCSV(context.Context, Job, string) (ResultFileImport, error) {
	return ResultFileImport{}, nil
}

func (r *fixedResultRepository) SearchBusinesses(context.Context, ResultSearch) (ResultPage, error) {
	if r.searchErr != nil {
		return ResultPage{}, r.searchErr
	}

	return r.page, nil
}

func (r *fixedResultRepository) GetBusiness(_ context.Context, id string) (BusinessDetail, error) {
	if id != r.detail.Business.ID {
		return BusinessDetail{}, ErrBusinessNotFound
	}

	return r.detail, nil
}

func (r *fixedResultRepository) ResultOverview(context.Context) (ResultOverview, error) {
	return r.overview, nil
}

func (r *fixedResultRepository) MutateBusinesses(_ context.Context, mutation ResultMutation) (int64, error) {
	if r.mutateErr != nil {
		return 0, r.mutateErr
	}
	r.lastMutation = mutation
	return int64(len(mutation.IDs)), nil
}

var _ ResultRepository = (*fixedResultRepository)(nil)

type backfillResultRepository struct {
	jobs  []Job
	fail  map[string]error
	calls []string
}

func (r *backfillResultRepository) Get(_ context.Context, id string) (Job, error) {
	for _, job := range r.jobs {
		if job.ID == id {
			return job, nil
		}
	}

	return Job{}, ErrPlacesNotFound
}

func (r *backfillResultRepository) Create(context.Context, *Job) error   { return nil }
func (r *backfillResultRepository) Delete(context.Context, string) error { return nil }
func (r *backfillResultRepository) Update(context.Context, *Job) error   { return nil }
func (r *backfillResultRepository) Select(context.Context, SelectParams) ([]Job, error) {
	return append([]Job(nil), r.jobs...), nil
}

func (r *backfillResultRepository) ImportLegacyCSV(_ context.Context, job Job, _ string) (ResultFileImport, error) {
	r.calls = append(r.calls, job.ID)
	if err := r.fail[job.ID]; err != nil {
		return ResultFileImport{}, err
	}

	return ResultFileImport{JobID: job.ID, Rows: 1}, nil
}

func (r *backfillResultRepository) SearchBusinesses(context.Context, ResultSearch) (ResultPage, error) {
	return ResultPage{}, nil
}

func (r *backfillResultRepository) GetBusiness(context.Context, string) (BusinessDetail, error) {
	return BusinessDetail{}, ErrBusinessNotFound
}

func (r *backfillResultRepository) ResultOverview(context.Context) (ResultOverview, error) {
	return ResultOverview{}, nil
}

var _ JobRepository = (*backfillResultRepository)(nil)
var _ ResultRepository = (*backfillResultRepository)(nil)
