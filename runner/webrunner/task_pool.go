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
)

// taskPoolPlan divides one job's resource budget between parallel tasks.
//
// Running tasks side by side must not multiply browser usage: the job's
// configured concurrency and browser pool are shared out between workers, so a
// pool of four workers each gets a quarter of the budget. Parallelism buys
// resume granularity and latency, not extra capacity.
type taskPoolPlan struct {
	Workers            int
	PerTaskConcurrency int
	PerTaskBrowserPool int
}

func planTaskPool(job *web.Job, effectiveConcurrency, pendingTasks int) taskPoolPlan {
	if effectiveConcurrency < 1 {
		effectiveConcurrency = 1
	}

	workers := job.Data.TaskWorkers
	if workers <= 0 {
		workers = defaultTaskWorkers
		if effectiveConcurrency < workers {
			workers = effectiveConcurrency
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

	// browserBudget and pagesBudget are the per-task browser pool and
	// pages-per-browser values new tasks take. They start at the plan's
	// values and only ever shrink, under measured RAM pressure.
	browserBudget atomic.Int64
	pagesBudget   atomic.Int64
	// baselineBrowsers and baselinePages remember the configured budgets so
	// an adaptation can recover exactly to them and never beyond.
	baselineBrowsers int
	baselinePages    int

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

	w.adaptBrowserBudget(run, sample)

	allowedBrowsers := int(run.browserBudget.Load()) * int(max(1, run.workers.Load()))
	headroom := recoveryHasHeadroom(sample, int(blocks), allowedBrowsers)

	currentFailureBudget := int(run.failureBudget.Load())
	failureBudget := decideFailureBudget(currentFailureBudget, desired, int(failures), int(successes))

	// A clean failure window is not on its own a reason to take capacity
	// back: CPU, RAM, browser count, and the block rate must all agree.
	if failureBudget > currentFailureBudget && !headroom {
		failureBudget = currentFailureBudget
	}

	run.failureBudget.Store(int64(failureBudget))

	currentBlockBudget := int(run.blockBudget.Load())
	blockBudget := decideBlockBudget(currentBlockBudget, desired, int(blocks), attempts)

	if blockBudget > currentBlockBudget && !headroom {
		blockBudget = currentBlockBudget
	}

	run.blockBudget.Store(int64(blockBudget))

	resourceBudget := adaptiveWorkerConcurrency(desired, sample, run.job.Data.LowDiskBytes)
	next := int64(min(resourceBudget, failureBudget, blockBudget))

	previous := run.effectiveConcurrency.Load()
	if next == previous {
		return
	}

	run.effectiveConcurrency.Store(next)

	reason := "resource pressure"

	switch {
	case blocks > 0 || blockBudget < min(resourceBudget, failureBudget):
		reason = "platform block rate"
	case failureBudget < resourceBudget:
		reason = "task failure rate"
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
	pool, pages := adaptiveBrowserBudget(run.baselineBrowsers, run.baselinePages, sample)

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
			"browser_processes":      sample.BrowserProcesses,
		},
	)
}

// decideFailureBudget is the pure adaptation rule, kept separate so it can be
// tested exhaustively.
//
//   - A window with at least four attempts where half or more failed halves
//     the budget (never below one).
//   - A window with at least three attempts and zero failures recovers one
//     step toward the desired concurrency.
//   - Anything else (quiet or mixed windows) leaves the budget unchanged.
func decideFailureBudget(current, desired, failures, successes int) int {
	if current < 1 {
		current = 1
	}

	if current > desired {
		current = desired
	}

	attempts := failures + successes

	switch {
	case attempts >= 4 && failures*2 >= attempts:
		return max(1, current/2)
	case attempts >= 3 && failures == 0 && current < desired:
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
