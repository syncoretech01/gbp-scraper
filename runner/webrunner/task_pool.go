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

	return web.JobTaskCheckpoint{
		RowsAdded:         summary.RunAdded,
		RowsReplaced:      summary.ExistingReplaced,
		DuplicatesSkipped: summary.DuplicatesSkipped,
		DiskFreeBytes:     diskFree,
	}, nil
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
) jobruntime.StopReason {
	run := &taskPoolRun{job: job, outpath: outpath}
	run.desiredConcurrency.Store(int64(desiredConcurrency))
	run.effectiveConcurrency.Store(int64(plan.PerTaskConcurrency * plan.Workers))
	run.workers.Store(int64(plan.Workers))

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
				return
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
			_ = w.svc.FailJobTask(
				context.Background(), job.ID, task.Key,
				fmt.Errorf("checkpoint task %q has no current seed", task.Key), false,
				web.JobTaskCheckpoint{},
			)
			run.taskFailures.Add(1)

			continue
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

	run.activeTasks.Add(1)
	defer run.activeTasks.Add(-1)

	taskJob := *job
	taskJob.Data = job.Data
	taskJob.Data.Concurrency = plan.PerTaskConcurrency

	if plan.PerTaskBrowserPool > 0 {
		taskJob.Data.BrowserPool = plan.PerTaskBrowserPool
	}

	heartbeatCtx, stopHeartbeat := context.WithCancel(runCtx)
	leaseLost := make(chan struct{})
	heartbeatDone := make(chan struct{})

	go func() {
		defer close(heartbeatDone)
		w.heartbeatLeasedTask(heartbeatCtx, job.ID, task.Key, owner, leaseLost)
	}()

	taskCtx, cancelTask := context.WithCancel(runCtx)

	go func() {
		select {
		case <-leaseLost:
			cancelTask()
		case <-taskCtx.Done():
		}
	}()

	runPath, taskErr := w.runCheckpointTask(taskCtx, &taskJob, seed, exitMonitor)

	cancelTask()
	stopHeartbeat()
	<-heartbeatDone

	if runPath != "" {
		defer func() { _ = os.Remove(runPath) }()
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

		w.failLeasedTask(run, task, taskErr, web.JobTaskCheckpoint{})

		return true
	}

	checkpoint, mergeErr := run.mergeTaskOutput(runPath, sample.DiskFreeBytes)
	if mergeErr != nil {
		w.failLeasedTask(run, task, fmt.Errorf("merge checkpoint task results: %w", mergeErr), checkpoint)

		return true
	}

	if taskErr == nil {
		if completeErr := w.svc.CompleteJobTask(context.Background(), job.ID, task.Key, checkpoint); completeErr != nil {
			_ = w.svc.RecordJobWorkerEvent(
				context.Background(), job.ID, "task-commit-failed", "error",
				"Could not commit a completed task checkpoint",
				map[string]any{
					"task_key": task.Key,
					"error":    jobruntime.RedactString(completeErr.Error()),
				},
			)
		}

		return true
	}

	w.failLeasedTask(run, task, taskErr, checkpoint)

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

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// adaptTaskPool records an adaptive change to the shared concurrency budget.
// The pool size itself stays fixed for the run so leases and browser shares
// remain predictable; what changes is the reported effective capacity.
func (w *webrunner) adaptTaskPool(run *taskPoolRun, sample workerResourceSample) {
	desired := int(run.desiredConcurrency.Load())
	next := int64(adaptiveWorkerConcurrency(desired, sample, run.job.Data.LowDiskBytes))

	previous := run.effectiveConcurrency.Load()
	if next == previous {
		return
	}

	run.effectiveConcurrency.Store(next)

	_ = w.svc.RecordJobWorkerEvent(
		context.Background(), run.job.ID, "adaptive-performance", "information",
		fmt.Sprintf("Adaptive performance changed the concurrency budget from %d to %d", previous, next),
		map[string]any{
			"previous_concurrency":   previous,
			"effective_concurrency":  next,
			"desired_concurrency":    desired,
			"task_workers":           run.workers.Load(),
			"cpu_percent":            sample.CPUPercent,
			"memory_available_bytes": sample.MemoryAvailableBytes,
			"disk_free_bytes":        sample.DiskFreeBytes,
		},
	)
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
