package acceptance

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// recordsLogName is the durable append-only log of every recorded run.
	recordsLogName = "records.jsonl"
	// dirPerm and filePerm are the permissions for created output.
	dirPerm  = 0o755
	filePerm = 0o644
)

// SavedPaths reports where a record was persisted.
type SavedPaths struct {
	JSON    string `json:"json"`
	Summary string `json:"summary"`
	Log     string `json:"log"`
}

// Store persists experiment records under a single output directory. Records
// are written both as one indented JSON file per run (for reading and diffing)
// and appended to a durable JSON-lines log (for machine aggregation).
type Store struct {
	dir string
}

// NewStore prepares an output directory for experiment records.
func NewStore(dir string) (*Store, error) {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return nil, fmt.Errorf("acceptance: output directory is required")
	}
	if err := os.MkdirAll(trimmed, dirPerm); err != nil {
		return nil, fmt.Errorf("acceptance: create output directory: %w", err)
	}

	return &Store{dir: trimmed}, nil
}

// Save writes one record as indented JSON and a human summary, and appends it
// to the durable log. It returns the paths written.
func (s *Store) Save(record ExperimentRecord) (SavedPaths, error) {
	experimentDir := filepath.Join(s.dir, safeName(record.Experiment))
	if err := os.MkdirAll(experimentDir, dirPerm); err != nil {
		return SavedPaths{}, fmt.Errorf("acceptance: create experiment directory: %w", err)
	}

	runID := recordRunID(record)
	jsonPath := filepath.Join(experimentDir, runID+".json")
	summaryPath := filepath.Join(experimentDir, runID+".txt")
	logPath := filepath.Join(s.dir, recordsLogName)

	indented, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return SavedPaths{}, fmt.Errorf("acceptance: encode record: %w", err)
	}
	if err := os.WriteFile(jsonPath, append(indented, '\n'), filePerm); err != nil {
		return SavedPaths{}, fmt.Errorf("acceptance: write record json: %w", err)
	}

	if err := os.WriteFile(summaryPath, []byte(FormatSummary(record)), filePerm); err != nil {
		return SavedPaths{}, fmt.Errorf("acceptance: write record summary: %w", err)
	}

	if err := s.appendLog(logPath, record); err != nil {
		return SavedPaths{}, err
	}

	return SavedPaths{JSON: jsonPath, Summary: summaryPath, Log: logPath}, nil
}

// SaveRepeatability writes a repeatability report as indented JSON and a
// human summary alongside the individual run records.
func (s *Store) SaveRepeatability(report RepeatabilityReport) (SavedPaths, error) {
	experimentDir := filepath.Join(s.dir, safeName(report.Experiment))
	if err := os.MkdirAll(experimentDir, dirPerm); err != nil {
		return SavedPaths{}, fmt.Errorf("acceptance: create experiment directory: %w", err)
	}
	jsonPath := filepath.Join(experimentDir, "repeatability.json")
	summaryPath := filepath.Join(experimentDir, "repeatability.txt")

	indented, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return SavedPaths{}, fmt.Errorf("acceptance: encode repeatability: %w", err)
	}
	if err := os.WriteFile(jsonPath, append(indented, '\n'), filePerm); err != nil {
		return SavedPaths{}, fmt.Errorf("acceptance: write repeatability json: %w", err)
	}
	if err := os.WriteFile(summaryPath, []byte(FormatRepeatability(report)), filePerm); err != nil {
		return SavedPaths{}, fmt.Errorf("acceptance: write repeatability summary: %w", err)
	}

	return SavedPaths{JSON: jsonPath, Summary: summaryPath}, nil
}

func (s *Store) appendLog(path string, record ExperimentRecord) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePerm)
	if err != nil {
		return fmt.Errorf("acceptance: open records log: %w", err)
	}
	defer func() { _ = file.Close() }()

	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("acceptance: encode records log line: %w", err)
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("acceptance: append records log: %w", err)
	}

	return nil
}

// recordRunID derives a stable file identifier for a record.
func recordRunID(record ExperimentRecord) string {
	if id := strings.TrimSpace(record.Run.JobID); id != "" {
		return safeName(id)
	}
	if !record.Run.StartedAtWall.IsZero() {
		return record.Run.StartedAtWall.UTC().Format("20060102T150405Z")
	}

	return time.Now().UTC().Format("20060102T150405Z")
}

// safeName reduces an identifier to a filesystem-safe token.
func safeName(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "run"
	}
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, trimmed)

	return mapped
}

// MetricStats summarises one metric across repeated runs.
type MetricStats struct {
	N      int     `json:"n"`
	Mean   float64 `json:"mean"`
	StdDev float64 `json:"stddev"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	// CV is the coefficient of variation (StdDev/Mean); zero when Mean is zero.
	// It is the scale-free repeatability measure: a small CV means the two runs
	// of the same configuration agreed.
	CV float64 `json:"cv"`
}

// RepeatabilityReport aggregates several runs of one experiment configuration
// and reports the variance of each headline metric, so the lead can see how
// repeatable a result is before trusting a version-to-version comparison.
type RepeatabilityReport struct {
	Schema     string                 `json:"schema"`
	Experiment string                 `json:"experiment"`
	Label      string                 `json:"label"`
	Repeats    int                    `json:"repeats"`
	Variance   map[string]MetricStats `json:"variance"`
	Runs       []ExperimentRecord     `json:"runs"`
}

// metricExtractors names the headline metrics whose repeatability is measured.
var metricExtractors = []struct {
	name    string
	valueOf func(ExperimentRecord) float64
}{
	{"unique_businesses", func(r ExperimentRecord) float64 { return float64(r.Outcomes.UniqueBusinesses) }},
	{"discovered_rows", func(r ExperimentRecord) float64 { return float64(r.Outcomes.DiscoveredRows) }},
	{"rows_per_minute", func(r ExperimentRecord) float64 { return r.Outcomes.RowsPerMinute }},
	{"task_success_rate", func(r ExperimentRecord) float64 { return r.Outcomes.TaskSuccessRate }},
	{"browser_failure_rate", func(r ExperimentRecord) float64 { return r.Outcomes.BrowserFailureRate }},
	{"block_rate", func(r ExperimentRecord) float64 { return r.Outcomes.BlockRate }},
	{"duplicate_rate", func(r ExperimentRecord) float64 { return r.Outcomes.DuplicateRate }},
	{"retry_count", func(r ExperimentRecord) float64 { return float64(r.Outcomes.RetryCount) }},
	{"final_effective_concurrency", func(r ExperimentRecord) float64 { return float64(r.Concurrency.FinalEffective) }},
	{"wall_seconds", func(r ExperimentRecord) float64 { return r.Run.WallSeconds }},
}

// Repeatability computes the variance report for several runs of the same
// experiment. Records should all be the same experiment configuration.
func Repeatability(records []ExperimentRecord) RepeatabilityReport {
	report := RepeatabilityReport{
		Schema:   RecordSchema,
		Repeats:  len(records),
		Variance: map[string]MetricStats{},
		Runs:     records,
	}
	if len(records) > 0 {
		report.Experiment = records[0].Experiment
		report.Label = records[0].Label
	}

	for _, extractor := range metricExtractors {
		values := make([]float64, 0, len(records))
		for _, record := range records {
			values = append(values, extractor.valueOf(record))
		}
		report.Variance[extractor.name] = summarise(values)
	}

	return report
}

// summarise computes the mean, population standard deviation, range, and
// coefficient of variation of a value set.
func summarise(values []float64) MetricStats {
	stats := MetricStats{N: len(values)}
	if len(values) == 0 {
		return stats
	}

	stats.Min = values[0]
	stats.Max = values[0]
	var sum float64
	for _, value := range values {
		sum += value
		if value < stats.Min {
			stats.Min = value
		}
		if value > stats.Max {
			stats.Max = value
		}
	}
	stats.Mean = roundRatio(sum / float64(len(values)))

	var variance float64
	for _, value := range values {
		delta := value - (sum / float64(len(values)))
		variance += delta * delta
	}
	variance /= float64(len(values))
	stats.StdDev = roundRatio(math.Sqrt(variance))
	if stats.Mean != 0 {
		stats.CV = roundRatio(stats.StdDev / math.Abs(stats.Mean))
	}

	return stats
}

// FormatSummary renders a compact, human-readable summary of one record.
func FormatSummary(record ExperimentRecord) string {
	var builder strings.Builder
	line := func(format string, args ...any) {
		fmt.Fprintf(&builder, format+"\n", args...)
	}

	line("Experiment %s — %s", record.Experiment, record.Label)
	line("  target        : %s", record.Config.BaseURL)
	line("  mode          : %s / %s / enrichment=%t", record.Config.Mode, record.Config.Connection, record.Config.Enrichment)
	line("  workload      : %d queries, grid=%q cell=%.2fkm, ~%d cells, ~%d seed tasks",
		record.Config.QueryCount, record.Config.GridBBox, record.Config.GridCellKM,
		record.Config.EstimatedGridCells, record.Config.EstimatedSeedTasks)
	line("  concurrency   : desired=%d planned=%dx%d (effective %d) final=%d reductions=%d [%s]",
		record.Config.Concurrency, record.Concurrency.PlannedWorkers, record.Concurrency.PerTaskConcurrency,
		record.Concurrency.PlannedEffective, record.Concurrency.FinalEffective,
		record.Concurrency.AdaptiveReductions, record.Concurrency.Source)
	line("  outcome       : state=%s stop=%s wall=%.1fs timed_out=%t",
		record.Run.TerminalState, record.Run.StopReason, record.Run.WallSeconds, record.Run.TimedOut)
	line("  discovery     : rows=%d unique=%d normalized=%d rows/min=%.2f new/min=%.2f",
		record.Outcomes.DiscoveredRows, record.Outcomes.UniqueBusinesses, record.Outcomes.ResultsTotal,
		record.Outcomes.RowsPerMinute, record.Outcomes.NewBusinessesPerMinute)
	line("  reliability   : task_success=%.3f browser_failure=%.3f block=%.3f dup=%.3f retries=%d",
		record.Outcomes.TaskSuccessRate, record.Outcomes.BrowserFailureRate, record.Outcomes.BlockRate,
		record.Outcomes.DuplicateRate, record.Outcomes.RetryCount)
	line("  tasks         : total=%d done=%d failed=%d skipped=%d pending=%d running=%d",
		record.Outcomes.Tasks.Total, record.Outcomes.Tasks.Completed, record.Outcomes.Tasks.Failed,
		record.Outcomes.Tasks.Skipped, record.Outcomes.Tasks.Pending, record.Outcomes.Tasks.Running)
	line("  failure_kinds : %s", formatCounts(record.Outcomes.FailureKinds))
	line("  recovery      : checkpoint=%t recovery_required=%t remaining=%d coverage_stopped=%t(%s)",
		record.Recovery.CheckpointPresent, record.Recovery.RecoveryRequired, record.Recovery.TasksRemainingAtEnd,
		record.Recovery.CoverageStopped, record.Recovery.CoverageStopReason)
	line("  resources*    : cpu_peak=%.1f%% mem=%d%% peak_browsers=%d peak_pages=%d samples=%d",
		record.Resources.CPUPercent, int(record.Resources.MemoryUsedPercent), record.Resources.PeakActiveBrowser,
		record.Resources.PeakActivePages, record.Resources.SampleCount)
	line("  availability  : progress=%t benchmark=%t coverage=%t logs=%t events=%t results=%t metrics=%t",
		record.Availability.Progress, record.Availability.Benchmark, record.Availability.Coverage,
		record.Availability.Logs, record.Availability.Events, record.Availability.Results, record.Availability.Metrics)
	if record.Run.Error != "" {
		line("  error         : %s", record.Run.Error)
	}
	line("  * resources are app-reported and host-wide, not scoped to this job")

	return builder.String()
}

// FormatRepeatability renders a human-readable variance table.
func FormatRepeatability(report RepeatabilityReport) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Repeatability of experiment %s — %s (%d runs)\n",
		report.Experiment, report.Label, report.Repeats)

	names := make([]string, 0, len(report.Variance))
	for name := range report.Variance {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Fprintf(&builder, "  %-28s %10s %10s %10s %10s %8s\n", "metric", "mean", "stddev", "min", "max", "cv")
	for _, name := range names {
		stats := report.Variance[name]
		fmt.Fprintf(&builder, "  %-28s %10.3f %10.3f %10.3f %10.3f %8.3f\n",
			name, stats.Mean, stats.StdDev, stats.Min, stats.Max, stats.CV)
	}

	return builder.String()
}

// formatCounts renders a count map in a stable order for summaries.
func formatCounts(counts map[string]int64) string {
	if len(counts) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}

	return strings.Join(parts, " ")
}
