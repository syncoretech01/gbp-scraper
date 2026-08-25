package acceptance

import (
	"encoding/json"
	"time"
)

// The types below mirror only the subset of the local API's JSON responses the
// harness records. They are deliberately decoupled from the web package: the
// harness must tolerate the application growing new fields (it ignores unknown
// keys) and must tolerate fields the classification specialist has not landed
// yet (it binds those defensively). Every response is wrapped by the local API
// envelope, so each parse first unwraps Data.

// apiEnvelope is the standard local-API response wrapper. Data carries the
// payload; Error is set instead on failures. Meta carries pagination and
// similar side-channel values.
type apiEnvelope struct {
	Data  json.RawMessage `json:"data"`
	Error *apiEnvelopeErr `json:"error"`
	Meta  json.RawMessage `json:"meta"`
}

// apiEnvelopeErr is the machine-readable error inside an envelope.
type apiEnvelopeErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// createJobResponse is the body of a successful POST /api/v1/jobs. This route
// returns the id at the top level rather than inside the standard envelope.
type createJobResponse struct {
	ID string `json:"id"`
}

// jobProgress mirrors GET /api/v1/jobs/{id}/progress. Only the fields the
// poll loop and the record need are declared; unknown keys are ignored.
type jobProgress struct {
	JobID        string             `json:"job_id"`
	Name         string             `json:"name"`
	State        string             `json:"state"`
	LegacyStatus string             `json:"legacy_status"`
	Stage        string             `json:"stage"`
	Percent      float64            `json:"percent"`
	Message      string             `json:"message"`
	StopReason   string             `json:"stop_reason"`
	Config       jobConfigSummary   `json:"config"`
	Results      resultStats        `json:"results"`
	Execution    *executionSnapshot `json:"execution"`
}

// jobConfigSummary mirrors the immutable configuration echo the progress DTO
// carries. The harness records the estimated plan size from it.
type jobConfigSummary struct {
	Keywords            []string `json:"keywords"`
	Language            string   `json:"language"`
	Latitude            string   `json:"latitude"`
	Longitude           string   `json:"longitude"`
	Zoom                int      `json:"zoom"`
	RadiusMetres        int      `json:"radius_metres"`
	Depth               int      `json:"depth"`
	FastMode            bool     `json:"fast_mode"`
	EmailCrawl          bool     `json:"email_crawl"`
	RuntimeLimitSeconds int64    `json:"runtime_limit_seconds"`
	GridBoundingBox     string   `json:"grid_bounding_box"`
	GridCellKM          float64  `json:"grid_cell_km"`
	EstimatedGridCells  int      `json:"estimated_grid_cells"`
	EstimatedSeedTasks  int      `json:"estimated_seed_tasks"`
}

// resultStats mirrors the file-backed per-job summary embedded in progress.
type resultStats struct {
	Rows             int `json:"rows"`
	UniqueBusinesses int `json:"unique_businesses"`
	Duplicates       int `json:"duplicates"`
	WithWebsite      int `json:"with_website"`
	WithPhone        int `json:"with_phone"`
	WithEmail        int `json:"with_email"`
}

// executionSnapshot mirrors the checkpoint/worker projection inside progress.
type executionSnapshot struct {
	Tasks            taskSummary    `json:"tasks"`
	Checkpoint       *jobCheckpoint `json:"checkpoint"`
	Progress         workerProgress `json:"progress"`
	RecoveryRequired bool           `json:"recovery_required"`
}

// taskSummary mirrors the aggregate durable task plan counters.
type taskSummary struct {
	Total     int64 `json:"total"`
	Pending   int64 `json:"pending"`
	Running   int64 `json:"running"`
	Completed int64 `json:"completed"`
	Failed    int64 `json:"failed"`
	Skipped   int64 `json:"skipped"`
	Retries   int64 `json:"retries"`
}

// jobCheckpoint mirrors the most recent durable resume boundary.
type jobCheckpoint struct {
	ID        int64     `json:"id"`
	TaskKey   string    `json:"task_key"`
	Stage     string    `json:"stage"`
	CreatedAt time.Time `json:"created_at"`
}

// workerProgress mirrors the live worker/resource evidence in the snapshot.
type workerProgress struct {
	ActiveTasks      int64   `json:"active_tasks"`
	Retries          int64   `json:"retries"`
	PlacesPerMinute  float64 `json:"places_per_minute"`
	BrowserCount     int64   `json:"browser_count"`
	ActivePages      int64   `json:"active_pages"`
	CPUPercent       float64 `json:"cpu_percent"`
	MemoryBytes      uint64  `json:"memory_bytes"`
	DesiredWorkers   int64   `json:"desired_workers"`
	EffectiveWorkers int64   `json:"effective_workers"`
}

// benchmarkReport mirrors GET /api/v1/jobs/{id}/benchmark. The harness reads
// the headline totals, the runtime block, and the coarse failure classes. A
// finer failure-kind breakdown the classification specialist may add later is
// captured defensively from Extra rather than declared here, so its absence is
// never an error.
type benchmarkReport struct {
	JobID         string           `json:"job_id"`
	JobName       string           `json:"job_name"`
	EngineVersion string           `json:"engine_version"`
	SchemaVersion int              `json:"schema_version"`
	Totals        benchmarkTotals  `json:"totals"`
	Failures      []failureClass   `json:"failures"`
	Runtime       benchmarkRuntime `json:"runtime"`

	// Extra retains every top-level key so a newly landed field (for example a
	// structured failure-kind map) is available without a code change here.
	Extra map[string]json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes the declared benchmark fields and also retains every
// top-level key in Extra for defensive, forward-compatible field binding.
func (b *benchmarkReport) UnmarshalJSON(data []byte) error {
	type alias benchmarkReport
	var typed alias
	if err := json.Unmarshal(data, &typed); err != nil {
		return err
	}
	*b = benchmarkReport(typed)

	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	b.Extra = raw

	return nil
}

// benchmarkTotals mirrors the headline scalar outcomes of one run.
type benchmarkTotals struct {
	TasksPlanned           int64   `json:"tasks_planned"`
	TasksExpanded          int64   `json:"tasks_expanded"`
	TasksCompleted         int64   `json:"tasks_completed"`
	TasksFailed            int64   `json:"tasks_failed"`
	TasksSkipped           int64   `json:"tasks_skipped"`
	Attempts               int64   `json:"attempts"`
	Retries                int64   `json:"retries"`
	RowsAdded              int64   `json:"rows_added"`
	RowsReplaced           int64   `json:"rows_replaced"`
	DuplicatesSkipped      int64   `json:"duplicates_skipped"`
	DuplicateRate          float64 `json:"duplicate_rate"`
	UniqueBusinesses       int64   `json:"unique_businesses"`
	TotalDiscoveredRows    int64   `json:"total_discovered_rows"`
	NewBusinessesPerMinute float64 `json:"new_businesses_per_minute"`
}

// failureClass mirrors one coarse failure grouping in the benchmark report.
type failureClass struct {
	Class   string `json:"class"`
	Count   int64  `json:"count"`
	Retries int64  `json:"retries"`
	Sample  string `json:"sample"`
}

// benchmarkRuntime mirrors the wall-clock evidence in the benchmark report.
type benchmarkRuntime struct {
	CreatedAt        int64   `json:"created_at"`
	StartedAt        int64   `json:"started_at"`
	FinishedAt       int64   `json:"finished_at"`
	WallSeconds      float64 `json:"wall_seconds"`
	TasksPerMinute   float64 `json:"tasks_per_minute"`
	RawRecords       int64   `json:"raw_records"`
	UniqueRecords    int64   `json:"unique_records"`
	DuplicateRecords int64   `json:"duplicate_records"`
}

// coverageReport mirrors the subset of GET /api/v1/jobs/{id}/coverage the
// harness records: the plan totals and the adaptive saturation summary.
type coverageReport struct {
	Totals     coverageTotals     `json:"totals"`
	Saturation coverageSaturation `json:"saturation"`
}

// coverageTotals mirrors the durable plan aggregate in the coverage report.
type coverageTotals struct {
	TasksTotal        int64 `json:"tasks_total"`
	TasksDone         int64 `json:"tasks_done"`
	TasksFailed       int64 `json:"tasks_failed"`
	TasksSkipped      int64 `json:"tasks_skipped"`
	RowsAdded         int64 `json:"rows_added"`
	RowsReplaced      int64 `json:"rows_replaced"`
	DuplicatesSkipped int64 `json:"duplicates_skipped"`
	ExpansionsAdded   int64 `json:"expansions_added"`
	RefinementsAdded  int64 `json:"refinements_added"`
	TasksTruncated    int64 `json:"tasks_truncated"`
}

// coverageSaturation mirrors the adaptive-stop evidence in coverage.
type coverageSaturation struct {
	Enabled         bool    `json:"enabled"`
	Window          int     `json:"window"`
	CurrentNewRatio float64 `json:"current_new_ratio"`
	Stopped         bool    `json:"stopped"`
	Reason          string  `json:"reason"`
	WindowSamples   int     `json:"window_samples"`
	EmptySamples    int     `json:"empty_samples"`
}

// systemMetrics mirrors the subset of GET /api/v1/system/metrics the harness
// records as app-reported resource evidence. These figures are host-wide, not
// scoped to the single job, which the record labels explicitly.
type systemMetrics struct {
	Status      string          `json:"status"`
	CollectedAt time.Time       `json:"collected_at"`
	Resources   systemResources `json:"resources"`
	Database    systemDatabase  `json:"database"`
}

// systemResources mirrors the app-reported CPU/memory snapshot.
type systemResources struct {
	CPUPercent        float64 `json:"cpu_percent"`
	LogicalCPUs       int     `json:"logical_cpus"`
	MemoryTotalBytes  uint64  `json:"memory_total_bytes"`
	MemoryUsedBytes   uint64  `json:"memory_used_bytes"`
	MemoryUsedPercent float64 `json:"memory_used_percent"`
}

// systemDatabase mirrors the queue/browser counters in system metrics.
type systemDatabase struct {
	RunningJobs    int64 `json:"running_jobs"`
	QueuedJobs     int64 `json:"queued_jobs"`
	ActiveBrowsers int64 `json:"active_browsers"`
	ActivePages    int64 `json:"active_pages"`
}

// resultsMeta mirrors the pagination meta returned by GET /api/v1/results.
type resultsMeta struct {
	Total  int64 `json:"total"`
	Limit  int64 `json:"limit"`
	Offset int64 `json:"offset"`
}
