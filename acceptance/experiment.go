package acceptance

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	// RecordSchema versions the durable ExperimentRecord shape. Two records
	// with the same schema are field-for-field comparable.
	RecordSchema = "acceptance/v1"
	// HarnessVersion identifies the harness build that produced a record.
	HarnessVersion = "1"

	// defaultLanguage is the two-letter language used when a request omits one.
	defaultLanguage = "en"
	// defaultZoom mirrors the wizard's default map zoom.
	defaultZoom = 15
	// defaultDepth mirrors the wizard's default scroll depth.
	defaultDepth = 10
)

// ModeBrowser and ModeFast are the two collection modes an experiment records.
// Fast mode is the pure-HTTP stealth fetcher; browser mode drives Chromium.
const (
	ModeBrowser = "browser"
	ModeFast    = "fast"
)

// ConnectionDirect and ConnectionProxy label how a run reached the platform.
const (
	ConnectionDirect = "direct"
	ConnectionProxy  = "proxy"
)

// JobRequest is the configuration the harness posts to create one scrape job.
// It is a stable, minimal projection of the application's job data: only the
// knobs the acceptance experiments vary are exposed.
type JobRequest struct {
	Name            string          `json:"name"`
	Keywords        []string        `json:"keywords"`
	Language        string          `json:"language"`
	Zoom            int             `json:"zoom"`
	Depth           int             `json:"depth"`
	FastMode        bool            `json:"fast_mode"`
	Email           bool            `json:"email"`
	Radius          int             `json:"radius"`
	Lat             string          `json:"lat"`
	Lon             string          `json:"lon"`
	RuntimeSeconds  int64           `json:"runtime_seconds"`
	Concurrency     int             `json:"concurrency"`
	TaskWorkers     int             `json:"task_workers"`
	BrowserPool     int             `json:"browser_pool_size"`
	PagesPerBrowser int             `json:"pages_per_browser"`
	GridBBox        string          `json:"grid_bbox"`
	GridCellKM      float64         `json:"grid_cell_km"`
	Adaptive        bool            `json:"adaptive_performance"`
	ProxyPoolID     string          `json:"proxy_pool_id"`
	Coverage        json.RawMessage `json:"coverage,omitempty"`
}

// Validate reports the configuration errors the local API would also reject,
// so a bad experiment fails before a job is queued.
func (r JobRequest) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("acceptance: job name is required")
	}
	if len(r.keywords()) == 0 {
		return errors.New("acceptance: at least one keyword is required")
	}
	if len(r.language()) != 2 {
		return errors.New("acceptance: language must be a two-letter code")
	}
	if r.RuntimeSeconds <= 0 {
		return errors.New("acceptance: runtime_seconds must be positive")
	}
	if r.Concurrency < 0 || r.Concurrency > 64 {
		return errors.New("acceptance: concurrency must be between 0 and 64")
	}
	if r.TaskWorkers < 0 || r.TaskWorkers > 16 {
		return errors.New("acceptance: task_workers must be between 0 and 16")
	}

	return nil
}

func (r JobRequest) keywords() []string {
	trimmed := make([]string, 0, len(r.Keywords))
	for _, keyword := range r.Keywords {
		if value := strings.TrimSpace(keyword); value != "" {
			trimmed = append(trimmed, value)
		}
	}

	return trimmed
}

func (r JobRequest) language() string {
	if strings.TrimSpace(r.Language) == "" {
		return defaultLanguage
	}

	return strings.TrimSpace(r.Language)
}

func (r JobRequest) mode() string {
	if r.FastMode {
		return ModeFast
	}

	return ModeBrowser
}

func (r JobRequest) connection() string {
	if strings.TrimSpace(r.ProxyPoolID) != "" {
		return ConnectionProxy
	}

	return ConnectionDirect
}

// toWire renders the request as the JSON body POST /api/v1/jobs expects. Only
// meaningful fields are included so zero values never trip server validation,
// and max_time is sent in seconds, which the route multiplies by one second.
func (r JobRequest) toWire() map[string]any {
	zoom := r.Zoom
	if zoom == 0 {
		zoom = defaultZoom
	}
	depth := r.Depth
	if depth == 0 {
		depth = defaultDepth
	}

	body := map[string]any{
		"Name":      r.Name,
		"keywords":  r.keywords(),
		"lang":      r.language(),
		"zoom":      zoom,
		"depth":     depth,
		"max_time":  r.RuntimeSeconds,
		"fast_mode": r.FastMode,
		"email":     r.Email,
	}
	if r.Radius > 0 {
		body["radius"] = r.Radius
	}
	if strings.TrimSpace(r.Lat) != "" {
		body["lat"] = r.Lat
	}
	if strings.TrimSpace(r.Lon) != "" {
		body["lon"] = r.Lon
	}
	if r.Concurrency > 0 {
		body["concurrency"] = r.Concurrency
	}
	if r.TaskWorkers > 0 {
		body["task_workers"] = r.TaskWorkers
	}
	if r.BrowserPool > 0 {
		body["browser_pool_size"] = r.BrowserPool
	}
	if r.PagesPerBrowser > 0 {
		body["pages_per_browser"] = r.PagesPerBrowser
	}
	if strings.TrimSpace(r.GridBBox) != "" {
		body["grid_bbox"] = r.GridBBox
	}
	if r.GridCellKM > 0 {
		body["grid_cell_km"] = r.GridCellKM
	}
	if r.Adaptive {
		body["adaptive_performance"] = true
	}
	if strings.TrimSpace(r.ProxyPoolID) != "" {
		body["proxy_pool_id"] = r.ProxyPoolID
	}
	if len(r.Coverage) > 0 {
		body["coverage"] = json.RawMessage(r.Coverage)
	}

	return body
}

// ExperimentConfig names one experiment and the job it runs. ID is the short
// name used on the command line and in the record ("A".."E", "sparse", …);
// Label is a human description; Job is the exact configuration posted.
type ExperimentConfig struct {
	ID     string
	Label  string
	Job    JobRequest
	Repeat int
}

// ExperimentRecord is the durable, comparable result of one experiment run.
// Every field is always present so two records diff position for position and
// two code versions can be compared field by field.
type ExperimentRecord struct {
	Schema         string          `json:"schema"`
	HarnessVersion string          `json:"harness_version"`
	Experiment     string          `json:"experiment"`
	Label          string          `json:"label"`
	Config         recordedConfig  `json:"config"`
	Run            recordedRun     `json:"run"`
	Outcomes       recordedOutcome `json:"outcomes"`
	Concurrency    recordedConc    `json:"concurrency"`
	Resources      recordedRes     `json:"resources_app_reported"`
	Recovery       recordedRec     `json:"recovery"`
	Availability   recordedAvail   `json:"availability"`
}

// recordedConfig echoes the exact configuration a run was created with.
type recordedConfig struct {
	BaseURL             string   `json:"base_url"`
	Mode                string   `json:"mode"`
	Connection          string   `json:"connection"`
	ProxyPoolID         string   `json:"proxy_pool_id"`
	Enrichment          bool     `json:"enrichment"`
	Keywords            []string `json:"queries"`
	QueryCount          int      `json:"query_count"`
	GridBBox            string   `json:"grid_bbox"`
	GridCellKM          float64  `json:"grid_cell_km"`
	EstimatedGridCells  int      `json:"estimated_grid_cells"`
	EstimatedSeedTasks  int      `json:"estimated_seed_tasks"`
	Concurrency         int      `json:"concurrency"`
	TaskWorkers         int      `json:"task_workers"`
	BrowserPool         int      `json:"browser_pool_size"`
	PagesPerBrowser     int      `json:"pages_per_browser"`
	RuntimeLimitSeconds int64    `json:"runtime_limit_seconds"`
	Zoom                int      `json:"zoom"`
	Depth               int      `json:"depth"`
	Language            string   `json:"language"`
}

// recordedRun captures the lifecycle of the single job the experiment drove.
type recordedRun struct {
	JobID         string    `json:"job_id"`
	StartedAtWall time.Time `json:"harness_started_at"`
	EndedAtWall   time.Time `json:"harness_ended_at"`
	CreatedAtUnix int64     `json:"job_created_at_unix"`
	StartedAtUnix int64     `json:"job_started_at_unix"`
	FinishedAtUn  int64     `json:"job_finished_at_unix"`
	WallSeconds   float64   `json:"wall_seconds"`
	TerminalState string    `json:"terminal_state"`
	StopReason    string    `json:"stop_reason"`
	PollCount     int       `json:"poll_count"`
	TimedOut      bool      `json:"timed_out"`
	Error         string    `json:"error"`
}

// recordedOutcome is the headline productivity and reliability of the run.
type recordedOutcome struct {
	DiscoveredRows         int64            `json:"discovered_rows"`
	UniqueBusinesses       int64            `json:"unique_businesses"`
	ResultsTotal           int64            `json:"normalized_results_total"`
	RowsPerMinute          float64          `json:"rows_per_minute"`
	NewBusinessesPerMinute float64          `json:"new_businesses_per_minute"`
	DuplicateRate          float64          `json:"duplicate_rate"`
	DuplicateCount         int64            `json:"duplicate_count"`
	TaskSuccessRate        float64          `json:"task_success_rate"`
	BrowserFailureRate     float64          `json:"browser_failure_rate"`
	BlockRate              float64          `json:"block_rate"`
	RetryCount             int64            `json:"retry_count"`
	Tasks                  recordedTasks    `json:"tasks"`
	FailureClasses         []failureClass   `json:"failure_classes"`
	FailureKinds           map[string]int64 `json:"failure_kinds"`
	EventsByType           map[string]int64 `json:"events_by_type"`
}

// recordedTasks is the terminal task-plan breakdown.
type recordedTasks struct {
	Total     int64 `json:"total"`
	Completed int64 `json:"completed"`
	Failed    int64 `json:"failed"`
	Skipped   int64 `json:"skipped"`
	Pending   int64 `json:"pending"`
	Running   int64 `json:"running"`
}

// recordedConc is the reconstructed effective concurrency evidence.
type recordedConc struct {
	Desired            int    `json:"desired"`
	PlannedWorkers     int    `json:"planned_workers"`
	PerTaskConcurrency int    `json:"per_task_concurrency"`
	PlannedEffective   int    `json:"planned_effective"`
	FinalEffective     int    `json:"final_effective"`
	AdaptiveReductions int    `json:"adaptive_reductions"`
	EffectiveWorkers   int64  `json:"effective_workers_reported"`
	Source             string `json:"source"`
}

// recordedRes is the app-reported host resource snapshot, taken while the run
// was active where possible. It is host-wide, not scoped to the single job.
type recordedRes struct {
	Label             string    `json:"label"`
	CPUPercent        float64   `json:"cpu_percent"`
	LogicalCPUs       int       `json:"logical_cpus"`
	MemoryUsedBytes   uint64    `json:"memory_used_bytes"`
	MemoryUsedPercent float64   `json:"memory_used_percent"`
	MemoryTotalBytes  uint64    `json:"memory_total_bytes"`
	PeakActiveBrowser int64     `json:"peak_active_browsers"`
	PeakActivePages   int64     `json:"peak_active_pages"`
	SampleCount       int       `json:"sample_count"`
	CollectedAt       time.Time `json:"collected_at"`
}

// recordedRec is the checkpoint and recovery outcome of the run.
type recordedRec struct {
	CheckpointPresent   bool   `json:"checkpoint_present"`
	CheckpointTaskKey   string `json:"checkpoint_task_key"`
	RecoveryRequired    bool   `json:"recovery_required"`
	TasksRemainingAtEnd int64  `json:"tasks_remaining_at_end"`
	CoverageStopped     bool   `json:"coverage_stopped"`
	CoverageStopReason  string `json:"coverage_stop_reason"`
}

// recordedAvail records which readback endpoints answered, so a record with
// zeroed metrics can be told apart from an endpoint that was unavailable.
type recordedAvail struct {
	Progress  bool `json:"progress"`
	Benchmark bool `json:"benchmark"`
	Coverage  bool `json:"coverage"`
	Logs      bool `json:"logs"`
	Events    bool `json:"events"`
	Results   bool `json:"results"`
	Metrics   bool `json:"metrics"`
}
