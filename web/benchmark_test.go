package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	benchmarkBaseJobID      = "11111111-1111-1111-1111-111111111111"
	benchmarkCandidateJobID = "22222222-2222-2222-2222-222222222222"
)

// benchmarkFixtureRepository serves crafted benchmark evidence per job so the
// report math and the read-only endpoints can be asserted exactly.
type benchmarkFixtureRepository struct {
	fixedJobRepository
	jobs     map[string]Job
	evidence map[string]BenchmarkEvidence
}

func (r *benchmarkFixtureRepository) Get(_ context.Context, id string) (Job, error) {
	job, ok := r.jobs[id]
	if !ok {
		return Job{}, ErrPlacesNotFound
	}

	return job, nil
}

func (r *benchmarkFixtureRepository) JobBenchmarkEvidence(_ context.Context, jobID string) (BenchmarkEvidence, error) {
	evidence, ok := r.evidence[jobID]
	if !ok {
		return BenchmarkEvidence{}, ErrLifecycleNotFound
	}

	return evidence, nil
}

// benchmarkFixtureEvidence is a five-task GBP-shaped run: three completed
// tasks (one born from adaptive expansion), one exhausted failure, and one
// skipped task, over a 20-minute active runtime.
func benchmarkFixtureEvidence() BenchmarkEvidence {
	return BenchmarkEvidence{
		JobID:            benchmarkBaseJobID,
		JobName:          "Austin plumbers",
		ScraperVersion:   "v1.17.3",
		SchemaVersion:    12,
		CreatedAt:        300,
		StartedAt:        400,
		FinishedAt:       1600,
		RawRecords:       33,
		UniqueRecords:    16,
		DuplicateRecords: 16,
		Tasks: []BenchmarkTaskEvidence{
			{
				Key: "plumber/78701", Query: "plumber in Austin TX 78701",
				State: "completed", Sequence: 0, Attempts: 1, FinishedAt: 1000,
				RowsAdded: 10, RowsReplaced: 1, DuplicatesSkipped: 2,
			},
			{
				Key: "plumber/78702", Query: "plumber in Austin TX 78702",
				State: "completed", Sequence: 1, Attempts: 2, FinishedAt: 1100,
				RowsAdded: 4, DuplicatesSkipped: 6,
			},
			{
				Key: "expansion/78701", Query: "emergency plumber in Austin TX 78701",
				Origin: "expansion", State: "completed", Sequence: 2, Attempts: 1,
				FinishedAt: 1200, RowsAdded: 2, DuplicatesSkipped: 8,
			},
			{
				Key: "roofer/78703", Query: "roofer in Austin TX 78703",
				State: "failed", Sequence: 3, Attempts: 3, LastError: "page timeout exceeded",
			},
			{
				Key: "duplicate-area", Query: "no gbp shape query",
				State: "skipped", Sequence: 4,
			},
		},
		Events: []BenchmarkEventEvidence{
			{Type: "task-checkpoint", Severity: "warning", Message: "attempt stopped",
				Context: `{"last_error":"page timeout exceeded","state":"pending"}`},
			{Type: "task-checkpoint", Severity: "warning", Message: "attempt stopped",
				Context: `{"last_error":"proxy connection refused","state":"pending"}`},
			{Type: "task-checkpoint", Severity: "error", Message: "attempt stopped",
				Context: `{"last_error":"CAPTCHA encountered","state":"failed"}`},
		},
		Businesses: BenchmarkBusinessEvidence{
			Unique:         6,
			WebsiteStatus:  map[string]int64{"no_website": 3, "has_website": 2, "social_only": 1},
			ProspectTier:   map[string]int64{"hot": 2, "warm": 1, "unclassified": 3},
			ProspectStatus: map[string]int64{"no_website": 3, "unclassified": 3},
			WithEmail:      2,
			WithPhone:      5,
			WithBoth:       2,
		},
		Proxies: []BenchmarkProxyEvidence{
			{
				ProxyID: "proxy-1", ProxyName: "dc-us-1", PoolID: "pool-a",
				TaskSuccesses: 8, TaskFailures: 2, ConsecutiveFailures: 1,
				TotalTaskSeconds: 30, LastSuccessAt: 1500, LastFailureAt: 900,
				LastError: "connect refused",
			},
		},
	}
}

func TestBuildBenchmarkReportComputesExactNumbers(t *testing.T) {
	t.Parallel()

	report := buildBenchmarkReport(benchmarkFixtureEvidence(), time.Unix(2000, 0).UTC())

	wantTotals := BenchmarkTotals{
		TasksPlanned:           4,
		TasksExpanded:          1,
		TasksCompleted:         3,
		TasksFailed:            1,
		TasksSkipped:           1,
		Attempts:               7,
		Retries:                3,
		RowsAdded:              16,
		RowsReplaced:           1,
		DuplicatesSkipped:      16,
		DuplicateRate:          0.5,
		UniqueBusinesses:       6,
		TotalDiscoveredRows:    33,
		NewBusinessesPerMinute: 0.3, // 6 unique over 20 active minutes
	}
	if report.Totals != wantTotals {
		t.Fatalf("totals = %#v, want %#v", report.Totals, wantTotals)
	}

	if len(report.YieldByQuery) != 5 || report.YieldByQuery[0].Key != "plumber in Austin TX 78701" {
		t.Fatalf("yield_by_query = %#v", report.YieldByQuery)
	}

	wantZips := []BenchmarkYieldRow{
		{Key: "78701", Tasks: 2, RowsAdded: 12, DuplicatesSkipped: 10, UniqueRatio: 0.5455},
		{Key: "78702", Tasks: 1, RowsAdded: 4, DuplicatesSkipped: 6, UniqueRatio: 0.4},
		{Key: "78703", Tasks: 1},
	}
	assertBenchmarkYield(t, "yield_by_zip", report.YieldByZip, wantZips)

	wantSynonyms := []BenchmarkYieldRow{
		{Key: "plumber", Tasks: 2, RowsAdded: 14, DuplicatesSkipped: 8, UniqueRatio: 0.6364},
		{Key: "emergency plumber", Tasks: 1, RowsAdded: 2, DuplicatesSkipped: 8, UniqueRatio: 0.2},
		{Key: "roofer", Tasks: 1},
	}
	assertBenchmarkYield(t, "yield_by_synonym", report.YieldBySynonym, wantSynonyms)

	wantSaturation := []BenchmarkSaturationPoint{
		{Seq: 1, TaskKey: "plumber/78701", RowsAdded: 10, DuplicatesSkipped: 2, CumulativeNewRatio: 0.8333},
		{Seq: 2, TaskKey: "plumber/78702", RowsAdded: 4, DuplicatesSkipped: 6, CumulativeNewRatio: 0.6364},
		{Seq: 3, TaskKey: "expansion/78701", RowsAdded: 2, DuplicatesSkipped: 8, CumulativeNewRatio: 0.5},
	}
	if len(report.SaturationTrend) != len(wantSaturation) {
		t.Fatalf("saturation_trend = %#v", report.SaturationTrend)
	}
	for index, want := range wantSaturation {
		if report.SaturationTrend[index] != want {
			t.Fatalf("saturation_trend[%d] = %#v, want %#v", index, report.SaturationTrend[index], want)
		}
	}

	wantFailures := []BenchmarkFailureClass{
		{Class: "blocked", Count: 1, Retries: 0, Sample: "CAPTCHA encountered"},
		{Class: "proxy", Count: 1, Retries: 1, Sample: "proxy connection refused"},
		{Class: "timeout", Count: 1, Retries: 1, Sample: "page timeout exceeded"},
	}
	if len(report.Failures) != len(wantFailures) {
		t.Fatalf("failures = %#v", report.Failures)
	}
	for index, want := range wantFailures {
		if report.Failures[index] != want {
			t.Fatalf("failures[%d] = %#v, want %#v", index, report.Failures[index], want)
		}
	}

	if len(report.ProxyPerformance) != 1 {
		t.Fatalf("proxy_performance = %#v", report.ProxyPerformance)
	}
	proxy := report.ProxyPerformance[0]
	if proxy.ProxyID != "proxy-1" || proxy.AverageTaskSeconds != 3 || proxy.TaskSuccesses != 8 {
		t.Fatalf("proxy_performance[0] = %#v", proxy)
	}

	wantWebsite := []BenchmarkDistributionRow{
		{Label: "no_website", Count: 3}, {Label: "has_website", Count: 2}, {Label: "social_only", Count: 1},
	}
	for index, want := range wantWebsite {
		if report.WebsiteStatusDistribution[index] != want {
			t.Fatalf("website_status_distribution = %#v", report.WebsiteStatusDistribution)
		}
	}
	if report.EmailAvailability != (BenchmarkEmailAvailability{WithEmail: 2, WithPhone: 5, WithBoth: 2, Total: 6}) {
		t.Fatalf("email_availability = %#v", report.EmailAvailability)
	}
	if len(report.ProspectTierDistribution) != 3 || report.ProspectTierDistribution[0].Label != "unclassified" {
		t.Fatalf("prospect_tier_distribution = %#v", report.ProspectTierDistribution)
	}

	wantRuntime := BenchmarkRuntime{
		CreatedAt: 300, StartedAt: 400, FinishedAt: 1600, WallSeconds: 1200,
		TasksPerMinute: 0.15, RawRecords: 33, UniqueRecords: 16, DuplicateRecords: 16,
	}
	if report.Runtime != wantRuntime {
		t.Fatalf("runtime = %#v, want %#v", report.Runtime, wantRuntime)
	}
	if report.SchemaVersion != 12 || report.EngineVersion != "v1.17.3" || report.JobID != benchmarkBaseJobID {
		t.Fatalf("report identity = %#v", report)
	}
}

func assertBenchmarkYield(t *testing.T, section string, got, want []BenchmarkYieldRow) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %#v, want %#v", section, got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("%s[%d] = %#v, want %#v", section, index, got[index], want[index])
		}
	}
}

// TestBenchmarkReportZeroEvidenceKeepsEveryFieldPresent guards the diffing
// contract: a report built from an empty run still serializes every field,
// with empty arrays instead of nulls.
func TestBenchmarkReportZeroEvidenceKeepsEveryFieldPresent(t *testing.T) {
	t.Parallel()

	report := buildBenchmarkReport(BenchmarkEvidence{JobID: benchmarkBaseJobID}, time.Unix(2000, 0).UTC())
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	body := string(payload)
	for _, want := range []string{
		`"tasks_planned":0`, `"tasks_expanded":0`, `"duplicate_rate":0`,
		`"new_businesses_per_minute":0`, `"yield_by_query":[]`, `"yield_by_zip":[]`,
		`"yield_by_synonym":[]`, `"saturation_trend":[]`, `"failures":[]`,
		`"proxy_performance":[]`, `"website_status_distribution":[]`,
		`"email_availability":{"with_email":0,"with_phone":0,"with_both":0,"total":0}`,
		`"prospect_tier_distribution":[]`, `"prospect_status_distribution":[]`,
		`"wall_seconds":0`, `"tasks_per_minute":0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("zero report missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "null") {
		t.Fatalf("zero report serialized a null: %s", body)
	}
}

func newBenchmarkTestServer(t *testing.T) (*Server, *http.ServeMux) {
	t.Helper()

	base := benchmarkFixtureEvidence()
	candidate := benchmarkFixtureEvidence()
	candidate.JobID = benchmarkCandidateJobID
	candidate.FinishedAt = 1000 // half the base wall time
	candidate.Businesses.Unique = 9
	candidate.Tasks[3].State = "completed" // the base failure now completes
	candidate.Tasks[3].Attempts = 1
	candidate.Tasks[3].FinishedAt = 990
	candidate.Events = candidate.Events[:1]

	repository := &benchmarkFixtureRepository{
		jobs: map[string]Job{
			benchmarkBaseJobID:      {ID: benchmarkBaseJobID, Name: "base", Status: StatusOK, Date: time.Unix(300, 0)},
			benchmarkCandidateJobID: {ID: benchmarkCandidateJobID, Name: "candidate", Status: StatusOK, Date: time.Unix(300, 0)},
		},
		evidence: map[string]BenchmarkEvidence{
			benchmarkBaseJobID:      base,
			benchmarkCandidateJobID: candidate,
		},
	}
	server, err := New(NewService(repository, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mux := http.NewServeMux()
	server.registerBenchmarkRoutes(mux)

	return server, mux
}

func TestBenchmarkEndpointServesTheReport(t *testing.T) {
	t.Parallel()

	_, mux := newBenchmarkTestServer(t)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+benchmarkBaseJobID+"/benchmark", http.NoBody))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		`"job_id":"` + benchmarkBaseJobID + `"`, `"unique_businesses":6`,
		`"duplicate_rate":0.5`, `"new_businesses_per_minute":0.3`,
		`"tasks_expanded":1`, `"tasks_skipped":1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("report body missing %q: %s", want, body)
		}
	}
}

func TestBenchmarkEndpointRejectsMissingAndInvalidJobs(t *testing.T) {
	t.Parallel()

	_, mux := newBenchmarkTestServer(t)
	tests := []struct {
		name string
		path string
		want int
	}{
		{name: "invalid id", path: "/api/v1/jobs/not-a-uuid/benchmark", want: http.StatusUnprocessableEntity},
		{name: "unknown job", path: "/api/v1/jobs/99999999-9999-9999-9999-999999999999/benchmark", want: http.StatusNotFound},
		{name: "compare missing params", path: "/api/v1/benchmark/compare", want: http.StatusUnprocessableEntity},
		{name: "compare bad candidate", path: "/api/v1/benchmark/compare?base=" + benchmarkBaseJobID + "&candidate=nope", want: http.StatusUnprocessableEntity},
		{
			name: "compare unknown candidate",
			path: "/api/v1/benchmark/compare?base=" + benchmarkBaseJobID + "&candidate=99999999-9999-9999-9999-999999999999",
			want: http.StatusNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, http.NoBody))
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.want, recorder.Body.String())
			}
		})
	}
}

func TestBenchmarkUnsupportedRepositoryReturnsNotImplemented(t *testing.T) {
	t.Parallel()

	repository := &fixedJobRepository{job: Job{ID: benchmarkBaseJobID, Name: "plain", Status: StatusOK, Date: time.Unix(300, 0)}}
	server, err := New(NewService(repository, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if server.svc.SupportsJobBenchmarks() {
		t.Fatal("plain repository must not claim benchmark support")
	}
	mux := http.NewServeMux()
	server.registerBenchmarkRoutes(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+benchmarkBaseJobID+"/benchmark", http.NoBody))
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestCompareJobBenchmarksReportsHeadlineDeltas(t *testing.T) {
	t.Parallel()

	server, mux := newBenchmarkTestServer(t)

	comparison, err := server.svc.CompareJobBenchmarks(
		context.Background(), benchmarkBaseJobID, benchmarkCandidateJobID,
	)
	if err != nil {
		t.Fatalf("CompareJobBenchmarks: %v", err)
	}
	// Candidate: 9 unique in 10 minutes (0.9/min), rows 16+16+2 replaced by the
	// recovered fourth task keeping rates equal, one fewer failed task and two
	// fewer retries, one fewer failure event, and 600 fewer wall seconds.
	wantDelta := BenchmarkDelta{
		UniqueBusinesses:       3,
		NewBusinessesPerMinute: 0.6,
		DuplicateRate:          0,
		TasksFailed:            -1,
		FailureCount:           -2,
		Retries:                -2,
		WallSeconds:            -600,
	}
	if comparison.Delta != wantDelta {
		t.Fatalf("delta = %#v, want %#v", comparison.Delta, wantDelta)
	}
	if comparison.Base.JobID != benchmarkBaseJobID || comparison.Candidate.JobID != benchmarkCandidateJobID {
		t.Fatalf("comparison identity = %#v / %#v", comparison.Base.JobID, comparison.Candidate.JobID)
	}

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/benchmark/compare?base="+benchmarkBaseJobID+"&candidate="+benchmarkCandidateJobID,
		http.NoBody,
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	for _, want := range []string{`"base":{`, `"candidate":{`, `"delta":{`, `"wall_seconds":-600`} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Fatalf("compare body missing %q: %s", want, recorder.Body.String())
		}
	}
}

func TestJobBenchmarkPageRendersEverySection(t *testing.T) {
	t.Parallel()

	server, _ := newBenchmarkTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "/app/jobs/"+benchmarkBaseJobID+"/benchmark", http.NoBody)
	request.SetPathValue("id", benchmarkBaseJobID)
	request = requestWithID(request)
	recorder := httptest.NewRecorder()
	server.jobBenchmarkPage(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"Report identity", "Task plan outcome", "Runtime", "Yield by query",
		"Yield by ZIP", "Yield by synonym", "Saturation trend", "Failures by class",
		"Proxy performance", "Website status", "Contact availability",
		"Prospect tiers", "Prospect statuses",
		"plumber in Austin TX 78701", "78701", "dc-us-1", "50.0%",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("benchmark page missing %q", want)
		}
	}
}

func TestJobBenchmarkPageRejectsUnknownJob(t *testing.T) {
	t.Parallel()

	server, _ := newBenchmarkTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "/app/jobs/99999999-9999-9999-9999-999999999999/benchmark", http.NoBody)
	request.SetPathValue("id", "99999999-9999-9999-9999-999999999999")
	request = requestWithID(request)
	recorder := httptest.NewRecorder()
	server.jobBenchmarkPage(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
