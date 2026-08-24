package webrunner

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// oneGibibyte is the unit these tests express memory in.
const oneGibibyte uint64 = 1 << 30

func TestAdaptiveWorkerConcurrencyHonoursTheMemoryCeiling(t *testing.T) {
	t.Parallel()

	// A host with plenty of head-room: only the ceiling can reduce anything.
	roomy := workerResourceSample{
		CPUPercent: 10, MemoryUsedBytes: 6 * oneGibibyte,
		MemoryAvailableBytes: 8 * oneGibibyte, DiskFreeBytes: 64 * oneGibibyte,
	}

	if got := adaptiveWorkerConcurrency(8, roomy, 0, 0); got != 8 {
		t.Fatalf("without a ceiling concurrency = %d, want the desired 8", got)
	}

	if got := adaptiveWorkerConcurrency(8, roomy, 0, 12*oneGibibyte); got != 8 {
		t.Fatalf("below the ceiling concurrency = %d, want the desired 8", got)
	}

	// At the ceiling exactly, and above it, the run is pinned to one worker.
	if got := adaptiveWorkerConcurrency(8, roomy, 0, 6*oneGibibyte); got != 1 {
		t.Fatalf("at the ceiling concurrency = %d, want 1", got)
	}

	if got := adaptiveWorkerConcurrency(8, roomy, 0, 4*oneGibibyte); got != 1 {
		t.Fatalf("above the ceiling concurrency = %d, want 1", got)
	}

	// The ceiling can never raise a budget the other rules already lowered.
	starved := roomy
	starved.MemoryAvailableBytes = oneGibibyte / 2

	if got := adaptiveWorkerConcurrency(8, starved, 0, 64*oneGibibyte); got != 1 {
		t.Fatalf("a slack ceiling raised a pressured budget to %d, want 1", got)
	}
}

func TestAdaptiveBrowserBudgetHonoursTheMemoryCeiling(t *testing.T) {
	t.Parallel()

	roomy := workerResourceSample{
		MemoryUsedBytes: 6 * oneGibibyte, MemoryAvailableBytes: 8 * oneGibibyte,
	}

	pool, pages := adaptiveBrowserBudget(4, 3, roomy, 0)
	if pool != 4 || pages != 3 {
		t.Fatalf("without a ceiling budget = (%d, %d), want the configured (4, 3)", pool, pages)
	}

	pool, pages = adaptiveBrowserBudget(4, 3, roomy, 12*oneGibibyte)
	if pool != 4 || pages != 3 {
		t.Fatalf("below the ceiling budget = (%d, %d), want the configured (4, 3)", pool, pages)
	}

	pool, pages = adaptiveBrowserBudget(4, 3, roomy, 6*oneGibibyte)
	if pool != 1 || pages != 1 {
		t.Fatalf("at the ceiling budget = (%d, %d), want (1, 1)", pool, pages)
	}

	// A ceiling applies even when the host reports no available-memory
	// reading at all, which is the case the pressure rules skip.
	unmeasured := workerResourceSample{MemoryUsedBytes: 6 * oneGibibyte}

	pool, pages = adaptiveBrowserBudget(4, 3, unmeasured, 4*oneGibibyte)
	if pool != 1 || pages != 1 {
		t.Fatalf("an unmeasured host above the ceiling = (%d, %d), want (1, 1)", pool, pages)
	}

	// The ceiling never enlarges an engine-default budget.
	pool, pages = adaptiveBrowserBudget(0, 0, roomy, 12*oneGibibyte)
	if pool != 0 || pages != 0 {
		t.Fatalf("a slack ceiling changed the engine defaults to (%d, %d), want (0, 0)", pool, pages)
	}
}

func TestRecoveryIsVetoedWhileTheMemoryCeilingIsExceeded(t *testing.T) {
	t.Parallel()

	healthy := workerResourceSample{
		CPUPercent: 20, MemoryUsedBytes: 6 * oneGibibyte,
		MemoryAvailableBytes: 8 * oneGibibyte, DiskFreeBytes: 64 * oneGibibyte, BrowserProcesses: 4,
	}

	if !recoveryHasHeadroom(healthy, 0, 4, 0) {
		t.Fatal("no ceiling must leave recovery available on a healthy host")
	}

	if !recoveryHasHeadroom(healthy, 0, 4, 12*oneGibibyte) {
		t.Fatal("a ceiling that is not reached must leave recovery available")
	}

	if recoveryHasHeadroom(healthy, 0, 4, 6*oneGibibyte) {
		t.Fatal("a reached ceiling must veto recovery even on an otherwise healthy host")
	}
}

// newCeilingTestRun builds a pool run whose job exists in the durable store,
// so the worker events the adaptation records are actually written and can be
// read back as evidence.
func newCeilingTestRun(t *testing.T, ceiling uint64) (*webrunner, *taskPoolRun, string) {
	t.Helper()

	service, dataFolder := newPoolTestService(t)
	job := gridScrapeJob("44444444-4444-4444-8444-444444444444", 2)
	job.Data.Adaptive = true
	job.Data.MemoryCeilingBytes = ceiling

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	worker := &webrunner{svc: service, sampleResources: healthyResources}
	worker.cfg = &runner.Config{DataFolder: dataFolder, Concurrency: 8}

	run := &taskPoolRun{job: &job, baselineBrowsers: 4, baselinePages: 3}
	run.desiredConcurrency.Store(8)
	run.effectiveConcurrency.Store(8)
	run.failureBudget.Store(8)
	run.blockBudget.Store(8)
	run.browserBudget.Store(4)
	run.pagesBudget.Store(3)
	run.workers.Store(1)

	return worker, run, dataFolder
}

// adaptivePerformanceMessages reads back the adaptive-performance evidence a
// run recorded, newest first.
func adaptivePerformanceMessages(t *testing.T, dataFolder string) []string {
	t.Helper()

	database, err := sql.Open("sqlite", filepath.Join(dataFolder, "jobs.db"))
	if err != nil {
		t.Fatalf("open job database: %v", err)
	}

	t.Cleanup(func() { _ = database.Close() })

	rows, err := database.QueryContext(
		context.Background(),
		`SELECT message FROM job_events WHERE type = 'adaptive-performance' ORDER BY id DESC`,
	)
	if err != nil {
		t.Fatalf("read adaptive events: %v", err)
	}

	defer func() { _ = rows.Close() }()

	messages := make([]string, 0)

	for rows.Next() {
		var message string
		if err := rows.Scan(&message); err != nil {
			t.Fatalf("scan adaptive event: %v", err)
		}

		messages = append(messages, message)
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("iterate adaptive events: %v", err)
	}

	return messages
}

// TestAdaptTaskPoolEnforcesTheMemoryCeiling is the end-to-end proof for the
// specification clause: a run whose sampled memory reaches the operator's
// ceiling drops to the minimum concurrency and browser budget, and says so.
func TestAdaptTaskPoolEnforcesTheMemoryCeiling(t *testing.T) {
	t.Parallel()

	// healthyResources reports 2 GiB in use, so a 1 GiB ceiling is exceeded.
	worker, run, dataFolder := newCeilingTestRun(t, oneGibibyte)

	sample, err := healthyResources(t.Context(), "")
	if err != nil {
		t.Fatalf("sample resources: %v", err)
	}

	worker.adaptTaskPool(run, sample)

	if got := run.effectiveConcurrency.Load(); got != 1 {
		t.Fatalf("effective concurrency = %d, want 1 under a reached ceiling", got)
	}

	if got := run.browserBudget.Load(); got != 1 {
		t.Fatalf("browser budget = %d, want 1 under a reached ceiling", got)
	}

	if got := run.pagesBudget.Load(); got != 1 {
		t.Fatalf("pages budget = %d, want 1 under a reached ceiling", got)
	}

	messages := adaptivePerformanceMessages(t, dataFolder)

	var namedCeiling bool

	for _, message := range messages {
		// The event has to name both the ceiling and the measurement so an
		// operator can tell which limit acted and what tripped it.
		if strings.Contains(message, "ceiling of 1024 MB") && strings.Contains(message, "2048 MB in use") {
			namedCeiling = true
		}
	}

	if !namedCeiling {
		t.Fatalf("no adaptive-performance event named the ceiling and the measurement; got %v", messages)
	}
}

// TestAdaptTaskPoolWithoutACeilingIsUnchanged pins the compatibility clause:
// a zero ceiling reproduces exactly the behaviour that shipped before it.
func TestAdaptTaskPoolWithoutACeilingIsUnchanged(t *testing.T) {
	t.Parallel()

	worker, run, dataFolder := newCeilingTestRun(t, 0)

	sample, err := healthyResources(t.Context(), "")
	if err != nil {
		t.Fatalf("sample resources: %v", err)
	}

	worker.adaptTaskPool(run, sample)

	if got := run.effectiveConcurrency.Load(); got != 8 {
		t.Fatalf("effective concurrency = %d, want the desired 8 on a healthy host", got)
	}

	if got := run.browserBudget.Load(); got != 4 {
		t.Fatalf("browser budget = %d, want the configured 4", got)
	}

	for _, message := range adaptivePerformanceMessages(t, dataFolder) {
		if strings.Contains(message, "ceiling") {
			t.Fatalf("a run without a ceiling recorded a ceiling event: %q", message)
		}
	}
}

// TestMemoryCeilingTransitionIsReportedOncePerCrossing keeps the worker log
// readable: the supervisor samples continuously, so the ceiling must be
// reported when it engages and when it clears, not on every sample.
func TestMemoryCeilingTransitionIsReportedOncePerCrossing(t *testing.T) {
	t.Parallel()

	worker, run, dataFolder := newCeilingTestRun(t, 4*oneGibibyte)

	over := workerResourceSample{
		CPUPercent: 10, MemoryUsedBytes: 6 * oneGibibyte,
		MemoryAvailableBytes: 8 * oneGibibyte, DiskFreeBytes: 32 * oneGibibyte,
	}
	under := over
	under.MemoryUsedBytes = 2 * oneGibibyte

	ceiling := run.job.Data.MemoryCeilingBytes

	// Three consecutive samples over the ceiling are one crossing.
	for range 3 {
		worker.recordMemoryCeilingTransition(run, over, ceiling)
	}

	// Two consecutive samples back under it are one release.
	for range 2 {
		worker.recordMemoryCeilingTransition(run, under, ceiling)
	}

	var reached, released int

	for _, message := range adaptivePerformanceMessages(t, dataFolder) {
		switch {
		case strings.Contains(message, "reached the configured ceiling"):
			reached++
		case strings.Contains(message, "fell back below the configured ceiling"):
			released++
		}
	}

	if reached != 1 || released != 1 {
		t.Fatalf("ceiling events = %d reached / %d released, want exactly 1 of each", reached, released)
	}
}
