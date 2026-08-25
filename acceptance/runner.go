package acceptance

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// defaultPollInterval is how often the poll loop reads job progress. It is
	// well under the local API rate limit even with a second metrics call per
	// tick.
	defaultPollInterval = 5 * time.Second
	// maxWaitSlack is added to the job's runtime limit to bound how long the
	// harness waits for a terminal state, covering startup and teardown.
	maxWaitSlack = 5 * time.Minute
)

// terminalStates are the lifecycle states after which no more work runs
// without an explicit restart.
var terminalStates = map[string]struct{}{
	"completed": {},
	"partial":   {},
	"failed":    {},
	"cancelled": {},
}

// RunOptions tunes one experiment run. The zero value is valid and uses the
// defaults; tests inject a clock and a short interval.
type RunOptions struct {
	// PollInterval is the gap between progress polls. Zero uses the default.
	PollInterval time.Duration
	// MaxWait bounds how long the harness waits for a terminal state. Zero
	// derives it from the job's runtime limit plus slack.
	MaxWait time.Duration
	// SampleResources reads app-reported system metrics on every poll to record
	// peak browser/page counts and a peak-memory resource snapshot.
	SampleResources bool
	// Now injects the clock; nil uses time.Now. Tests set it for determinism.
	Now func() time.Time
	// sleep injects the wait between polls; nil uses a context-aware sleep.
	sleep func(context.Context, time.Duration)
}

func (o RunOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}

	return time.Now().UTC()
}

func (o RunOptions) pollInterval() time.Duration {
	if o.PollInterval > 0 {
		return o.PollInterval
	}

	return defaultPollInterval
}

func (o RunOptions) maxWait(runtimeSeconds int64) time.Duration {
	if o.MaxWait > 0 {
		return o.MaxWait
	}

	return time.Duration(runtimeSeconds)*time.Second + maxWaitSlack
}

func (o RunOptions) waitFor(ctx context.Context, d time.Duration) {
	if o.sleep != nil {
		o.sleep(ctx, d)

		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// resourceAggregate accumulates app-reported resource evidence across polls.
type resourceAggregate struct {
	samples       int
	peakBrowsers  int64
	peakPages     int64
	peakMemPct    float64
	snapshot      systemResources
	logicalCPUs   int
	memTotalBytes uint64
	collectedAt   time.Time
	peakCPU       float64
}

func (a *resourceAggregate) observe(metrics systemMetrics) {
	a.samples++
	if metrics.Database.ActiveBrowsers > a.peakBrowsers {
		a.peakBrowsers = metrics.Database.ActiveBrowsers
	}
	if metrics.Database.ActivePages > a.peakPages {
		a.peakPages = metrics.Database.ActivePages
	}
	if metrics.Resources.CPUPercent > a.peakCPU {
		a.peakCPU = metrics.Resources.CPUPercent
	}
	// Keep the snapshot from the highest memory pressure seen, which is the
	// most representative reading of the run's peak load.
	if metrics.Resources.MemoryUsedPercent >= a.peakMemPct || a.samples == 1 {
		a.peakMemPct = metrics.Resources.MemoryUsedPercent
		a.snapshot = metrics.Resources
		a.collectedAt = metrics.CollectedAt
	}
	a.logicalCPUs = metrics.Resources.LogicalCPUs
	a.memTotalBytes = metrics.Resources.MemoryTotalBytes
}

func (a *resourceAggregate) record() recordedRes {
	return recordedRes{
		Label:             "app-reported (GET /api/v1/system/metrics); host-wide, not scoped to this job",
		CPUPercent:        roundRatio(a.peakCPU),
		LogicalCPUs:       a.logicalCPUs,
		MemoryUsedBytes:   a.snapshot.MemoryUsedBytes,
		MemoryUsedPercent: roundRatio(a.snapshot.MemoryUsedPercent),
		MemoryTotalBytes:  a.memTotalBytes,
		PeakActiveBrowser: a.peakBrowsers,
		PeakActivePages:   a.peakPages,
		SampleCount:       a.samples,
		CollectedAt:       a.collectedAt,
	}
}

// RunExperiment drives one job through the local API and returns its record.
//
// The returned record is always populated, even when the job fails or a
// readback endpoint is unavailable: endpoint availability is captured in the
// record rather than raised as an error. A non-nil error is returned only for
// a fatal condition, such as being unable to create the job at all; the record
// is still returned so it can be persisted for a post-mortem.
func RunExperiment(ctx context.Context, client *Client, config ExperimentConfig, options RunOptions) (ExperimentRecord, error) {
	record := ExperimentRecord{
		Schema:         RecordSchema,
		HarnessVersion: HarnessVersion,
		Experiment:     config.ID,
		Label:          config.Label,
		Config:         echoConfig(client.BaseURL(), config.Job),
	}
	record.Run.StartedAtWall = options.now()

	if err := config.Job.Validate(); err != nil {
		record.Run.Error = err.Error()
		record.Run.EndedAtWall = options.now()

		return record, err
	}

	jobID, err := client.CreateJob(ctx, config.Job)
	if err != nil {
		record.Run.Error = err.Error()
		record.Run.EndedAtWall = options.now()

		return record, fmt.Errorf("acceptance: create job for experiment %s: %w", config.ID, err)
	}
	record.Run.JobID = jobID

	progress, resources := pollToTerminal(ctx, client, jobID, options, &record)

	// Stamp the end of the active run before gathering readbacks so the
	// harness wall-clock fallback (used only when the benchmark report is
	// unavailable) measures elapsed time rather than reading a zero timestamp.
	record.Run.EndedAtWall = options.now()

	gatherReadbacks(ctx, client, jobID, progress, resources, &record)

	return record, nil
}

// RunRepeated runs one experiment configuration config.Repeat times (at least
// once) and returns every record together with a repeatability report of the
// headline metrics. Running the same configuration twice and comparing the
// variance is how the harness measures how trustworthy a single result is. A
// fatal error stops further repeats but the records gathered so far are still
// returned so a partial series can be inspected.
func RunRepeated(ctx context.Context, client *Client, config ExperimentConfig, options RunOptions) ([]ExperimentRecord, RepeatabilityReport, error) {
	repeats := config.Repeat
	if repeats < 1 {
		repeats = 1
	}

	records := make([]ExperimentRecord, 0, repeats)
	var runErr error
	for attempt := 0; attempt < repeats; attempt++ {
		record, err := RunExperiment(ctx, client, config, options)
		records = append(records, record)
		if err != nil {
			runErr = err

			break
		}
		if ctx.Err() != nil {
			runErr = ctx.Err()

			break
		}
	}

	return records, Repeatability(records), runErr
}

// pollToTerminal polls job progress until the run reaches a terminal state,
// the max wait elapses, or the context is cancelled. It samples resources on
// every poll and returns the last progress seen and the resource aggregate.
func pollToTerminal(
	ctx context.Context,
	client *Client,
	jobID string,
	options RunOptions,
	record *ExperimentRecord,
) (jobProgress, resourceAggregate) {
	deadline := options.now().Add(options.maxWait(record.Config.RuntimeLimitSeconds))
	interval := options.pollInterval()

	var last jobProgress
	var resources resourceAggregate

	for {
		progress, err := client.Progress(ctx, jobID)
		record.Run.PollCount++
		if err == nil {
			last = progress
			record.Availability.Progress = true
		}

		if options.SampleResources {
			if metrics, metricsErr := client.SystemMetrics(ctx); metricsErr == nil {
				resources.observe(metrics)
				record.Availability.Metrics = true
			}
		}

		if err == nil && isTerminal(progress.State) {
			return last, resources
		}

		if ctx.Err() != nil {
			record.Run.TimedOut = true

			return last, resources
		}
		if !options.now().Before(deadline) {
			record.Run.TimedOut = true

			return last, resources
		}

		options.waitFor(ctx, interval)
		if ctx.Err() != nil {
			record.Run.TimedOut = true

			return last, resources
		}
	}
}

// gatherReadbacks fetches every durable readback for a terminal job and folds
// it into the record.
func gatherReadbacks(
	ctx context.Context,
	client *Client,
	jobID string,
	progress jobProgress,
	resources resourceAggregate,
	record *ExperimentRecord,
) {
	record.Run.TerminalState = progress.State
	record.Run.StopReason = progress.StopReason

	benchmark, benchmarkOK, _ := client.Benchmark(ctx, jobID)
	record.Availability.Benchmark = benchmarkOK

	coverage, coverageOK, _ := client.Coverage(ctx, jobID)
	record.Availability.Coverage = coverageOK

	logText, logsOK, _ := client.Logs(ctx, jobID)
	record.Availability.Logs = logsOK

	events, eventsErr := client.Events(ctx, jobID)
	record.Availability.Events = eventsErr == nil && len(events) > 0

	resultsTotal, resultsOK, _ := client.ResultsTotal(ctx, jobID)
	record.Availability.Results = resultsOK

	record.Resources = resources.record()

	if record.Availability.Progress {
		record.Config.EstimatedGridCells = progress.Config.EstimatedGridCells
		record.Config.EstimatedSeedTasks = progress.Config.EstimatedSeedTasks
	}

	computeOutcomes(progress, benchmark, benchmarkOK, resultsTotal, events, record)
	computeConcurrency(progress, events, logText, record)
	computeRecovery(progress, coverage, coverageOK, record)
}

// computeOutcomes derives the headline productivity and reliability metrics.
func computeOutcomes(
	progress jobProgress,
	benchmark benchmarkReport,
	benchmarkOK bool,
	resultsTotal int64,
	events []workerEvent,
	record *ExperimentRecord,
) {
	tasks := taskSummary{}
	if progress.Execution != nil {
		tasks = progress.Execution.Tasks
	}

	wallSeconds := runWallSeconds(progress, benchmark, benchmarkOK, record)
	record.Run.WallSeconds = roundRatio(wallSeconds)

	discovered := discoveredRows(progress, benchmark, benchmarkOK)
	unique := uniqueBusinesses(progress, benchmark, benchmarkOK, resultsTotal)

	// Prefer a structured fine failure-kind breakdown the classification
	// specialist may have landed on the benchmark report; otherwise measure it
	// from the worker events; otherwise derive it from the coarse classes.
	failureKinds := failureKindsFromBenchmarkExtra(benchmark)
	if len(failureKinds) == 0 {
		failureKinds = failureKindsFromEvents(events)
	}
	if len(failureKinds) == 0 && benchmarkOK {
		failureKinds = failureKindsFromBenchmark(benchmark.Failures)
	}

	finished := finishedTasks(tasks)
	browserFailures := sumKinds(failureKinds, browserFailureKinds)
	blocks := sumKinds(failureKinds, blockKinds)

	record.Outcomes = recordedOutcome{
		DiscoveredRows:         discovered,
		UniqueBusinesses:       unique,
		ResultsTotal:           resultsTotal,
		RowsPerMinute:          rowsPerMinute(discovered, wallSeconds),
		NewBusinessesPerMinute: benchmark.Totals.NewBusinessesPerMinute,
		DuplicateRate:          benchmark.Totals.DuplicateRate,
		DuplicateCount:         benchmark.Totals.DuplicatesSkipped,
		TaskSuccessRate:        taskSuccessRate(tasks),
		BrowserFailureRate:     rateAgainstFinished(browserFailures, finished),
		BlockRate:              rateAgainstFinished(blocks, finished),
		RetryCount:             retryCount(tasks, benchmark, benchmarkOK),
		Tasks: recordedTasks{
			Total:     tasks.Total,
			Completed: tasks.Completed,
			Failed:    tasks.Failed,
			Skipped:   tasks.Skipped,
			Pending:   tasks.Pending,
			Running:   tasks.Running,
		},
		FailureClasses: benchmark.Failures,
		FailureKinds:   failureKinds,
		EventsByType:   eventsByType(events),
	}
}

// computeConcurrency reconstructs the effective concurrency evidence.
func computeConcurrency(progress jobProgress, events []workerEvent, logText string, record *ExperimentRecord) {
	evidence := concurrencyFromEvents(events, logText)
	record.Concurrency = recordedConc{
		Desired:            evidence.Desired,
		PlannedWorkers:     evidence.PlannedWorkers,
		PerTaskConcurrency: evidence.PerTaskConcurrency,
		PlannedEffective:   evidence.PlannedEffective,
		FinalEffective:     evidence.FinalEffective,
		AdaptiveReductions: evidence.AdaptiveReductions,
		Source:             evidence.Source,
	}
	if progress.Execution != nil {
		record.Concurrency.EffectiveWorkers = progress.Execution.Progress.EffectiveWorkers
	}
}

// computeRecovery captures the checkpoint and recovery outcome.
func computeRecovery(progress jobProgress, coverage coverageReport, coverageOK bool, record *ExperimentRecord) {
	recovery := recordedRec{}
	if progress.Execution != nil {
		recovery.RecoveryRequired = progress.Execution.RecoveryRequired
		recovery.TasksRemainingAtEnd = progress.Execution.Tasks.Pending + progress.Execution.Tasks.Running
		if progress.Execution.Checkpoint != nil {
			recovery.CheckpointPresent = true
			recovery.CheckpointTaskKey = progress.Execution.Checkpoint.TaskKey
		}
	}
	if coverageOK {
		recovery.CoverageStopped = coverage.Saturation.Stopped
		recovery.CoverageStopReason = coverage.Saturation.Reason
	}
	record.Recovery = recovery
}

// runWallSeconds picks the most authoritative active-runtime measurement.
func runWallSeconds(progress jobProgress, benchmark benchmarkReport, benchmarkOK bool, record *ExperimentRecord) float64 {
	if benchmarkOK && benchmark.Runtime.WallSeconds > 0 {
		record.Run.CreatedAtUnix = benchmark.Runtime.CreatedAt
		record.Run.StartedAtUnix = benchmark.Runtime.StartedAt
		record.Run.FinishedAtUn = benchmark.Runtime.FinishedAt

		return benchmark.Runtime.WallSeconds
	}

	return record.Run.EndedAtWall.Sub(record.Run.StartedAtWall).Seconds()
}

// discoveredRows picks the discovered-row count from the richest source.
func discoveredRows(progress jobProgress, benchmark benchmarkReport, benchmarkOK bool) int64 {
	if benchmarkOK && benchmark.Totals.TotalDiscoveredRows > 0 {
		return benchmark.Totals.TotalDiscoveredRows
	}
	if benchmarkOK && benchmark.Runtime.RawRecords > 0 {
		return benchmark.Runtime.RawRecords
	}

	return int64(progress.Results.Rows)
}

// uniqueBusinesses picks the unique-business count from the richest source.
func uniqueBusinesses(progress jobProgress, benchmark benchmarkReport, benchmarkOK bool, resultsTotal int64) int64 {
	if benchmarkOK && benchmark.Totals.UniqueBusinesses > 0 {
		return benchmark.Totals.UniqueBusinesses
	}
	if progress.Results.UniqueBusinesses > 0 {
		return int64(progress.Results.UniqueBusinesses)
	}

	return resultsTotal
}

// retryCount picks the retry total from the richest source.
func retryCount(tasks taskSummary, benchmark benchmarkReport, benchmarkOK bool) int64 {
	if benchmarkOK && benchmark.Totals.Retries > 0 {
		return benchmark.Totals.Retries
	}

	return tasks.Retries
}

// echoConfig records the exact configuration a run was created with.
func echoConfig(baseURL string, job JobRequest) recordedConfig {
	zoom := job.Zoom
	if zoom == 0 {
		zoom = defaultZoom
	}
	depth := job.Depth
	if depth == 0 {
		depth = defaultDepth
	}

	return recordedConfig{
		BaseURL:             baseURL,
		Mode:                job.mode(),
		Connection:          job.connection(),
		ProxyPoolID:         strings.TrimSpace(job.ProxyPoolID),
		Enrichment:          job.Email,
		Keywords:            job.keywords(),
		QueryCount:          len(job.keywords()),
		GridBBox:            strings.TrimSpace(job.GridBBox),
		GridCellKM:          job.GridCellKM,
		Concurrency:         job.Concurrency,
		TaskWorkers:         job.TaskWorkers,
		BrowserPool:         job.BrowserPool,
		PagesPerBrowser:     job.PagesPerBrowser,
		RuntimeLimitSeconds: job.RuntimeSeconds,
		Zoom:                zoom,
		Depth:               depth,
		Language:            job.language(),
	}
}

// isTerminal reports whether a lifecycle state ends the run.
func isTerminal(state string) bool {
	_, ok := terminalStates[state]

	return ok
}
