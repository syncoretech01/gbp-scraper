package jobruntime

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"time"
)

// Stage identifies the current user-visible pipeline stage.
type Stage string

const (
	StageNone               Stage = ""
	StagePreparingQueries   Stage = "preparing_queries"
	StageGeneratingGrid     Stage = "generating_grid"
	StageSearchingMaps      Stage = "searching_maps"
	StageExtractingDetails  Stage = "extracting_details"
	StageCrawlingWebsites   Stage = "crawling_websites"
	StageExtractingContacts Stage = "extracting_contacts"
	StageDeduplicating      Stage = "deduplicating"
	StageSavingExporting    Stage = "saving_exporting"
)

var (
	// ErrInvalidStage indicates an unknown progress stage.
	ErrInvalidStage = errors.New("invalid job progress stage")
	// ErrInvalidProgress indicates inconsistent progress counters or runtime.
	ErrInvalidProgress = errors.New("invalid job progress")
)

// Valid reports whether s is an empty/not-started stage or a canonical
// pipeline stage.
func (s Stage) Valid() bool {
	switch s {
	case StageNone,
		StagePreparingQueries,
		StageGeneratingGrid,
		StageSearchingMaps,
		StageExtractingDetails,
		StageCrawlingWebsites,
		StageExtractingContacts,
		StageDeduplicating,
		StageSavingExporting:
		return true
	default:
		return false
	}
}

// ProgressCounters contains durable task, result, warning, and retry counts.
type ProgressCounters struct {
	TotalTasks     int64 `json:"total_tasks"`
	CompletedTasks int64 `json:"completed_tasks"`
	FailedTasks    int64 `json:"failed_tasks"`
	SkippedTasks   int64 `json:"skipped_tasks"`
	ActiveTasks    int64 `json:"active_tasks"`
	RawRecords     int64 `json:"raw_records"`
	UniqueRecords  int64 `json:"unique_records"`
	Websites       int64 `json:"websites"`
	Emails         int64 `json:"emails"`
	Duplicates     int64 `json:"duplicates"`
	Retries        int64 `json:"retries"`
	Warnings       int64 `json:"warnings"`
	Errors         int64 `json:"errors"`
}

// TerminalTasks returns the number of completed, failed, or skipped tasks.
func (c ProgressCounters) TerminalTasks() int64 {
	return c.CompletedTasks + c.FailedTasks + c.SkippedTasks
}

// RemainingTasks returns the queued/incomplete task count. Invalid counters
// return zero; call Validate before relying on it.
func (c ProgressCounters) RemainingTasks() int64 {
	remaining := c.TotalTasks - c.TerminalTasks() - c.ActiveTasks
	if remaining < 0 {
		return 0
	}

	return remaining
}

// Validate checks that progress counters can describe a real task set.
func (c ProgressCounters) Validate() error {
	values := []int64{
		c.TotalTasks,
		c.CompletedTasks,
		c.FailedTasks,
		c.SkippedTasks,
		c.ActiveTasks,
		c.RawRecords,
		c.UniqueRecords,
		c.Websites,
		c.Emails,
		c.Duplicates,
		c.Retries,
		c.Warnings,
		c.Errors,
	}
	for _, value := range values {
		if value < 0 {
			return fmt.Errorf("%w: counters cannot be negative", ErrInvalidProgress)
		}
	}

	if c.TerminalTasks()+c.ActiveTasks > c.TotalTasks {
		return fmt.Errorf(
			"%w: terminal and active tasks (%d) exceed total tasks (%d)",
			ErrInvalidProgress,
			c.TerminalTasks()+c.ActiveTasks,
			c.TotalTasks,
		)
	}

	if c.UniqueRecords > c.RawRecords {
		return fmt.Errorf("%w: unique records exceed raw records", ErrInvalidProgress)
	}

	return nil
}

// ProgressInput is the source data for a calculated progress snapshot.
type ProgressInput struct {
	State          State
	Stage          Stage
	Counters       ProgressCounters
	Elapsed        time.Duration
	CurrentQuery   string
	CurrentCell    string
	CurrentDomain  string
	CPUPercent     float64
	MemoryBytes    uint64
	DiskFreeBytes  uint64
	BrowserCount   int
	ActivePages    int
	DatabaseWrites int64
	WebsiteQueue   int64
}

// ProgressSnapshot is a stable, JSON-ready view used by polling and SSE APIs.
// Milliseconds/seconds are explicit to avoid time.Duration's nanosecond JSON
// representation leaking into the new API contract.
type ProgressSnapshot struct {
	State           State            `json:"state"`
	Stage           Stage            `json:"stage"`
	Counters        ProgressCounters `json:"counters"`
	RemainingTasks  int64            `json:"remaining_tasks"`
	Percent         float64          `json:"percent"`
	RuntimeMillis   int64            `json:"runtime_ms"`
	ETASeconds      *int64           `json:"eta_seconds"`
	PlacesPerMinute float64          `json:"places_per_minute"`
	CurrentQuery    string           `json:"current_query,omitempty"`
	CurrentCell     string           `json:"current_cell,omitempty"`
	CurrentDomain   string           `json:"current_domain,omitempty"`
	CPUPercent      float64          `json:"cpu_percent"`
	MemoryBytes     uint64           `json:"memory_bytes"`
	DiskFreeBytes   uint64           `json:"disk_free_bytes"`
	BrowserCount    int              `json:"browser_count"`
	ActivePages     int              `json:"active_pages"`
	DatabaseWrites  int64            `json:"database_writes"`
	WebsiteQueue    int64            `json:"website_queue"`
}

// BuildProgressSnapshot validates source data and calculates percentage,
// processing rate, and ETA. ETA is unavailable until at least one task is
// terminal and elapsed time is positive.
func BuildProgressSnapshot(input ProgressInput) (ProgressSnapshot, error) {
	if !input.State.Valid() {
		return ProgressSnapshot{}, fmt.Errorf("%w: %q", ErrInvalidState, input.State)
	}

	if !input.Stage.Valid() {
		return ProgressSnapshot{}, fmt.Errorf("%w: %q", ErrInvalidStage, input.Stage)
	}

	if err := input.Counters.Validate(); err != nil {
		return ProgressSnapshot{}, err
	}

	if input.Elapsed < 0 {
		return ProgressSnapshot{}, fmt.Errorf("%w: elapsed time cannot be negative", ErrInvalidProgress)
	}

	if input.CPUPercent < 0 || math.IsNaN(input.CPUPercent) || math.IsInf(input.CPUPercent, 0) {
		return ProgressSnapshot{}, fmt.Errorf("%w: invalid CPU percentage", ErrInvalidProgress)
	}

	if input.BrowserCount < 0 || input.ActivePages < 0 || input.DatabaseWrites < 0 || input.WebsiteQueue < 0 {
		return ProgressSnapshot{}, fmt.Errorf("%w: resource counters cannot be negative", ErrInvalidProgress)
	}

	terminal := input.Counters.TerminalTasks()
	percent := calculatePercent(input.State, input.Counters.TotalTasks, terminal)
	eta, available := EstimateETA(input.Counters.TotalTasks, terminal, input.Elapsed)

	var etaSeconds *int64
	if available {
		seconds := int64(math.Ceil(eta.Seconds()))
		etaSeconds = &seconds
	}

	return ProgressSnapshot{
		State:           input.State,
		Stage:           input.Stage,
		Counters:        input.Counters,
		RemainingTasks:  input.Counters.RemainingTasks(),
		Percent:         percent,
		RuntimeMillis:   input.Elapsed.Milliseconds(),
		ETASeconds:      etaSeconds,
		PlacesPerMinute: RatePerMinute(input.Counters.RawRecords, input.Elapsed),
		CurrentQuery:    input.CurrentQuery,
		CurrentCell:     input.CurrentCell,
		CurrentDomain:   input.CurrentDomain,
		CPUPercent:      input.CPUPercent,
		MemoryBytes:     input.MemoryBytes,
		DiskFreeBytes:   input.DiskFreeBytes,
		BrowserCount:    input.BrowserCount,
		ActivePages:     input.ActivePages,
		DatabaseWrites:  input.DatabaseWrites,
		WebsiteQueue:    input.WebsiteQueue,
	}, nil
}

// EstimateETA estimates remaining task time from the run-wide average terminal
// task rate. The bool is false when no meaningful estimate is available.
func EstimateETA(total, terminal int64, elapsed time.Duration) (time.Duration, bool) {
	if total < 0 || terminal < 0 || terminal > total || elapsed < 0 {
		return 0, false
	}

	if total == 0 {
		return 0, false
	}

	if terminal == total {
		return 0, true
	}

	if terminal == 0 || elapsed == 0 {
		return 0, false
	}

	// Calculate elapsed * remaining / terminal in nanoseconds without first
	// multiplying two int64 values. A floating-point calculation can silently
	// wrap to a negative time.Duration at the upper duration bound.
	elapsedNanoseconds := uint64(elapsed)
	terminalTasks := uint64(terminal)
	remainingTasks := uint64(total - terminal)
	maximumDuration := ^uint64(0) >> 1

	wholePerTask := elapsedNanoseconds / terminalTasks
	remainderPerTask := elapsedNanoseconds % terminalTasks
	if wholePerTask != 0 && remainingTasks > maximumDuration/wholePerTask {
		return 0, false
	}

	wholeNanoseconds := wholePerTask * remainingTasks
	high, low := bits.Mul64(remainderPerTask, remainingTasks)
	fractionalNanoseconds, _ := bits.Div64(high, low, terminalTasks)
	if fractionalNanoseconds > maximumDuration-wholeNanoseconds {
		return 0, false
	}

	return time.Duration(wholeNanoseconds + fractionalNanoseconds), true
}

// RatePerMinute calculates a non-negative throughput rate. Invalid or empty
// samples return zero rather than NaN or infinity.
func RatePerMinute(count int64, elapsed time.Duration) float64 {
	if count <= 0 || elapsed <= 0 {
		return 0
	}

	return float64(count) / elapsed.Minutes()
}

func calculatePercent(state State, total, terminal int64) float64 {
	if total == 0 {
		if state.Terminal() {
			return 100
		}

		return 0
	}

	percent := float64(terminal) / float64(total) * 100

	return math.Max(0, math.Min(100, percent))
}
