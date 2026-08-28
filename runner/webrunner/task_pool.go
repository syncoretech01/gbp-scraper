package webrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/deduper"
	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
	"github.com/gosom/scrapemate"
)

const (
	// defaultTaskWorkers is the parallel task count used when a job does not
	// choose one. It is deliberately small: each worker owns a browser share.
	defaultTaskWorkers = 4
	// taskLeaseDuration bounds how long a task may be held without reporting.
	// A worker that dies loses its lease within this window and the task
	// returns to the queue.
	taskLeaseDuration = 90 * time.Second
	// taskHeartbeatInterval must stay well under the lease so a healthy worker
	// never loses a task it is still running.
	taskHeartbeatInterval = 20 * time.Second
	// idleClaimBackoff spaces out claim attempts once the queue looks empty but
	// other workers still hold leases that may yet fail and requeue work.
	idleClaimBackoff = 250 * time.Millisecond
	// backedOffClaimWait spaces out claim attempts while every remaining
	// pending task is deferred by failure-class backoff. The pool must
	// neither spin nor conclude the plan is drained in that window.
	backedOffClaimWait = 2 * time.Second
	// adaptiveRecoveryCooldown is how long the adaptive controller must wait
	// after any reduction before it may recover a concurrency step. It is the
	// hysteresis that stops the budget oscillating: a cascade collapses the
	// budget fast, but the run then holds low for a settling window before it
	// begins its slow, one-step-per-clean-window climb back. [harden/scheduler-adaptive]
	adaptiveRecoveryCooldown = 30 * time.Second
)

// taskPoolPlan divides one job's resource budget between parallel tasks.
//
// # The concurrency model, end to end
//
// A checkpointed job is a durable plan of tasks (one Maps query, or one grid
// cell of a query). The pool runs that plan with a fixed set of task WORKERS.
// The relationships that decide the real load on Google Maps are:
//
//   - Workers: how many tasks run side by side (taskPoolPlan.Workers). Fixed
//     for the whole run — leases and browser shares stay predictable — and set
//     once here at plan time. The adaptive controller never changes it.
//   - Per-task worker concurrency (PerTaskConcurrency): the scrapemate
//     Concurrency each worker's app runs at = effectiveConcurrency / Workers.
//     This is the number of concurrent Maps page operations one worker drives.
//   - Per-task browser pool (PerTaskBrowserPool): the Playwright browser pool
//     size each worker's app is given. Zero means "let the engine derive it".
//   - Pages per browser (job.Data.PagesBrowser): how many pages share one
//     browser context inside a worker's app.
//   - Adaptive concurrency: between tasks, effectiveConcurrency (and the
//     per-task browser/page budgets) may shrink under measured pressure and
//     recover cautiously. It changes what NEW tasks take; it cannot change the
//     worker count.
//
// The load that reaches the platform is therefore:
//
//	simultaneous Maps operations = Workers * PerTaskConcurrency  (== effectiveConcurrency)
//	simultaneous browsers        = Workers * browsersPerWorker
//
// where each worker runs its OWN scrapemate app and therefore its OWN browser
// pool, and browsersPerWorker is what that app derives from its config —
// PerTaskBrowserPool when set, else ceil(PerTaskConcurrency / PagesBrowser).
// Because a browser pool never rounds below one browser, browsersPerWorker >= 1
// always, so:
//
//	simultaneous browsers >= Workers   (always, for any concurrency budget)
//
// This is the crux of the incident. A default browser-mode grid job resolved to
// effectiveConcurrency 4 and Workers 4 (defaultTaskWorkers), giving
// PerTaskConcurrency 4/4 = 1 — the log line "Running 4 task(s) in parallel with
// 1 worker concurrency each" — and therefore FOUR independent --single-process
// Chromium browsers under Docker. The adaptive controller then lowered
// effectiveConcurrency, but lowering concurrency cannot lower the browser count
// below Workers, so all four browsers stayed alive and the cascade continued.
// Bounding the browser total is only possible by bounding Workers, which is why
// browserWorkerBudget is applied here rather than left to adaptation.
//
// Dividing the budget between workers does NOT divide browsers: eight
// concurrency across one worker is one app with its own coherent pool, while
// eight concurrency across four workers is four apps and at least four browsers.
// Parallel tasks buy resume granularity and latency; in browser mode they also
// multiply browser processes, so the default fan-out is capped for browser mode.
type taskPoolPlan struct {
	Workers            int
	PerTaskConcurrency int
	PerTaskBrowserPool int
	// PerTaskPages is the pages-per-browser each worker's app runs with. It is
	// raised above the job's configured value only when the browser budget
	// clamped the pool, so the concurrency the operator asked for still fits
	// into fewer browsers instead of being cut.
	PerTaskPages int
	// BrowserBudgetTotal echoes the memory-derived browser budget the plan was
	// bounded by (zero in Fast mode), so events can show the arithmetic.
	BrowserBudgetTotal int
	// CappedExplicit is true when a physical cap lowered a value the operator
	// set explicitly (TaskWorkers or BrowserPool); the caller records it.
	CappedExplicit bool
	// FastMode mirrors the job's Fast mode. Fast mode is a pure-HTTP stealth
	// fetcher that launches NO browser, so every browser-denominated number in
	// this plan is zero for it — including the planned browser count, which
	// used to be reported as one-per-worker and told operators a fast job was
	// holding browsers it had never launched. [throughput/auto-capacity]
	FastMode bool
}

// PlannedBrowsers is the number of Chromium processes this plan launches:
// every worker runs its own app whose pool is PerTaskBrowserPool when set,
// else derived by the engine as ceil(concurrency/pages), never below one.
//
// Fast mode launches none. It is exempt from the browser budget precisely
// because it needs no browser, so the arithmetic below — which floors a pool at
// one browser per worker — does not describe it and must not be applied to it.
// liveBrowserFootprint already reported zero for a running fast job; this is
// the same truth at plan time. [throughput/auto-capacity]
func (p taskPoolPlan) PlannedBrowsers() int {
	if p.FastMode {
		return 0
	}

	perWorker := p.PerTaskBrowserPool
	if perWorker <= 0 {
		pages := max(1, p.PerTaskPages)
		perWorker = (p.PerTaskConcurrency + pages - 1) / pages
	}

	return p.Workers * max(1, perWorker)
}

// planTaskPool resolves the worker/concurrency/browser split for one run.
//
// browserWorkerBudget caps the worker count for browser-mode jobs to what the
// host's measured memory can support (see browserModeWorkerBudget); it lowers
// an explicit TaskWorkers too, because the memory ceiling is physical.
// browserBudgetTotal bounds the browser TOTAL (workers x per-worker pool) to
// the memory-derived browser-process budget (see browserProcessBudget). Fast
// mode passes zero for both so its pure-HTTP throughput is never penalised for
// the browser problem. The enforced invariant, tested by the pool tests:
//
//	FastMode || Workers * browsersPerWorker <= max(browserBudgetTotal, 1)
func planTaskPool(job *web.Job, effectiveConcurrency, pendingTasks, browserWorkerBudget, browserBudgetTotal int) taskPoolPlan {
	if effectiveConcurrency < 1 {
		effectiveConcurrency = 1
	}

	cappedExplicit := false

	workers := job.Data.TaskWorkers
	if workers <= 0 {
		workers = defaultTaskWorkers
		if effectiveConcurrency < workers {
			workers = effectiveConcurrency
		}

		// Browser mode: every worker is a separate browser pool that never
		// drops below one browser, so the default fan-out sets the floor on
		// simultaneous browsers. Cap it to what the host can support instead of
		// silently launching defaultTaskWorkers single-process Chromium
		// browsers the way the incident run did. Fast mode passes zero here and
		// keeps the full default fan-out.
		if browserWorkerBudget > 0 && workers > browserWorkerBudget {
			workers = browserWorkerBudget
		}
	} else if browserWorkerBudget > 0 && workers > browserWorkerBudget {
		// An explicitly configured worker count is honoured as the operator's
		// intent, but never past the physical memory ceiling: launching more
		// single-process browsers than RAM can hold is what OOM-killed the
		// incident run regardless of who chose the number. The budget only ever
		// lowers an explicit value, never raises it, and Fast mode passes zero
		// here so its higher fan-out is untouched.
		workers = browserWorkerBudget
		cappedExplicit = true
	}

	// The browser budget bounds the browser TOTAL, so it also bounds workers:
	// each worker holds at least one browser open.
	if browserBudgetTotal > 0 && workers > browserBudgetTotal {
		if job.Data.TaskWorkers > browserBudgetTotal {
			cappedExplicit = true
		}

		workers = browserBudgetTotal
	}

	if workers > web.MaximumJobTaskWorkers {
		workers = web.MaximumJobTaskWorkers
	}

	if pendingTasks > 0 && workers > pendingTasks {
		workers = pendingTasks
	}

	if workers < 1 {
		workers = 1
	}

	plan := taskPoolPlan{
		Workers:            workers,
		PerTaskConcurrency: max(1, effectiveConcurrency/workers),
		PerTaskPages:       job.Data.PagesBrowser,
		BrowserBudgetTotal: browserBudgetTotal,
		CappedExplicit:     cappedExplicit,
		FastMode:           job.Data.FastMode,
	}

	if job.Data.BrowserPool > 0 {
		plan.PerTaskBrowserPool = max(1, job.Data.BrowserPool/workers)
	}

	// Browser mode: bound the browser TOTAL, not just the worker count. Each
	// worker's app derives its pool as ceil(concurrency/pages) when no explicit
	// pool is set, so with defaults the browser total equals the concurrency
	// budget no matter how few workers carry it — the incident class the two
	// live 2x2=4 and 1x4=4 observations proved. Setting PerTaskBrowserPool
	// explicitly is the enforcement seam: the engine honours it verbatim, and
	// fetch workers beyond the pool block on the slot channel instead of
	// launching browsers. When the budget is ample the explicit pool equals
	// what the engine would derive anyway, so healthy hosts are unchanged.
	if browserBudgetTotal > 0 {
		pages := max(1, plan.PerTaskPages)
		engineDerived := (plan.PerTaskConcurrency + pages - 1) / pages

		pool := engineDerived
		if plan.PerTaskBrowserPool > 0 && plan.PerTaskBrowserPool < pool {
			pool = plan.PerTaskBrowserPool
		}

		perWorkerCap := max(1, browserBudgetTotal/workers)
		if pool > perWorkerCap {
			pool = perWorkerCap

			if job.Data.BrowserPool > 0 && job.Data.BrowserPool/workers > perWorkerCap {
				plan.CappedExplicit = true
			}

			// The pool was clamped by memory: raise pages-per-browser so the
			// requested Maps-operation concurrency still fits into the fewer
			// browsers instead of being cut. Pages are soft-capped because the
			// per-page incremental cost is unmeasured; beyond the cap the
			// surplus concurrency queues harmlessly on the engine's slot
			// channel.
			needPages := (plan.PerTaskConcurrency + pool - 1) / pool
			if needPages > pages {
				plan.PerTaskPages = min(needPages, maxCompensationPagesPerBrowser)
			}
		}

		plan.PerTaskBrowserPool = pool
	}

	return plan
}

// maxCompensationPagesPerBrowser bounds how far pages-per-browser is raised to
// compensate for a memory-clamped browser pool. Four keeps the blast radius of
// one crashed --single-process browser at four in-flight operations; the
// per-page incremental memory cost is unmeasured, which is why the surplus
// beyond it queues instead of packing more pages.
const maxCompensationPagesPerBrowser = 4

// taskPoolRun is the shared state of one concurrent checkpoint run.
type taskPoolRun struct {
	job     *web.Job
	outpath string

	// mergeMu serialises CSV merges. Merges are idempotent by place identity,
	// but they rewrite one file, so exactly one may run at a time.
	mergeMu sync.Mutex

	stopMu     sync.Mutex
	stopReason jobruntime.StopReason

	committedWrites      atomic.Int64
	taskFailures         atomic.Int64
	activeTasks          atomic.Int64
	effectiveConcurrency atomic.Int64
	desiredConcurrency   atomic.Int64
	workers              atomic.Int64

	// The failure window feeds adaptive concurrency: attempts that failed and
	// succeeded since the last adaptation decide whether the budget shrinks
	// (failure rate rising) or cautiously recovers (a clean window).
	windowFailures  atomic.Int64
	windowSuccesses atomic.Int64
	failureBudget   atomic.Int64

	// windowBlocks counts the attempts in the current window that failed
	// because the platform refused them (rate limit, challenge, or consent
	// interstitial) rather than because of a local fault. Blocks decay the
	// budget faster than ordinary failures and veto every recovery step.
	windowBlocks atomic.Int64
	blockBudget  atomic.Int64

	// lastReductionAt is the wall-clock nanosecond of the most recent
	// concurrency reduction. It gates recovery: no step is taken back until
	// adaptiveRecoveryCooldown has elapsed since the last decrease, which is
	// the hysteresis that keeps the budget from oscillating around a cascade.
	lastReductionAt atomic.Int64

	// workerTarget is how many task workers auto capacity wants running right
	// now; run.workers is how many are actually alive. The two converge from
	// both ends: the supervisor spawns to close a gap upward, and a worker
	// retires itself between tasks to close one downward.
	//
	// The worker count used to be frozen for the whole run, which is why a
	// healthy run could never take back the parallelism a cascade had cost it,
	// and why lowering concurrency could not lower the browser count below the
	// worker floor. [throughput/auto-capacity]
	workerTarget atomic.Int64
	// lastWorkerChangeAt is the wall-clock nanosecond of the last worker-count
	// change. It gates BOTH directions: a new worker needs a whole task to show
	// what it costs, so judging it sooner measures nothing.
	lastWorkerChangeAt atomic.Int64
	// scaleCooldown is how long lastWorkerChangeAt gates for. It is copied from
	// the runner once, at pool start, so a test can watch several scaling
	// decisions without waiting the production settling time for each.
	scaleCooldown time.Duration

	// The worker window accumulates task outcomes ACROSS sampling ticks.
	//
	// The concurrency controller re-decides every resourceSampleInterval (two
	// seconds) and swaps its counters empty each time. A browser-mode task
	// takes tens of seconds, so a two-second window almost never contains the
	// three corroborating successes a growth step requires: a controller
	// reading the per-tick window could react to trouble but could never take
	// capacity back. These counters give growth a window the length of the
	// worker-scale cooldown instead, and are emptied whenever the worker count
	// actually changes. [throughput/auto-capacity]
	workerWindowFailures  atomic.Int64
	workerWindowSuccesses atomic.Int64
	workerWindowBlocks    atomic.Int64
	// workerGroup owns the worker goroutines, so the controller can add one
	// mid-run without racing the pool's own shutdown wait.
	workerGroup *dynamicWorkerGroup
	// spawnWorker starts one more task worker. It closes over the run's
	// contexts, seeds and plan so the supervisor can grow the pool without
	// carrying all of them, and reports false once the pool has drained.
	spawnWorker func() bool
	// nextWorkerIndex numbers spawned workers so their lease owners stay
	// readable in the event log. Uniqueness comes from the owner's UUID, not
	// from this counter.
	nextWorkerIndex atomic.Int64

	// pendingCache and pendingCachedAt memoise how much claimable work is left.
	// The growth rule needs it, but SQLite pressure is itself one of the
	// signals, so it is read at most once per worker-scale cooldown rather than
	// once per two-second sample.
	pendingCache    atomic.Int64
	pendingCachedAt atomic.Int64

	// taskLatency and writeLatency are the throughput signals auto capacity
	// weighs beside the resource ones: how long a whole task takes, and how
	// long its durable finish write (CSV merge plus the ownership-checked
	// completion) takes. Both are compared against the best this run itself
	// achieved, so neither needs a per-machine threshold.
	taskLatency  latencySeries
	writeLatency latencySeries

	// browserBudget and pagesBudget are the per-task browser pool and
	// pages-per-browser values new tasks take. They start at the plan's
	// values and only ever shrink, under measured RAM pressure.
	browserBudget atomic.Int64
	pagesBudget   atomic.Int64
	// baselineBrowsers and baselinePages remember the configured budgets so
	// an adaptation can recover exactly to them and never beyond.
	baselineBrowsers int
	baselinePages    int

	// memoryCeilingActive remembers whether the operator's memory ceiling was
	// exceeded at the previous sample, so the crossing is reported once when
	// it happens and once when it clears rather than on every sample.
	memoryCeilingActive atomic.Bool

	// live carries the between-task reconfiguration state: extendable
	// deadline, switchable proxy plan, and retry-current signalling.
	live *liveRunState

	// coverage is the adaptive discovery engine; nil keeps exactly the
	// historical behaviour.
	coverage *coverageEngine
	// dedup and extraReviews let a worker rebuild the seed of a
	// coverage-expansion task from its durable payload.
	dedup        deduper.Deduper
	extraReviews bool
}

// taskPoolExtras bundles the optional collaborators of one pool run so the
// pool entry point keeps a manageable signature.
type taskPoolExtras struct {
	proxyPlan    *web.ProxyPlan
	proxyHealth  map[string]web.ProxyTaskHealth
	coverage     *coverageEngine
	dedup        deduper.Deduper
	extraReviews bool
}

// requestStop latches the first stop reason. Later reasons are ignored so the
// outcome reflects why the run actually stopped.
func (run *taskPoolRun) requestStop(reason jobruntime.StopReason) bool {
	if reason == jobruntime.StopReasonNone {
		return false
	}

	run.stopMu.Lock()
	defer run.stopMu.Unlock()

	if run.stopReason != jobruntime.StopReasonNone {
		return false
	}

	run.stopReason = reason

	return true
}

func (run *taskPoolRun) currentStop() jobruntime.StopReason {
	run.stopMu.Lock()
	defer run.stopMu.Unlock()

	return run.stopReason
}

// recoveryCooldownElapsed reports whether enough time has passed since the last
// concurrency reduction for a recovery step to be allowed. A run that has never
// reduced is free to recover immediately.
func (run *taskPoolRun) recoveryCooldownElapsed() bool {
	last := run.lastReductionAt.Load()
	if last == 0 {
		return true
	}

	return time.Since(time.Unix(0, last)) >= adaptiveRecoveryCooldown
}

// workerScaleCooldownElapsed reports whether enough time has passed since the
// last worker-count change for another one to be allowed.
//
// A real pool stamps lastWorkerChangeAt when it starts, so a run always holds
// its planned width for one settling window before anything reshapes it. The
// zero case below is therefore only reached by a hand-built run in a test,
// where an immediate decision is what the test is asking for.
func (run *taskPoolRun) workerScaleCooldownElapsed() bool {
	last := run.lastWorkerChangeAt.Load()
	if last == 0 {
		return true
	}

	cooldown := run.scaleCooldown
	if cooldown <= 0 {
		cooldown = autoWorkerScaleCooldown
	}

	return time.Since(time.Unix(0, last)) >= cooldown
}

// resetWorkerWindow empties the accumulated task outcomes. It is called
// whenever the worker count actually changes, so the evidence that justified
// one decision can never justify the next one as well.
func (run *taskPoolRun) resetWorkerWindow() {
	run.workerWindowFailures.Store(0)
	run.workerWindowSuccesses.Store(0)
	run.workerWindowBlocks.Store(0)
}

// retireWorker reports whether the calling worker should stop claiming because
// auto capacity lowered the target below the number of live workers.
//
// It is only ever consulted at the top of the claim loop, where the worker
// holds no lease and owns no task, so shrinking the pool can never abandon
// leased work, lose a checkpoint or leave a task "running" with nobody to
// finish it. The last worker never retires: a run always keeps one.
func (run *taskPoolRun) retireWorker() bool {
	for {
		live := run.workers.Load()
		if live <= 1 {
			return false
		}

		target := max(int64(1), run.workerTarget.Load())
		if live <= target {
			return false
		}

		if run.workers.CompareAndSwap(live, live-1) {
			run.lastWorkerChangeAt.Store(time.Now().UnixNano())

			return true
		}
	}
}

// dynamicWorkerGroup is a WaitGroup that may be added to while it is being
// waited on. The pool needs exactly that: the supervisor spawns a worker in
// response to a measurement taken long after the initial fan-out, and a plain
// sync.WaitGroup panics if an Add races the counter reaching zero.
//
// wait() returns only once no worker is running AND no further worker can be
// spawned, so a late spawn can never resurrect a pool the run has finished
// with, and an in-flight spawn can never be missed.
type dynamicWorkerGroup struct {
	mu      sync.Mutex
	idle    *sync.Cond
	running int
	closed  bool
}

func newDynamicWorkerGroup() *dynamicWorkerGroup {
	group := &dynamicWorkerGroup{}
	group.idle = sync.NewCond(&group.mu)

	return group
}

// spawn starts one worker goroutine and reports whether it was started. It
// fails only once the group is closed, which happens exactly when wait() has
// observed an idle pool.
func (group *dynamicWorkerGroup) spawn(work func()) bool {
	group.mu.Lock()

	if group.closed {
		group.mu.Unlock()

		return false
	}

	group.running++
	group.mu.Unlock()

	go func() {
		defer group.finish()
		work()
	}()

	return true
}

func (group *dynamicWorkerGroup) finish() {
	group.mu.Lock()
	defer group.mu.Unlock()

	group.running--

	if group.running == 0 {
		group.idle.Broadcast()
	}
}

// wait blocks until every worker has returned, then closes the group so no
// later spawn can start one again.
func (group *dynamicWorkerGroup) wait() {
	group.mu.Lock()
	defer group.mu.Unlock()

	for group.running > 0 {
		group.idle.Wait()
	}

	group.closed = true
}

// mergeTaskOutput folds one finished task's rows into the job CSV under the
// merge lock and reports the resulting checkpoint.
func (run *taskPoolRun) mergeTaskOutput(runPath string, diskFree uint64) (web.JobTaskCheckpoint, error) {
	// Timed from BEFORE the lock, so the measurement includes the queueing
	// delay every extra worker adds. That is the write-pressure signal auto
	// capacity needs: a merge that is fast but waited a long time for its turn
	// is exactly the contention another worker would make worse.
	startedAt := time.Now()

	run.mergeMu.Lock()
	defer run.mergeMu.Unlock()

	summary, err := mergeResultCSV(context.Background(), run.outpath, runPath)
	if err != nil {
		return web.JobTaskCheckpoint{}, err
	}

	run.committedWrites.Add(1)
	run.writeLatency.observe(time.Since(startedAt))

	checkpoint := web.JobTaskCheckpoint{
		RowsAdded:         summary.RunAdded,
		RowsReplaced:      summary.ExistingReplaced,
		DuplicatesSkipped: summary.DuplicatesSkipped,
		DiskFreeBytes:     diskFree,
	}

	// Additive coverage evidence: whether this query's own result set hit
	// the cap its depth allows. A run without a coverage engine writes the
	// historical payload unchanged.
	markCoverageTruncation(run.coverage, &checkpoint, run.job.Data.Depth)

	return checkpoint, nil
}

// runTaskPool executes the job's pending plan with a bounded set of workers.
// It returns the reason the run stopped.
func (w *webrunner) runTaskPool(
	ctx context.Context,
	runCtx context.Context,
	runCancel context.CancelFunc,
	job *web.Job,
	outpath string,
	seedsByKey map[string]scrapemate.IJob,
	exitMonitor exiter.Exiter,
	stopReasons <-chan jobruntime.StopReason,
	plan taskPoolPlan,
	desiredConcurrency int,
	startedAt time.Time,
	deadline time.Time,
	extras taskPoolExtras,
) jobruntime.StopReason {
	run := &taskPoolRun{
		job:              job,
		outpath:          outpath,
		live:             newLiveRunState(deadline),
		coverage:         extras.coverage,
		dedup:            extras.dedup,
		extraReviews:     extras.extraReviews,
		baselineBrowsers: plan.PerTaskBrowserPool,
		baselinePages:    plan.PerTaskPages,
		workerGroup:      newDynamicWorkerGroup(),
		scaleCooldown:    w.scaleCooldown(),
	}
	run.desiredConcurrency.Store(int64(desiredConcurrency))
	run.effectiveConcurrency.Store(int64(plan.PerTaskConcurrency * plan.Workers))
	run.workers.Store(int64(plan.Workers))
	run.workerTarget.Store(int64(plan.Workers))
	run.nextWorkerIndex.Store(int64(plan.Workers))
	// The scale cooldown starts at the pool, not at the first change. A run
	// that has just launched has no evidence about itself yet, and a zero here
	// would make every early tick count as "cooldown elapsed" — which would
	// empty the accumulating worker window on every tick and throw away the
	// very corroboration growth needs. It also keeps the pool at its planned
	// width for one settling window before anything reshapes it.
	run.lastWorkerChangeAt.Store(time.Now().UnixNano())
	run.spawnWorker = func() bool {
		index := int(run.nextWorkerIndex.Add(1))

		return w.spawnTaskWorker(ctx, runCtx, runCancel, run, index, seedsByKey, exitMonitor, plan)
	}
	run.failureBudget.Store(int64(desiredConcurrency))
	run.blockBudget.Store(int64(desiredConcurrency))
	run.browserBudget.Store(int64(plan.PerTaskBrowserPool))
	run.pagesBudget.Store(int64(plan.PerTaskPages))

	if extras.proxyPlan != nil {
		run.live.setProxyPlan(extras.proxyPlan, false)
		run.live.applyProxyHealth(extras.proxyHealth)
	}

	supervisorDone := make(chan struct{})

	go func() {
		defer close(supervisorDone)
		w.superviseTaskPool(runCtx, run, exitMonitor, startedAt, runCancel)
	}()

	// A stop request from the lifecycle controls arrives on this channel; it
	// must cancel every worker, not just the one that happened to read it.
	stopWatchDone := make(chan struct{})

	go func() {
		defer close(stopWatchDone)

		for {
			select {
			case <-runCtx.Done():
				return
			case reason, ok := <-stopReasons:
				if !ok {
					return
				}

				if run.requestStop(reason) {
					runCancel()

					return
				}
			}
		}
	}()

	started := 0

	for index := range plan.Workers {
		if !w.spawnTaskWorker(ctx, runCtx, runCancel, run, index, seedsByKey, exitMonitor, plan) {
			break
		}

		started++
	}

	// The live worker count is what actually started, not what was planned. A
	// refused spawn is impossible on a fresh group, but an over-count here
	// would make the retire gate believe there is a surplus worker to shed and
	// silently narrow the pool below its own target.
	if started > 0 && started != plan.Workers {
		run.workers.Store(int64(started))
		run.workerTarget.Store(int64(started))
	}

	run.workerGroup.wait()
	flushListingKeys(context.Background(), run.dedup)
	runCancel()
	<-stopWatchDone
	<-supervisorDone

	return run.currentStop()
}

// spawnTaskWorker starts one task worker on the run's dynamic group and reports
// whether it started. Every worker gets a fresh, unique lease owner: an owner
// is the identity a claim is written under and a finish is verified against, so
// a worker added mid-run must never be able to inherit a retired worker's
// identity. [throughput/auto-capacity]
func (w *webrunner) spawnTaskWorker(
	ctx context.Context,
	runCtx context.Context,
	runCancel context.CancelFunc,
	run *taskPoolRun,
	index int,
	seedsByKey map[string]scrapemate.IJob,
	exitMonitor exiter.Exiter,
	plan taskPoolPlan,
) bool {
	owner := fmt.Sprintf("%s/%d/%s", run.job.ID, index, uuid.NewString()[:8])

	return run.workerGroup.spawn(func() {
		w.runTaskWorker(ctx, runCtx, runCancel, run, owner, seedsByKey, exitMonitor, plan)
	})
}

// runTaskWorker claims and executes tasks until the plan is drained, the run is
// stopped, or auto capacity retires this worker.
func (w *webrunner) runTaskWorker(
	ctx context.Context,
	runCtx context.Context,
	runCancel context.CancelFunc,
	run *taskPoolRun,
	owner string,
	seedsByKey map[string]scrapemate.IJob,
	exitMonitor exiter.Exiter,
	plan taskPoolPlan,
) {
	job := run.job

	for {
		if run.currentStop() != jobruntime.StopReasonNone {
			return
		}

		if runCtx.Err() != nil {
			run.requestStop(stoppedBecauseContext(ctx, runCtx.Err()))

			return
		}

		// Auto capacity may have lowered the worker target since the last
		// task. Retiring here — before a claim, holding no lease and owning no
		// task — is the only point at which a worker can leave without
		// stranding work.
		if run.retireWorker() {
			_ = w.svc.RecordJobWorkerEvent(
				context.Background(), job.ID, "task-worker-retired", "information",
				"A parallel task worker stopped to release its share of the browser budget",
				map[string]any{"owner": owner, "task_workers": run.workers.Load()},
			)

			return
		}

		// A claim must survive the run context being cancelled mid-statement,
		// otherwise a task could be marked running with nobody to finish it.
		task, found, claimErr := w.svc.ClaimNextJobTask(
			context.WithoutCancel(runCtx), job.ID, owner, taskLeaseDuration,
		)
		if claimErr != nil {
			run.requestStop(jobruntime.StopReasonTaskFailures)
			_ = w.svc.RecordJobWorkerEvent(
				context.Background(), job.ID, "task-claim-failed", "error",
				"Could not lease the next task from the durable plan",
				map[string]any{"error": jobruntime.RedactString(claimErr.Error())},
			)
			runCancel()

			return
		}

		if !found {
			// Other workers may still fail and requeue their tasks, so idle
			// once before concluding the plan is drained.
			if run.activeTasks.Load() == 0 {
				// Pending tasks may all be deferred by failure-class
				// backoff. The plan is not drained then: wait until the
				// earliest of them becomes claimable again.
				snapshot, snapshotErr := w.svc.GetJobExecution(context.WithoutCancel(runCtx), job.ID)
				if snapshotErr != nil || snapshot.Tasks.Pending == 0 {
					return
				}

				select {
				case <-runCtx.Done():
					return
				case <-time.After(backedOffClaimWait):
				}

				continue
			}

			select {
			case <-runCtx.Done():
				return
			case <-time.After(idleClaimBackoff):
			}

			continue
		}

		seed, exists := seedsByKey[task.Key]
		if !exists {
			// Coverage-expansion tasks were appended after the plan was
			// seeded (possibly in an earlier process); their seeds are
			// rebuilt from the durable payload so they run identically.
			expansionSeed, buildErr := buildExpansionSeed(job, task, run.dedup, exitMonitor, run.extraReviews)
			if buildErr == nil && expansionSeed != nil {
				seed = expansionSeed
			} else {
				if buildErr == nil {
					buildErr = fmt.Errorf("checkpoint task %q has no current seed", task.Key)
				}

				_ = w.svc.FailJobTaskAs(
					context.Background(), job.ID, task.Key, owner, buildErr, false,
					web.JobTaskCheckpoint{},
				)
				run.taskFailures.Add(1)

				continue
			}
		}

		if !w.executeLeasedTask(ctx, runCtx, runCancel, run, owner, task, seed, exitMonitor, plan) {
			return
		}
	}
}

// executeLeasedTask runs one leased task to a durable conclusion. It reports
// whether the worker should keep claiming work.
func (w *webrunner) executeLeasedTask(
	ctx context.Context,
	runCtx context.Context,
	runCancel context.CancelFunc,
	run *taskPoolRun,
	owner string,
	task web.JobTask,
	seed scrapemate.IJob,
	exitMonitor exiter.Exiter,
	plan taskPoolPlan,
) bool {
	job := run.job

	sample, sampleErr := w.sampleWorkerResources(runCtx)
	if sampleErr == nil && job.Data.LowDiskBytes > 0 && sample.DiskFreeBytes < job.Data.LowDiskBytes {
		// Give the task straight back: a low-disk pause is not a task failure
		// and must not consume one of its attempts.
		_ = w.svc.ReleaseJobTask(
			context.Background(), job.ID, task.Key, owner,
			fmt.Sprintf("available disk %d bytes is below safety threshold %d",
				sample.DiskFreeBytes, job.Data.LowDiskBytes),
		)

		if run.requestStop(jobruntime.StopReasonLowDisk) {
			_ = w.svc.RecordJobWorkerEvent(
				context.Background(), job.ID, "low-disk", "warning",
				"Paused before starting the next task because free disk is below the configured safety threshold",
				map[string]any{
					"disk_free_bytes": sample.DiskFreeBytes,
					"threshold_bytes": job.Data.LowDiskBytes,
				},
			)
			runCancel()
		}

		return false
	}

	// Proxy assignment happens per task so sticky pools pin a query or cell to
	// one exit and caps are enforceable. An exhausted pool pauses the job
	// rather than burning attempts against dead proxies.
	assignment, assignErr := run.live.assignTaskProxies(task)
	if assignErr != nil {
		_ = w.svc.ReleaseJobTask(
			context.Background(), job.ID, task.Key, owner,
			"every proxy in the pool is failed or at its task cap",
		)

		if run.requestStop(jobruntime.StopReasonProxiesUnavailable) {
			_ = w.svc.RecordJobWorkerEvent(
				context.Background(), job.ID, "proxy-failure", "warning",
				"Pausing: every proxy in the pool has failed or reached its task cap",
				map[string]any{"task_key": task.Key},
			)
			runCancel()
		}

		return false
	}

	run.activeTasks.Add(1)
	defer run.activeTasks.Add(-1)

	taskJob := *job
	taskJob.Data = job.Data
	// The live budget can shrink or recover between tasks; each new task takes
	// its share of the budget as it stands, which is the safe reconfiguration
	// point the engine supports.
	//
	// The divisor is the LIVE worker count, not the plan's opening one: auto
	// capacity moves the worker count during the run, and dividing by a stale
	// number would make Workers * PerTaskConcurrency drift away from the
	// effective budget — the one quantity that describes the real load on the
	// platform. [throughput/auto-capacity]
	taskJob.Data.Concurrency = max(1, int(run.effectiveConcurrency.Load())/max(1, int(run.workers.Load())))

	if assignment.override {
		taskJob.Data.Proxies = assignment.proxies
	}

	// Browser and page budgets are taken per task for the same reason as
	// concurrency: a task boundary is the only safe reconfiguration point the
	// engine supports. Both only ever shrink below the configured values.
	if browsers := run.browserBudget.Load(); browsers > 0 {
		taskJob.Data.BrowserPool = int(browsers)
	}

	if pages := run.pagesBudget.Load(); pages > 0 {
		taskJob.Data.PagesBrowser = int(pages)
	}

	heartbeatCtx, stopHeartbeat := context.WithCancel(runCtx)
	leaseLost := make(chan struct{})
	heartbeatDone := make(chan struct{})

	go func() {
		defer close(heartbeatDone)
		w.heartbeatLeasedTask(heartbeatCtx, job.ID, task.Key, owner, leaseLost)
	}()

	taskCtx, cancelTask := context.WithCancel(runCtx)
	run.live.registerTaskCancel(task.Key, cancelTask)

	go func() {
		select {
		case <-leaseLost:
			cancelTask()
		case <-taskCtx.Done():
		}
	}()

	taskStartedAt := time.Now()
	runPath, taskCounters, taskErr := w.runCheckpointTask(taskCtx, &taskJob, seed, exitMonitor)
	taskDuration := time.Since(taskStartedAt)

	run.live.unregisterTaskCancel(task.Key)
	cancelTask()
	stopHeartbeat()
	<-heartbeatDone

	// Truncation and empty cells used to be silent: a task that ended with
	// found places uncommitted, or a seed that walked a cell and found
	// nothing, both looked exactly like a healthy completion. Both are now
	// first-class evidence on the job's event log, whatever the exit status.
	switch {
	case taskCounters.PlacesFound > taskCounters.PlacesCompleted:
		_ = w.svc.RecordJobWorkerEvent(
			context.Background(), job.ID, "task-truncated", "warning",
			fmt.Sprintf("Task ended with %d of %d found places committed",
				taskCounters.PlacesCompleted, taskCounters.PlacesFound),
			map[string]any{
				"task_key":         task.Key,
				"places_found":     taskCounters.PlacesFound,
				"places_completed": taskCounters.PlacesCompleted,
				"seeds_completed":  taskCounters.SeedsCompleted,
			},
		)
	case taskCounters.SeedsCompleted > 0 && taskCounters.PlacesFound == 0:
		_ = w.svc.RecordJobWorkerEvent(
			// A cell whose area holds no matching business is a fact about the
			// area, not a fault. Recorded at warning severity it was 117 of the 118
			// "warnings" job 7100e95b reported after a run that completed 180/180
			// searches and failed none. See web/job_event_severity.go.
			context.Background(), job.ID, "cell-empty", web.JobEventSeverityInformation,
			"Task completed its walk but found zero places; the cell is either genuinely empty or was served an empty page",
			map[string]any{"task_key": task.Key},
		)
	}

	if runPath != "" {
		defer func() { _ = os.Remove(runPath) }()
	}

	// A retry-current request cancels only this task: keep committed rows,
	// give the task back without consuming an attempt, and keep claiming.
	if run.live.consumeRetryFlag(task.Key) &&
		runCtx.Err() == nil && run.currentStop() == jobruntime.StopReasonNone {
		if runPath != "" {
			if _, mergeErr := run.mergeTaskOutput(runPath, sample.DiskFreeBytes); mergeErr != nil {
				_ = w.svc.RecordJobWorkerEvent(
					context.Background(), job.ID, "task-merge-failed", "warning",
					"Could not merge rows from a task requeued by retry-current",
					map[string]any{"task_key": task.Key, "error": jobruntime.RedactString(mergeErr.Error())},
				)
			}
		}

		_ = w.svc.ReleaseJobTask(
			context.Background(), job.ID, task.Key, owner,
			"Requeued by an operator retry-current request",
		)

		return true
	}

	// A cancelled run must leave the task exactly resumable rather than
	// recording an attempt against work that never got a fair chance.
	interrupted := runCtx.Err() != nil || run.currentStop() != jobruntime.StopReasonNone

	if interrupted {
		return w.concludeInterruptedTask(ctx, runCtx, run, owner, task, runPath, sample)
	}

	if leaseWasLost(leaseLost) {
		// Another worker owns this task now. Discard the output rather than
		// merging rows a second owner will also produce.
		return true
	}

	if runPath == "" {
		if taskErr == nil {
			taskErr = errors.New("task produced no output file")
		}

		w.recordProxyTaskOutcome(run, assignment, false, taskDuration, taskErr)
		w.failLeasedTask(run, owner, task, taskErr, web.JobTaskCheckpoint{})
		w.deferFailedTask(run.job.ID, task, classifyTaskFailure(taskErr))

		return true
	}

	// Exact provenance is captured BEFORE the merge: the merge consumes the
	// run file, and the legacy job CSV has no column to carry which query or
	// cell found a row. Missing provenance is never a reason to fail a task.
	observationKeys, _ := runFileIdentityKeys(context.Background(), runPath)

	checkpoint, mergeErr := run.mergeTaskOutput(runPath, sample.DiskFreeBytes)
	if mergeErr == nil && len(observationKeys) > 0 {
		if provenanceErr := w.svc.RecordTaskObservations(
			context.Background(), job.ID, task.Key, task.Query, task.SourceCell, observationKeys,
		); provenanceErr != nil && !errors.Is(provenanceErr, web.ErrObservationProvenanceUnsupported) {
			_ = w.svc.RecordJobWorkerEvent(
				context.Background(), job.ID, "provenance-not-recorded", "warning",
				"The exact query and cell for this task's rows could not be stored; the import falls back to the job's keyword list",
				map[string]any{"task_key": task.Key, "error": jobruntime.RedactString(provenanceErr.Error())},
			)
		}
	}
	if mergeErr != nil {
		mergeErr = fmt.Errorf("merge checkpoint task results: %w", mergeErr)

		w.recordProxyTaskOutcome(run, assignment, false, taskDuration, mergeErr)
		w.failLeasedTask(run, owner, task, mergeErr, checkpoint)
		w.deferFailedTask(run.job.ID, task, classifyTaskFailure(mergeErr))

		return true
	}

	if taskErr == nil {
		w.recordProxyTaskOutcome(run, assignment, true, taskDuration, nil)

		// The task reached a durable boundary: persist the listing identities
		// discovered since the last one so a restart does not re-visit them.
		flushListingKeys(context.Background(), run.dedup)

		var completeErr error

		retryFinishWrite(func() error {
			completeErr = w.svc.CompleteJobTaskAs(context.Background(), job.ID, task.Key, owner, checkpoint)
			if errors.Is(completeErr, web.ErrCheckpointLeaseLost) {
				return nil // not transient; retrying cannot regain a lost lease
			}

			return completeErr
		})

		if errors.Is(completeErr, web.ErrCheckpointLeaseLost) {
			// Another worker owns this task now: our completion is stale and
			// was refused by the ownership guard. Discard it — the new
			// owner's run produces the authoritative result — and do not
			// count it as a success for the adaptive window.
			_ = w.svc.RecordJobWorkerEvent(
				context.Background(), job.ID, "task-lease-lost", "warning",
				"A stale worker tried to complete a task another worker now owns; the stale write was discarded",
				map[string]any{"task_key": task.Key, "owner": owner},
			)

			return true
		}

		// The success only counts once it is durably committed; counting it
		// earlier skews the adaptive window when the commit is refused. The
		// same is true of the latency series: only a task that ran to a durable
		// conclusion says anything about how fast this width of pool is.
		run.windowSuccesses.Add(1)
		run.taskLatency.observe(taskDuration)

		if completeErr != nil {
			_ = w.svc.RecordJobWorkerEvent(
				context.Background(), job.ID, "task-commit-failed", "error",
				"Could not commit a completed task checkpoint",
				map[string]any{
					"task_key": task.Key,
					"error":    jobruntime.RedactString(completeErr.Error()),
				},
			)

			return true
		}

		w.applyCoverage(run, task, checkpoint, exitMonitor)

		return true
	}

	// classification carries both the coarse bucket scheduling acts on and the
	// finer root cause (stream: harden/failure-classification). failureKind is
	// the coarse value, unchanged from classifyTaskFailure, so every scheduling
	// decision below behaves exactly as before; the fine kind and its
	// sub-signal are surfaced on the worker event so an operator sees the real
	// cause instead of a generic "browser-failure".
	classification := classifyFailureKind(taskErr)
	failureKind := classification.Coarse
	if failureKind == "blocked" {
		// Block evidence is measured here, where the attempt error is still
		// available. It feeds the adaptive block rate without any extra
		// request or engine change.
		run.windowBlocks.Add(1)
	}

	// A failed attempt is not evidence about the area: the coverage engine
	// rejects it, so it neither enters the saturation window nor evicts the
	// good evidence already there.
	w.applyCoverageFailure(run, task, checkpoint)

	w.recordProxyTaskOutcome(run, assignment, false, taskDuration, taskErr)
	_ = w.svc.RecordJobWorkerEvent(
		context.Background(), job.ID, failureKind, "warning",
		fmt.Sprintf("Task attempt failed (%s: %s); a retry gets a fresh browser context", failureKind, classification.Fine),
		classification.annotate(map[string]any{
			"task_key": task.Key,
			"error":    jobruntime.RedactString(taskErr.Error()),
		}),
	)

	// A refusal is attributed to the exit it came through: the proxy is not
	// broken, but it is burned for this run, so rotation must move on exactly
	// as it does for a proxy failure.
	if (failureKind == "proxy-failure" || failureKind == "blocked") && assignment.index >= 0 {
		if run.live.markProxyFailed(assignment.index) {
			// The last usable proxy just failed: pause instead of burning the
			// remaining attempts of every task against a dead pool.
			if run.requestStop(jobruntime.StopReasonProxiesUnavailable) {
				_ = w.svc.RecordJobWorkerEvent(
					context.Background(), job.ID, "proxy-failure", "warning",
					"Pausing: the last usable proxy in the pool failed",
					map[string]any{"task_key": task.Key},
				)
				runCancel()
			}
		}
	}

	w.failLeasedTask(run, owner, task, taskErr, checkpoint)
	w.deferFailedTask(job.ID, task, failureKind)

	if job.Data.RetryDelay > 0 {
		select {
		case <-runCtx.Done():
		case <-time.After(job.Data.RetryDelay):
		}
	}

	return true
}

// concludeInterruptedTask commits whatever the interrupted task produced and
// returns the task itself to the queue so a restart resumes it exactly.
func (w *webrunner) concludeInterruptedTask(
	ctx context.Context,
	runCtx context.Context,
	run *taskPoolRun,
	owner string,
	task web.JobTask,
	runPath string,
	sample workerResourceSample,
) bool {
	job := run.job

	if runPath != "" {
		if _, mergeErr := run.mergeTaskOutput(runPath, sample.DiskFreeBytes); mergeErr != nil {
			_ = w.svc.RecordJobWorkerEvent(
				context.Background(), job.ID, "task-merge-failed", "warning",
				"Could not merge rows from an interrupted task",
				map[string]any{
					"task_key": task.Key,
					"error":    jobruntime.RedactString(mergeErr.Error()),
				},
			)
		}
	}

	flushListingKeys(context.Background(), run.dedup)

	reason := run.currentStop()
	if reason == jobruntime.StopReasonNone {
		reason = stoppedBecauseContext(ctx, runCtx.Err())
		run.requestStop(reason)
	}

	// The release is the write that keeps this task resumable; swallowing a
	// transient failure here is what used to strand a phantom "running" row
	// past the end of the run, so it gets a bounded retry before giving up to
	// the reclaim sweeps.
	retryFinishWrite(func() error {
		return w.svc.ReleaseJobTask(
			context.Background(), job.ID, task.Key, owner,
			fmt.Sprintf("Interrupted by %s; the task resumes from its plan entry", reason),
		)
	})

	return false
}

// retryFinishWrite retries a terminal task write a few times with a short
// backoff. Terminal writes run on context.Background — a cancelled run must
// still conclude its rows — so the only failures seen here are transient
// storage errors, and giving up leaves the row to the lease-reclaim sweeps.
func retryFinishWrite(write func() error) {
	const attempts = 3

	for attempt := range attempts {
		if err := write(); err == nil {
			return
		}

		if attempt < attempts-1 {
			time.Sleep(time.Duration(attempt+1) * 250 * time.Millisecond)
		}
	}
}

func (w *webrunner) failLeasedTask(
	run *taskPoolRun,
	owner string,
	task web.JobTask,
	taskErr error,
	checkpoint web.JobTaskCheckpoint,
) {
	run.windowFailures.Add(1)

	retryable := task.Attempts < task.MaxAttempts

	var failErr error

	retryFinishWrite(func() error {
		failErr = w.svc.FailJobTaskAs(
			context.Background(), run.job.ID, task.Key, owner, taskErr, retryable, checkpoint,
		)
		if errors.Is(failErr, web.ErrCheckpointLeaseLost) {
			return nil // not transient; retrying cannot regain a lost lease
		}

		return failErr
	})

	if errors.Is(failErr, web.ErrCheckpointLeaseLost) {
		// Another worker owns this task now; its attempt governs. Recording
		// our stale failure would corrupt the new owner's state, so it is
		// discarded — visibly.
		_ = w.svc.RecordJobWorkerEvent(
			context.Background(), run.job.ID, "task-lease-lost", "warning",
			"A stale worker tried to fail a task another worker now owns; the stale write was discarded",
			map[string]any{"task_key": task.Key, "owner": owner},
		)

		return
	}

	if failErr != nil {
		_ = w.svc.RecordJobWorkerEvent(
			context.Background(), run.job.ID, "task-commit-failed", "error",
			"Could not commit a failed task checkpoint",
			map[string]any{
				"task_key": task.Key,
				"error":    jobruntime.RedactString(failErr.Error()),
			},
		)
	}

	if !retryable {
		run.taskFailures.Add(1)
	}
}

// deferFailedTask applies failure-class backoff to a task that returned to
// pending, so retries do not burn attempts in a tight loop.
func (w *webrunner) deferFailedTask(jobID string, task web.JobTask, failureKind string) {
	if task.Attempts >= task.MaxAttempts {
		return
	}

	wait := taskFailureBackoff(failureKind, task.Attempts)
	if wait <= 0 {
		return
	}

	_ = w.svc.DeferJobTask(context.Background(), jobID, task.Key, time.Now().UTC().Add(wait))
}

// recordProxyTaskOutcome attributes one finished task attempt to the proxy it
// ran through: the in-memory health used for assignment ordering and the
// durable proxy_task_stats aggregate.
func (w *webrunner) recordProxyTaskOutcome(
	run *taskPoolRun,
	assignment taskProxyAssignment,
	success bool,
	duration time.Duration,
	taskErr error,
) {
	if assignment.statsIndex < 0 || assignment.statsProxyID == "" {
		return
	}

	run.live.recordProxyOutcome(assignment.statsIndex, success)

	message := ""
	if taskErr != nil {
		message = jobruntime.RedactString(taskErr.Error())
	}

	_ = w.svc.UpsertProxyTaskStat(context.Background(), web.ProxyTaskStatInput{
		ProxyID:         assignment.statsProxyID,
		PoolID:          run.live.currentPoolID(),
		Success:         success,
		DurationSeconds: duration.Seconds(),
		LastError:       message,
	})
}

// heartbeatLeasedTask keeps one lease alive and signals when it is lost.
func (w *webrunner) heartbeatLeasedTask(
	ctx context.Context,
	jobID, taskKey, owner string,
	leaseLost chan<- struct{},
) {
	ticker := time.NewTicker(taskHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		err := w.svc.HeartbeatJobTask(
			context.WithoutCancel(ctx), jobID, taskKey, owner, taskLeaseDuration,
		)
		if err == nil {
			continue
		}

		if errors.Is(err, web.ErrCheckpointLeaseLost) {
			_ = w.svc.RecordJobWorkerEvent(
				context.Background(), jobID, "task-lease-lost", "warning",
				"A task lease was reclaimed while the worker was still running it",
				map[string]any{"task_key": taskKey},
			)
			close(leaseLost)

			return
		}
	}
}

func leaseWasLost(leaseLost <-chan struct{}) bool {
	select {
	case <-leaseLost:
		return true
	default:
		return false
	}
}

// liveBrowserFootprint reports what the run is holding open right now: the
// number of simultaneous Chromium browsers, and the number of simultaneous Maps
// page operations.
//
// Each task worker runs its own scrapemate app and therefore its own browser
// pool, so the browser total is workers x browsers-per-worker. The pool size
// configured on the parent job is per-app and usually zero, so reporting it
// made a four-browser run look like a one-browser run to the operator, and made
// Fast mode — which launches no browser at all — claim one. The operator uses
// this number to judge memory risk, so it has to be the real one.
func (r *taskPoolRun) liveBrowserFootprint() (browsers, pages int64) {
	// Fast mode is a pure-HTTP stealth fetcher: no browser, no page.
	if r.job.Data.FastMode {
		return 0, 0
	}

	workers := max(int64(1), r.workers.Load())
	effective := max(int64(1), r.effectiveConcurrency.Load())

	perWorker := r.browserBudget.Load()
	if perWorker <= 0 {
		// Unset means the engine derives the pool from that worker's
		// concurrency and pages-per-browser, never rounding below one browser.
		pagesPerBrowser := r.pagesBudget.Load()
		if pagesPerBrowser <= 0 {
			pagesPerBrowser = 1
		}

		perTask := max(int64(1), effective/workers)
		perWorker = (perTask + pagesPerBrowser - 1) / pagesPerBrowser
	}

	return workers * max(int64(1), perWorker), effective
}

// superviseTaskPool owns all live progress reporting for the run. One reporter
// avoids parallel workers overwriting each other's view of the same job.
func (w *webrunner) superviseTaskPool(
	ctx context.Context,
	run *taskPoolRun,
	exitMonitor exiter.Exiter,
	startedAt time.Time,
	runCancel context.CancelFunc,
) {
	job := run.job

	ticker := time.NewTicker(w.samplingInterval())
	defer ticker.Stop()

	// The configurable interval checkpoint complements the one written after
	// every completed task, so a long task still reports how recently the run
	// was making progress.
	checkpointInterval := job.Data.CheckpointInterval()
	lastCheckpoint := time.Now()
	lastReclaim := time.Now()

	for {
		sample, err := w.sampleWorkerResources(ctx)
		if err == nil {
			snapshot := exiter.SnapshotOf(exitMonitor)
			browsers, pages := run.liveBrowserFootprint()

			// DesiredWorkers and EffectiveWorkers are BOTH concurrency
			// (requested and adaptive-effective Maps operations), so the pair
			// the operator sees compares like with like. The task worker count
			// is in the task-pool announcement event; the real browser
			// footprint is BrowserCount.
			_ = w.svc.UpdateJobWorkerProgress(context.Background(), job.ID, web.JobWorkerProgress{
				Stage:            jobruntime.StageSearchingMaps,
				ActiveTasks:      run.activeTasks.Load(),
				PlacesPerMinute:  jobruntime.RatePerMinute(int64(snapshot.PlacesCompleted), time.Since(startedAt)),
				BrowserCount:     browsers,
				ActivePages:      pages,
				CPUPercent:       sample.CPUPercent,
				MemoryBytes:      sample.MemoryUsedBytes,
				DiskFreeBytes:    sample.DiskFreeBytes,
				DatabaseWrites:   run.committedWrites.Load(),
				DesiredWorkers:   run.desiredConcurrency.Load(),
				EffectiveWorkers: max(1, run.effectiveConcurrency.Load()),
				UpdatedAt:        time.Now().UTC(),
			})

			if job.Data.LowDiskBytes > 0 && sample.DiskFreeBytes < job.Data.LowDiskBytes {
				if run.requestStop(jobruntime.StopReasonLowDisk) {
					_ = w.svc.RecordJobWorkerEvent(
						context.Background(), job.ID, "low-disk", "warning",
						"Free disk fell below the configured safety threshold; pausing at the current task checkpoints",
						map[string]any{
							"disk_free_bytes": sample.DiskFreeBytes,
							"threshold_bytes": job.Data.LowDiskBytes,
						},
					)
					runCancel()
				}

				return
			}

			if job.Data.Adaptive {
				w.adaptTaskPool(run, sample)
			}
		}

		if time.Since(lastCheckpoint) >= checkpointInterval {
			lastCheckpoint = time.Now()

			w.recordIntervalCheckpoint(run, checkpointInterval)
		}

		// Reclaim expired leases on the supervisor's own cadence. A healthy
		// worker heartbeats every 20s against a 90s lease, so it can never be
		// reclaimed; a worker that died without a terminal write is what this
		// recovers, bounding a phantom "running" task to lease + cadence. The
		// claim path also reclaims, but only while somebody is still claiming —
		// this tick covers the window when every worker is busy or gone.
		if time.Since(lastReclaim) >= taskLeaseDuration/3 {
			lastReclaim = time.Now()

			if reclaimed, reclaimErr := w.svc.ReclaimExpiredJobTasks(context.Background(), job.ID); reclaimErr == nil && reclaimed > 0 {
				_ = w.svc.RecordJobWorkerEvent(
					context.Background(), job.ID, "task-lease-reclaimed", "warning",
					fmt.Sprintf("%d expired task lease(s) were returned to the queue", reclaimed),
					map[string]any{"reclaimed": reclaimed},
				)
			}
		}

		w.pollLiveControls(run, runCancel)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// recordIntervalCheckpoint flushes the durable deduplication state and appends
// a time-based safe resume boundary describing where the plan stands.
//
// It records where the run is, never what it collected, and a repository
// without durable listing state simply skips it: the per-task checkpoint
// remains the authoritative resume point either way.
func (w *webrunner) recordIntervalCheckpoint(run *taskPoolRun, interval time.Duration) {
	ctx := context.Background()

	flushListingKeys(ctx, run.dedup)

	if !w.svc.SupportsListingState() {
		return
	}

	execution, err := w.svc.GetJobExecution(ctx, run.job.ID)
	if err != nil {
		return
	}

	listingKeys, _ := w.svc.CountJobListingKeys(ctx, run.job.ID)

	_ = w.svc.RecordJobIntervalCheckpoint(ctx, run.job.ID, web.JobIntervalCheckpoint{
		Reason:           "interval",
		IntervalSeconds:  int(interval / time.Second),
		TasksCompleted:   execution.Tasks.Completed,
		TasksRunning:     execution.Tasks.Running,
		TasksPending:     execution.Tasks.Pending,
		TasksFailed:      execution.Tasks.Failed,
		ListingKeys:      listingKeys,
		CommittedMerges:  run.committedWrites.Load(),
		EffectiveWorkers: run.effectiveConcurrency.Load(),
	})
}

// adaptTaskPool records an adaptive change to the shared concurrency budget.
// The pool size itself stays fixed for the run so leases and browser shares
// remain predictable; what changes is the effective capacity new tasks take.
//
// Three independent signals cap the budget: resource pressure (CPU, RAM, free
// disk, and the live browser-process census), the recent task failure rate,
// and the recent block rate. The failure budget halves when at least half of a
// meaningful window failed; the block budget halves as soon as one attempt was
// refused by the platform. Recovery is one step at a time and only when every
// measured dimension has head-room, so recovery is always slower than decay.
func (w *webrunner) adaptTaskPool(run *taskPoolRun, sample workerResourceSample) {
	desired := int(run.desiredConcurrency.Load())

	failures := run.windowFailures.Swap(0)
	successes := run.windowSuccesses.Swap(0)
	blocks := run.windowBlocks.Swap(0)
	attempts := int(failures + successes)

	ceiling := run.job.Data.MemoryCeilingBytes
	w.recordMemoryCeilingTransition(run, sample, ceiling)
	w.adaptBrowserBudget(run, sample)

	allowedBrowsers := int(run.browserBudget.Load()) * int(max(1, run.workers.Load()))
	headroom := recoveryHasHeadroom(sample, int(blocks), allowedBrowsers, ceiling)

	// Recovery is edge-triggered on a window that is clean on EVERY adverse
	// axis, has measured resource head-room, and lies past the cooldown that
	// followed the last reduction. Any failure or block in the window — not
	// only a block — vetoes taking capacity back, so the controller can never
	// increase concurrency while a failure or block cascade is still active.
	cleanWindow := failures == 0 && blocks == 0
	mayRecover := cleanWindow && headroom && run.recoveryCooldownElapsed()

	currentFailureBudget := int(run.failureBudget.Load())
	failureBudget := decideFailureBudget(currentFailureBudget, desired, int(failures), int(successes))

	if failureBudget > currentFailureBudget && !mayRecover {
		failureBudget = currentFailureBudget
	}

	run.failureBudget.Store(int64(failureBudget))

	currentBlockBudget := int(run.blockBudget.Load())
	blockBudget := decideBlockBudget(currentBlockBudget, desired, int(blocks), attempts)

	if blockBudget > currentBlockBudget && !mayRecover {
		blockBudget = currentBlockBudget
	}

	run.blockBudget.Store(int64(blockBudget))

	resourceBudget := adaptiveWorkerConcurrency(desired, sample, run.job.Data.LowDiskBytes, ceiling)
	next := int64(min(resourceBudget, failureBudget, blockBudget))

	// Auto capacity decides the WORKER count from the SAME window, before the
	// early return below: the failure/success/block counters have already been
	// swapped out, so a second pass would see an empty window and never act.
	// Workers are what cost browsers and therefore memory, so a controller that
	// could only move concurrency could never undo a browser cascade.
	// [throughput/auto-capacity]
	w.adaptWorkerCount(run, sample, int(failures), int(successes), int(blocks), int(next))

	previous := run.effectiveConcurrency.Load()
	if next == previous {
		return
	}

	// A reduction starts the recovery cooldown, so the run settles at the lower
	// budget before it may climb again. This is the hysteresis that stops the
	// budget oscillating window-to-window around a cascade.
	if next < previous {
		run.lastReductionAt.Store(time.Now().UnixNano())
	}

	run.effectiveConcurrency.Store(next)

	ceilingExceeded := memoryCeilingExceeded(ceiling, sample)
	reason := "resource pressure"

	switch {
	case blocks > 0 || blockBudget < min(resourceBudget, failureBudget):
		reason = "platform block rate"
	case failureBudget < resourceBudget:
		reason = "task failure rate"
	case ceilingExceeded:
		reason = fmt.Sprintf(
			"the configured memory ceiling of %d MB was reached at %d MB in use",
			ceiling>>bytesPerMebibyteShift, sample.MemoryUsedBytes>>bytesPerMebibyteShift,
		)
	case next > previous:
		reason = "recovered after a stable success window with measured head-room"
	}

	_ = w.svc.RecordJobWorkerEvent(
		context.Background(), run.job.ID, "adaptive-performance", "information",
		fmt.Sprintf("Adaptive performance changed the concurrency budget from %d to %d (%s)", previous, next, reason),
		map[string]any{
			"previous_concurrency":   previous,
			"effective_concurrency":  next,
			"desired_concurrency":    desired,
			"failure_budget":         failureBudget,
			"block_budget":           blockBudget,
			"resource_budget":        resourceBudget,
			"window_failures":        failures,
			"window_successes":       successes,
			"window_blocks":          blocks,
			"task_workers":           run.workers.Load(),
			"cpu_percent":            sample.CPUPercent,
			"memory_available_bytes": sample.MemoryAvailableBytes,
			"memory_used_bytes":      sample.MemoryUsedBytes,
			"memory_ceiling_bytes":   ceiling,
			"memory_ceiling_reached": ceilingExceeded,
			"disk_free_bytes":        sample.DiskFreeBytes,
			"browser_processes":      sample.BrowserProcesses,
			"recovery_headroom":      headroom,
		},
	)
}

// adaptBrowserBudget lowers the per-task browser pool and pages-per-browser
// budgets while RAM pressure lasts and restores the configured values when it
// clears. Every change is recorded with the measurement that caused it.
func (w *webrunner) adaptBrowserBudget(run *taskPoolRun, sample workerResourceSample) {
	ceiling := run.job.Data.MemoryCeilingBytes
	pool, pages := adaptiveBrowserBudget(run.baselineBrowsers, run.baselinePages, sample, ceiling)

	previousPool := run.browserBudget.Load()
	previousPages := run.pagesBudget.Load()

	if int64(pool) == previousPool && int64(pages) == previousPages {
		return
	}

	run.browserBudget.Store(int64(pool))
	run.pagesBudget.Store(int64(pages))

	_ = w.svc.RecordJobWorkerEvent(
		context.Background(), run.job.ID, "adaptive-performance", "information",
		fmt.Sprintf(
			"Adaptive performance changed the per-task browser budget from %d browsers/%d pages to %d browsers/%d pages (memory pressure)",
			previousPool, previousPages, pool, pages,
		),
		map[string]any{
			"previous_browser_pool":  previousPool,
			"previous_pages_browser": previousPages,
			"browser_pool":           pool,
			"pages_per_browser":      pages,
			"baseline_browser_pool":  run.baselineBrowsers,
			"baseline_pages_browser": run.baselinePages,
			"memory_available_bytes": sample.MemoryAvailableBytes,
			"memory_used_bytes":      sample.MemoryUsedBytes,
			"memory_ceiling_bytes":   ceiling,
			"memory_ceiling_reached": memoryCeilingExceeded(ceiling, sample),
			"browser_processes":      sample.BrowserProcesses,
		},
	)
}

// recordMemoryCeilingTransition reports each time the operator's memory
// ceiling starts or stops being exceeded.
//
// It is edge-triggered on purpose. The supervisor samples every few seconds,
// so a level-triggered event would fill the worker log with one identical line
// per sample for as long as the pressure lasted. Recording only the two
// transitions gives an operator the ceiling, the measurement that crossed it,
// and the moment it cleared, which is what the event is for.
//
// A job without a ceiling records nothing at all.
func (w *webrunner) recordMemoryCeilingTransition(
	run *taskPoolRun,
	sample workerResourceSample,
	ceiling uint64,
) {
	if ceiling == 0 {
		return
	}

	exceeded := memoryCeilingExceeded(ceiling, sample)
	if exceeded == run.memoryCeilingActive.Swap(exceeded) {
		return
	}

	message := fmt.Sprintf(
		"Memory use fell back below the configured ceiling of %d MB (%d MB in use); budgets may recover",
		ceiling>>bytesPerMebibyteShift, sample.MemoryUsedBytes>>bytesPerMebibyteShift,
	)
	if exceeded {
		message = fmt.Sprintf(
			"Memory use reached the configured ceiling of %d MB (%d MB in use); "+
				"reducing to one worker and one browser with one page",
			ceiling>>bytesPerMebibyteShift, sample.MemoryUsedBytes>>bytesPerMebibyteShift,
		)
	}

	_ = w.svc.RecordJobWorkerEvent(
		context.Background(), run.job.ID, "adaptive-performance", "information", message,
		map[string]any{
			"memory_ceiling_bytes":   ceiling,
			"memory_used_bytes":      sample.MemoryUsedBytes,
			"memory_available_bytes": sample.MemoryAvailableBytes,
			"memory_ceiling_reached": exceeded,
			"browser_processes":      sample.BrowserProcesses,
		},
	)
}

// decideFailureBudget is the pure adaptation rule, kept separate so it can be
// tested exhaustively.
//
//   - A window with at least adaptiveFailureBurst failed attempts that form a
//     majority halves the budget (never below one). Keying the trigger on a
//     small burst of failures rather than a large window means a
//     browser-failure cascade collapses concurrency on the first window it
//     appears, even when task-failure backoff keeps that window small.
//   - A window with at least adaptiveRecoveryAttempts attempts and zero
//     failures recovers one step toward the desired concurrency.
//   - Anything else (quiet or mixed-but-tolerable windows) leaves it unchanged.
//
// Decay is always faster than recovery: a bad window halves, a clean one adds a
// single step.
func decideFailureBudget(current, desired, failures, successes int) int {
	if current < 1 {
		current = 1
	}

	if current > desired {
		current = desired
	}

	attempts := failures + successes

	switch {
	case failures >= adaptiveFailureBurst && failures*2 >= attempts:
		return max(1, current/2)
	case attempts >= adaptiveRecoveryAttempts && failures == 0 && current < desired:
		return current + 1
	default:
		return current
	}
}

// stoppedBecauseContext maps a cancelled run to the reason a caller should see.
func stoppedBecauseContext(parent context.Context, runErr error) jobruntime.StopReason {
	switch {
	case errors.Is(runErr, context.DeadlineExceeded):
		return jobruntime.StopReasonRuntimeLimit
	case parent.Err() != nil:
		return jobruntime.StopReasonShutdown
	case runErr != nil:
		return jobruntime.StopReasonShutdown
	default:
		return jobruntime.StopReasonNone
	}
}

// adaptWorkerCount is the auto-capacity worker controller wiring: it gathers the
// window measurements, asks the pure decision function what the worker count
// should be, and converges the live pool on that answer.
//
// Growing spawns a worker immediately; shrinking only sets the target, because
// a worker may leave only between tasks, where it holds no lease. The two
// directions are deliberately asymmetric in cost as well as in speed.
func (w *webrunner) adaptWorkerCount(
	run *taskPoolRun,
	sample workerResourceSample,
	failures, successes, blocks, effectiveConcurrency int,
) {
	current := int(max(int64(1), run.workers.Load()))
	scaleCooldownElapsed := run.workerScaleCooldownElapsed()

	run.workerWindowFailures.Add(int64(failures))
	run.workerWindowSuccesses.Add(int64(successes))
	run.workerWindowBlocks.Add(int64(blocks))

	// Adverse evidence is acted on the tick it is seen, so a reduction reads
	// THIS tick's counters. Growth needs corroboration over a whole settling
	// window, so once the cooldown has elapsed the decision reads the
	// accumulated counters and empties them, starting a fresh window. The two
	// are identical early in a window and diverge exactly when a task (tens of
	// seconds) outlasts a sampling tick (two) — which is every browser run.
	if scaleCooldownElapsed {
		failures = int(run.workerWindowFailures.Swap(0))
		successes = int(run.workerWindowSuccesses.Swap(0))
		blocks = int(run.workerWindowBlocks.Swap(0))
	}

	// SQLite pressure is one of the signals this controller weighs, so the one
	// query it needs is read only when a growth step is actually possible.
	pending := 0
	if scaleCooldownElapsed {
		pending = w.pendingTasksForScaling(run)
	}

	taskMean, taskBest, taskSamples := run.taskLatency.snapshot()
	writeMean, writeBest, writeSamples := run.writeLatency.snapshot()

	perWorkerBrowsers := max(1, int(run.browserBudget.Load()))
	allowedBrowsers := 0

	if !run.job.Data.FastMode {
		allowedBrowsers = current * perWorkerBrowsers
	}

	ceiling, ceilingReason := w.workerCeilingForRun(run, sample, effectiveConcurrency)

	decision := decideWorkerTarget(current, workerScalingSignals{
		Ceiling:                 ceiling,
		CeilingReason:           ceilingReason,
		Pending:                 pending,
		Failures:                failures,
		Successes:               successes,
		Blocks:                  blocks,
		CPUPercent:              sample.CPUPercent,
		MemoryAvailable:         sample.MemoryAvailableBytes,
		MemoryUsed:              sample.MemoryUsedBytes,
		MemoryCeiling:           run.job.Data.MemoryCeilingBytes,
		BrowserCensus:           sample.BrowserProcesses,
		AllowedBrowsers:         allowedBrowsers,
		TaskMean:                taskMean,
		TaskBest:                taskBest,
		TaskSamples:             taskSamples,
		WriteMean:               writeMean,
		WriteBest:               writeBest,
		WriteSamples:            writeSamples,
		ScaleCooldownElapsed:    scaleCooldownElapsed,
		RecoveryCooldownElapsed: run.recoveryCooldownElapsed(),
	})

	if decision.Workers == current {
		return
	}

	if decision.Workers < current {
		// A reduction takes effect only when the surplus workers reach their
		// next claim boundary, which may be a whole task away. Re-deciding the
		// same target in the meantime is not a new decision, and recording it
		// again would fill the operator's log with a change that has already
		// happened. The target alone shrinks the pool: each surplus worker
		// retires itself at the top of its claim loop, holding no lease.
		if decision.Workers == int(run.workerTarget.Load()) {
			return
		}

		run.workerTarget.Store(int64(decision.Workers))
		run.lastWorkerChangeAt.Store(time.Now().UnixNano())
		run.resetWorkerWindow()
		w.recordWorkerScaling(run, current, decision, sample, pending)

		return
	}

	// The target is raised BEFORE the first spawn. A newly started worker
	// consults the retire gate at the top of its very first claim loop, so with
	// the old target still in place it would see live > target and retire
	// itself the instant it was created — growing the pool by adding a worker
	// that immediately leaves.
	run.workerTarget.Store(int64(decision.Workers))

	started := current

	for started < decision.Workers {
		run.workers.Add(1)

		if run.spawnWorker == nil || !run.spawnWorker() {
			run.workers.Add(-1)

			break
		}

		started++
	}

	// Growth is only ever committed to the target once the workers have
	// actually started. Leaving an unreachable target behind would make every
	// later window look like "already decided" and quietly freeze the pool at a
	// width it never reached.
	run.workerTarget.Store(int64(started))

	if started == current {
		return
	}

	run.lastWorkerChangeAt.Store(time.Now().UnixNano())
	run.resetWorkerWindow()
	w.recordWorkerScaling(run, current, workerScalingDecision{
		Workers: started, Reason: decision.Reason,
	}, sample, pending)
}

// recordWorkerScaling puts the arithmetic behind a worker-count change on the
// job event log. An operator who sees a run change width is entitled to see
// which measurement moved it.
func (w *webrunner) recordWorkerScaling(
	run *taskPoolRun,
	previous int,
	decision workerScalingDecision,
	sample workerResourceSample,
	pending int,
) {
	verb := "increased"
	if decision.Workers < previous {
		verb = "reduced"
	}

	scalingContext := map[string]any{
		"previous_task_workers":  previous,
		"task_workers":           decision.Workers,
		"reason":                 decision.Reason,
		"pending_tasks":          pending,
		"effective_concurrency":  run.effectiveConcurrency.Load(),
		"cpu_percent":            sample.CPUPercent,
		"cpu_cores":              sample.CPUCores,
		"memory_available_bytes": sample.MemoryAvailableBytes,
	}

	// Browser-denominated evidence belongs only to the mode that has browsers.
	// Attaching a browser budget to a Fast-mode decision would explain a
	// concurrency clamp with an arithmetic that had no part in it.
	if !run.job.Data.FastMode {
		scalingContext["per_task_browser_pool"] = run.browserBudget.Load()
		scalingContext["browser_processes"] = sample.BrowserProcesses
		scalingContext["browser_memory_bytes"] = sample.BrowserMemoryBytes
		scalingContext["auto_worker_ceiling"] = autoWorkerCeiling(sample)
		scalingContext["browser_budget_total"] = browserProcessBudget(sample)
	}

	_ = w.svc.RecordJobWorkerEvent(
		context.Background(), run.job.ID, "adaptive-workers", "information",
		fmt.Sprintf("Auto capacity %s parallel tasks from %d to %d (%s)",
			verb, previous, decision.Workers, decision.Reason),
		scalingContext,
	)
}

// workerCeilingForRun is the highest worker count this run may take right now.
//
// It layers the operator intent over the host physics, and the tightest bound
// wins:
//
//   - the effective concurrency budget, because simultaneous Maps operations
//     are Workers * PerTaskConcurrency and PerTaskConcurrency never falls below
//     one. Auto capacity may reshape the operator load budget between workers
//     and per-task concurrency; it may never exceed it.
//   - an explicit TaskWorkers choice, never exceeded.
//   - web.MaximumJobTaskWorkers, the product own bound.
//   - in browser mode, autoWorkerCeiling (measured memory and CPU) and the
//     browser-denominated budget: workers * per-worker pool <=
//     browserProcessBudget. That invariant belongs to the hardening phase and
//     is not negotiable here.
//
// Fast mode launches no browser, so neither browser bound applies to it.
//
// It also reports WHICH bound is tightest, because a clamp has to explain
// itself in the operator's terms: "your concurrency budget" and "this machine's
// memory" are different problems with different fixes, and Fast mode has
// neither browser bound to blame.
func (w *webrunner) workerCeilingForRun(
	run *taskPoolRun, sample workerResourceSample, effectiveConcurrency int,
) (int, string) {
	job := run.job

	ceiling := max(1, effectiveConcurrency)
	reason := "the concurrency budget this run is allowed only covers fewer parallel tasks"

	if job.Data.TaskWorkers > 0 && ceiling > job.Data.TaskWorkers {
		ceiling = job.Data.TaskWorkers
		reason = "the configured parallel-task count"
	}

	if ceiling > web.MaximumJobTaskWorkers {
		ceiling = web.MaximumJobTaskWorkers
		reason = "the maximum parallel tasks one job may run"
	}

	if !job.Data.FastMode {
		if derived := autoWorkerCeiling(sample); ceiling > derived {
			ceiling = derived
			reason = "the measured memory and CPU budget now supports fewer parallel tasks"
		}

		perWorker := max(1, int(run.browserBudget.Load()))
		if browsers := browserProcessBudget(sample) / perWorker; ceiling > browsers {
			ceiling = browsers
			reason = "available memory now holds fewer simultaneous browsers"
		}
	}

	return max(1, ceiling), reason
}

// pendingTasksForScaling reports how much claimable work is left, memoised for
// one worker-scale cooldown. Another worker with nothing to claim costs a
// browser and buys nothing, so the growth rule needs this number - but reading
// it on every two-second sample would add database load to answer a question
// about database load.
func (w *webrunner) pendingTasksForScaling(run *taskPoolRun) int {
	cooldown := run.scaleCooldown
	if cooldown <= 0 {
		cooldown = autoWorkerScaleCooldown
	}

	cachedAt := run.pendingCachedAt.Load()
	if cachedAt != 0 && time.Since(time.Unix(0, cachedAt)) < cooldown {
		return int(run.pendingCache.Load())
	}

	execution, err := w.svc.GetJobExecution(context.Background(), run.job.ID)
	if err != nil {
		// A failed read is not evidence that the plan emptied. Returning zero
		// would silently veto growth for as long as the database is busy —
		// which is exactly when several workers are committing at once, so the
		// read is most likely to fail on a healthy, productive run. The last
		// known value is stale at worst, and the cost of acting on a stale
		// count is one worker that finds nothing to claim and exits.
		return int(run.pendingCache.Load())
	}

	run.pendingCache.Store(execution.Tasks.Pending)
	run.pendingCachedAt.Store(time.Now().UnixNano())

	return int(execution.Tasks.Pending)
}
