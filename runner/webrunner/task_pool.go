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
}

// planTaskPool resolves the worker/concurrency/browser split for one run.
//
// browserWorkerBudget caps the DEFAULT worker count for browser-mode jobs to
// what the host's measured memory can support (see browserModeWorkerBudget). It
// applies only when the job leaves TaskWorkers unset: an explicit choice is
// preserved exactly, and Fast mode passes zero so its pure-HTTP throughput is
// never penalised for the browser problem.
func planTaskPool(job *web.Job, effectiveConcurrency, pendingTasks, browserWorkerBudget int) taskPoolPlan {
	if effectiveConcurrency < 1 {
		effectiveConcurrency = 1
	}

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
	}

	if job.Data.BrowserPool > 0 {
		plan.PerTaskBrowserPool = max(1, job.Data.BrowserPool/workers)
	}

	return plan
}

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

// mergeTaskOutput folds one finished task's rows into the job CSV under the
// merge lock and reports the resulting checkpoint.
func (run *taskPoolRun) mergeTaskOutput(runPath string, diskFree uint64) (web.JobTaskCheckpoint, error) {
	run.mergeMu.Lock()
	defer run.mergeMu.Unlock()

	summary, err := mergeResultCSV(context.Background(), run.outpath, runPath)
	if err != nil {
		return web.JobTaskCheckpoint{}, err
	}

	run.committedWrites.Add(1)

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
		baselinePages:    job.Data.PagesBrowser,
	}
	run.desiredConcurrency.Store(int64(desiredConcurrency))
	run.effectiveConcurrency.Store(int64(plan.PerTaskConcurrency * plan.Workers))
	run.workers.Store(int64(plan.Workers))
	run.failureBudget.Store(int64(desiredConcurrency))
	run.blockBudget.Store(int64(desiredConcurrency))
	run.browserBudget.Store(int64(plan.PerTaskBrowserPool))
	run.pagesBudget.Store(int64(job.Data.PagesBrowser))

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

	var group sync.WaitGroup

	for index := range plan.Workers {
		owner := fmt.Sprintf("%s/%d/%s", job.ID, index, uuid.NewString()[:8])

		group.Add(1)

		go func() {
			defer group.Done()
			w.runTaskWorker(ctx, runCtx, runCancel, run, owner, seedsByKey, exitMonitor, plan)
		}()
	}

	group.Wait()
	flushListingKeys(context.Background(), run.dedup)
	runCancel()
	<-stopWatchDone
	<-supervisorDone

	return run.currentStop()
}

// runTaskWorker claims and executes tasks until the plan is drained or the run
// is stopped.
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

				_ = w.svc.FailJobTask(
					context.Background(), job.ID, task.Key, buildErr, false,
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
	taskJob.Data.Concurrency = max(1, int(run.effectiveConcurrency.Load())/max(1, plan.Workers))

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
	runPath, taskErr := w.runCheckpointTask(taskCtx, &taskJob, seed, exitMonitor)
	taskDuration := time.Since(taskStartedAt)

	run.live.unregisterTaskCancel(task.Key)
	cancelTask()
	stopHeartbeat()
	<-heartbeatDone

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
		w.failLeasedTask(run, task, taskErr, web.JobTaskCheckpoint{})
		w.deferFailedTask(run.job.ID, task, classifyTaskFailure(taskErr))

		return true
	}

	checkpoint, mergeErr := run.mergeTaskOutput(runPath, sample.DiskFreeBytes)
	if mergeErr != nil {
		mergeErr = fmt.Errorf("merge checkpoint task results: %w", mergeErr)

		w.recordProxyTaskOutcome(run, assignment, false, taskDuration, mergeErr)
		w.failLeasedTask(run, task, mergeErr, checkpoint)
		w.deferFailedTask(run.job.ID, task, classifyTaskFailure(mergeErr))

		return true
	}

	if taskErr == nil {
		run.windowSuccesses.Add(1)
		w.recordProxyTaskOutcome(run, assignment, true, taskDuration, nil)

		// The task reached a durable boundary: persist the listing identities
		// discovered since the last one so a restart does not re-visit them.
		flushListingKeys(context.Background(), run.dedup)

		if completeErr := w.svc.CompleteJobTask(context.Background(), job.ID, task.Key, checkpoint); completeErr != nil {
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

	failureKind := classifyTaskFailure(taskErr)
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
		fmt.Sprintf("Task attempt failed (%s); a retry gets a fresh browser context", failureKind),
		map[string]any{"task_key": task.Key, "error": jobruntime.RedactString(taskErr.Error())},
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

	w.failLeasedTask(run, task, taskErr, checkpoint)
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

	_ = w.svc.ReleaseJobTask(
		context.Background(), job.ID, task.Key, owner,
		fmt.Sprintf("Interrupted by %s; the task resumes from its plan entry", reason),
	)

	return false
}

func (w *webrunner) failLeasedTask(
	run *taskPoolRun,
	task web.JobTask,
	taskErr error,
	checkpoint web.JobTaskCheckpoint,
) {
	run.windowFailures.Add(1)

	retryable := task.Attempts < task.MaxAttempts

	if failErr := w.svc.FailJobTask(
		context.Background(), run.job.ID, task.Key, taskErr, retryable, checkpoint,
	); failErr != nil {
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

	ticker := time.NewTicker(resourceSampleInterval)
	defer ticker.Stop()

	// The configurable interval checkpoint complements the one written after
	// every completed task, so a long task still reports how recently the run
	// was making progress.
	checkpointInterval := job.Data.CheckpointInterval()
	lastCheckpoint := time.Now()

	for {
		sample, err := w.sampleWorkerResources(ctx)
		if err == nil {
			snapshot := exiter.SnapshotOf(exitMonitor)
			workers := run.workers.Load()
			effective := run.effectiveConcurrency.Load()

			_ = w.svc.UpdateJobWorkerProgress(context.Background(), job.ID, web.JobWorkerProgress{
				Stage:            jobruntime.StageSearchingMaps,
				ActiveTasks:      run.activeTasks.Load(),
				PlacesPerMinute:  jobruntime.RatePerMinute(int64(snapshot.PlacesCompleted), time.Since(startedAt)),
				BrowserCount:     max(1, int64(job.Data.BrowserPool)),
				ActivePages:      max(1, effective),
				CPUPercent:       sample.CPUPercent,
				MemoryBytes:      sample.MemoryUsedBytes,
				DiskFreeBytes:    sample.DiskFreeBytes,
				DatabaseWrites:   run.committedWrites.Load(),
				DesiredWorkers:   run.desiredConcurrency.Load(),
				EffectiveWorkers: max(1, workers),
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
