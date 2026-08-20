package web

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrCoverageUnsupported indicates that the active repository cannot serve
// the adaptive discovery / coverage engine.
var ErrCoverageUnsupported = errors.New("coverage storage is unavailable")

// CoverageSkipReason marks tasks the coverage engine skipped because the
// recent result window was saturated with duplicates. It is stored on the
// skipped rows so the coverage report can tell an adaptive stop apart from
// any other skipped work.
const CoverageSkipReason = "coverage-saturated"

// CoverageEmptySkipReason marks tasks the coverage engine skipped because a
// full window of successful tasks returned nothing at all — neither new rows
// nor duplicates. It is deliberately distinct from CoverageSkipReason so an
// operator can tell "this area is empty" apart from "we keep re-finding the
// same businesses" long after the run finished.
const CoverageEmptySkipReason = "coverage-exhausted"

// Saturation reasons reported by GET /api/v1/jobs/{id}/coverage. The empty
// string means the adaptive engine did not stop the plan.
const (
	// CoverageSaturationReasonDuplicates is the historical stop: a full
	// window whose new-row ratio fell below MinNewRatio.
	CoverageSaturationReasonDuplicates = "duplicate-saturation"
	// CoverageSaturationReasonEmpty is a full window of successful tasks
	// that all returned zero new and zero duplicate rows.
	CoverageSaturationReasonEmpty = "empty-area"
)

// CoverageExpansionOriginPrefix prefixes the origin of every task the
// coverage engine appended mid-run; the suffix is the parent ZIP whose
// results justified the expansion.
const CoverageExpansionOriginPrefix = "expansion:"

// CoverageRefinementOriginPrefix prefixes the origin of every task the
// coverage engine appended to re-cover a cell whose own result set looked
// truncated; the suffix is that cell's ZIP. Refinements are tagged apart
// from neighbour expansions so their yield can be measured separately.
const CoverageRefinementOriginPrefix = "refine:"

// Coverage option defaults and bounds. Zero values fall back to the default
// so a stored configuration stays valid when new knobs appear.
const (
	DefaultCoverageSaturationWindow = 8
	DefaultCoverageExpansionMinNew  = 10

	minCoverageSaturationWindow = 3
	maxCoverageSaturationWindow = 50
	maxCoverageExpansions       = 500
	maxCoverageExpansionMinNew  = 100000
)

// DefaultCoverageMinNewRatio is the fraction of genuinely new rows below
// which a full saturation window stops the remaining plan.
const DefaultCoverageMinNewRatio = 0.10

const (
	minCoverageMinNewRatio = 0.01
	maxCoverageMinNewRatio = 0.9
)

// CoverageOptions configures the adaptive discovery engine for one job. A
// nil options pointer on JobData disables the engine entirely.
type CoverageOptions struct {
	// AutoStop stops the remaining plan once the saturation window shows
	// mostly duplicates.
	AutoStop bool `json:"auto_stop"`
	// SaturationWindow is how many recently finished tasks feed the
	// saturation ratio (default 8).
	SaturationWindow int `json:"saturation_window"`
	// MinNewRatio is the new-row fraction below which a full window stops
	// the job (default 0.10).
	MinNewRatio float64 `json:"min_new_ratio"`
	// MaxExpansions bounds how many tasks the engine may append mid-run;
	// zero disables expansion.
	MaxExpansions int `json:"max_expansions"`
	// ExpansionMinNew is how many new rows a finished GBP-shaped task needs
	// before its neighbourhood is worth expanding into (default 10).
	ExpansionMinNew int `json:"expansion_min_new"`
	// StopOnEmptyWindow stops the remaining plan once a FULL saturation
	// window is made entirely of successful tasks that returned nothing at
	// all. A nil pointer — which is what every configuration persisted
	// before this knob existed decodes to — follows AutoStop: an operator
	// who asked for an adaptive stop wants the run to stop on genuine
	// exhaustion too, and the duplicate-ratio rule can never detect it
	// (a window with no rows and no duplicates scores a perfect 1.0).
	// A nil CoverageOptions keeps exactly the historical behaviour and
	// never stops on an empty window.
	StopOnEmptyWindow *bool `json:"stop_on_empty_window,omitempty"`
}

// Validate bounds every configured knob; zero values mean "use the default"
// and always pass.
func (c *CoverageOptions) Validate() error {
	if c == nil {
		return nil
	}

	if c.SaturationWindow != 0 &&
		(c.SaturationWindow < minCoverageSaturationWindow || c.SaturationWindow > maxCoverageSaturationWindow) {
		return fmt.Errorf("coverage saturation window must be between %d and %d",
			minCoverageSaturationWindow, maxCoverageSaturationWindow)
	}

	if c.MinNewRatio != 0 && (c.MinNewRatio < minCoverageMinNewRatio || c.MinNewRatio > maxCoverageMinNewRatio) {
		return fmt.Errorf("coverage minimum new ratio must be between %.2f and %.2f",
			minCoverageMinNewRatio, maxCoverageMinNewRatio)
	}

	if c.MaxExpansions < 0 || c.MaxExpansions > maxCoverageExpansions {
		return fmt.Errorf("coverage expansions must be between 0 and %d", maxCoverageExpansions)
	}

	if c.ExpansionMinNew < 0 || c.ExpansionMinNew > maxCoverageExpansionMinNew {
		return fmt.Errorf("coverage expansion threshold must be between 0 and %d", maxCoverageExpansionMinNew)
	}

	return nil
}

// WindowOrDefault returns the configured saturation window or its default.
func (c *CoverageOptions) WindowOrDefault() int {
	if c == nil || c.SaturationWindow == 0 {
		return DefaultCoverageSaturationWindow
	}

	return c.SaturationWindow
}

// MinNewRatioOrDefault returns the configured ratio or its default.
func (c *CoverageOptions) MinNewRatioOrDefault() float64 {
	if c == nil || c.MinNewRatio == 0 {
		return DefaultCoverageMinNewRatio
	}

	return c.MinNewRatio
}

// StopOnEmptyWindowOrDefault reports whether a full window of successful,
// completely empty tasks may stop the remaining plan.
//
// A nil CoverageOptions reports false: without a coverage block the engine
// does not run at all, which is exactly today's behaviour. With a coverage
// block and no explicit choice the knob follows AutoStop, because sustained
// zero yield is the one form of exhaustion the duplicate-ratio rule is
// structurally blind to. An explicit value always wins in both directions.
func (c *CoverageOptions) StopOnEmptyWindowOrDefault() bool {
	if c == nil {
		return false
	}

	if c.StopOnEmptyWindow == nil {
		return c.AutoStop
	}

	return *c.StopOnEmptyWindow
}

// ExpansionMinNewOrDefault returns the configured expansion threshold or its
// default.
func (c *CoverageOptions) ExpansionMinNewOrDefault() int {
	if c == nil || c.ExpansionMinNew == 0 {
		return DefaultCoverageExpansionMinNew
	}

	return c.ExpansionMinNew
}

// CoverageTaskRow is one durable task row with the checkpoint counters the
// coverage report is built from.
type CoverageTaskRow struct {
	TaskKey           string
	Query             string
	Origin            string
	State             string
	LastError         string
	Attempts          int
	Priority          int
	Sequence          int
	RowsAdded         int64
	RowsReplaced      int64
	DuplicatesSkipped int64
	// Truncated mirrors the task checkpoint's truncation signal: the query
	// returned as many listings as its depth could ever render.
	Truncated  bool
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// CoverageSeedState is what the mid-run coverage engine needs to know about
// the durable plan before it may expand it.
type CoverageSeedState struct {
	// Queries are every task query already in the plan (any state).
	Queries []string
	// MaxSequence is the highest sequence in the plan, -1 for an empty plan.
	MaxSequence int
	// ExpansionTasks counts tasks the engine already appended — neighbour
	// expansions and cell refinements alike, since both draw on the same
	// budget — so that budget survives a process restart.
	ExpansionTasks int
}

// CoverageTotals aggregates the durable plan for the coverage report.
type CoverageTotals struct {
	TasksTotal        int64 `json:"tasks_total"`
	TasksDone         int64 `json:"tasks_done"`
	TasksFailed       int64 `json:"tasks_failed"`
	TasksSkipped      int64 `json:"tasks_skipped"`
	RowsAdded         int64 `json:"rows_added"`
	RowsReplaced      int64 `json:"rows_replaced"`
	DuplicatesSkipped int64 `json:"duplicates_skipped"`
	ExpansionsAdded   int64 `json:"expansions_added"`
	// RefinementsAdded counts tasks the engine appended to re-cover a
	// truncated cell, kept apart from ExpansionsAdded so a benchmark can
	// value refinements and neighbour expansions separately.
	RefinementsAdded int64 `json:"refinements_added"`
	// TasksTruncated counts tasks whose own result set reached the
	// effective per-query cap, which is the plan's discovery blind spot.
	TasksTruncated int64 `json:"tasks_truncated"`
}

// CoverageSaturation reports the adaptive-stop configuration and the current
// window evidence. Every field below "stopped" is additive; the five original
// keys keep their exact names and meanings for the existing UI.
type CoverageSaturation struct {
	Enabled         bool    `json:"enabled"`
	Window          int     `json:"window"`
	MinNewRatio     float64 `json:"min_new_ratio"`
	CurrentNewRatio float64 `json:"current_new_ratio"`
	Stopped         bool    `json:"stopped"`
	// Reason names why the adaptive engine stopped the plan:
	// CoverageSaturationReasonDuplicates, CoverageSaturationReasonEmpty, or
	// the empty string when the plan was not stopped.
	Reason string `json:"reason"`
	// StopOnEmptyWindow echoes the effective zero-yield knob.
	StopOnEmptyWindow bool `json:"stop_on_empty_window"`
	// WindowSamples is how many finished tasks the window currently holds;
	// it is below Window until enough tasks have finished.
	WindowSamples int `json:"window_samples"`
	// SuccessfulSamples counts window entries that completed without error.
	// Only successful tasks ever enter the window, so this equals
	// WindowSamples for any window the engine built.
	SuccessfulSamples int `json:"successful_samples"`
	// EmptySamples counts window entries that succeeded with neither new
	// rows nor duplicates.
	EmptySamples int `json:"empty_samples"`
}

// CoverageQueryRow is one task of the plan as the coverage UI renders it.
type CoverageQueryRow struct {
	TaskKey           string  `json:"task_key"`
	Query             string  `json:"query"`
	ZIP               string  `json:"zip"`
	Origin            string  `json:"origin"`
	State             string  `json:"state"`
	Attempts          int     `json:"attempts"`
	RowsAdded         int64   `json:"rows_added"`
	DuplicatesSkipped int64   `json:"duplicates_skipped"`
	Seconds           float64 `json:"seconds"`
	// RowsReplaced is how many of this query's rows the job had already
	// collected from an earlier query. It is the overlap measure:
	// DuplicatesSkipped only counts collisions inside one result file, so
	// two queries covering the same ground show up here and nowhere else.
	RowsReplaced int64 `json:"rows_replaced"`
	// Truncated marks a query whose result set reached its depth's cap, so
	// the operator can see which cells are probably under-covered.
	Truncated bool `json:"truncated"`
}

// CoverageTrendPoint is one finished task in completion order.
type CoverageTrendPoint struct {
	Seq               int       `json:"seq"`
	RowsAdded         int64     `json:"rows_added"`
	DuplicatesSkipped int64     `json:"duplicates_skipped"`
	FinishedAt        time.Time `json:"finished_at"`
}

// CoverageReport is the full coverage readback for one job.
type CoverageReport struct {
	Totals     CoverageTotals       `json:"totals"`
	Saturation CoverageSaturation   `json:"saturation"`
	ByQuery    []CoverageQueryRow   `json:"by_query"`
	Trend      []CoverageTrendPoint `json:"trend"`
}

// ProxyTaskStatInput records one finished task against the proxy it ran
// through.
type ProxyTaskStatInput struct {
	ProxyID         string
	PoolID          string
	Success         bool
	DurationSeconds float64
	LastError       string
}

// ProxyTaskHealth is the aggregate task history of one pool proxy, keyed in
// the repository by the decrypted proxy URL so the task pool can attribute
// its in-memory plan entries.
type ProxyTaskHealth struct {
	ProxyID             string
	ConsecutiveFailures int64
	Successes           int64
	Failures            int64
}

type coverageRepository interface {
	JobCoverageTasks(context.Context, string) ([]CoverageTaskRow, error)
	JobCoverageSeedState(context.Context, string) (CoverageSeedState, error)
	SkipPendingJobTasks(context.Context, string, string) (int, error)
	AppendJobTasks(context.Context, string, []JobTaskDefinition, int) ([]JobTask, error)
	DeferJobTask(context.Context, string, string, time.Time) error
	UpsertProxyTaskStat(context.Context, ProxyTaskStatInput) error
	ProxyTaskHealthByURL(context.Context, string) (map[string]ProxyTaskHealth, error)
}

func (s *Service) coverageRepository() (coverageRepository, error) {
	repository, ok := s.repo.(coverageRepository)
	if !ok {
		return nil, ErrCoverageUnsupported
	}

	return repository, nil
}

// JobCoverage builds the coverage report for one job from its durable task
// plan and its stored coverage options.
func (s *Service) JobCoverage(ctx context.Context, jobID string) (CoverageReport, error) {
	repository, err := s.coverageRepository()
	if err != nil {
		return CoverageReport{}, err
	}

	job, err := s.repo.Get(ctx, jobID)
	if err != nil {
		return CoverageReport{}, err
	}

	rows, err := repository.JobCoverageTasks(ctx, jobID)
	if err != nil {
		return CoverageReport{}, err
	}

	return buildCoverageReport(job.Data.Coverage, rows), nil
}

// JobCoverageSeedState reads the plan facts the mid-run engine needs.
func (s *Service) JobCoverageSeedState(ctx context.Context, jobID string) (CoverageSeedState, error) {
	repository, err := s.coverageRepository()
	if err != nil {
		return CoverageSeedState{}, err
	}

	return repository.JobCoverageSeedState(ctx, jobID)
}

// SkipPendingJobTasks marks every still-pending task of the job as skipped
// with the given reason and reports how many were skipped. Skipped tasks are
// terminal: they are never reclaimed and never resurrected by a restart.
func (s *Service) SkipPendingJobTasks(ctx context.Context, jobID, reason string) (int, error) {
	repository, err := s.coverageRepository()
	if err != nil {
		return 0, err
	}

	if strings.TrimSpace(jobID) == "" {
		return 0, errors.New("job ID is required")
	}

	if strings.TrimSpace(reason) == "" {
		return 0, errors.New("a skip reason is required")
	}

	return repository.SkipPendingJobTasks(ctx, jobID, reason)
}

// AppendJobTasks adds new pending tasks to an existing durable plan without
// touching the tasks already there. Appending an already-known task key is a
// no-op, which makes a repeated expansion decision idempotent.
func (s *Service) AppendJobTasks(
	ctx context.Context,
	jobID string,
	definitions []JobTaskDefinition,
	maxAttempts int,
) ([]JobTask, error) {
	repository, err := s.coverageRepository()
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(jobID) == "" {
		return nil, errors.New("job ID is required")
	}

	if len(definitions) == 0 {
		return []JobTask{}, nil
	}

	if maxAttempts < 1 || maxAttempts > 100 {
		return nil, errors.New("maximum task attempts must be between 1 and 100")
	}

	seen := make(map[string]struct{}, len(definitions))

	for index := range definitions {
		definition := &definitions[index]
		definition.Key = strings.TrimSpace(definition.Key)
		definition.Kind = strings.TrimSpace(definition.Kind)

		if definition.Key == "" || len(definition.Key) > 256 {
			return nil, fmt.Errorf("appended task %d has an invalid key", index+1)
		}

		if definition.Kind == "" || len(definition.Kind) > 64 {
			return nil, fmt.Errorf("appended task %d has an invalid kind", index+1)
		}

		if _, duplicate := seen[definition.Key]; duplicate {
			return nil, fmt.Errorf("duplicate appended task key %q", definition.Key)
		}

		seen[definition.Key] = struct{}{}

		if definition.Sequence < 0 {
			return nil, fmt.Errorf("appended task %q has a negative sequence", definition.Key)
		}

		if len(definition.Origin) > 128 {
			return nil, fmt.Errorf("appended task %q has an oversized origin", definition.Key)
		}

		if definition.Priority < 0 || definition.Priority > 1000 {
			return nil, fmt.Errorf("appended task %q priority must be between 0 and 1000", definition.Key)
		}

		if len(definition.Payload) == 0 {
			definition.Payload = []byte("{}")
		}

		if len(definition.Payload) > 64*1024 {
			return nil, fmt.Errorf("appended task %q has an oversized payload", definition.Key)
		}
	}

	return repository.AppendJobTasks(ctx, jobID, definitions, maxAttempts)
}

// DeferJobTask sets the earliest moment a pending task may be claimed again,
// so failure-class backoff does not burn attempts in a tight loop.
func (s *Service) DeferJobTask(ctx context.Context, jobID, taskKey string, until time.Time) error {
	repository, err := s.coverageRepository()
	if err != nil {
		return err
	}

	return repository.DeferJobTask(ctx, jobID, taskKey, until)
}

// UpsertProxyTaskStat folds one finished task into the proxy's aggregate
// task history.
func (s *Service) UpsertProxyTaskStat(ctx context.Context, input ProxyTaskStatInput) error {
	repository, err := s.coverageRepository()
	if err != nil {
		return err
	}

	if strings.TrimSpace(input.ProxyID) == "" {
		return errors.New("a proxy ID is required")
	}

	return repository.UpsertProxyTaskStat(ctx, input)
}

// ProxyTaskHealthByURL returns the task history of every proxy in the pool,
// keyed by decrypted proxy URL for in-memory plan attribution.
func (s *Service) ProxyTaskHealthByURL(ctx context.Context, poolID string) (map[string]ProxyTaskHealth, error) {
	repository, err := s.coverageRepository()
	if err != nil {
		return nil, err
	}

	return repository.ProxyTaskHealthByURL(ctx, poolID)
}

// ParseGBPQueryZIP extracts the trailing 5-digit ZIP from a GBP-shaped
// query ("<synonym> in <city> <ST> <zip5>"). It reports false for any other
// query shape.
func ParseGBPQueryZIP(query string) (string, bool) {
	_, zip, ok := SplitGBPQuery(query)

	return zip, ok
}

// SplitGBPQuery splits a GBP-shaped query ("<synonym> in <city> <ST>
// <zip5>") into its synonym and ZIP parts. The synonym is everything before
// the last " in " separator, so synonyms containing "in" survive.
func SplitGBPQuery(query string) (synonym, zip string, ok bool) {
	fields := strings.Fields(strings.TrimSpace(query))
	if len(fields) < 4 {
		return "", "", false
	}

	zip = fields[len(fields)-1]
	if len(zip) != 5 {
		return "", "", false
	}

	for _, r := range zip {
		if r < '0' || r > '9' {
			return "", "", false
		}
	}

	state := fields[len(fields)-2]
	if len(state) != 2 || state[0] < 'A' || state[0] > 'Z' || state[1] < 'A' || state[1] > 'Z' {
		return "", "", false
	}

	joined := strings.Join(fields, " ")

	separator := strings.LastIndex(joined, " in ")
	if separator <= 0 {
		return "", "", false
	}

	synonym = strings.TrimSpace(joined[:separator])
	if synonym == "" {
		return "", "", false
	}

	// The city must sit between the separator and the state token.
	if strings.TrimSpace(joined[separator+len(" in "):]) == state+" "+zip {
		return "", "", false
	}

	return synonym, zip, true
}

func buildCoverageReport(options *CoverageOptions, rows []CoverageTaskRow) CoverageReport {
	report := CoverageReport{
		ByQuery: make([]CoverageQueryRow, 0, len(rows)),
		Trend:   make([]CoverageTrendPoint, 0, len(rows)),
	}

	completed := make([]CoverageTaskRow, 0, len(rows))

	for _, row := range rows {
		report.Totals.TasksTotal++

		switch row.State {
		case "completed":
			report.Totals.TasksDone++
		case "failed":
			report.Totals.TasksFailed++
		case "skipped":
			report.Totals.TasksSkipped++
		}

		report.Totals.RowsAdded += row.RowsAdded
		report.Totals.RowsReplaced += row.RowsReplaced
		report.Totals.DuplicatesSkipped += row.DuplicatesSkipped

		if strings.HasPrefix(row.Origin, CoverageExpansionOriginPrefix) {
			report.Totals.ExpansionsAdded++
		}

		if strings.HasPrefix(row.Origin, CoverageRefinementOriginPrefix) {
			report.Totals.RefinementsAdded++
		}

		if row.Truncated {
			report.Totals.TasksTruncated++
		}

		// The skip reason is durable, so a restarted process still reports
		// which of the two adaptive stops ended the plan.
		if row.State == "skipped" {
			switch row.LastError {
			case CoverageSkipReason:
				report.Saturation.Stopped = true
				report.Saturation.Reason = CoverageSaturationReasonDuplicates
			case CoverageEmptySkipReason:
				report.Saturation.Stopped = true
				report.Saturation.Reason = CoverageSaturationReasonEmpty
			}
		}

		zip, _ := ParseGBPQueryZIP(row.Query)

		seconds := float64(0)
		if row.StartedAt != nil && row.FinishedAt != nil && row.FinishedAt.After(*row.StartedAt) {
			seconds = row.FinishedAt.Sub(*row.StartedAt).Seconds()
		}

		report.ByQuery = append(report.ByQuery, CoverageQueryRow{
			TaskKey:           row.TaskKey,
			Query:             row.Query,
			ZIP:               zip,
			Origin:            row.Origin,
			State:             row.State,
			Attempts:          row.Attempts,
			RowsAdded:         row.RowsAdded,
			DuplicatesSkipped: row.DuplicatesSkipped,
			Seconds:           seconds,
			RowsReplaced:      row.RowsReplaced,
			Truncated:         row.Truncated,
		})

		if row.State == "completed" && row.FinishedAt != nil {
			completed = append(completed, row)
		}
	}

	sort.SliceStable(completed, func(a, b int) bool {
		if completed[a].FinishedAt.Equal(*completed[b].FinishedAt) {
			return completed[a].Sequence < completed[b].Sequence
		}

		return completed[a].FinishedAt.Before(*completed[b].FinishedAt)
	})

	for index, row := range completed {
		report.Trend = append(report.Trend, CoverageTrendPoint{
			Seq:               index + 1,
			RowsAdded:         row.RowsAdded,
			DuplicatesSkipped: row.DuplicatesSkipped,
			FinishedAt:        row.FinishedAt.UTC(),
		})
	}

	report.Saturation.Enabled = options != nil && options.AutoStop
	report.Saturation.Window = options.WindowOrDefault()
	report.Saturation.MinNewRatio = options.MinNewRatioOrDefault()
	report.Saturation.StopOnEmptyWindow = options.StopOnEmptyWindowOrDefault()

	samples := coverageWindowSamples(completed, report.Saturation.Window)
	report.Saturation.CurrentNewRatio = CoverageWindowRatio(samples)

	evidence := CoverageWindowEvidenceOf(samples)
	report.Saturation.WindowSamples = evidence.Samples
	report.Saturation.SuccessfulSamples = evidence.Successful
	report.Saturation.EmptySamples = evidence.Empty

	return report
}

func coverageWindowSamples(completed []CoverageTaskRow, window int) []CoverageSample {
	if window <= 0 || len(completed) == 0 {
		return nil
	}

	start := len(completed) - window
	if start < 0 {
		start = 0
	}

	samples := make([]CoverageSample, 0, len(completed)-start)
	// Only rows in the 'completed' state reach here, so every sample the
	// report rebuilds is by definition a successful task.
	for _, row := range completed[start:] {
		samples = append(samples, NewCoverageSample(row.RowsAdded, row.RowsReplaced, row.DuplicatesSkipped, true))
	}

	return samples
}

// CoverageSample is one finished task's contribution to the saturation
// window. The two counter fields are the historical payload; Succeeded and
// Empty are additive evidence-quality flags whose zero values mean "quality
// unknown", which every rule below treats as the conservative case.
type CoverageSample struct {
	RowsAdded int64 `json:"rows_added"`
	// RowsReplaced counts stored rows this attempt superseded: businesses
	// the workspace already held. The CSV merge reports a re-found business
	// as one added row AND one replaced row, so a task that re-found only
	// known businesses looks productive on RowsAdded alone. Net-new is what
	// saturation must judge.
	RowsReplaced      int64 `json:"rows_replaced,omitempty"`
	DuplicatesSkipped int64 `json:"duplicates_skipped"`
	// Succeeded reports that the task attempt completed without error. A
	// failed, timed-out or proxy-broken attempt is not evidence about the
	// area, so it must never be recorded as a successful sample.
	Succeeded bool `json:"succeeded,omitempty"`
	// Empty reports that the task succeeded and returned nothing at all:
	// zero new rows and zero duplicates. Build samples with
	// NewCoverageSample so this stays consistent with the counters.
	Empty bool `json:"empty,omitempty"`
}

// NewCoverageSample builds one window sample from a finished task attempt,
// deriving Empty from the counters so the flag can never disagree with them.
// A failed attempt is never empty evidence, however few rows it produced.
func NewCoverageSample(rowsAdded, rowsReplaced, duplicatesSkipped int64, succeeded bool) CoverageSample {
	return CoverageSample{
		RowsAdded:         rowsAdded,
		RowsReplaced:      rowsReplaced,
		DuplicatesSkipped: duplicatesSkipped,
		Succeeded:         succeeded,
		Empty:             succeeded && rowsAdded <= 0 && duplicatesSkipped <= 0,
	}
}

// CoverageSampleNetNew is how many genuinely new businesses one sample
// contributed: added rows minus the stored rows they superseded, floored at
// zero because a single run row can supersede several stored rows through
// multi-key identity matching.
func CoverageSampleNetNew(sample CoverageSample) int64 {
	netNew := sample.RowsAdded - sample.RowsReplaced
	if netNew < 0 {
		return 0
	}

	return netNew
}

// CoverageWindowRatio computes the share of a window's observed businesses
// that were genuinely new: sum(net-new) / sum(net-new + re-found +
// duplicates). Re-found businesses (RowsReplaced) count against the ratio
// because a query that only rediscovers what the workspace already holds has
// covered no new ground, however many rows it wrote. A window with no
// observations at all reports 1 so that "no evidence" never looks like
// saturation.
//
// The zero-yield rule (see CoverageWindowEvidence.ZeroYield) handles the
// genuinely empty case; this ratio deliberately stays blind to it.
func CoverageWindowRatio(samples []CoverageSample) float64 {
	var netNew, refound, duplicates int64

	for _, sample := range samples {
		netNew += CoverageSampleNetNew(sample)
		refound += sample.RowsReplaced
		duplicates += sample.DuplicatesSkipped
	}

	total := netNew + refound + duplicates
	if total <= 0 {
		return 1
	}

	return float64(netNew) / float64(total)
}

// CoverageWindowEvidence summarises the outcome quality of one saturation
// window: how many entries it holds, how many were clean successes, and how
// many of those returned nothing at all.
type CoverageWindowEvidence struct {
	Samples    int `json:"samples"`
	Successful int `json:"successful"`
	Empty      int `json:"empty"`
}

// CoverageWindowEvidenceOf counts the outcome quality of a window.
func CoverageWindowEvidenceOf(samples []CoverageSample) CoverageWindowEvidence {
	evidence := CoverageWindowEvidence{Samples: len(samples)}

	for _, sample := range samples {
		if !sample.Succeeded {
			continue
		}

		evidence.Successful++

		if sample.Empty {
			evidence.Empty++
		}
	}

	return evidence
}

// ZeroYield reports whether the window is genuine exhaustion evidence: it is
// full (window entries, no fewer), every entry is a clean success, and every
// entry returned neither new rows nor duplicates.
//
// The rules are deliberately conservative:
//   - a window that is not yet full never qualifies, so one or two empty
//     tasks can never stop a run;
//   - a single non-successful entry disqualifies the whole window, because a
//     transient failure is not evidence that an area is empty;
//   - a single productive entry disqualifies it too — a mixed window is left
//     to the ratio rule.
func (e CoverageWindowEvidence) ZeroYield(window int) bool {
	if window <= 0 || e.Samples < window {
		return false
	}

	return e.Successful == e.Samples && e.Empty == e.Samples
}
