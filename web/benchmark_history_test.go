package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// benchmarkHistoryTestRepository is a JobRepository that also supplies
// benchmark evidence, campaign lineage and snapshot storage.
type benchmarkHistoryTestRepository struct {
	*campaignTestRepository

	evidence  map[string]BenchmarkEvidence
	snapshots map[string]BenchmarkSnapshot
	// derived counts how many times evidence had to be recomputed, which is
	// what proves the cache is used.
	derived int
}

func newBenchmarkHistoryTestRepository(seed ...Job) *benchmarkHistoryTestRepository {
	return &benchmarkHistoryTestRepository{
		campaignTestRepository: newCampaignTestRepository(seed...),
		evidence:               make(map[string]BenchmarkEvidence),
		snapshots:              make(map[string]BenchmarkSnapshot),
	}
}

func (r *benchmarkHistoryTestRepository) JobBenchmarkEvidence(
	_ context.Context,
	jobID string,
) (BenchmarkEvidence, error) {
	evidence, ok := r.evidence[jobID]
	if !ok {
		return BenchmarkEvidence{}, ErrLifecycleNotFound
	}

	r.derived++

	return evidence, nil
}

func (r *benchmarkHistoryTestRepository) SaveBenchmarkSnapshot(
	_ context.Context,
	snapshot BenchmarkSnapshot,
) error {
	r.snapshots[snapshot.JobID] = snapshot

	return nil
}

func (r *benchmarkHistoryTestRepository) GetBenchmarkSnapshot(
	_ context.Context,
	jobID string,
) (BenchmarkSnapshot, error) {
	snapshot, ok := r.snapshots[jobID]
	if !ok {
		return BenchmarkSnapshot{}, ErrBenchmarkSnapshotNotFound
	}

	return snapshot, nil
}

func (r *benchmarkHistoryTestRepository) ListBenchmarkSnapshots(
	_ context.Context,
	limit int,
) ([]BenchmarkSnapshot, error) {
	snapshots := make([]BenchmarkSnapshot, 0, len(r.snapshots))
	for _, snapshot := range r.snapshots {
		snapshots = append(snapshots, snapshot)
	}

	SortBenchmarkSnapshotsByCapture(snapshots)

	if len(snapshots) > limit {
		snapshots = snapshots[:limit]
	}

	return snapshots, nil
}

// benchmarkRunEvidence builds one run's evidence with a chosen unique-count
// and duplicate load.
func benchmarkRunEvidence(jobID, name string, unique, rowsAdded, duplicates int64) BenchmarkEvidence {
	started := time.Unix(1_700_000_000, 0).UTC().Unix()

	return BenchmarkEvidence{
		JobID: jobID, JobName: name, ScraperVersion: "v-test",
		CreatedAt: started, StartedAt: started, FinishedAt: started + 600,
		Businesses: BenchmarkBusinessEvidence{Unique: unique},
		Tasks: []BenchmarkTaskEvidence{{
			Key: jobID + "-t1", Query: "plumber in Austin TX 78701", State: "completed",
			Attempts: 1, FinishedAt: started + 300,
			RowsAdded: rowsAdded, DuplicatesSkipped: duplicates,
		}},
	}
}

func benchmarkHistoryFixture(t *testing.T) (*benchmarkHistoryTestRepository, *Service) {
	t.Helper()

	first := rerunSourceJob()
	repository := newBenchmarkHistoryTestRepository(first)
	service := NewService(repository, t.TempDir())

	repository.evidence[first.ID] = benchmarkRunEvidence(first.ID, "Austin plumbers", 40, 40, 10)

	rerun, err := service.RerunJob(context.Background(), RerunRequest{
		SourceJobID: first.ID, Mode: RerunModeNewOnly,
	})
	if err != nil {
		t.Fatalf("RerunJob: %v", err)
	}

	repository.evidence[rerun.Job.ID] = benchmarkRunEvidence(
		rerun.Job.ID, rerun.Job.Name, 65, 70, 5,
	)

	return repository, service
}

func TestCaptureJobBenchmarkStoresTheHeadlineScalars(t *testing.T) {
	t.Parallel()

	repository, service := benchmarkHistoryFixture(t)
	source := rerunSourceJob()

	snapshot, err := service.CaptureJobBenchmark(context.Background(), source.ID)
	if err != nil {
		t.Fatalf("CaptureJobBenchmark: %v", err)
	}

	if snapshot.JobID != source.ID || snapshot.UniqueBusinesses != 40 ||
		snapshot.RowsAdded != 40 || snapshot.DuplicatesSkipped != 10 {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	if snapshot.EngineVersion != "v-test" || snapshot.WallSeconds != 600 {
		t.Fatalf("snapshot runtime = %#v", snapshot)
	}

	// The full report is kept alongside the scalars for exact replay.
	var report BenchmarkReport
	if err := json.Unmarshal([]byte(snapshot.Report), &report); err != nil {
		t.Fatalf("decode stored report: %v", err)
	}

	if report.JobID != source.ID || report.Totals.UniqueBusinesses != 40 {
		t.Fatalf("stored report = %#v", report.Totals)
	}

	if len(repository.snapshots) != 1 {
		t.Fatalf("stored snapshots = %d, want 1", len(repository.snapshots))
	}

	history, err := service.BenchmarkHistory(context.Background(), 0)
	if err != nil || len(history) != 1 || history[0].JobID != source.ID {
		t.Fatalf("history = %#v (%v)", history, err)
	}
}

func TestBenchmarkSeriesChartsACampaignInGenerationOrder(t *testing.T) {
	t.Parallel()

	repository, service := benchmarkHistoryFixture(t)
	source := rerunSourceJob()

	series, err := service.CompareJobBenchmarkSeries(context.Background(), BenchmarkSeriesRequest{
		CampaignID: source.ID,
	})
	if err != nil {
		t.Fatalf("CompareJobBenchmarkSeries: %v", err)
	}

	if series.CampaignID != source.ID || len(series.Points) != 2 {
		t.Fatalf("series = %#v", series)
	}

	if series.Points[0].JobID != source.ID || series.Points[0].Generation != 0 ||
		series.Points[0].Seq != 1 {
		t.Fatalf("first point = %#v", series.Points[0])
	}

	if series.Points[1].Generation != 1 || series.Points[1].Mode != RerunModeNewOnly ||
		series.Points[1].Seq != 2 {
		t.Fatalf("second point = %#v", series.Points[1])
	}

	if series.Delta.UniqueBusinesses != 25 {
		t.Fatalf("delta unique businesses = %d, want 25", series.Delta.UniqueBusinesses)
	}

	// The metric catalogue is what a chart binds its axes to, so it must
	// always be present and stable.
	if len(series.Metrics) == 0 || series.Metrics[0].Key != "unique_businesses" {
		t.Fatalf("metrics = %#v", series.Metrics)
	}

	// Both runs were derived once and cached; asking again reuses them.
	derivedBefore := repository.derived

	if _, err := service.CompareJobBenchmarkSeries(context.Background(), BenchmarkSeriesRequest{
		CampaignID: source.ID,
	}); err != nil {
		t.Fatalf("repeat series: %v", err)
	}

	if repository.derived != derivedBefore {
		t.Fatalf("repeat series recomputed %d report(s)", repository.derived-derivedBefore)
	}
}

func TestBenchmarkSeriesAcceptsAnExplicitRunListAndRejectsEmptyOnes(t *testing.T) {
	t.Parallel()

	_, service := benchmarkHistoryFixture(t)
	source := rerunSourceJob()

	campaign, err := service.JobCampaignOf(context.Background(), source.ID)
	if err != nil {
		t.Fatalf("JobCampaignOf: %v", err)
	}

	// Reversed, and with a repeat, to prove the caller's order is kept and
	// duplicates are collapsed.
	series, err := service.CompareJobBenchmarkSeries(context.Background(), BenchmarkSeriesRequest{
		JobIDs: []string{campaign.Jobs[1].JobID, source.ID, source.ID, "  "},
	})
	if err != nil {
		t.Fatalf("CompareJobBenchmarkSeries: %v", err)
	}

	if len(series.Points) != 2 || series.Points[0].JobID != campaign.Jobs[1].JobID ||
		series.Points[1].JobID != source.ID {
		t.Fatalf("series points = %#v", series.Points)
	}

	// Outside a campaign a point reports no generation rather than a
	// misleading zero.
	if series.Points[0].Generation != -1 || series.CampaignID != "" {
		t.Fatalf("explicit-list series = %#v", series)
	}

	if _, err := service.CompareJobBenchmarkSeries(
		context.Background(), BenchmarkSeriesRequest{},
	); !errors.Is(err, ErrInvalidBenchmarkSeries) {
		t.Fatalf("empty series error = %v, want ErrInvalidBenchmarkSeries", err)
	}

	tooMany := make([]string, MaximumBenchmarkSeriesJobs+1)
	for index := range tooMany {
		tooMany[index] = "job-" + string(rune('a'+index%26)) + string(rune('a'+index/26))
	}

	if _, err := service.CompareJobBenchmarkSeries(context.Background(), BenchmarkSeriesRequest{
		JobIDs: tooMany,
	}); !errors.Is(err, ErrInvalidBenchmarkSeries) {
		t.Fatalf("oversized series error = %v, want ErrInvalidBenchmarkSeries", err)
	}
}

func TestBenchmarkCompareEndpointKeepsItsPairShapeAndGainsASeries(t *testing.T) {
	t.Parallel()

	repository, service := benchmarkHistoryFixture(t)
	source := rerunSourceJob()

	campaign, err := service.JobCampaignOf(context.Background(), source.ID)
	if err != nil {
		t.Fatalf("JobCampaignOf: %v", err)
	}

	rescanID := campaign.Jobs[1].JobID

	server, err := New(service, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The historical two-run comparison is untouched.
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/benchmark/compare?base="+source.ID+"&candidate="+rescanID, nil)
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("pair compare = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	for _, expected := range []string{`"base"`, `"candidate"`, `"delta"`} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("pair compare body missing %s: %s", expected, recorder.Body.String())
		}
	}

	// A campaign returns the chartable series instead.
	request = httptest.NewRequest(http.MethodGet, "/api/v1/benchmark/compare?campaign="+source.ID, nil)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("campaign compare = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var seriesResponse struct {
		Data BenchmarkSeries `json:"data"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &seriesResponse); err != nil {
		t.Fatalf("decode series response: %v", err)
	}

	if len(seriesResponse.Data.Points) != 2 || seriesResponse.Data.CampaignID != source.ID {
		t.Fatalf("series response = %#v", seriesResponse.Data)
	}

	// So does an explicit job list.
	request = httptest.NewRequest(http.MethodGet,
		"/api/v1/benchmark/compare?jobs="+source.ID+","+rescanID, nil)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"points"`) {
		t.Fatalf("job-list compare = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	// History lists what has been captured.
	request = httptest.NewRequest(http.MethodGet, "/api/v1/benchmark/history?limit=10", nil)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), source.ID) {
		t.Fatalf("history = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/benchmark/history?limit=0", nil)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid history limit = %d, want 422", recorder.Code)
	}

	if len(repository.snapshots) != 2 {
		t.Fatalf("stored snapshots = %d, want 2", len(repository.snapshots))
	}
}

func TestBenchmarkSnapshotEndpointRequiresCSRF(t *testing.T) {
	t.Parallel()

	repository, service := benchmarkHistoryFixture(t)
	source := rerunSourceJob()

	server, err := New(service, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+source.ID+"/benchmark/snapshot", nil)
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden || len(repository.snapshots) != 0 {
		t.Fatalf("snapshot without a CSRF token = %d with %d stored", recorder.Code, len(repository.snapshots))
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+source.ID+"/benchmark/snapshot", nil)
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated || len(repository.snapshots) != 1 {
		t.Fatalf("snapshot = %d with %d stored, body = %s",
			recorder.Code, len(repository.snapshots), recorder.Body.String())
	}
}

func TestBenchmarkHistoryReportsAnUnsupportedStore(t *testing.T) {
	t.Parallel()

	service := NewService(newCampaignTestRepository(), t.TempDir())

	if _, err := service.BenchmarkHistory(context.Background(), 5); !errors.Is(
		err, ErrBenchmarkHistoryUnsupported,
	) {
		t.Fatalf("history error = %v, want ErrBenchmarkHistoryUnsupported", err)
	}

	if service.SupportsBenchmarkHistory() {
		t.Fatal("SupportsBenchmarkHistory() = true for a plain repository")
	}
}
