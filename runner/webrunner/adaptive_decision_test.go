package webrunner

import (
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/web"
)

// newAdaptiveTestRun builds a pool run at full budget with a real service, so
// the adaptation records its evidence exactly as it does in production.
func newAdaptiveTestRun(t *testing.T, desired, browsers, pages int) (*webrunner, *taskPoolRun) {
	t.Helper()

	service, dataFolder := newPoolTestService(t)
	worker := &webrunner{svc: service, sampleResources: healthyResources}
	worker.cfg = &runner.Config{DataFolder: dataFolder, Concurrency: desired}

	job := &web.Job{ID: "adaptive-job"}
	job.Data.Adaptive = true
	job.Data.PagesBrowser = pages

	run := &taskPoolRun{job: job, baselineBrowsers: browsers, baselinePages: pages}
	run.desiredConcurrency.Store(int64(desired))
	run.effectiveConcurrency.Store(int64(desired))
	run.failureBudget.Store(int64(desired))
	run.blockBudget.Store(int64(desired))
	run.browserBudget.Store(int64(browsers))
	run.pagesBudget.Store(int64(pages))
	run.workers.Store(1)

	return worker, run
}

func TestAdaptTaskPoolHalvesTheBudgetOnAMeasuredBlock(t *testing.T) {
	t.Parallel()

	worker, run := newAdaptiveTestRun(t, 8, 4, 2)

	// One refused attempt in an otherwise successful window.
	run.windowBlocks.Store(1)
	run.windowFailures.Store(1)
	run.windowSuccesses.Store(5)

	sample, err := healthyResources(t.Context(), "")
	if err != nil {
		t.Fatalf("sample resources: %v", err)
	}

	worker.adaptTaskPool(run, sample)

	if got := run.effectiveConcurrency.Load(); got != 4 {
		t.Fatalf("effective concurrency = %d, want the budget halved to 4", got)
	}

	if got := run.blockBudget.Load(); got != 4 {
		t.Fatalf("block budget = %d, want 4", got)
	}
}

func TestAdaptTaskPoolRefusesToRecoverWhileBlocksOrPressureLast(t *testing.T) {
	t.Parallel()

	worker, run := newAdaptiveTestRun(t, 8, 4, 2)
	run.effectiveConcurrency.Store(4)
	run.failureBudget.Store(4)
	run.blockBudget.Store(4)

	// A clean failure window that nevertheless contained a refusal.
	run.windowBlocks.Store(1)
	run.windowSuccesses.Store(6)

	sample, err := healthyResources(t.Context(), "")
	if err != nil {
		t.Fatalf("sample resources: %v", err)
	}

	worker.adaptTaskPool(run, sample)

	if got := run.effectiveConcurrency.Load(); got > 4 {
		t.Fatalf("effective concurrency = %d, want no recovery while a block was measured", got)
	}

	// A clean window with too many live browsers must also hold.
	run.effectiveConcurrency.Store(4)
	run.failureBudget.Store(4)
	run.blockBudget.Store(4)
	run.windowBlocks.Store(0)
	run.windowSuccesses.Store(6)

	crowded := sample
	crowded.BrowserProcesses = int(run.browserBudget.Load()) + browserHeadroomSlack + 5

	worker.adaptTaskPool(run, crowded)

	if got := run.effectiveConcurrency.Load(); got > 4 {
		t.Fatalf("effective concurrency = %d, want no recovery while browsers exceed the plan", got)
	}
}

func TestAdaptTaskPoolRecoversWhenEveryMeasuredDimensionHasHeadroom(t *testing.T) {
	t.Parallel()

	worker, run := newAdaptiveTestRun(t, 8, 4, 2)
	run.effectiveConcurrency.Store(4)
	run.failureBudget.Store(4)
	run.blockBudget.Store(4)
	run.windowSuccesses.Store(6)

	sample, err := healthyResources(t.Context(), "")
	if err != nil {
		t.Fatalf("sample resources: %v", err)
	}

	sample.BrowserProcesses = 4

	worker.adaptTaskPool(run, sample)

	if got := run.effectiveConcurrency.Load(); got != 5 {
		t.Fatalf("effective concurrency = %d, want one recovered step to 5", got)
	}
}

func TestAdaptTaskPoolCollapsesDecisivelyOnABrowserFailureBurst(t *testing.T) {
	t.Parallel()

	worker, run := newAdaptiveTestRun(t, 8, 4, 2)

	// A small burst of browser-failures with no successes, exactly the shape a
	// browser-failure cascade produces once task-failure backoff keeps each
	// adaptation window small. The old rule waited for four attempts and would
	// have left the budget at 8; the burst rule halves it on this one window.
	run.windowFailures.Store(3)
	run.windowSuccesses.Store(0)
	run.windowBlocks.Store(0)

	sample, err := healthyResources(t.Context(), "")
	if err != nil {
		t.Fatalf("sample resources: %v", err)
	}

	worker.adaptTaskPool(run, sample)

	if got := run.effectiveConcurrency.Load(); got != 4 {
		t.Fatalf("effective concurrency = %d, want a decisive halving to 4 on the burst", got)
	}

	if run.lastReductionAt.Load() == 0 {
		t.Fatal("a reduction did not start the recovery cooldown")
	}
}

func TestAdaptTaskPoolNeverIncreasesWhileAFailureIsPresent(t *testing.T) {
	t.Parallel()

	worker, run := newAdaptiveTestRun(t, 8, 4, 2)
	run.effectiveConcurrency.Store(4)
	// The failure budget still sits high because the failure has not yet formed
	// a majority; the block budget is mid-recovery. Without the clean-window
	// veto the block budget would climb and raise concurrency during a window
	// that still contained a failure.
	run.failureBudget.Store(8)
	run.blockBudget.Store(4)
	run.windowFailures.Store(1)
	run.windowSuccesses.Store(5)
	run.windowBlocks.Store(0)

	sample, err := healthyResources(t.Context(), "")
	if err != nil {
		t.Fatalf("sample resources: %v", err)
	}

	worker.adaptTaskPool(run, sample)

	if got := run.effectiveConcurrency.Load(); got != 4 {
		t.Fatalf("effective concurrency = %d, want no increase while a failure was present", got)
	}
}

func TestAdaptTaskPoolHoldsThroughTheRecoveryCooldownThenClimbsOneStep(t *testing.T) {
	t.Parallel()

	worker, run := newAdaptiveTestRun(t, 8, 4, 2)
	run.effectiveConcurrency.Store(4)
	run.failureBudget.Store(4)
	run.blockBudget.Store(4)

	sample, err := healthyResources(t.Context(), "")
	if err != nil {
		t.Fatalf("sample resources: %v", err)
	}
	sample.BrowserProcesses = 4

	cleanWindow := func() {
		run.windowFailures.Store(0)
		run.windowSuccesses.Store(6)
		run.windowBlocks.Store(0)
	}

	// A reduction just happened: even a perfectly clean window with full
	// head-room must hold until the cooldown elapses.
	run.lastReductionAt.Store(time.Now().UnixNano())
	cleanWindow()
	worker.adaptTaskPool(run, sample)

	if got := run.effectiveConcurrency.Load(); got != 4 {
		t.Fatalf("effective concurrency = %d, want the budget held during the cooldown", got)
	}

	// Once the cooldown has passed, the same clean window recovers exactly one
	// step — recovery stays slower than the decisive decay.
	run.lastReductionAt.Store(time.Now().Add(-2 * adaptiveRecoveryCooldown).UnixNano())
	cleanWindow()
	worker.adaptTaskPool(run, sample)

	if got := run.effectiveConcurrency.Load(); got != 5 {
		t.Fatalf("effective concurrency = %d, want one recovered step to 5 after the cooldown", got)
	}
}

func TestAdaptTaskPoolShrinksTheBrowserBudgetUnderMemoryPressure(t *testing.T) {
	t.Parallel()

	worker, run := newAdaptiveTestRun(t, 8, 4, 4)

	sample, err := healthyResources(t.Context(), "")
	if err != nil {
		t.Fatalf("sample resources: %v", err)
	}

	sample.MemoryAvailableBytes = severeMemoryBytes - 1

	worker.adaptTaskPool(run, sample)

	if got := run.browserBudget.Load(); got != 1 {
		t.Fatalf("browser budget = %d, want 1 under severe memory pressure", got)
	}

	if got := run.pagesBudget.Load(); got != 1 {
		t.Fatalf("pages budget = %d, want 1 under severe memory pressure", got)
	}

	// The configured budget returns once the pressure clears.
	sample.MemoryAvailableBytes = 16 << 30
	worker.adaptTaskPool(run, sample)

	if got := run.browserBudget.Load(); got != 4 {
		t.Fatalf("browser budget = %d, want the configured 4 restored", got)
	}

	if got := run.pagesBudget.Load(); got != 4 {
		t.Fatalf("pages budget = %d, want the configured 4 restored", got)
	}
}
