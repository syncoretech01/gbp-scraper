package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

const benchmarkTestJobID = "33333333-3333-3333-3333-333333333333"

// seedBenchmarkWorkspace builds a small but complete durable run in a
// temporary workspace: four planned tasks plus one adaptive-expansion task,
// one retry, one exhausted failure, one skipped task, four linked businesses
// with mixed contact and prospect fields, one worker error event, and one
// proxy stats row.
func seedBenchmarkWorkspace(t *testing.T, ctx context.Context) *repo {
	t.Helper()

	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete := repository.(*repo)
	t.Cleanup(func() { _ = concrete.db.Close() })

	job := lifecycleTestJob(benchmarkTestJobID, time.Unix(1_800_000_000, 0).UTC())
	if err := concrete.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	definitions := []web.JobTaskDefinition{
		{Key: "plumber-78701", Kind: "map-query", Sequence: 0, Query: "plumber in Austin TX 78701"},
		{Key: "plumber-78702", Kind: "map-query", Sequence: 1, Query: "plumber in Austin TX 78702"},
		{Key: "roofer-78703", Kind: "map-query", Sequence: 2, Query: "roofer in Austin TX 78703"},
		{Key: "skip-78704", Kind: "map-query", Sequence: 3, Query: "landscaper in Austin TX 78704"},
	}
	if _, err := concrete.PrepareJobTasks(ctx, job.ID, definitions, 2); err != nil {
		t.Fatalf("prepare tasks: %v", err)
	}

	// Task 1 completes first try.
	if _, err := concrete.StartJobTask(ctx, job.ID, "plumber-78701"); err != nil {
		t.Fatalf("start task 1: %v", err)
	}
	if err := concrete.CompleteJobTask(ctx, job.ID, "plumber-78701",
		web.JobTaskCheckpoint{RowsAdded: 5, RowsReplaced: 1, DuplicatesSkipped: 3}); err != nil {
		t.Fatalf("complete task 1: %v", err)
	}

	// Task 2 fails once retryably, then completes on the second attempt.
	if _, err := concrete.StartJobTask(ctx, job.ID, "plumber-78702"); err != nil {
		t.Fatalf("start task 2: %v", err)
	}
	if err := concrete.FailJobTask(ctx, job.ID, "plumber-78702",
		errors.New("page timeout exceeded"), true, web.JobTaskCheckpoint{}); err != nil {
		t.Fatalf("fail task 2 retryable: %v", err)
	}
	if _, err := concrete.StartJobTask(ctx, job.ID, "plumber-78702"); err != nil {
		t.Fatalf("restart task 2: %v", err)
	}
	if err := concrete.CompleteJobTask(ctx, job.ID, "plumber-78702",
		web.JobTaskCheckpoint{RowsAdded: 2, DuplicatesSkipped: 4}); err != nil {
		t.Fatalf("complete task 2: %v", err)
	}

	// Task 3 exhausts.
	if _, err := concrete.StartJobTask(ctx, job.ID, "roofer-78703"); err != nil {
		t.Fatalf("start task 3: %v", err)
	}
	if err := concrete.FailJobTask(ctx, job.ID, "roofer-78703",
		errors.New("proxy connection refused"), false, web.JobTaskCheckpoint{}); err != nil {
		t.Fatalf("fail task 3: %v", err)
	}

	// Task 4 is skipped by the planner (no exported transition writes this
	// state yet, so the fixture records the durable outcome directly).
	if _, err := concrete.db.ExecContext(ctx,
		`UPDATE job_tasks SET state = 'skipped' WHERE job_id = ? AND task_key = 'skip-78704'`,
		job.ID); err != nil {
		t.Fatalf("skip task 4: %v", err)
	}

	// Task 5 arrives later from adaptive expansion and completes. The origin
	// column is populated by the discovery planner at runtime; the fixture
	// stamps it directly so the report can be asserted without that code.
	expansionOnly := []web.JobTaskDefinition{{
		Key: "expansion-78701", Kind: "map-query", Sequence: 4,
		Query: "emergency plumber in Austin TX 78701",
	}}
	if _, err := concrete.PrepareJobTasks(ctx, job.ID, expansionOnly, 2); err != nil {
		t.Fatalf("prepare expansion task: %v", err)
	}
	if _, err := concrete.db.ExecContext(ctx,
		`UPDATE job_tasks SET origin = 'expansion' WHERE job_id = ? AND task_key = 'expansion-78701'`,
		job.ID); err != nil {
		t.Fatalf("mark expansion origin: %v", err)
	}
	if _, err := concrete.StartJobTask(ctx, job.ID, "expansion-78701"); err != nil {
		t.Fatalf("start expansion task: %v", err)
	}
	if err := concrete.CompleteJobTask(ctx, job.ID, "expansion-78701",
		web.JobTaskCheckpoint{RowsAdded: 1, DuplicatesSkipped: 5}); err != nil {
		t.Fatalf("complete expansion task: %v", err)
	}

	if err := concrete.RecordJobWorkerEvent(ctx, job.ID, "scrape-error", "error",
		"browser crashed", map[string]any{"last_error": "browser crashed", "state": "failed"}); err != nil {
		t.Fatalf("record worker event: %v", err)
	}

	// Businesses arrive through the normal import path so job_businesses,
	// emails, and phones are linked exactly as production would.
	path := filepath.Join(t.TempDir(), "results.csv")
	writeLegacyResultRows(t, path,
		map[string]string{
			"input_id": "seed-1", "title": "Both Contacts Plumbing", "category": "Plumber",
			"address": "10 Pine St, Austin, TX 78701", "phone": "+1 512-555-0101",
			"emails": "owner@bothcontacts.test", "latitude": "30.2711", "longitude": "-97.7437",
			"place_id": "bench-both",
		},
		map[string]string{
			"input_id": "seed-2", "title": "Phone Only Roofing", "category": "Roofer",
			"address": "22 Oak Ave, Austin, TX 78702", "phone": "+1 512-555-0102",
			"latitude": "30.2802", "longitude": "-97.7301", "place_id": "bench-phone",
		},
		map[string]string{
			"input_id": "seed-3", "title": "Email Only Landscaping", "category": "Landscaper",
			"address": "31 Elm Rd, Austin, TX 78703", "emails": "hello@emailonly.test",
			"latitude": "30.2905", "longitude": "-97.7604", "place_id": "bench-email",
		},
		map[string]string{
			"input_id": "seed-4", "title": "Quiet Business", "category": "Bakery",
			"address": "44 Cedar Blvd, Austin, TX 78704",
			"latitude": "30.2455", "longitude": "-97.7502", "place_id": "bench-none",
		},
	)
	if _, err := concrete.ImportLegacyCSV(ctx, job, path); err != nil {
		t.Fatalf("import businesses: %v", err)
	}

	prospectStates := []struct{ placeID, status, tier, prospect string }{
		{placeID: "bench-both", status: "has_website", tier: "cold", prospect: "has_website"},
		{placeID: "bench-phone", status: "no_website", tier: "hot", prospect: "no_website"},
		{placeID: "bench-email", status: "no_website", tier: "hot", prospect: "no_website"},
		// The import path auto-classifies prospects; reset the fourth business
		// so the report's "unclassified" labelling stays observable.
		{placeID: "bench-none", status: "unknown", tier: "", prospect: ""},
	}
	for _, state := range prospectStates {
		if _, err := concrete.db.ExecContext(ctx,
			`UPDATE businesses SET website_status = ?, prospect_tier = ?, prospect_status = ?
			WHERE place_id = ?`,
			state.status, state.tier, state.prospect, state.placeID); err != nil {
			t.Fatalf("set prospect state for %s: %v", state.placeID, err)
		}
	}

	// Fix the runtime window (10 active minutes) and version stamp.
	if _, err := concrete.db.ExecContext(ctx,
		`UPDATE job_runtime SET started_at = 1000, finished_at = 1600,
			raw_records = 21, unique_records = 9, duplicate_records = 12,
			scraper_version = 'v-bench-test'
		WHERE job_id = ?`,
		job.ID); err != nil {
		t.Fatalf("stamp runtime: %v", err)
	}

	// One proxy with aggregate task stats.
	now := time.Now().UTC().Unix()
	if _, err := concrete.db.ExecContext(ctx,
		`INSERT INTO proxies(id, name, url_encrypted, url_masked, protocol, created_at, updated_at)
		VALUES ('proxy-1', 'dc-us-1', 'enc', 'http://***@proxy.test:8080', 'http', ?, ?)`,
		now, now); err != nil {
		t.Fatalf("insert proxy: %v", err)
	}
	if _, err := concrete.db.ExecContext(ctx,
		`INSERT INTO proxy_task_stats(
			proxy_id, pool_id, task_successes, task_failures, consecutive_failures,
			total_task_seconds, last_success_at, last_failure_at, last_error, updated_at)
		VALUES ('proxy-1', 'pool-a', 8, 2, 1, 30, 1500, 900, 'connect refused', ?)`,
		now); err != nil {
		t.Fatalf("insert proxy task stats: %v", err)
	}

	return concrete
}

func TestJobBenchmarkEvidenceGathersDurableRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := seedBenchmarkWorkspace(t, ctx)

	evidence, err := repository.JobBenchmarkEvidence(ctx, benchmarkTestJobID)
	if err != nil {
		t.Fatalf("JobBenchmarkEvidence: %v", err)
	}

	if evidence.JobID != benchmarkTestJobID || evidence.JobName != "Dentists "+benchmarkTestJobID {
		t.Fatalf("job identity = %q / %q", evidence.JobID, evidence.JobName)
	}
	if evidence.SchemaVersion != currentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", evidence.SchemaVersion, currentSchemaVersion)
	}
	if evidence.ScraperVersion != "v-bench-test" || evidence.StartedAt != 1000 || evidence.FinishedAt != 1600 {
		t.Fatalf("runtime header = %#v", evidence)
	}
	if evidence.RawRecords != 21 || evidence.UniqueRecords != 9 || evidence.DuplicateRecords != 12 {
		t.Fatalf("record counters = %d/%d/%d", evidence.RawRecords, evidence.UniqueRecords, evidence.DuplicateRecords)
	}

	if len(evidence.Tasks) != 5 {
		t.Fatalf("tasks = %#v", evidence.Tasks)
	}
	byKey := make(map[string]web.BenchmarkTaskEvidence, len(evidence.Tasks))
	for _, task := range evidence.Tasks {
		byKey[task.Key] = task
	}
	first := byKey["plumber-78701"]
	if first.State != "completed" || first.Attempts != 1 || first.RowsAdded != 5 ||
		first.RowsReplaced != 1 || first.DuplicatesSkipped != 3 || first.Origin != "" {
		t.Fatalf("task 1 = %#v", first)
	}
	second := byKey["plumber-78702"]
	if second.State != "completed" || second.Attempts != 2 || second.RowsAdded != 2 || second.DuplicatesSkipped != 4 {
		t.Fatalf("task 2 = %#v", second)
	}
	third := byKey["roofer-78703"]
	if third.State != "failed" || third.Attempts != 1 || third.LastError == "" {
		t.Fatalf("task 3 = %#v", third)
	}
	if byKey["skip-78704"].State != "skipped" {
		t.Fatalf("task 4 = %#v", byKey["skip-78704"])
	}
	expansion := byKey["expansion-78701"]
	if expansion.Origin != "expansion" || expansion.State != "completed" || expansion.RowsAdded != 1 {
		t.Fatalf("expansion task = %#v", expansion)
	}

	// Two attempt-failure warnings from the checkpoint writer plus the
	// explicit worker error event; completion events are informational and
	// excluded.
	if len(evidence.Events) != 3 {
		t.Fatalf("events = %#v", evidence.Events)
	}

	wantBusinesses := web.BenchmarkBusinessEvidence{
		Unique:         4,
		WebsiteStatus:  map[string]int64{"has_website": 1, "no_website": 2, "unknown": 1},
		ProspectTier:   map[string]int64{"cold": 1, "hot": 2, "unclassified": 1},
		ProspectStatus: map[string]int64{"has_website": 1, "no_website": 2, "unclassified": 1},
		WithEmail:      2,
		WithPhone:      2,
		WithBoth:       1,
	}
	assertBenchmarkCounts(t, "website_status", evidence.Businesses.WebsiteStatus, wantBusinesses.WebsiteStatus)
	assertBenchmarkCounts(t, "prospect_tier", evidence.Businesses.ProspectTier, wantBusinesses.ProspectTier)
	assertBenchmarkCounts(t, "prospect_status", evidence.Businesses.ProspectStatus, wantBusinesses.ProspectStatus)
	if evidence.Businesses.Unique != 4 || evidence.Businesses.WithEmail != 2 ||
		evidence.Businesses.WithPhone != 2 || evidence.Businesses.WithBoth != 1 {
		t.Fatalf("business contacts = %#v", evidence.Businesses)
	}

	if len(evidence.Proxies) != 1 {
		t.Fatalf("proxies = %#v", evidence.Proxies)
	}
	proxy := evidence.Proxies[0]
	if proxy.ProxyID != "proxy-1" || proxy.ProxyName != "dc-us-1" || proxy.PoolID != "pool-a" ||
		proxy.TaskSuccesses != 8 || proxy.TaskFailures != 2 || proxy.TotalTaskSeconds != 30 {
		t.Fatalf("proxy stats = %#v", proxy)
	}
}

func assertBenchmarkCounts(t *testing.T, name string, got, want map[string]int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %#v, want %#v", name, got, want)
	}
	for label, count := range want {
		if got[label] != count {
			t.Fatalf("%s[%q] = %d, want %d", name, label, got[label], count)
		}
	}
}

// TestJobBenchmarkReportEndToEnd runs the full service assembly over the real
// seeded workspace and asserts the exact headline numbers a production
// acceptance diff relies on.
func TestJobBenchmarkReportEndToEnd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := seedBenchmarkWorkspace(t, ctx)
	service := web.NewService(repository, t.TempDir())

	if !service.SupportsJobBenchmarks() {
		t.Fatal("sqlite repository must support benchmarks")
	}
	report, err := service.JobBenchmark(ctx, benchmarkTestJobID)
	if err != nil {
		t.Fatalf("JobBenchmark: %v", err)
	}

	want := web.BenchmarkTotals{
		TasksPlanned:           4,
		TasksExpanded:          1,
		TasksCompleted:         3,
		TasksFailed:            1,
		TasksSkipped:           1,
		Attempts:               5,
		Retries:                1,
		RowsAdded:              8,
		RowsReplaced:           1,
		DuplicatesSkipped:      12,
		DuplicateRate:          0.6, // 12 of 20 merged rows were duplicates
		UniqueBusinesses:       4,
		TotalDiscoveredRows:    21,
		NewBusinessesPerMinute: 0.4, // 4 unique over 10 active minutes
	}
	if report.Totals != want {
		t.Fatalf("totals = %#v, want %#v", report.Totals, want)
	}
	if report.SchemaVersion != currentSchemaVersion || report.EngineVersion != "v-bench-test" {
		t.Fatalf("report identity = %#v", report)
	}
	if report.Runtime.WallSeconds != 600 || report.Runtime.TasksPerMinute != 0.3 {
		t.Fatalf("runtime = %#v", report.Runtime)
	}

	// ZIP 78701 is served by the planned and the expansion task together.
	if len(report.YieldByZip) != 4 {
		t.Fatalf("yield_by_zip = %#v", report.YieldByZip)
	}
	top := report.YieldByZip[0]
	if top.Key != "78701" || top.Tasks != 2 || top.RowsAdded != 6 || top.DuplicatesSkipped != 8 || top.UniqueRatio != 0.4286 {
		t.Fatalf("yield_by_zip[0] = %#v", top)
	}
	if len(report.YieldBySynonym) != 4 || report.YieldBySynonym[0].Key != "plumber" {
		t.Fatalf("yield_by_synonym = %#v", report.YieldBySynonym)
	}

	wantRatios := []float64{0.625, 0.5, 0.4}
	if len(report.SaturationTrend) != len(wantRatios) {
		t.Fatalf("saturation_trend = %#v", report.SaturationTrend)
	}
	for index, ratio := range wantRatios {
		if report.SaturationTrend[index].CumulativeNewRatio != ratio {
			t.Fatalf("saturation_trend[%d] = %#v, want ratio %v", index, report.SaturationTrend[index], ratio)
		}
	}

	wantFailures := map[string]int64{"browser": 1, "proxy": 1, "timeout": 1}
	if len(report.Failures) != len(wantFailures) {
		t.Fatalf("failures = %#v", report.Failures)
	}
	for _, failure := range report.Failures {
		if wantFailures[failure.Class] != failure.Count {
			t.Fatalf("failure class %q = %#v", failure.Class, failure)
		}
	}

	if report.EmailAvailability != (web.BenchmarkEmailAvailability{WithEmail: 2, WithPhone: 2, WithBoth: 1, Total: 4}) {
		t.Fatalf("email_availability = %#v", report.EmailAvailability)
	}
	if len(report.ProxyPerformance) != 1 || report.ProxyPerformance[0].AverageTaskSeconds != 3 {
		t.Fatalf("proxy_performance = %#v", report.ProxyPerformance)
	}
}

func TestJobBenchmarkEvidenceUnknownJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := seedBenchmarkWorkspace(t, ctx)

	_, err := repository.JobBenchmarkEvidence(ctx, "99999999-9999-9999-9999-999999999999")
	if !errors.Is(err, web.ErrLifecycleNotFound) {
		t.Fatalf("unknown job error = %v, want ErrLifecycleNotFound", err)
	}
}
