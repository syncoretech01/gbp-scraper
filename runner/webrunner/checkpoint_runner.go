package webrunner

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
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

	allowedSeconds := max(60, len(pending)*10*job.Data.Depth/50+120)
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
		}
	}

	// Tasks run side by side, but the browser budget is divided between them, so
	// parallelism buys resume granularity rather than extra load. In browser
	// mode the default worker count is additionally capped to the memory-derived
	// browser budget, because each worker is a separate browser pool.
	plan := planTaskPool(job, effectiveConcurrency, len(pending), browserWorkerBudget)

	_ = w.svc.RecordJobWorkerEvent(
		context.Background(), job.ID, "task-pool", "information",
		fmt.Sprintf("Running %d task(s) in parallel with %d worker concurrency each", plan.Workers, plan.PerTaskConcurrency),
		map[string]any{
			"task_workers": plan.Workers, "per_task_concurrency": plan.PerTaskConcurrency,
			"per_task_browser_pool": plan.PerTaskBrowserPool,
			"desired_concurrency":   desiredConcurrency, "effective_concurrency": effectiveConcurrency,
			"pending_tasks": len(pending),
		},
	)

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
) (string, error) {
	outfile, err := os.CreateTemp(w.cfg.DataFolder, job.ID+".run-checkpoint-*.csv")
	if err != nil {
		return "", fmt.Errorf("create checkpoint task result file: %w", err)
	}
	runPath := outfile.Name()
	keepRun := false
	defer func() {
		_ = outfile.Close()
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
		return "", err
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

	runErr := mate.Start(taskCtx, seedWithExitMonitor(seed, taskMonitor))
	// Only this task's own counters can say whether its seed finished; the
	// run-level snapshot also moves for every other task running in parallel.
	after := taskMonitor.ownSnapshot()

	if closeErr := mate.Close(); closeErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("close checkpoint worker: %w", closeErr))
	}
	closed = true
	if headerErr := ensureCheckpointCSV(outfile); headerErr != nil {
		return "", errors.Join(runErr, headerErr)
	}
	if syncErr := outfile.Sync(); syncErr != nil {
		return "", errors.Join(runErr, fmt.Errorf("flush checkpoint result CSV: %w", syncErr))
	}
	if closeErr := outfile.Close(); closeErr != nil {
		return "", errors.Join(runErr, fmt.Errorf("close checkpoint result CSV: %w", closeErr))
	}
	keepRun = true

	return runPath, normalizeCheckpointRunError(taskCtx, runErr, after.SeedsCompleted > 0)
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

func normalizeCheckpointRunError(ctx context.Context, runErr error, seedCompleted bool) error {
	if runErr == nil {
		return nil
	}
	if errors.Is(runErr, context.Canceled) && (ctx.Err() == nil || seedCompleted) {
		return nil
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

	return workerResourceSample{
		CPUPercent: cpuPercent, MemoryUsedBytes: memory.Used,
		MemoryAvailableBytes: memory.Available, DiskFreeBytes: diskUsage.Free,
	}, nil
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
