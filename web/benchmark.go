package web

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ErrBenchmarkUnsupported indicates that the active repository cannot supply
// the durable evidence a production benchmark report is computed from.
var ErrBenchmarkUnsupported = errors.New("job benchmark evidence is unavailable")

const (
	// benchmarkRatioPrecision keeps derived ratios stable across runs so two
	// reports for identical evidence diff cleanly.
	benchmarkRatioPrecision = 10000
	secondsPerMinute        = 60
)

// benchmarkZipPattern matches the trailing ZIP5 of a GBP-shaped query such as
// "plumber in Austin TX 78701".
var benchmarkZipPattern = regexp.MustCompile(`\b(\d{5})\s*$`)

// BenchmarkTaskEvidence is one durable job task reduced to the fields the
// benchmark report is computed from. Row counters come from the merged
// checkpoint JSON and stay zero for tasks that never completed an attempt.
type BenchmarkTaskEvidence struct {
	Key               string
	Query             string
	Origin            string
	State             string
	Sequence          int
	Attempts          int64
	LastError         string
	FinishedAt        int64
	RowsAdded         int64
	RowsReplaced      int64
	DuplicatesSkipped int64
}

// BenchmarkEventEvidence is one redacted warning/error job event. Context is
// the stored JSON payload; the report reads last_error and state from it.
type BenchmarkEventEvidence struct {
	Type     string
	Severity string
	Message  string
	Context  string
}

// BenchmarkBusinessEvidence aggregates the businesses linked to one job.
type BenchmarkBusinessEvidence struct {
	Unique         int64
	WebsiteStatus  map[string]int64
	ProspectTier   map[string]int64
	ProspectStatus map[string]int64
	WithEmail      int64
	WithPhone      int64
	WithBoth       int64
}

// BenchmarkProxyEvidence is one aggregate proxy_task_stats row. It never
// carries proxy URLs or credentials, only the operator-chosen name.
type BenchmarkProxyEvidence struct {
	ProxyID             string
	ProxyName           string
	PoolID              string
	TaskSuccesses       int64
	TaskFailures        int64
	ConsecutiveFailures int64
	TotalTaskSeconds    float64
	LastSuccessAt       int64
	LastFailureAt       int64
	LastError           string
}

// BenchmarkEvidence is the read-only raw material for one job's benchmark
// report, gathered by the repository in a single pass.
type BenchmarkEvidence struct {
	JobID            string
	JobName          string
	ScraperVersion   string
	SchemaVersion    int
	CreatedAt        int64
	StartedAt        int64
	FinishedAt       int64
	RawRecords       int64
	UniqueRecords    int64
	DuplicateRecords int64
	Tasks            []BenchmarkTaskEvidence
	Events           []BenchmarkEventEvidence
	Businesses       BenchmarkBusinessEvidence
	Proxies          []BenchmarkProxyEvidence
}

// BenchmarkTotals are the headline scalar outcomes of one run. Every field is
// always serialized, so two reports diff position for position.
type BenchmarkTotals struct {
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

// BenchmarkYieldRow reports the productivity of one query, ZIP, or synonym.
type BenchmarkYieldRow struct {
	Key               string  `json:"key"`
	Tasks             int64   `json:"tasks"`
	RowsAdded         int64   `json:"rows_added"`
	DuplicatesSkipped int64   `json:"duplicates_skipped"`
	UniqueRatio       float64 `json:"unique_ratio"`
}

// BenchmarkSaturationPoint is one completed task in finish order with the
// cumulative new-versus-duplicate ratio up to and including that task.
type BenchmarkSaturationPoint struct {
	Seq                int     `json:"seq"`
	TaskKey            string  `json:"task_key"`
	RowsAdded          int64   `json:"rows_added"`
	DuplicatesSkipped  int64   `json:"duplicates_skipped"`
	CumulativeNewRatio float64 `json:"cumulative_new_ratio"`
}

// BenchmarkFailureClass groups warning/error worker events (which embed each
// task attempt's last error) by coarse cause. Retries counts the retryable
// attempt failures in the class, i.e. events whose task returned to pending.
type BenchmarkFailureClass struct {
	Class   string `json:"class"`
	Count   int64  `json:"count"`
	Retries int64  `json:"retries"`
	Sample  string `json:"sample"`
}

// BenchmarkProxyPerformance is the per-proxy task outcome aggregate. The
// array is empty when the run used no proxies or recorded no stats.
type BenchmarkProxyPerformance struct {
	ProxyID             string  `json:"proxy_id"`
	ProxyName           string  `json:"proxy_name"`
	PoolID              string  `json:"pool_id"`
	TaskSuccesses       int64   `json:"task_successes"`
	TaskFailures        int64   `json:"task_failures"`
	ConsecutiveFailures int64   `json:"consecutive_failures"`
	AverageTaskSeconds  float64 `json:"average_task_seconds"`
	LastSuccessAt       int64   `json:"last_success_at"`
	LastFailureAt       int64   `json:"last_failure_at"`
	LastError           string  `json:"last_error"`
}

// BenchmarkDistributionRow is one label/count pair of a distribution, sorted
// by count descending then label so output order is deterministic.
type BenchmarkDistributionRow struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

// BenchmarkEmailAvailability counts contact coverage over the businesses
// linked to the job.
type BenchmarkEmailAvailability struct {
	WithEmail int64 `json:"with_email"`
	WithPhone int64 `json:"with_phone"`
	WithBoth  int64 `json:"with_both"`
	Total     int64 `json:"total"`
}

// BenchmarkRuntime carries wall-clock evidence. Timestamps are unix seconds
// and stay zero when the job never reached that point.
type BenchmarkRuntime struct {
	CreatedAt        int64   `json:"created_at"`
	StartedAt        int64   `json:"started_at"`
	FinishedAt       int64   `json:"finished_at"`
	WallSeconds      float64 `json:"wall_seconds"`
	TasksPerMinute   float64 `json:"tasks_per_minute"`
	RawRecords       int64   `json:"raw_records"`
	UniqueRecords    int64   `json:"unique_records"`
	DuplicateRecords int64   `json:"duplicate_records"`
}

// BenchmarkReport is the stable, self-contained acceptance record for one
// real scrape run. Reports from different builds of the application can be
// diffed field by field: every field is always present, ratios are rounded to
// four decimals, and arrays are deterministically ordered.
type BenchmarkReport struct {
	JobID                      string                     `json:"job_id"`
	JobName                    string                     `json:"job_name"`
	EngineVersion              string                     `json:"engine_version"`
	SchemaVersion              int                        `json:"schema_version"`
	GeneratedAt                time.Time                  `json:"generated_at"`
	Totals                     BenchmarkTotals            `json:"totals"`
	YieldByQuery               []BenchmarkYieldRow        `json:"yield_by_query"`
	YieldByZip                 []BenchmarkYieldRow        `json:"yield_by_zip"`
	YieldBySynonym             []BenchmarkYieldRow        `json:"yield_by_synonym"`
	SaturationTrend            []BenchmarkSaturationPoint `json:"saturation_trend"`
	Failures                   []BenchmarkFailureClass    `json:"failures"`
	ProxyPerformance           []BenchmarkProxyPerformance `json:"proxy_performance"`
	WebsiteStatusDistribution  []BenchmarkDistributionRow `json:"website_status_distribution"`
	EmailAvailability          BenchmarkEmailAvailability `json:"email_availability"`
	ProspectTierDistribution   []BenchmarkDistributionRow `json:"prospect_tier_distribution"`
	ProspectStatusDistribution []BenchmarkDistributionRow `json:"prospect_status_distribution"`
	Runtime                    BenchmarkRuntime           `json:"runtime"`
}

// BenchmarkDelta is candidate minus base for the headline scalars of two
// benchmark reports.
type BenchmarkDelta struct {
	UniqueBusinesses       int64   `json:"unique_businesses"`
	NewBusinessesPerMinute float64 `json:"new_businesses_per_minute"`
	DuplicateRate          float64 `json:"duplicate_rate"`
	TasksFailed            int64   `json:"tasks_failed"`
	FailureCount           int64   `json:"failure_count"`
	Retries                int64   `json:"retries"`
	WallSeconds            float64 `json:"wall_seconds"`
}

// BenchmarkComparison pairs two full reports with their headline delta so a
// before/after acceptance check needs a single request.
type BenchmarkComparison struct {
	Base      BenchmarkReport `json:"base"`
	Candidate BenchmarkReport `json:"candidate"`
	Delta     BenchmarkDelta  `json:"delta"`
}

type benchmarkRepository interface {
	JobBenchmarkEvidence(context.Context, string) (BenchmarkEvidence, error)
}

// SupportsJobBenchmarks reports whether the active repository can serve the
// durable evidence behind benchmark reports.
func (s *Service) SupportsJobBenchmarks() bool {
	_, ok := s.repo.(benchmarkRepository)

	return ok
}

// JobBenchmark assembles the acceptance/benchmark report for one job from
// durable evidence only; it never mutates stored data.
func (s *Service) JobBenchmark(ctx context.Context, jobID string) (BenchmarkReport, error) {
	repository, ok := s.repo.(benchmarkRepository)
	if !ok {
		return BenchmarkReport{}, ErrBenchmarkUnsupported
	}

	evidence, err := repository.JobBenchmarkEvidence(ctx, jobID)
	if err != nil {
		return BenchmarkReport{}, err
	}

	return buildBenchmarkReport(evidence, time.Now().UTC()), nil
}

// CompareJobBenchmarks builds both reports and their headline delta
// (candidate minus base) for version-to-version acceptance comparisons.
func (s *Service) CompareJobBenchmarks(ctx context.Context, baseID, candidateID string) (BenchmarkComparison, error) {
	base, err := s.JobBenchmark(ctx, baseID)
	if err != nil {
		return BenchmarkComparison{}, err
	}

	candidate, err := s.JobBenchmark(ctx, candidateID)
	if err != nil {
		return BenchmarkComparison{}, err
	}

	return BenchmarkComparison{
		Base:      base,
		Candidate: candidate,
		Delta: BenchmarkDelta{
			UniqueBusinesses:       candidate.Totals.UniqueBusinesses - base.Totals.UniqueBusinesses,
			NewBusinessesPerMinute: roundBenchmarkRatio(candidate.Totals.NewBusinessesPerMinute - base.Totals.NewBusinessesPerMinute),
			DuplicateRate:          roundBenchmarkRatio(candidate.Totals.DuplicateRate - base.Totals.DuplicateRate),
			TasksFailed:            candidate.Totals.TasksFailed - base.Totals.TasksFailed,
			FailureCount:           benchmarkFailureCount(candidate.Failures) - benchmarkFailureCount(base.Failures),
			Retries:                candidate.Totals.Retries - base.Totals.Retries,
			WallSeconds:            roundBenchmarkRatio(candidate.Runtime.WallSeconds - base.Runtime.WallSeconds),
		},
	}, nil
}

func benchmarkFailureCount(classes []BenchmarkFailureClass) int64 {
	var total int64
	for _, class := range classes {
		total += class.Count
	}

	return total
}

func buildBenchmarkReport(evidence BenchmarkEvidence, generatedAt time.Time) BenchmarkReport {
	report := BenchmarkReport{
		JobID:         evidence.JobID,
		JobName:       evidence.JobName,
		EngineVersion: evidence.ScraperVersion,
		SchemaVersion: evidence.SchemaVersion,
		GeneratedAt:   generatedAt,
	}

	report.Totals = buildBenchmarkTotals(evidence)
	report.YieldByQuery = buildBenchmarkYield(evidence.Tasks, benchmarkQueryKey)
	report.YieldByZip = buildBenchmarkYield(evidence.Tasks, benchmarkZipKey)
	report.YieldBySynonym = buildBenchmarkYield(evidence.Tasks, benchmarkSynonymKey)
	report.SaturationTrend = buildBenchmarkSaturation(evidence.Tasks)
	report.Failures = buildBenchmarkFailures(evidence.Events)
	report.ProxyPerformance = buildBenchmarkProxies(evidence.Proxies)
	report.WebsiteStatusDistribution = benchmarkDistribution(evidence.Businesses.WebsiteStatus)
	report.EmailAvailability = BenchmarkEmailAvailability{
		WithEmail: evidence.Businesses.WithEmail,
		WithPhone: evidence.Businesses.WithPhone,
		WithBoth:  evidence.Businesses.WithBoth,
		Total:     evidence.Businesses.Unique,
	}
	report.ProspectTierDistribution = benchmarkDistribution(evidence.Businesses.ProspectTier)
	report.ProspectStatusDistribution = benchmarkDistribution(evidence.Businesses.ProspectStatus)
	report.Runtime = buildBenchmarkRuntime(evidence, report.Totals)

	return report
}

func buildBenchmarkTotals(evidence BenchmarkEvidence) BenchmarkTotals {
	totals := BenchmarkTotals{UniqueBusinesses: evidence.Businesses.Unique}
	for _, task := range evidence.Tasks {
		if task.Origin == "" {
			totals.TasksPlanned++
		} else {
			totals.TasksExpanded++
		}
		switch task.State {
		case "completed":
			totals.TasksCompleted++
		case "failed":
			totals.TasksFailed++
		case "skipped":
			totals.TasksSkipped++
		}
		totals.Attempts += task.Attempts
		if task.Attempts > 1 {
			totals.Retries += task.Attempts - 1
		}
		totals.RowsAdded += task.RowsAdded
		totals.RowsReplaced += task.RowsReplaced
		totals.DuplicatesSkipped += task.DuplicatesSkipped
	}
	totals.DuplicateRate = benchmarkRatio(totals.DuplicatesSkipped, totals.RowsAdded+totals.DuplicatesSkipped)
	totals.TotalDiscoveredRows = totals.RowsAdded + totals.RowsReplaced + totals.DuplicatesSkipped
	if seconds := benchmarkWallSeconds(evidence); seconds > 0 {
		totals.NewBusinessesPerMinute = roundBenchmarkRatio(
			float64(totals.UniqueBusinesses) / (seconds / secondsPerMinute),
		)
	}

	return totals
}

// benchmarkWallSeconds is the active runtime: finish minus start when both
// were recorded, otherwise the latest task finish minus start, otherwise 0.
func benchmarkWallSeconds(evidence BenchmarkEvidence) float64 {
	if evidence.StartedAt <= 0 {
		return 0
	}
	end := evidence.FinishedAt
	if end <= 0 {
		for _, task := range evidence.Tasks {
			if task.FinishedAt > end {
				end = task.FinishedAt
			}
		}
	}
	if end <= evidence.StartedAt {
		return 0
	}

	return float64(end - evidence.StartedAt)
}

func benchmarkQueryKey(task BenchmarkTaskEvidence) string {
	return strings.TrimSpace(task.Query)
}

// benchmarkZipKey extracts the trailing ZIP5 of a GBP-shaped
// "<synonym> in <city> <ST> <zip5>" query; other queries yield no key.
func benchmarkZipKey(task BenchmarkTaskEvidence) string {
	match := benchmarkZipPattern.FindStringSubmatch(strings.TrimSpace(task.Query))
	if match == nil {
		return ""
	}

	return match[1]
}

// benchmarkSynonymKey is the query prefix before the first " in "; queries
// without the GBP shape yield no key.
func benchmarkSynonymKey(task BenchmarkTaskEvidence) string {
	query := strings.TrimSpace(task.Query)
	prefix, _, found := strings.Cut(query, " in ")
	if !found {
		return ""
	}

	return strings.TrimSpace(prefix)
}

func buildBenchmarkYield(
	tasks []BenchmarkTaskEvidence,
	keyOf func(BenchmarkTaskEvidence) string,
) []BenchmarkYieldRow {
	byKey := make(map[string]*BenchmarkYieldRow)
	for _, task := range tasks {
		key := keyOf(task)
		if key == "" {
			continue
		}
		row, ok := byKey[key]
		if !ok {
			row = &BenchmarkYieldRow{Key: key}
			byKey[key] = row
		}
		row.Tasks++
		row.RowsAdded += task.RowsAdded
		row.DuplicatesSkipped += task.DuplicatesSkipped
	}

	rows := make([]BenchmarkYieldRow, 0, len(byKey))
	for _, row := range byKey {
		row.UniqueRatio = benchmarkRatio(row.RowsAdded, row.RowsAdded+row.DuplicatesSkipped)
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].RowsAdded != rows[j].RowsAdded {
			return rows[i].RowsAdded > rows[j].RowsAdded
		}

		return rows[i].Key < rows[j].Key
	})

	return rows
}

func buildBenchmarkSaturation(tasks []BenchmarkTaskEvidence) []BenchmarkSaturationPoint {
	completed := make([]BenchmarkTaskEvidence, 0, len(tasks))
	for _, task := range tasks {
		if task.State == "completed" {
			completed = append(completed, task)
		}
	}
	sort.Slice(completed, func(i, j int) bool {
		if completed[i].FinishedAt != completed[j].FinishedAt {
			return completed[i].FinishedAt < completed[j].FinishedAt
		}
		if completed[i].Sequence != completed[j].Sequence {
			return completed[i].Sequence < completed[j].Sequence
		}

		return completed[i].Key < completed[j].Key
	})

	points := make([]BenchmarkSaturationPoint, 0, len(completed))
	var cumulativeNew, cumulativeDuplicates int64
	for index, task := range completed {
		cumulativeNew += task.RowsAdded
		cumulativeDuplicates += task.DuplicatesSkipped
		points = append(points, BenchmarkSaturationPoint{
			Seq:                index + 1,
			TaskKey:            task.Key,
			RowsAdded:          task.RowsAdded,
			DuplicatesSkipped:  task.DuplicatesSkipped,
			CumulativeNewRatio: benchmarkRatio(cumulativeNew, cumulativeNew+cumulativeDuplicates),
		})
	}

	return points
}

// benchmarkEventFields is the subset of a worker event's stored context the
// failure grouping reads.
type benchmarkEventFields struct {
	LastError string `json:"last_error"`
	State     string `json:"state"`
}

func buildBenchmarkFailures(events []BenchmarkEventEvidence) []BenchmarkFailureClass {
	byClass := make(map[string]*BenchmarkFailureClass)
	for _, event := range events {
		var fields benchmarkEventFields
		_ = json.Unmarshal([]byte(event.Context), &fields)
		text := strings.TrimSpace(fields.LastError)
		if text == "" {
			text = strings.TrimSpace(event.Message)
		}
		class := classifyBenchmarkFailure(text)
		row, ok := byClass[class]
		if !ok {
			row = &BenchmarkFailureClass{Class: class, Sample: text}
			byClass[class] = row
		}
		row.Count++
		if fields.State == "pending" {
			row.Retries++
		}
	}

	rows := make([]BenchmarkFailureClass, 0, len(byClass))
	for _, row := range byClass {
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}

		return rows[i].Class < rows[j].Class
	})

	return rows
}

// classifyBenchmarkFailure buckets an error text into a coarse operational
// cause. Order matters: blocking signals win over generic network words.
func classifyBenchmarkFailure(text string) string {
	lowered := strings.ToLower(text)
	switch {
	case strings.Contains(lowered, "captcha"), strings.Contains(lowered, "blocked"),
		strings.Contains(lowered, "denied"), strings.Contains(lowered, "429"):
		return "blocked"
	case strings.Contains(lowered, "timeout"), strings.Contains(lowered, "timed out"),
		strings.Contains(lowered, "deadline"):
		return "timeout"
	case strings.Contains(lowered, "proxy"):
		return "proxy"
	case strings.Contains(lowered, "dns"), strings.Contains(lowered, "connection"),
		strings.Contains(lowered, "network"), strings.Contains(lowered, "refused"):
		return "network"
	case strings.Contains(lowered, "browser"), strings.Contains(lowered, "playwright"),
		strings.Contains(lowered, "crash"):
		return "browser"
	default:
		return "other"
	}
}

func buildBenchmarkProxies(proxies []BenchmarkProxyEvidence) []BenchmarkProxyPerformance {
	rows := make([]BenchmarkProxyPerformance, 0, len(proxies))
	for _, proxy := range proxies {
		attempts := proxy.TaskSuccesses + proxy.TaskFailures
		average := float64(0)
		if attempts > 0 {
			average = roundBenchmarkRatio(proxy.TotalTaskSeconds / float64(attempts))
		}
		rows = append(rows, BenchmarkProxyPerformance{
			ProxyID:             proxy.ProxyID,
			ProxyName:           proxy.ProxyName,
			PoolID:              proxy.PoolID,
			TaskSuccesses:       proxy.TaskSuccesses,
			TaskFailures:        proxy.TaskFailures,
			ConsecutiveFailures: proxy.ConsecutiveFailures,
			AverageTaskSeconds:  average,
			LastSuccessAt:       proxy.LastSuccessAt,
			LastFailureAt:       proxy.LastFailureAt,
			LastError:           proxy.LastError,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TaskSuccesses != rows[j].TaskSuccesses {
			return rows[i].TaskSuccesses > rows[j].TaskSuccesses
		}

		return rows[i].ProxyID < rows[j].ProxyID
	})

	return rows
}

func benchmarkDistribution(counts map[string]int64) []BenchmarkDistributionRow {
	rows := make([]BenchmarkDistributionRow, 0, len(counts))
	for label, count := range counts {
		rows = append(rows, BenchmarkDistributionRow{Label: label, Count: count})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}

		return rows[i].Label < rows[j].Label
	})

	return rows
}

func buildBenchmarkRuntime(evidence BenchmarkEvidence, totals BenchmarkTotals) BenchmarkRuntime {
	runtime := BenchmarkRuntime{
		CreatedAt:        evidence.CreatedAt,
		StartedAt:        evidence.StartedAt,
		FinishedAt:       evidence.FinishedAt,
		WallSeconds:      benchmarkWallSeconds(evidence),
		RawRecords:       evidence.RawRecords,
		UniqueRecords:    evidence.UniqueRecords,
		DuplicateRecords: evidence.DuplicateRecords,
	}
	if runtime.WallSeconds > 0 {
		runtime.TasksPerMinute = roundBenchmarkRatio(
			float64(totals.TasksCompleted) / (runtime.WallSeconds / secondsPerMinute),
		)
	}

	return runtime
}

// benchmarkRatio is part/whole rounded to a stable precision; 0 when the
// denominator is zero.
func benchmarkRatio(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}

	return roundBenchmarkRatio(float64(part) / float64(whole))
}

func roundBenchmarkRatio(value float64) float64 {
	return math.Round(value*benchmarkRatioPrecision) / benchmarkRatioPrecision
}
