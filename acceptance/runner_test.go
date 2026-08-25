package acceptance

import (
	"context"
	"math"
	"testing"
	"time"
)

func newClientFor(t *testing.T, fake *fakeServer) *Client {
	t.Helper()
	client, err := NewClient(fake.URL, WithHTTPClient(fake.Client()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return client
}

func testConfig() ExperimentConfig {
	return ExperimentConfig{
		ID:    "T",
		Label: "test",
		Job: JobRequest{
			Name:           "acceptance-test",
			Keywords:       []string{"plumber in Austin TX 78701"},
			RuntimeSeconds: 3600,
			Concurrency:    8,
			GridBBox:       "30.250,-97.760,30.285,-97.720",
			GridCellKM:     1,
		},
	}
}

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.0005 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func TestRunExperimentRecordsEveryMetric(t *testing.T) {
	fake := newFakeServer(t, defaultScenario())
	client := newClientFor(t, fake)

	record, err := runFast(client, testConfig())
	if err != nil {
		t.Fatalf("runFast: %v", err)
	}

	if record.Schema != RecordSchema || record.HarnessVersion != HarnessVersion {
		t.Errorf("schema/version = %q/%q", record.Schema, record.HarnessVersion)
	}
	if record.Run.JobID != defaultScenario().jobID {
		t.Errorf("job id = %q", record.Run.JobID)
	}
	if record.Run.TerminalState != "partial" || record.Run.StopReason != "runtime_limit" {
		t.Errorf("terminal=%q stop=%q", record.Run.TerminalState, record.Run.StopReason)
	}
	approx(t, "wall_seconds", record.Run.WallSeconds, 120)

	// Config echo.
	if record.Config.Mode != ModeBrowser || record.Config.Connection != ConnectionDirect {
		t.Errorf("mode/connection = %q/%q", record.Config.Mode, record.Config.Connection)
	}
	if record.Config.Concurrency != 8 {
		t.Errorf("config concurrency = %d", record.Config.Concurrency)
	}
	if record.Config.EstimatedGridCells != 16 || record.Config.EstimatedSeedTasks != 48 {
		t.Errorf("estimated cells/tasks = %d/%d", record.Config.EstimatedGridCells, record.Config.EstimatedSeedTasks)
	}

	// Outcomes.
	out := record.Outcomes
	if out.DiscoveredRows != 130 {
		t.Errorf("discovered_rows = %d", out.DiscoveredRows)
	}
	if out.UniqueBusinesses != 100 {
		t.Errorf("unique_businesses = %d", out.UniqueBusinesses)
	}
	if out.ResultsTotal != 100 {
		t.Errorf("results_total = %d", out.ResultsTotal)
	}
	approx(t, "rows_per_minute", out.RowsPerMinute, 65) // 130 rows / 2 minutes
	approx(t, "new_businesses_per_minute", out.NewBusinessesPerMinute, 50)
	approx(t, "duplicate_rate", out.DuplicateRate, 0.1538)
	if out.DuplicateCount != 20 {
		t.Errorf("duplicate_count = %d", out.DuplicateCount)
	}
	approx(t, "task_success_rate", out.TaskSuccessRate, 40.0/48.0)      // 0.8333
	approx(t, "browser_failure_rate", out.BrowserFailureRate, 3.0/51.0) // 3 fails / (3 + 48 finished)
	approx(t, "block_rate", out.BlockRate, 2.0/50.0)                    // 2 blocks / (2 + 48 finished)
	if out.RetryCount != 9 {
		t.Errorf("retry_count = %d", out.RetryCount)
	}
	if out.Tasks.Total != 48 || out.Tasks.Completed != 40 || out.Tasks.Failed != 6 || out.Tasks.Skipped != 2 {
		t.Errorf("tasks = %+v", out.Tasks)
	}
	if out.FailureKinds["browser-failure"] != 3 || out.FailureKinds["blocked"] != 2 {
		t.Errorf("failure_kinds = %v", out.FailureKinds)
	}
	if out.EventsByType["task-pool"] != 1 || out.EventsByType["adaptive-performance"] != 2 ||
		out.EventsByType["browser-failure"] != 3 || out.EventsByType["blocked"] != 2 {
		t.Errorf("events_by_type = %v", out.EventsByType)
	}
	if len(out.FailureClasses) != 2 {
		t.Errorf("failure_classes = %v", out.FailureClasses)
	}

	// Concurrency reconstruction.
	conc := record.Concurrency
	if conc.Source != "worker-events" {
		t.Errorf("concurrency source = %q", conc.Source)
	}
	if conc.Desired != 8 || conc.PlannedWorkers != 4 || conc.PerTaskConcurrency != 2 || conc.PlannedEffective != 8 {
		t.Errorf("planned concurrency = %+v", conc)
	}
	if conc.FinalEffective != 2 {
		t.Errorf("final_effective = %d", conc.FinalEffective)
	}
	if conc.AdaptiveReductions != 2 {
		t.Errorf("adaptive_reductions = %d", conc.AdaptiveReductions)
	}
	if conc.EffectiveWorkers != 2 {
		t.Errorf("effective_workers = %d", conc.EffectiveWorkers)
	}

	// Recovery.
	if !record.Recovery.CheckpointPresent || record.Recovery.CheckpointTaskKey != "task-40" {
		t.Errorf("recovery checkpoint = %+v", record.Recovery)
	}
	if record.Recovery.RecoveryRequired || record.Recovery.TasksRemainingAtEnd != 0 {
		t.Errorf("recovery state = %+v", record.Recovery)
	}

	// App-reported resources.
	if record.Resources.PeakActiveBrowser != 4 || record.Resources.PeakActivePages != 8 {
		t.Errorf("resources peaks = %+v", record.Resources)
	}
	approx(t, "cpu_percent", record.Resources.CPUPercent, 73.5)
	if record.Resources.SampleCount < 1 {
		t.Errorf("resources sample_count = %d", record.Resources.SampleCount)
	}

	// Availability.
	avail := record.Availability
	if !avail.Progress || !avail.Benchmark || !avail.Coverage || !avail.Logs || !avail.Events || !avail.Results || !avail.Metrics {
		t.Errorf("availability = %+v", avail)
	}
}

func TestRunExperimentSendsSecondsMaxTime(t *testing.T) {
	fake := newFakeServer(t, defaultScenario())
	client := newClientFor(t, fake)

	if _, err := runFast(client, testConfig()); err != nil {
		t.Fatalf("runFast: %v", err)
	}

	fake.mu.Lock()
	body := fake.lastJobBody
	fake.mu.Unlock()

	if body["Name"] != "acceptance-test" {
		t.Errorf("Name = %v", body["Name"])
	}
	if body["max_time"] != float64(3600) {
		t.Errorf("max_time = %v, want 3600 (seconds)", body["max_time"])
	}
	if body["concurrency"] != float64(8) {
		t.Errorf("concurrency = %v", body["concurrency"])
	}
	if body["fast_mode"] != false {
		t.Errorf("fast_mode = %v", body["fast_mode"])
	}
}

func TestRunExperimentPollsUntilTerminal(t *testing.T) {
	sc := defaultScenario()
	sc.progressStates = []string{"running", "running", "completed"}
	fake := newFakeServer(t, sc)
	client := newClientFor(t, fake)

	record, err := runFast(client, testConfig())
	if err != nil {
		t.Fatalf("runFast: %v", err)
	}
	if record.Run.TerminalState != "completed" {
		t.Errorf("terminal = %q", record.Run.TerminalState)
	}
	if record.Run.PollCount != 3 {
		t.Errorf("poll_count = %d, want 3", record.Run.PollCount)
	}
	if record.Run.TimedOut {
		t.Errorf("timed_out should be false")
	}
}

func TestRunExperimentBenchmarkUnavailableFallsBack(t *testing.T) {
	sc := defaultScenario()
	sc.benchmarkUnavailable = true
	fake := newFakeServer(t, sc)
	client := newClientFor(t, fake)

	record, err := runFast(client, testConfig())
	if err != nil {
		t.Fatalf("runFast: %v", err)
	}
	if record.Availability.Benchmark {
		t.Errorf("benchmark should be unavailable")
	}
	// With no benchmark, discovered rows fall back to the file-backed summary
	// and unique to the progress stats; failure kinds still come from events.
	if record.Outcomes.DiscoveredRows != 120 {
		t.Errorf("discovered_rows fallback = %d, want 120", record.Outcomes.DiscoveredRows)
	}
	if record.Outcomes.UniqueBusinesses != 100 {
		t.Errorf("unique fallback = %d", record.Outcomes.UniqueBusinesses)
	}
	if record.Outcomes.FailureKinds["browser-failure"] != 3 {
		t.Errorf("failure kinds from events = %v", record.Outcomes.FailureKinds)
	}
	// Wall time falls back to harness wall clock, which is tiny but present.
	if record.Run.WallSeconds < 0 {
		t.Errorf("wall seconds = %v", record.Run.WallSeconds)
	}
}

func TestRunExperimentEventsUnavailableUsesBenchmarkFailures(t *testing.T) {
	sc := defaultScenario()
	sc.eventsUnavailable = true
	fake := newFakeServer(t, sc)
	client := newClientFor(t, fake)

	record, err := runFast(client, testConfig())
	if err != nil {
		t.Fatalf("runFast: %v", err)
	}
	if record.Availability.Events {
		t.Errorf("events should be unavailable")
	}
	// Failure kinds are derived from the benchmark coarse classes instead.
	if record.Outcomes.FailureKinds["browser-failure"] != 4 || record.Outcomes.FailureKinds["blocked"] != 2 {
		t.Errorf("failure kinds from benchmark = %v", record.Outcomes.FailureKinds)
	}
	// Effective concurrency falls back to the plain-text log messages.
	if record.Concurrency.Source != "log-messages" {
		t.Errorf("concurrency source = %q, want log-messages", record.Concurrency.Source)
	}
	if record.Concurrency.PlannedEffective != 8 || record.Concurrency.FinalEffective != 2 {
		t.Errorf("log-derived concurrency = %+v", record.Concurrency)
	}
	if record.Concurrency.AdaptiveReductions != 2 {
		t.Errorf("log-derived reductions = %d", record.Concurrency.AdaptiveReductions)
	}
}

func TestRunExperimentCreateJobErrorReturnsRecord(t *testing.T) {
	sc := defaultScenario()
	fake := newFakeServer(t, sc)
	// Break the create endpoint by pointing the client at a closed server path:
	// use a client whose base URL is valid but the server rejects create.
	client := newClientFor(t, fake)
	// Overwrite the create handler expectation by closing the server; a fresh
	// request then fails at the transport level.
	fake.Server.Close()

	record, err := RunExperiment(context.Background(), client, testConfig(), RunOptions{
		PollInterval: time.Millisecond, MaxWait: time.Second,
		sleep: func(context.Context, time.Duration) {},
	})
	if err == nil {
		t.Fatalf("expected create error")
	}
	if record.Run.Error == "" {
		t.Errorf("record should carry the error")
	}
	if record.Run.JobID != "" {
		t.Errorf("job id should be empty on create failure")
	}
}

func TestRunExperimentInvalidConfigFailsBeforeQueue(t *testing.T) {
	fake := newFakeServer(t, defaultScenario())
	client := newClientFor(t, fake)

	bad := testConfig()
	bad.Job.Keywords = nil

	record, err := RunExperiment(context.Background(), client, bad, RunOptions{
		sleep: func(context.Context, time.Duration) {},
	})
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if fake.createCalls.Load() != 0 {
		t.Errorf("no job should be created for an invalid config")
	}
	if record.Run.Error == "" {
		t.Errorf("record should carry the validation error")
	}
}
