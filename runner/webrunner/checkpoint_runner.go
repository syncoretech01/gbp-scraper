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
	runCtx, runCancel := context.WithTimeout(ctx, time.Duration(allowedSeconds)*time.Second)
	defer runCancel()
	stopReasons := make(chan jobruntime.StopReason, 4)
	go w.watchRequestedStop(runCtx, job.ID, runCancel, stopReasons)
	exitMonitor.SetCancelFunc(runCancel)
	go exitMonitor.Run(runCtx)

	startedAt := time.Now().UTC()
	desiredConcurrency := job.Data.Concurrency
	if desiredConcurrency <= 0 {
		desiredConcurrency = w.cfg.Concurrency
	}
	if desiredConcurrency <= 0 {
		desiredConcurrency = 1
	}
	resourceSample, _ := w.sampleWorkerResources(runCtx)
	effectiveConcurrency := desiredConcurrency
	if job.Data.Adaptive {
		effectiveConcurrency = adaptiveWorkerConcurrency(desiredConcurrency, resourceSample, job.Data.LowDiskBytes)
		if effectiveConcurrency != desiredConcurrency {
			_ = w.svc.RecordJobWorkerEvent(
				context.Background(), job.ID, "adaptive-performance", "information",
				fmt.Sprintf("Adaptive performance selected concurrency %d instead of %d for the next task", effectiveConcurrency, desiredConcurrency),
				map[string]any{
					"desired_concurrency": desiredConcurrency, "effective_concurrency": effectiveConcurrency,
					"cpu_percent":            resourceSample.CPUPercent,
					"memory_available_bytes": resourceSample.MemoryAvailableBytes,
					"disk_free_bytes":        resourceSample.DiskFreeBytes,
				},
			)
		}
	}

	var stopReason jobruntime.StopReason
	var taskFailures int64
	var committedWrites int64
	for _, task := range pending {
		if reason := receiveStopReason(stopReasons); reason != jobruntime.StopReasonNone {
			stopReason = reason
			break
		}
		if runCtx.Err() != nil {
			stopReason = stoppedBecause(ctx, runCtx.Err(), stopReasons)
			break
		}
		seed, exists := seedsByKey[task.Key]
		if !exists {
			return w.failJob(ctx, job, fmt.Errorf("checkpoint task %q has no current seed", task.Key))
		}

		for {
			claimed, claimErr := w.svc.StartJobTask(runCtx, job.ID, task.Key)
			if claimErr != nil {
				return w.failJob(ctx, job, fmt.Errorf("start checkpoint task: %w", claimErr))
			}
			task = claimed
			if task.State == "completed" || task.State == "skipped" {
				break
			}

			currentSample, sampleErr := w.sampleWorkerResources(runCtx)
			if sampleErr == nil && job.Data.LowDiskBytes > 0 && currentSample.DiskFreeBytes < job.Data.LowDiskBytes {
				stopReason = jobruntime.StopReasonLowDisk
				_ = w.svc.FailJobTask(
					context.Background(), job.ID, task.Key,
					fmt.Errorf("available disk %d bytes is below safety threshold %d", currentSample.DiskFreeBytes, job.Data.LowDiskBytes),
					true,
					web.JobTaskCheckpoint{DiskFreeBytes: currentSample.DiskFreeBytes},
				)
				_ = w.svc.RecordJobWorkerEvent(
					context.Background(), job.ID, "low-disk", "warning",
					"Paused before starting the next task because free disk is below the configured safety threshold",
					map[string]any{"disk_free_bytes": currentSample.DiskFreeBytes, "threshold_bytes": job.Data.LowDiskBytes},
				)
				break
			}
			if job.Data.Adaptive && sampleErr == nil {
				nextConcurrency := adaptiveWorkerConcurrency(desiredConcurrency, currentSample, job.Data.LowDiskBytes)
				if nextConcurrency != effectiveConcurrency {
					previous := effectiveConcurrency
					effectiveConcurrency = nextConcurrency
					_ = w.svc.RecordJobWorkerEvent(
						context.Background(), job.ID, "adaptive-performance", "information",
						fmt.Sprintf("Adaptive performance changed concurrency from %d to %d at a safe task checkpoint", previous, effectiveConcurrency),
						map[string]any{
							"previous_concurrency": previous, "effective_concurrency": effectiveConcurrency,
							"desired_concurrency": desiredConcurrency, "cpu_percent": currentSample.CPUPercent,
							"memory_available_bytes": currentSample.MemoryAvailableBytes,
							"disk_free_bytes":        currentSample.DiskFreeBytes,
						},
					)
				}
			}

			taskJob := *job
			taskJob.Data = job.Data
			taskJob.Data.Concurrency = effectiveConcurrency
			runPath, taskErr := w.runCheckpointTask(
				runCtx,
				&taskJob,
				seed,
				task,
				exitMonitor,
				stopReasons,
				desiredConcurrency,
				effectiveConcurrency,
				startedAt,
				committedWrites,
			)
			taskStopReason := receiveStopReason(stopReasons)
			if taskStopReason == jobruntime.StopReasonNone {
				switch {
				case exiter.LimitReached(exitMonitor):
					taskStopReason = jobruntime.StopReasonMaximumRecords
				case errors.Is(runCtx.Err(), context.DeadlineExceeded):
					taskStopReason = jobruntime.StopReasonRuntimeLimit
				case ctx.Err() != nil:
					taskStopReason = jobruntime.StopReasonShutdown
				}
			}
			if taskStopReason != jobruntime.StopReasonNone {
				stopReason = taskStopReason
				if taskErr == nil {
					taskErr = fmt.Errorf("task stopped for %s", taskStopReason)
				}
			}
			if runPath != "" {
				mergeSummary, mergeErr := mergeResultCSV(context.Background(), outpath, runPath)
				if mergeErr != nil {
					return w.failJob(ctx, job, fmt.Errorf("merge checkpoint task results: %w", mergeErr))
				}
				committedWrites++
				checkpoint := web.JobTaskCheckpoint{
					RowsAdded: mergeSummary.RunAdded, RowsReplaced: mergeSummary.ExistingReplaced,
					DuplicatesSkipped: mergeSummary.DuplicatesSkipped, DiskFreeBytes: currentSample.DiskFreeBytes,
				}
				if taskErr == nil {
					if completeErr := w.svc.CompleteJobTask(context.Background(), job.ID, task.Key, checkpoint); completeErr != nil {
						return w.failJob(ctx, job, fmt.Errorf("commit task checkpoint: %w", completeErr))
					}
					break
				}
				retryable := stopReason != jobruntime.StopReasonNone ||
					(runCtx.Err() == nil && claimed.Attempts < claimed.MaxAttempts)
				if failErr := w.svc.FailJobTask(
					context.Background(), job.ID, task.Key, taskErr, retryable, checkpoint,
				); failErr != nil {
					return w.failJob(ctx, job, fmt.Errorf("commit failed task checkpoint: %w", failErr))
				}
				if !retryable {
					taskFailures++
					break
				}
				if job.Data.RetryDelay > 0 {
					select {
					case <-runCtx.Done():
					case <-time.After(job.Data.RetryDelay):
					}
				}
				continue
			}
			if taskErr != nil {
				return w.failJob(ctx, job, fmt.Errorf("task output was unavailable: %w", taskErr))
			}
		}
		if stopReason != jobruntime.StopReasonNone {
			break
		}
	}

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
		case taskFailures > 0:
			stopReason = jobruntime.StopReasonTaskFailures
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

func (w *webrunner) runCheckpointTask(
	ctx context.Context,
	job *web.Job,
	seed scrapemate.IJob,
	task web.JobTask,
	exitMonitor exiter.Exiter,
	stopReasons chan<- jobruntime.StopReason,
	desiredConcurrency, effectiveConcurrency int,
	startedAt time.Time,
	databaseWrites int64,
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
	monitorCtx, monitorCancel := context.WithCancel(taskCtx)
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		w.monitorTaskResources(
			monitorCtx, job, task, exitMonitor, stopReasons,
			desiredConcurrency, effectiveConcurrency, startedAt, databaseWrites, taskCancel,
		)
	}()

	before := exiter.SnapshotOf(exitMonitor)
	runErr := mate.Start(taskCtx, seed)
	after := exiter.SnapshotOf(exitMonitor)
	monitorCancel()
	<-monitorDone
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

	return runPath, normalizeCheckpointRunError(taskCtx, runErr, after.SeedsCompleted > before.SeedsCompleted)
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

func (w *webrunner) monitorTaskResources(
	ctx context.Context,
	job *web.Job,
	task web.JobTask,
	exitMonitor exiter.Exiter,
	stopReasons chan<- jobruntime.StopReason,
	desiredConcurrency, effectiveConcurrency int,
	startedAt time.Time,
	databaseWrites int64,
	cancelTask context.CancelFunc,
) {
	ticker := time.NewTicker(resourceSampleInterval)
	defer ticker.Stop()
	for {
		sample, err := w.sampleWorkerResources(ctx)
		if err == nil {
			exitSnapshot := exiter.SnapshotOf(exitMonitor)
			elapsed := time.Since(startedAt)
			placesPerMinute := jobruntime.RatePerMinute(int64(exitSnapshot.PlacesCompleted), elapsed)
			_ = w.svc.UpdateJobWorkerProgress(context.Background(), job.ID, web.JobWorkerProgress{
				Stage: jobruntime.StageSearchingMaps, ActiveTasks: 1,
				PlacesPerMinute: placesPerMinute, CurrentQuery: task.Query, CurrentCell: task.SourceCell,
				BrowserCount: int64(max(1, job.Data.BrowserPool)), ActivePages: int64(max(1, effectiveConcurrency)),
				CPUPercent: sample.CPUPercent, MemoryBytes: sample.MemoryUsedBytes,
				DiskFreeBytes: sample.DiskFreeBytes, DatabaseWrites: databaseWrites,
				DesiredWorkers: int64(desiredConcurrency), EffectiveWorkers: int64(effectiveConcurrency),
				UpdatedAt: time.Now().UTC(),
			})
			if job.Data.LowDiskBytes > 0 && sample.DiskFreeBytes < job.Data.LowDiskBytes {
				select {
				case stopReasons <- jobruntime.StopReasonLowDisk:
				default:
				}
				_ = w.svc.RecordJobWorkerEvent(
					context.Background(), job.ID, "low-disk", "warning",
					"Free disk fell below the configured safety threshold; pausing at the current task checkpoint",
					map[string]any{"disk_free_bytes": sample.DiskFreeBytes, "threshold_bytes": job.Data.LowDiskBytes},
				)
				cancelTask()
				return
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *webrunner) sampleWorkerResources(ctx context.Context) (workerResourceSample, error) {
	if w.sampleResources != nil {
		return w.sampleResources(ctx, w.cfg.DataFolder)
	}

	return defaultWorkerResourceSample(ctx, w.cfg.DataFolder)
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

func adaptiveWorkerConcurrency(desired int, sample workerResourceSample, lowDiskThreshold uint64) int {
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
