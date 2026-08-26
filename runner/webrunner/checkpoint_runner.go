package webrunner

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/deduper"
	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/google-maps-scraper/gmaps"
	"github.com/gosom/google-maps-scraper/grid"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
	"github.com/gosom/google-maps-scraper/web/resultimport"
	"github.com/gosom/scrapemate"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
)

const (
	maximumAreaSeedCells   = 10_000
	resourceSampleInterval = 2 * time.Second
)

type workerResourceSample struct {
	CPUPercent           float64
	MemoryUsedBytes      uint64
	MemoryAvailableBytes uint64
	DiskFreeBytes        uint64
	// BrowserProcesses is how many browser processes this application
	// currently owns, measured from the local process table. Zero means the
	// census has not produced a value yet and must be treated as unknown
	// rather than as "no browsers are running".
	BrowserProcesses int
}

type seedTaskMetadata struct {
	Query      string
	SourceCell string
	InputID    string
}

// createAreaSeedJobs creates only cells whose centres are inside the saved
// geometry. Excluded preview IDs remain stable because the job contains the
// canonical GeoJSON snapshot, not a mutable saved-area reference.
func createAreaSeedJobs(
	job *web.Job,
	dedup deduper.Deduper,
	exitMonitor exiter.Exiter,
	extraReviews bool,
) ([]scrapemate.IJob, map[string]seedTaskMetadata, error) {
	geometry, err := web.ParseMapGeometry([]byte(job.Data.AreaGeoJSON))
	if err != nil {
		return nil, nil, fmt.Errorf("parse saved-area snapshot: %w", err)
	}
	preview, err := web.PreviewMapGrid(geometry, job.Data.GridCellKM, maximumAreaSeedCells)
	if err != nil {
		return nil, nil, fmt.Errorf("create clipped saved-area grid: %w", err)
	}
	excluded := make(map[string]struct{})
	for _, cellID := range geometry.ExcludedCellIDs() {
		excluded[cellID] = struct{}{}
	}

	jobs := make([]scrapemate.IJob, 0, len(job.Data.Keywords)*len(preview.Cells))
	metadata := make(map[string]seedTaskMetadata, cap(jobs))
	for queryIndex, rawQuery := range job.Data.Keywords {
		query, inputID := splitCheckpointQuery(rawQuery)
		if query == "" {
			continue
		}
		for _, cell := range preview.Cells {
			if _, skip := excluded[cell.ID]; skip {
				continue
			}
			coordinates := fmt.Sprintf("%.6f,%.6f", cell.Centre.Latitude, cell.Centre.Longitude)
			identity := strings.Join([]string{
				"area", fmt.Sprintf("%d", queryIndex), query, cell.ID, coordinates,
				fmt.Sprintf("%d", job.Data.Zoom),
			}, "\x1f")
			seedID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(identity)).String()
			if inputID != "" {
				seedID = inputID + "-" + seedID
			}
			options := make([]gmaps.GmapJobOptions, 0, 3)
			if dedup != nil {
				options = append(options, gmaps.WithDeduper(dedup))
			}
			if exitMonitor != nil {
				options = append(options, gmaps.WithExitMonitor(exitMonitor))
			}
			if extraReviews {
				options = append(options, gmaps.WithExtraReviews())
			}
			seed := gmaps.NewGmapJob(
				seedID,
				job.Data.Lang,
				query,
				job.Data.Depth,
				job.Data.Email,
				coordinates,
				job.Data.Zoom,
				options...,
			)
			jobs = append(jobs, seed)
			metadata[seedID] = seedTaskMetadata{Query: query, SourceCell: cell.ID, InputID: inputID}
		}
	}
	if len(jobs) == 0 {
		return nil, nil, errors.New("saved-area grid has no selected cells or queries")
	}

	return jobs, metadata, nil
}

func splitCheckpointQuery(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if before, after, ok := strings.Cut(raw, "#!#"); ok {
		return strings.TrimSpace(before), strings.TrimSpace(after)
	}

	return raw, ""
}

func buildCheckpointTaskDefinitions(
	job *web.Job,
	seeds []scrapemate.IJob,
	metadata map[string]seedTaskMetadata,
) ([]web.JobTaskDefinition, map[string]scrapemate.IJob, error) {
	definitions := make([]web.JobTaskDefinition, 0, len(seeds))
	byKey := make(map[string]scrapemate.IJob, len(seeds))
	gridCells := []grid.Cell(nil)
	if job.Data.GridBBox != "" && job.Data.AreaGeoJSON == "" {
		bbox, err := grid.ParseBoundingBox(job.Data.GridBBox)
		if err == nil {
			gridCells = grid.GenerateCells(bbox, job.Data.GridCellKM)
		}
	}
	queryCount := len(job.Data.Keywords)
	for sequence, seed := range seeds {
		key, err := checkpointSeedID(seed)
		if err != nil {
			return nil, nil, err
		}
		if _, duplicate := byKey[key]; duplicate {
			return nil, nil, fmt.Errorf("duplicate checkpoint seed ID %q", key)
		}
		item := metadata[key]
		if item.Query == "" && queryCount > 0 {
			queryIndex := sequence
			if len(gridCells) > 0 {
				queryIndex = sequence / len(gridCells)
			}
			if queryIndex >= queryCount {
				queryIndex = queryCount - 1
			}
			item.Query, item.InputID = splitCheckpointQuery(job.Data.Keywords[queryIndex])
		}
		if item.SourceCell == "" && len(gridCells) > 0 {
			cell := gridCells[sequence%len(gridCells)]
			item.SourceCell = cell.GeoCoordinates()
		}
		kind := "map-query"
		if item.SourceCell != "" {
			kind = "map-grid-cell"
		}
		payload, err := json.Marshal(map[string]any{
			"seed_id": key, "sequence": sequence, "query": item.Query,
			"source_cell": item.SourceCell,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("encode task definition: %w", err)
		}
		definitions = append(definitions, web.JobTaskDefinition{
			Key: key, Kind: kind, Sequence: sequence, Query: item.Query,
			SourceCell: item.SourceCell, InputID: item.InputID, Payload: payload,
		})
		byKey[key] = seed
	}

	return definitions, byKey, nil
}

func checkpointSeedID(seed scrapemate.IJob) (string, error) {
	switch typed := seed.(type) {
	case *gmaps.GmapJob:
		if typed.ID != "" {
			return typed.ID, nil
		}
	case *gmaps.SearchJob:
		if typed.ID != "" {
			return typed.ID, nil
		}
	}

	return "", fmt.Errorf("unsupported checkpoint seed %T", seed)
}

func (w *webrunner) scrapeJobCheckpointed(
	ctx context.Context,
	job *web.Job,
	outpath string,
	seedJobs []scrapemate.IJob,
	metadata map[string]seedTaskMetadata,
	dedup deduper.Deduper,
	exitMonitor exiter.Exiter,
) error {
	definitions, seedsByKey, err := buildCheckpointTaskDefinitions(job, seedJobs, metadata)
	if err != nil {
		return w.failJob(ctx, job, err)
	}
	maximumAttempts := 1
	if job.Data.RetryConfigured {
		maximumAttempts = max(1, job.Data.RetryCount+1)
	}
	pending, err := w.svc.PrepareJobTasks(ctx, job.ID, definitions, maximumAttempts)
	if err != nil {
		return w.failJob(ctx, job, fmt.Errorf("prepare task checkpoints: %w", err))
	}
	// The run-level seed count spans the whole plan: it keeps the shared
	// monitor's snapshot, its MaxRecords budget, and coverage expansion
	// coherent. Per-task completion is decided by each task's own monitor (see
	// taskExiter), so this count no longer gates when a task may exit.
	if len(pending) > 0 {
		exitMonitor.SetSeedCount(len(pending))
	}

	allowedSeconds := defaultAllowedSeconds(len(pending), job.Data.Depth, job.Data.Email)
	if job.Data.MaxTime > 0 {
		if job.Data.MaxTime.Seconds() < 180 {
			allowedSeconds = 180
		} else {
			allowedSeconds = int(job.Data.MaxTime.Seconds())
		}
	}
	// The deadline is enforced by the pool supervisor rather than a timeout
	// context, so an operator can extend the runtime while the job runs.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	if err := w.svc.ResetJobLiveControls(ctx, job.ID); err != nil &&
		!errors.Is(err, web.ErrLiveControlsUnsupported) {
		return w.failJob(ctx, job, fmt.Errorf("reset live controls: %w", err))
	}

	stopReasons := make(chan jobruntime.StopReason, 4)
	go w.watchRequestedStop(runCtx, job.ID, runCancel, stopReasons)
	// The run-level monitor keeps the run-wide cancel: reaching MaxRecords
	// still stops every worker at once. Its "all seeds and places done" signal
	// is deliberately left unconsumed here, because in a pool that signal can
	// only arrive while the final task is still inside its scrapemate run —
	// cancelling then would mark that finished task interrupted and hand it
	// back to the queue. The pool ends when its durable plan is drained, and
	// each task ends on its own monitor (see taskExiter).
	exitMonitor.SetCancelFunc(runCancel)

	startedAt := time.Now().UTC()
	deadline := startedAt.Add(time.Duration(allowedSeconds) * time.Second)

	// Sticky assignment and per-proxy caps need the pool's plan, not just its
	// resolved URL list. Absence of a plan keeps the job's own proxies as-is.
	extras := taskPoolExtras{
		dedup:        dedup,
		extraReviews: w.cfg.ExtraReviews || job.Data.ExtraReviews,
	}

	if job.Data.ProxyPoolID != "" {
		resolve := w.svc.ResolveProxyPlan
		if w.resolveProxyPlanForTest != nil {
			resolve = w.resolveProxyPlanForTest
		}

		if plan, planErr := resolve(ctx, job.Data.ProxyPoolID); planErr == nil && len(plan.Proxies) > 0 {
			extras.proxyPlan = &plan

			// Recorded task history orders the non-sticky candidate list;
			// running without it simply treats every proxy as fresh.
			if health, healthErr := w.svc.ProxyTaskHealthByURL(ctx, plan.PoolID); healthErr == nil {
				extras.proxyHealth = health
			}
		}
	}

	// A configured coverage block turns on the adaptive discovery engine.
	// Nil keeps exactly the historical behaviour.
	if job.Data.Coverage != nil {
		if seedState, seedErr := w.svc.JobCoverageSeedState(ctx, job.ID); seedErr == nil {
			extras.coverage = newCoverageEngine(job.ID, *job.Data.Coverage, seedState).
				withPlanZoom(job.Data.Zoom).
				withSearchRadius(job.Data.Radius)
		} else {
			_ = w.svc.RecordJobWorkerEvent(
				context.Background(), job.ID, "coverage-disabled", "warning",
				"The adaptive coverage engine could not read the plan state and stays off for this run",
				map[string]any{"error": jobruntime.RedactString(seedErr.Error())},
			)
		}
	}
	desiredConcurrency := job.Data.Concurrency

	if desiredConcurrency <= 0 {
		desiredConcurrency = w.cfg.Concurrency
	}

	if desiredConcurrency <= 0 {
		desiredConcurrency = 1
	}

	effectiveConcurrency := desiredConcurrency
	browserWorkerBudget := 0
	browserBudgetTotal := 0

	var budgetMemoryAvailable uint64

	// A browser-mode job fans out one scrapemate app — and therefore one
	// browser pool that never drops below a single browser — per task worker.
	// Sample the host once here so the default fan-out is bounded by real
	// memory even when the adaptive safeguards are off, and so a browser-mode
	// job never launches more simultaneous browsers than the host can support.
	// Fast mode needs no browser and keeps its full default fan-out.
	if job.Data.Adaptive || !job.Data.FastMode {
		resourceSample, _ := w.sampleWorkerResources(runCtx)

		if job.Data.Adaptive {
			effectiveConcurrency = adaptiveWorkerConcurrency(
				desiredConcurrency, resourceSample, job.Data.LowDiskBytes, job.Data.MemoryCeilingBytes,
			)
		}

		if !job.Data.FastMode {
			browserWorkerBudget = browserModeWorkerBudget(resourceSample)
			browserBudgetTotal = browserProcessBudget(resourceSample)
			budgetMemoryAvailable = resourceSample.MemoryAvailableBytes
		}
	}

	// Tasks run side by side, but the browser budget is divided between them, so
	// parallelism buys resume granularity rather than extra load. In browser
	// mode the worker count is capped to the memory-derived worker budget and
	// the browser TOTAL (workers x per-worker pool) is capped to the
	// memory-derived browser-process budget, because browsers — not workers —
	// are what cost memory.
	plan := planTaskPool(job, effectiveConcurrency, len(pending), browserWorkerBudget, browserBudgetTotal)

	_ = w.svc.RecordJobWorkerEvent(
		context.Background(), job.ID, "task-pool", "information",
		fmt.Sprintf("Running %d task(s) in parallel with %d worker concurrency each (%d browser(s) planned)",
			plan.Workers, plan.PerTaskConcurrency, plan.PlannedBrowsers()),
		map[string]any{
			"task_workers": plan.Workers, "per_task_concurrency": plan.PerTaskConcurrency,
			"per_task_browser_pool": plan.PerTaskBrowserPool, "per_task_pages": plan.PerTaskPages,
			"desired_concurrency": desiredConcurrency, "effective_concurrency": effectiveConcurrency,
			"pending_tasks":          len(pending),
			"planned_browsers":       plan.PlannedBrowsers(),
			"browser_budget_total":   browserBudgetTotal,
			"browser_worker_budget":  browserWorkerBudget,
			"memory_available_bytes": budgetMemoryAvailable,
			"per_browser_cost_bytes": uint64(perBrowserPlanningCostBytes),
			"budget_reserve_bytes":   uint64(browserBudgetReserveBytes),
		},
	)

	if plan.CappedExplicit {
		// The operator asked for more than the machine can physically hold.
		// The cap is not negotiable — an OOM kill loses the whole run — but
		// overriding an explicit setting must never be silent.
		_ = w.svc.RecordJobWorkerEvent(
			context.Background(), job.ID, "capacity-capped", "warning",
			fmt.Sprintf(
				"Requested workers/browsers exceed what available memory can hold; running %d worker(s) with %d browser(s) instead",
				plan.Workers, plan.PlannedBrowsers()),
			map[string]any{
				"requested_task_workers": job.Data.TaskWorkers,
				"requested_browser_pool": job.Data.BrowserPool,
				"granted_workers":        plan.Workers,
				"granted_browsers":       plan.PlannedBrowsers(),
				"browser_budget_total":   browserBudgetTotal,
				"memory_available_bytes": budgetMemoryAvailable,
			},
		)
	}

	stopReason := w.runTaskPool(
		ctx, runCtx, runCancel, job, outpath, seedsByKey, exitMonitor,
		stopReasons, plan, desiredConcurrency, startedAt, deadline, extras,
	)

	if stopReason == jobruntime.StopReasonNone {
		stopReason = receiveStopReason(stopReasons)
	}
	if stopReason == jobruntime.StopReasonNone {
		switch {
		case exiter.LimitReached(exitMonitor):
			stopReason = jobruntime.StopReasonMaximumRecords
		case errors.Is(runCtx.Err(), context.DeadlineExceeded):
			stopReason = jobruntime.StopReasonRuntimeLimit
		case ctx.Err() != nil:
			stopReason = jobruntime.StopReasonShutdown
		default:
			stopReason = jobruntime.StopReasonCompleted
		}
	}

	// End-of-pool sweep: a finish write that failed and was swallowed can
	// leave a task row "running" with nobody to conclude it — and once the
	// pool stops claiming, the claim-side reclaim never runs again. One
	// reclaim here returns any already-expired lease; a not-yet-expired
	// orphan is caught by the startup sweep or the next resume.
	if reclaimed, reclaimErr := w.svc.ReclaimExpiredJobTasks(context.Background(), job.ID); reclaimErr == nil && reclaimed > 0 {
		_ = w.svc.RecordJobWorkerEvent(
			context.Background(), job.ID, "task-lease-reclaimed", "warning",
			fmt.Sprintf("%d expired task lease(s) were returned to the queue after the pool drained", reclaimed),
			map[string]any{"reclaimed": reclaimed},
		)
	}

	execution, executionErr := w.svc.GetJobExecution(context.Background(), job.ID)
	if executionErr != nil {
		return w.failJob(ctx, job, fmt.Errorf("read task checkpoint summary: %w", executionErr))
	}
	taskSummary := jobruntime.TaskSummary{
		Total: execution.Tasks.Total, Completed: execution.Tasks.Completed,
		Failed: execution.Tasks.Failed, Skipped: execution.Tasks.Skipped,
	}

	// The durable plan, not a worker-local counter, decides whether exhausted
	// tasks turned an otherwise clean run into a failure outcome.
	if stopReason == jobruntime.StopReasonCompleted && execution.Tasks.Failed > 0 {
		stopReason = jobruntime.StopReasonTaskFailures
	}
	outcome, err := jobruntime.ClassifyOutcome(jobruntime.RunResult{Reason: stopReason, Tasks: taskSummary})
	if err != nil {
		return w.failJob(ctx, job, err)
	}
	if err := w.persistOutcome(ctx, job, outcome, outcomeMessage(outcome)); err != nil {
		return err
	}

	importCtx, cancelImport := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancelImport()
	if _, err := w.svc.ImportJobResults(importCtx, job.ID); err != nil &&
		!errors.Is(err, web.ErrResultStoreUnsupported) && !errors.Is(err, web.ErrPlacesNotFound) {
		return fmt.Errorf("import normalized checkpoint results: %w", err)
	}
	if options, enabled, optionsErr := web.EnrichmentOptionsForJob(job.Data); optionsErr != nil {
		return fmt.Errorf("validate website enrichment: %w", optionsErr)
	} else if enabled {
		batch, queueErr := w.svc.QueueJobEnrichment(importCtx, job.ID, options)
		if queueErr != nil && !errors.Is(queueErr, web.ErrEnrichmentUnsupported) {
			return fmt.Errorf("queue website enrichment: %w", queueErr)
		}
		if batch.Queued > 0 {
			log.Printf("job %s queued %d website enrichment tasks after checkpoint import", job.ID, batch.Queued)
		}
	}

	return nil
}

// defaultAllowedSeconds models the default runtime window for a run of seed
// tasks. The base rate covers the listing walk alone; enrichment roughly
// doubles the job count per place — every place with a website spawns an
// arbitrary-site email fetch, and the place's CSV row is deferred behind it —
// so an email run under the walk-only window gets truncated exactly where the
// deferred rows are still at risk. The controlled variance runs measured it:
// the identical 4-cell workload took ~290s without enrichment and 712-903s
// with it, the latter hitting the operator cap with tasks unfinished. An
// explicit MaxTime still overrides this default entirely.
func defaultAllowedSeconds(pending, depth int, email bool) int {
	perSeedRate := 10
	if email {
		perSeedRate = 20
	}

	return max(60, pending*perSeedRate*depth/50+120)
}

// engineShutdownGrace bounds how long the scrape engine may take to return
// after this task's context has already been cancelled.
//
// The upstream browser teardown takes neither a context nor a timeout:
// scrapemate's jsFetch.Close closes the Playwright browser context, and
// playwright-go's protocolCallback.waitResult blocks on a channel until the
// browser answers. A browser that has died or stopped answering never answers,
// so the call never returns. A controlled acceptance run caught exactly this:
// fifteen of sixteen tasks finished, the sixteenth parked a worker inside that
// teardown for twenty-one minutes, the in-memory active-task count stayed at
// one, and the job sailed past its runtime deadline without ever reaching a
// terminal state.
//
// scrapemate is a read-only dependency this repo does not fork, and a goroutine
// cannot be killed from outside — but nothing forces us to keep waiting on one.
// After the grace period the task gives up on the engine, keeps the rows the
// engine already wrote, and hands itself back to the pool. The wedged goroutine
// and its file handle leak; the job stays alive and finishes.
const engineShutdownGrace = 90 * time.Second

// errEngineShutdownTimeout marks a task abandoned because the scrape engine did
// not return after cancellation. It is a task-level outcome, not a job failure:
// the rows already written are kept and the task stays resumable.
var errEngineShutdownTimeout = errors.New("scrape engine did not shut down within the grace period")

// awaitEngine runs fn in its own goroutine and waits for it. It waits without a
// bound while ctx is live — a task legitimately in progress is never cut short
// — and once ctx is done it waits at most grace longer. It reports fn's error
// and whether fn actually returned.
func awaitEngine(ctx context.Context, grace time.Duration, fn func() error) (error, bool) {
	return awaitEngineOn(ctx, grace, make(chan error, 1), fn)
}

// awaitEngineOn is awaitEngine with a caller-owned done channel (capacity >= 1
// so a late goroutine can finish and exit instead of leaking on the send).
// Owning the channel lets the caller hand a timed-out engine to the
// containment registry, whose detached monitor keeps listening for the late
// return that a janitor driver-kill eventually forces.
func awaitEngineOn(ctx context.Context, grace time.Duration, done chan error, fn func() error) (error, bool) {
	go func() { done <- fn() }()

	select {
	case err := <-done:
		return err, true
	case <-ctx.Done():
	}

	timer := time.NewTimer(grace)
	defer timer.Stop()

	select {
	case err := <-done:
		return err, true
	case <-timer.C:
		return nil, false
	}
}

// adoptWedgedEngine hands a wedged engine's leftovers to the containment
// registry and records the containment action on the job, distinct from the
// engine-shutdown-timeout failure the task itself reports.
func (w *webrunner) adoptWedgedEngine(
	jobID string,
	seed scrapemate.IJob,
	runPath string,
	outfile *os.File,
	done <-chan error,
) {
	if w.containment == nil {
		return
	}

	taskKey := ""
	if key, err := checkpointSeedID(seed); err == nil {
		taskKey = key
	}

	w.containment.adopt(jobID, taskKey, runPath, outfile, done,
		func(engine abandonedEngine, wedgedFor time.Duration) {
			_ = w.svc.RecordJobWorkerEvent(
				context.Background(), engine.jobID, "engine-reclaimed", "information",
				fmt.Sprintf("An abandoned engine returned after %s; its file handle and goroutine were released", wedgedFor.Round(time.Second)),
				map[string]any{"task_key": engine.taskKey, "wedged_for": wedgedFor.Round(time.Second).String()},
			)
		})

	abandoned := w.containment.AbandonedNow()

	_ = w.svc.RecordJobWorkerEvent(
		context.Background(), jobID, "engine-abandoned", "warning",
		fmt.Sprintf("A wedged engine was placed under containment (%d currently abandoned); its processes will be reclaimed at the next safe point", abandoned),
		map[string]any{"task_key": taskKey, "abandoned_now": abandoned},
	)

	// Two abandoned engines is a full default browser-worker budget stranded:
	// recommend a recycle so an operator watching the log knows the service
	// would benefit from one even before the janitor's next safe point.
	if abandoned >= 2 {
		_ = w.svc.RecordJobWorkerEvent(
			context.Background(), jobID, "worker-recycle-recommended", "warning",
			"Multiple abandoned engines are being contained; a worker recycle (or container restart) would reclaim them immediately",
			map[string]any{"abandoned_now": abandoned},
		)
	}
}

// runCheckpointTask executes exactly one seed into its own temporary CSV. Live
// progress reporting belongs to the pool supervisor, so the task itself only
// runs the scraper and hands back a file to merge.
//
// The task runs behind a taskExiter: its own completion cancels this task's
// context as soon as its seed and every listing that seed found are done,
// while the run-level monitor keeps its record budget and its run-wide cancel.
// scrapemate's inactivity timeout stays configured and covers a stalled task.
func (w *webrunner) runCheckpointTask(
	ctx context.Context,
	job *web.Job,
	seed scrapemate.IJob,
	exitMonitor exiter.Exiter,
) (string, exiter.Snapshot, error) {
	outfile, err := os.CreateTemp(w.cfg.DataFolder, job.ID+".run-checkpoint-*.csv")
	if err != nil {
		return "", exiter.Snapshot{}, fmt.Errorf("create checkpoint task result file: %w", err)
	}
	runPath := outfile.Name()
	keepRun := false
	engineWedged := false
	defer func() {
		// A wedged engine goroutine may still hold this writer, so closing the
		// file under it would be a use-after-close. The handle is deliberately
		// leaked together with the goroutine that owns it.
		if !engineWedged {
			_ = outfile.Close()
		}
		if !keepRun {
			_ = os.Remove(runPath)
		}
	}()

	setupMate := w.setupMate
	if setupMate == nil {
		setupMate = defaultSetupMate(w.cfg)
	}
	mate, err := setupMate(ctx, outfile, job)
	if err != nil {
		return "", exiter.Snapshot{}, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = mate.Close()
		}
	}()

	taskCtx, taskCancel := context.WithCancel(ctx)
	defer taskCancel()

	taskMonitor := newTaskExiter(exitMonitor, taskSeedCount)
	taskMonitor.SetCancelFunc(taskCancel)

	go taskMonitor.Run(taskCtx)

	if w.containment != nil {
		w.containment.engineStarted()
	}

	engineDone := make(chan error, 1)
	runErr, returned := awaitEngineOn(taskCtx, engineShutdownGrace, engineDone, func() error {
		return mate.Start(taskCtx, seedWithExitMonitor(seed, taskMonitor))
	})
	// Only this task's own counters can say whether its seed finished; the
	// run-level snapshot also moves for every other task running in parallel.
	after := taskMonitor.ownSnapshot()

	if !returned {
		// The engine did not come back within the grace period after its
		// context was cancelled. Keep whatever it already wrote and hand the
		// task back to the pool: waiting longer is what wedged the job.
		// closed is set so the deferred mate.Close cannot re-touch the wedged
		// engine synchronously on the way out.
		engineWedged = true
		keepRun = true
		closed = true
		w.adoptWedgedEngine(job.ID, seed, runPath, outfile, engineDone)

		return runPath, after, errEngineShutdownTimeout
	}

	closeDone := make(chan error, 1)
	if closeErr, closeReturned := awaitEngineOn(taskCtx, engineShutdownGrace, closeDone, mate.Close); !closeReturned {
		engineWedged = true
		keepRun = true
		closed = true
		w.adoptWedgedEngine(job.ID, seed, runPath, outfile, closeDone)

		return runPath, after, errors.Join(runErr, errEngineShutdownTimeout)
	} else if closeErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("close checkpoint worker: %w", closeErr))
	}
	closed = true

	if w.containment != nil {
		w.containment.engineFinished()
	}
	if headerErr := ensureCheckpointCSV(outfile); headerErr != nil {
		return "", after, errors.Join(runErr, headerErr)
	}
	if syncErr := outfile.Sync(); syncErr != nil {
		return "", after, errors.Join(runErr, fmt.Errorf("flush checkpoint result CSV: %w", syncErr))
	}
	if closeErr := outfile.Close(); closeErr != nil {
		return "", after, errors.Join(runErr, fmt.Errorf("close checkpoint result CSV: %w", closeErr))
	}
	keepRun = true

	return runPath, after, normalizeCheckpointRunError(taskCtx, runErr, after)
}

func ensureCheckpointCSV(outfile *os.File) error {
	info, err := outfile.Stat()
	if err != nil {
		return fmt.Errorf("inspect checkpoint result CSV: %w", err)
	}
	if info.Size() > 0 {
		return nil
	}
	writer := csv.NewWriter(outfile)
	if err := writer.Write(resultimport.LegacyHeaders()); err != nil {
		return fmt.Errorf("write checkpoint CSV header: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("flush checkpoint CSV header: %w", err)
	}

	return nil
}

// errTaskTruncated marks a task cancelled while places it had already found
// were still uncommitted. Before this existed such a task was normalized to
// SUCCESS, which silently recorded a short cell as complete — with enrichment
// on, the loss is worse, because every place with a website has its CSV row
// deferred behind the email fetch. The task stays failed-and-resumable instead.
var errTaskTruncated = errors.New("task cancelled before all found places were committed")

func normalizeCheckpointRunError(ctx context.Context, runErr error, after exiter.Snapshot) error {
	if runErr == nil {
		return nil
	}

	if errors.Is(runErr, context.Canceled) {
		// No context cancellation reached the engine: the Canceled came from
		// an internal engine path, which the legacy behaviour treated as a
		// clean finish. Preserved.
		if ctx.Err() == nil {
			return nil
		}

		// The task's own exiter cancels when its seed AND every listing that
		// seed found are done — that is the normal end of a healthy task and
		// stays a success. A cancellation that arrives EARLIER leaves found
		// places uncommitted, and calling that success is how short cells were
		// silently recorded as complete.
		if after.SeedsCompleted > 0 && after.PlacesCompleted >= after.PlacesFound {
			return nil
		}

		return fmt.Errorf("%w: %d of %d found places committed",
			errTaskTruncated, after.PlacesCompleted, after.PlacesFound)
	}

	return runErr
}

// sampleWorkerResources measures the four local dimensions adaptive
// performance reads: CPU, RAM, free disk, and the number of browser processes
// this application owns. The browser census is cached and never fails the
// sample, so a host that refuses process enumeration simply keeps the last
// known count.
func (w *webrunner) sampleWorkerResources(ctx context.Context) (workerResourceSample, error) {
	if w.sampleResources != nil {
		return w.sampleResources(ctx, w.cfg.DataFolder)
	}

	sample, err := defaultWorkerResourceSample(ctx, w.cfg.DataFolder)
	if err != nil {
		return sample, err
	}

	sample.BrowserProcesses = w.browsers.countBrowsers(ctx, int32(os.Getpid()))

	return sample, nil
}

func defaultWorkerResourceSample(ctx context.Context, dataFolder string) (workerResourceSample, error) {
	percentages, err := cpu.PercentWithContext(ctx, 0, false)
	if err != nil {
		return workerResourceSample{}, fmt.Errorf("sample CPU: %w", err)
	}
	memory, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return workerResourceSample{}, fmt.Errorf("sample memory: %w", err)
	}
	diskUsage, err := disk.UsageWithContext(ctx, dataFolder)
	if err != nil {
		return workerResourceSample{}, fmt.Errorf("sample disk: %w", err)
	}
	cpuPercent := 0.0
	if len(percentages) > 0 {
		cpuPercent = percentages[0]
	}

	available := memory.Available
	// Inside a memory-limited container /proc/meminfo still shows the HOST,
	// which is exactly the over-estimate that let the incident's browsers run
	// into the cgroup ceiling. When a cgroup limit is readable and tighter,
	// it wins; when no cgroup file exists (native Windows, unlimited Linux)
	// the host figure stands.
	if cgAvailable, ok := cgroupAvailableBytes(cgroupRoot); ok && cgAvailable < available {
		available = cgAvailable
	}

	return workerResourceSample{
		CPUPercent: cpuPercent, MemoryUsedBytes: memory.Used,
		MemoryAvailableBytes: available, DiskFreeBytes: diskUsage.Free,
	}, nil
}

// cgroupRoot is where the container's own cgroup controllers are mounted.
const cgroupRoot = "/sys/fs/cgroup"

// cgroupAvailableBytes reads the container's memory headroom — limit minus
// current usage — from cgroup v2 (memory.max / memory.current) or cgroup v1
// (memory/memory.limit_in_bytes / memory.usage_in_bytes). The second return is
// false when no readable, finite limit exists: absent files (not a container,
// or Windows), the v2 literal "max", or a limit so large it is plainly "no
// limit" (>1TiB, the kernel's PAGE_COUNTER_MAX idiom).
func cgroupAvailableBytes(root string) (uint64, bool) {
	type pair struct{ limitFile, usageFile string }

	candidates := []pair{
		{filepath.Join(root, "memory.max"), filepath.Join(root, "memory.current")},
		{filepath.Join(root, "memory", "memory.limit_in_bytes"), filepath.Join(root, "memory", "memory.usage_in_bytes")},
	}

	const noLimitThreshold = uint64(1) << 40

	for _, c := range candidates {
		rawLimit, err := os.ReadFile(c.limitFile)
		if err != nil {
			continue
		}

		limitText := strings.TrimSpace(string(rawLimit))
		if limitText == "max" || limitText == "" {
			continue
		}

		limit, err := strconv.ParseUint(limitText, 10, 64)
		if err != nil || limit == 0 || limit > noLimitThreshold {
			continue
		}

		var usage uint64
		if rawUsage, usageErr := os.ReadFile(c.usageFile); usageErr == nil {
			usage, _ = strconv.ParseUint(strings.TrimSpace(string(rawUsage)), 10, 64)
		}

		if usage >= limit {
			// Exhausted headroom must read as "almost nothing", not as "no
			// measurement": one byte keeps the severe low-memory reductions
			// armed instead of falling back to the host's rosy figure.
			return 1, true
		}

		return limit - usage, true
	}

	return 0, false
}

// adaptiveWorkerConcurrency caps the worker concurrency a run may take from
// the local measurements. Every rule here can only lower the desired value.
//
// memoryCeiling is the operator's optional memory ceiling. Crossing it is
// treated exactly like the severe available-memory step: the run is pinned to
// a single worker until the measurement falls back under the ceiling. A zero
// ceiling is "no ceiling" and reproduces the behaviour every run had before
// the control existed.
func adaptiveWorkerConcurrency(
	desired int,
	sample workerResourceSample,
	lowDiskThreshold uint64,
	memoryCeiling uint64,
) int {
	if desired < 1 {
		desired = 1
	}
	effective := desired
	if sample.CPUPercent >= 85 {
		effective = max(1, effective/2)
	}
	if sample.MemoryAvailableBytes > 0 && sample.MemoryAvailableBytes < 1<<30 {
		effective = 1
	}
	if lowDiskThreshold > 0 && sample.DiskFreeBytes < lowDiskThreshold*2 {
		effective = 1
	}
	if memoryCeilingExceeded(memoryCeiling, sample) {
		effective = 1
	}

	return effective
}

func receiveStopReason(reasons <-chan jobruntime.StopReason) jobruntime.StopReason {
	select {
	case reason := <-reasons:
		return reason
	default:
		return jobruntime.StopReasonNone
	}
}
