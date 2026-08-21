package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/grid"
)

var jobs []Job

const (
	StatusPending = "pending"
	StatusWorking = "working"
	StatusOK      = "ok"
	StatusFailed  = "failed"
)

type SelectParams struct {
	Status string
	Limit  int
}

type JobRepository interface {
	Get(context.Context, string) (Job, error)
	Create(context.Context, *Job) error
	Delete(context.Context, string) error
	Select(context.Context, SelectParams) ([]Job, error)
	Update(context.Context, *Job) error
}

type Job struct {
	ID     string
	Name   string
	Date   time.Time
	Status string
	Data   JobData
}

func (j *Job) Validate() error {
	if j.ID == "" {
		return errors.New("missing id")
	}

	if j.Name == "" {
		return errors.New("missing name")
	}

	if j.Status == "" {
		return errors.New("missing status")
	}

	if j.Date.IsZero() {
		return errors.New("missing date")
	}

	if err := j.Data.Validate(); err != nil {
		return err
	}

	return nil
}

// MaximumJobTaskWorkers bounds how many plan tasks one job may run in parallel.
// The job's browser budget is divided between them, so a higher value trades
// per-task capacity for finer resume granularity rather than adding load.
const MaximumJobTaskWorkers = 16

// Checkpoint interval bounds. A job writes a safe resume boundary after every
// completed task; this interval adds a time-based one in between, so an
// interrupted long task still reports how recently it was making progress.
const (
	// DefaultCheckpointSeconds is used when a job does not choose an interval.
	DefaultCheckpointSeconds = 30
	// MinimumCheckpointSeconds keeps the interval from becoming a write loop.
	MinimumCheckpointSeconds = 5
	// MaximumCheckpointSeconds keeps a configured interval meaningful.
	MaximumCheckpointSeconds = 3600
)

// CheckpointInterval returns the effective interval between time-based
// checkpoints for one job.
func (d JobData) CheckpointInterval() time.Duration {
	seconds := d.CheckpointSeconds
	if seconds < MinimumCheckpointSeconds || seconds > MaximumCheckpointSeconds {
		seconds = DefaultCheckpointSeconds
	}

	return time.Duration(seconds) * time.Second
}

type JobData struct {
	Keywords        []string              `json:"keywords"`
	Lang            string                `json:"lang"`
	Zoom            int                   `json:"zoom"`
	Lat             string                `json:"lat"`
	Lon             string                `json:"lon"`
	LocationLabel   string                `json:"location_label,omitempty"`
	FastMode        bool                  `json:"fast_mode"`
	Radius          int                   `json:"radius"`
	Depth           int                   `json:"depth"`
	Email           bool                  `json:"email"`
	Enrichment      *JobEnrichmentOptions `json:"enrichment,omitempty"`
	ExtraReviews    bool                  `json:"extra_reviews"`
	MaxTime         time.Duration         `json:"max_time"`
	Concurrency     int                   `json:"concurrency,omitempty"`
	TaskWorkers     int                   `json:"task_workers,omitempty"`
	BrowserPool     int                   `json:"browser_pool_size,omitempty"`
	PagesBrowser    int                   `json:"pages_per_browser,omitempty"`
	MaxRecords      int                   `json:"max_records,omitempty"`
	RetryCount      int                   `json:"retry_count,omitempty"`
	RetryDelay      time.Duration         `json:"retry_delay,omitempty"`
	RetryConfigured bool                  `json:"retry_configured,omitempty"`
	PageTimeout     time.Duration         `json:"page_timeout,omitempty"`
	RandomDelayMin  time.Duration         `json:"random_delay_min,omitempty"`
	RandomDelayMax  time.Duration         `json:"random_delay_max,omitempty"`
	Headfull        bool                  `json:"headfull,omitempty"`
	LoadImages      bool                  `json:"load_images,omitempty"`
	Adaptive        bool                  `json:"adaptive_performance,omitempty"`
	// CheckpointSeconds is how often the running job writes a time-based safe
	// resume boundary in addition to the one written after every completed
	// task. Zero keeps the default interval.
	CheckpointSeconds int      `json:"checkpoint_seconds,omitempty"`
	LowDiskBytes      uint64   `json:"low_disk_bytes,omitempty"`
	ProxyPoolID       string   `json:"proxy_pool_id,omitempty"`
	Proxies           []string `json:"proxies"`
	SavedAreaID       string   `json:"saved_area_id,omitempty"`
	AreaGeoJSON       string   `json:"area_geojson,omitempty"`
	GridBBox          string   `json:"grid_bbox,omitempty"`
	GridCellKM        float64  `json:"grid_cell_km,omitempty"`
	IncrementalMode   string   `json:"incremental_mode,omitempty"`
	// Coverage enables the adaptive discovery engine for this job. A nil
	// value keeps exactly the historical behaviour: no saturation stop and
	// no mid-run expansion.
	Coverage *CoverageOptions `json:"coverage,omitempty"`
}

// Incremental rescan modes. An empty mode is a full collection; the other
// modes narrow what a rescan keeps, while new/changed/disappeared detection
// always happens when results are imported.
const (
	IncrementalModeNewOnly    = "new_only"
	IncrementalModeNewChanged = "new_changed"
)

func (d *JobData) Validate() error {
	if len(d.Keywords) == 0 {
		return errors.New("missing keywords")
	}

	if d.Lang == "" {
		return errors.New("missing lang")
	}

	if len(d.Lang) != 2 {
		return errors.New("invalid lang")
	}

	if d.Depth < 1 {
		return errors.New("missing depth")
	}

	if d.MaxTime <= 0 {
		return errors.New("missing max time")
	}

	if d.Concurrency < 0 || d.Concurrency > 64 {
		return errors.New("concurrency must be between 1 and 64 when set")
	}

	// Task workers run whole queries or grid cells side by side. The total
	// browser budget is divided between them, so this bounds parallel resume
	// units rather than adding capacity.
	if d.TaskWorkers < 0 || d.TaskWorkers > MaximumJobTaskWorkers {
		return fmt.Errorf("parallel tasks must be between 1 and %d when set", MaximumJobTaskWorkers)
	}

	if d.BrowserPool < 0 || d.BrowserPool > 32 {
		return errors.New("browser pool size must be between 1 and 32 when set")
	}

	if d.PagesBrowser < 0 || d.PagesBrowser > 16 {
		return errors.New("pages per browser must be between 1 and 16 when set")
	}
	if d.MaxRecords < 0 || d.MaxRecords > 10_000_000 {
		return errors.New("maximum records must be between 1 and 10000000 when set")
	}
	if d.RetryCount < 0 || d.RetryCount > 20 {
		return errors.New("retry count must be between 0 and 20")
	}
	if d.RetryDelay < 0 || d.RetryDelay > 5*time.Minute {
		return errors.New("retry delay must be between 0 and 5m")
	}
	if d.PageTimeout < 0 || d.PageTimeout > 5*time.Minute || (d.PageTimeout > 0 && d.PageTimeout < time.Second) {
		return errors.New("page timeout must be between 1s and 5m when set")
	}
	if d.RandomDelayMin < 0 || d.RandomDelayMax < 0 || d.RandomDelayMin > time.Minute || d.RandomDelayMax > time.Minute ||
		d.RandomDelayMax < d.RandomDelayMin {
		return errors.New("random delay must be an ordered range between 0 and 1m")
	}
	if d.LowDiskBytes > 1<<40 {
		return errors.New("low disk threshold must be at most 1 TiB")
	}
	if d.CheckpointSeconds < 0 || d.CheckpointSeconds > MaximumCheckpointSeconds ||
		(d.CheckpointSeconds > 0 && d.CheckpointSeconds < MinimumCheckpointSeconds) {
		return fmt.Errorf(
			"checkpoint interval must be between %d and %d seconds when set",
			MinimumCheckpointSeconds, MaximumCheckpointSeconds,
		)
	}
	if d.Enrichment != nil {
		if err := d.Enrichment.Validate(); err != nil {
			return err
		}
	}
	if d.Coverage != nil {
		if err := d.Coverage.Validate(); err != nil {
			return err
		}
	}
	switch d.IncrementalMode {
	case "", IncrementalModeNewOnly, IncrementalModeNewChanged:
	default:
		return fmt.Errorf(
			"rescan mode must be empty for a full collection, %q, or %q; got %q",
			IncrementalModeNewOnly, IncrementalModeNewChanged, d.IncrementalMode,
		)
	}
	if d.SavedAreaID != "" && !validMapEntityID(d.SavedAreaID) {
		return errors.New("saved area ID is invalid")
	}
	if d.SavedAreaID != "" && strings.TrimSpace(d.AreaGeoJSON) == "" {
		return errors.New("saved area snapshot is required")
	}
	if strings.TrimSpace(d.AreaGeoJSON) != "" {
		geometry, err := ParseMapGeometry([]byte(d.AreaGeoJSON))
		if err != nil {
			return fmt.Errorf("invalid saved area snapshot: %w", err)
		}
		if d.FastMode && geometry.Kind() != "circle" {
			return errors.New("fast mode supports saved circles only; use grid mode for polygons")
		}
		// A saved area is expanded into grid cells by the checkpoint runner, so it
		// needs the same cell size that an explicit bounding box requires. Without
		// this the job validates and then fails at seed-generation time.
		if !d.FastMode && d.GridCellKM <= 0 {
			return errors.New("grid cell size must be greater than 0 for a saved area")
		}
	}

	if d.Zoom < 1 || d.Zoom > 21 {
		return errors.New("zoom must be between 1 and 21")
	}

	if d.GridBBox != "" {
		if d.FastMode {
			return errors.New("grid coverage and fast mode cannot be used together")
		}

		if d.GridCellKM <= 0 {
			return errors.New("grid cell size must be greater than 0")
		}

		bbox, err := grid.ParseBoundingBox(d.GridBBox)
		if err != nil {
			return fmt.Errorf("invalid grid bounding box: %w", err)
		}

		const maxGridCells = 2500

		if count := grid.EstimateCellCount(bbox, d.GridCellKM); count > maxGridCells {
			return fmt.Errorf("grid creates %d cells; maximum is %d", count, maxGridCells)
		}
	}

	if d.FastMode && (d.Lat == "" || d.Lon == "") {
		return errors.New("missing geo coordinates")
	}

	if d.FastMode && d.Radius <= 0 {
		return errors.New("fast mode radius must be greater than 0")
	}

	return nil
}
