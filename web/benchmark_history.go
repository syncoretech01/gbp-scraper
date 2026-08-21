package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var (
	// ErrBenchmarkSnapshotNotFound indicates that no benchmark snapshot has
	// been captured for a job yet.
	ErrBenchmarkSnapshotNotFound = errors.New("benchmark snapshot not found")
	// ErrBenchmarkHistoryUnsupported indicates that the active repository
	// cannot persist benchmark snapshots.
	ErrBenchmarkHistoryUnsupported = errors.New("benchmark history storage is unavailable")
)

const (
	// DefaultBenchmarkHistoryLimit is how many snapshots a history request
	// returns when it does not ask for a specific number.
	DefaultBenchmarkHistoryLimit = 25
	// MaximumBenchmarkHistoryLimit bounds one history request.
	MaximumBenchmarkHistoryLimit = 200
	// MaximumBenchmarkSeriesJobs bounds how many runs one comparison series
	// may chart, so a request can never fan out into unbounded work.
	MaximumBenchmarkSeriesJobs = 50
)

// BenchmarkSnapshot is the lightweight, durable record of one finished run's
// headline benchmark scalars, kept so run history and run-to-run comparison
// do not have to recompute a full report every time. Report carries the JSON
// of the full report the scalars were taken from, for exact replay.
type BenchmarkSnapshot struct {
	JobID                  string    `json:"job_id"`
	JobName                string    `json:"job_name"`
	CapturedAt             time.Time `json:"captured_at"`
	EngineVersion          string    `json:"engine_version"`
	SchemaVersion          int       `json:"schema_version"`
	UniqueBusinesses       int64     `json:"unique_businesses"`
	RowsAdded              int64     `json:"rows_added"`
	DuplicatesSkipped      int64     `json:"duplicates_skipped"`
	DuplicateRate          float64   `json:"duplicate_rate"`
	TasksCompleted         int64     `json:"tasks_completed"`
	TasksFailed            int64     `json:"tasks_failed"`
	TasksSkipped           int64     `json:"tasks_skipped"`
	Retries                int64     `json:"retries"`
	WallSeconds            float64   `json:"wall_seconds"`
	NewBusinessesPerMinute float64   `json:"new_businesses_per_minute"`
	Report                 string    `json:"-"`
}

// BenchmarkSeriesMetric describes one chartable column of a series so a
// front end can build its axes without hard-coding field names. Kind is
// "count", "ratio", "rate" or "seconds"; Better is "higher" or "lower".
type BenchmarkSeriesMetric struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Kind   string `json:"kind"`
	Better string `json:"better"`
}

// BenchmarkSeriesPoint is one run of a comparison series, in the order the
// series defines (campaign generation, else the order requested).
type BenchmarkSeriesPoint struct {
	Seq        int       `json:"seq"`
	JobID      string    `json:"job_id"`
	JobName    string    `json:"job_name"`
	CapturedAt time.Time `json:"captured_at"`
	// Generation and Mode are the campaign lineage of this run when it has
	// one; Generation is -1 for a run outside any campaign.
	Generation             int     `json:"generation"`
	Mode                   string  `json:"mode,omitempty"`
	UniqueBusinesses       int64   `json:"unique_businesses"`
	RowsAdded              int64   `json:"rows_added"`
	DuplicatesSkipped      int64   `json:"duplicates_skipped"`
	DuplicateRate          float64 `json:"duplicate_rate"`
	TasksCompleted         int64   `json:"tasks_completed"`
	TasksFailed            int64   `json:"tasks_failed"`
	TasksSkipped           int64   `json:"tasks_skipped"`
	Retries                int64   `json:"retries"`
	WallSeconds            float64 `json:"wall_seconds"`
	NewBusinessesPerMinute float64 `json:"new_businesses_per_minute"`
}

// BenchmarkSeries is a stable, chartable comparison of two or more runs.
// Delta is the last point minus the first, so the whole series answers "did
// this get better" without a second request.
type BenchmarkSeries struct {
	CampaignID string                  `json:"campaign_id,omitempty"`
	Metrics    []BenchmarkSeriesMetric `json:"metrics"`
	Points     []BenchmarkSeriesPoint  `json:"points"`
	Delta      BenchmarkDelta          `json:"delta"`
}

type benchmarkHistoryRepository interface {
	SaveBenchmarkSnapshot(context.Context, BenchmarkSnapshot) error
	GetBenchmarkSnapshot(context.Context, string) (BenchmarkSnapshot, error)
	ListBenchmarkSnapshots(context.Context, int) ([]BenchmarkSnapshot, error)
}

// SupportsBenchmarkHistory reports whether benchmark snapshots can be kept.
func (s *Service) SupportsBenchmarkHistory() bool {
	_, ok := s.repo.(benchmarkHistoryRepository)

	return ok
}

func (s *Service) benchmarkHistoryRepository() (benchmarkHistoryRepository, error) {
	repository, ok := s.repo.(benchmarkHistoryRepository)
	if !ok {
		return nil, ErrBenchmarkHistoryUnsupported
	}

	return repository, nil
}

// benchmarkSnapshotOf reduces a full report to the durable scalars.
func benchmarkSnapshotOf(report BenchmarkReport, capturedAt time.Time) BenchmarkSnapshot {
	snapshot := BenchmarkSnapshot{
		JobID:                  report.JobID,
		JobName:                report.JobName,
		CapturedAt:             capturedAt.UTC(),
		EngineVersion:          report.EngineVersion,
		SchemaVersion:          report.SchemaVersion,
		UniqueBusinesses:       report.Totals.UniqueBusinesses,
		RowsAdded:              report.Totals.RowsAdded,
		DuplicatesSkipped:      report.Totals.DuplicatesSkipped,
		DuplicateRate:          report.Totals.DuplicateRate,
		TasksCompleted:         report.Totals.TasksCompleted,
		TasksFailed:            report.Totals.TasksFailed,
		TasksSkipped:           report.Totals.TasksSkipped,
		Retries:                report.Totals.Retries,
		WallSeconds:            report.Runtime.WallSeconds,
		NewBusinessesPerMinute: report.Totals.NewBusinessesPerMinute,
	}

	if encoded, err := json.Marshal(report); err == nil {
		snapshot.Report = string(encoded)
	} else {
		snapshot.Report = "{}"
	}

	return snapshot
}

// CaptureJobBenchmark derives one job's benchmark report and stores it as a
// snapshot, replacing any earlier snapshot for that job. It is the only
// writer of benchmark history and never touches the run itself.
func (s *Service) CaptureJobBenchmark(ctx context.Context, jobID string) (BenchmarkSnapshot, error) {
	repository, err := s.benchmarkHistoryRepository()
	if err != nil {
		return BenchmarkSnapshot{}, err
	}

	report, err := s.JobBenchmark(ctx, jobID)
	if err != nil {
		return BenchmarkSnapshot{}, err
	}

	snapshot := benchmarkSnapshotOf(report, time.Now().UTC())
	if err := repository.SaveBenchmarkSnapshot(ctx, snapshot); err != nil {
		return BenchmarkSnapshot{}, err
	}

	return snapshot, nil
}

// BenchmarkHistory lists the most recently captured snapshots, newest first.
func (s *Service) BenchmarkHistory(ctx context.Context, limit int) ([]BenchmarkSnapshot, error) {
	repository, err := s.benchmarkHistoryRepository()
	if err != nil {
		return nil, err
	}

	if limit <= 0 {
		limit = DefaultBenchmarkHistoryLimit
	}

	if limit > MaximumBenchmarkHistoryLimit {
		limit = MaximumBenchmarkHistoryLimit
	}

	return repository.ListBenchmarkSnapshots(ctx, limit)
}

// jobBenchmarkSnapshot returns the stored snapshot for a job, deriving and
// caching one when none exists yet. Deriving here is what lets an operator
// compare runs that finished before snapshots were ever captured.
func (s *Service) jobBenchmarkSnapshot(
	ctx context.Context,
	repository benchmarkHistoryRepository,
	jobID string,
) (BenchmarkSnapshot, error) {
	snapshot, err := repository.GetBenchmarkSnapshot(ctx, jobID)
	if err == nil {
		return snapshot, nil
	}

	if !errors.Is(err, ErrBenchmarkSnapshotNotFound) {
		return BenchmarkSnapshot{}, err
	}

	return s.CaptureJobBenchmark(ctx, jobID)
}

// BenchmarkSeriesRequest selects the runs one comparison series charts.
// Exactly one of CampaignID and JobIDs is used; CampaignID wins when both
// are supplied, because campaign order is the more meaningful sequence.
type BenchmarkSeriesRequest struct {
	CampaignID string
	JobIDs     []string
}

// CompareJobBenchmarkSeries builds a stable, chartable series over a
// campaign or an explicit list of runs.
//
// Points are ordered by campaign generation when the series covers a
// campaign, and by the order requested otherwise, so a chart's x-axis is
// meaningful and reproducible. Each point comes from the stored snapshot,
// derived and cached on first use, so the series is read-only with respect
// to the runs themselves.
func (s *Service) CompareJobBenchmarkSeries(
	ctx context.Context,
	request BenchmarkSeriesRequest,
) (BenchmarkSeries, error) {
	repository, err := s.benchmarkHistoryRepository()
	if err != nil {
		return BenchmarkSeries{}, err
	}

	series := BenchmarkSeries{Metrics: benchmarkSeriesMetrics()}

	lineage := make(map[string]JobCampaignLink)
	ids := request.JobIDs

	if campaignID := strings.TrimSpace(request.CampaignID); campaignID != "" {
		campaign, campaignErr := s.campaignSeriesLineage(ctx, campaignID)
		if campaignErr != nil {
			return BenchmarkSeries{}, campaignErr
		}

		series.CampaignID = campaign.CampaignID
		ids = ids[:0:0]

		for _, link := range campaign.Jobs {
			lineage[link.JobID] = link
			ids = append(ids, link.JobID)
		}
	}

	ids = uniqueBenchmarkJobIDs(ids)
	if len(ids) == 0 {
		return BenchmarkSeries{}, fmt.Errorf("%w: a campaign or at least one job is required", ErrInvalidBenchmarkSeries)
	}

	if len(ids) > MaximumBenchmarkSeriesJobs {
		return BenchmarkSeries{}, fmt.Errorf("%w: at most %d runs may be compared at once",
			ErrInvalidBenchmarkSeries, MaximumBenchmarkSeriesJobs)
	}

	series.Points = make([]BenchmarkSeriesPoint, 0, len(ids))

	for index, jobID := range ids {
		snapshot, snapshotErr := s.jobBenchmarkSnapshot(ctx, repository, jobID)
		if snapshotErr != nil {
			return BenchmarkSeries{}, snapshotErr
		}

		point := benchmarkSeriesPointOf(snapshot, index+1)
		point.Generation = -1

		if link, found := lineage[jobID]; found {
			point.Generation = link.Generation
			point.Mode = link.Mode
		}

		series.Points = append(series.Points, point)
	}

	if len(series.Points) > 1 {
		first, last := series.Points[0], series.Points[len(series.Points)-1]
		series.Delta = BenchmarkDelta{
			UniqueBusinesses:       last.UniqueBusinesses - first.UniqueBusinesses,
			NewBusinessesPerMinute: roundBenchmarkRatio(last.NewBusinessesPerMinute - first.NewBusinessesPerMinute),
			DuplicateRate:          roundBenchmarkRatio(last.DuplicateRate - first.DuplicateRate),
			TasksFailed:            last.TasksFailed - first.TasksFailed,
			Retries:                last.Retries - first.Retries,
			WallSeconds:            roundBenchmarkRatio(last.WallSeconds - first.WallSeconds),
		}
	}

	return series, nil
}

// ErrInvalidBenchmarkSeries identifies a rejected series request.
var ErrInvalidBenchmarkSeries = errors.New("invalid benchmark series request")

// campaignSeriesLineage resolves a campaign identifier, which an operator may
// give either as the campaign ID or as any job inside it.
func (s *Service) campaignSeriesLineage(ctx context.Context, campaignID string) (JobCampaign, error) {
	links, err := s.CampaignJobIDs(ctx, campaignID)
	if err != nil {
		return JobCampaign{}, err
	}

	if len(links) > 0 {
		campaign, campaignErr := s.JobCampaignOf(ctx, links[0])
		if campaignErr != nil {
			return JobCampaign{}, campaignErr
		}

		return campaign, nil
	}

	return s.JobCampaignOf(ctx, campaignID)
}

func benchmarkSeriesPointOf(snapshot BenchmarkSnapshot, seq int) BenchmarkSeriesPoint {
	return BenchmarkSeriesPoint{
		Seq:                    seq,
		JobID:                  snapshot.JobID,
		JobName:                snapshot.JobName,
		CapturedAt:             snapshot.CapturedAt,
		UniqueBusinesses:       snapshot.UniqueBusinesses,
		RowsAdded:              snapshot.RowsAdded,
		DuplicatesSkipped:      snapshot.DuplicatesSkipped,
		DuplicateRate:          snapshot.DuplicateRate,
		TasksCompleted:         snapshot.TasksCompleted,
		TasksFailed:            snapshot.TasksFailed,
		TasksSkipped:           snapshot.TasksSkipped,
		Retries:                snapshot.Retries,
		WallSeconds:            snapshot.WallSeconds,
		NewBusinessesPerMinute: snapshot.NewBusinessesPerMinute,
	}
}

// benchmarkSeriesMetrics is the fixed catalogue of chartable columns. It is
// deliberately stable: a front end binds to these keys.
func benchmarkSeriesMetrics() []BenchmarkSeriesMetric {
	return []BenchmarkSeriesMetric{
		{Key: "unique_businesses", Label: "Unique businesses", Kind: "count", Better: "higher"},
		{Key: "new_businesses_per_minute", Label: "New businesses per minute", Kind: "rate", Better: "higher"},
		{Key: "rows_added", Label: "Rows added", Kind: "count", Better: "higher"},
		{Key: "duplicates_skipped", Label: "Duplicates skipped", Kind: "count", Better: "lower"},
		{Key: "duplicate_rate", Label: "Duplicate rate", Kind: "ratio", Better: "lower"},
		{Key: "tasks_completed", Label: "Tasks completed", Kind: "count", Better: "higher"},
		{Key: "tasks_failed", Label: "Tasks failed", Kind: "count", Better: "lower"},
		{Key: "tasks_skipped", Label: "Tasks skipped", Kind: "count", Better: "lower"},
		{Key: "retries", Label: "Retries", Kind: "count", Better: "lower"},
		{Key: "wall_seconds", Label: "Wall seconds", Kind: "seconds", Better: "lower"},
	}
}

// uniqueBenchmarkJobIDs trims, drops empties, and removes repeats while
// keeping the caller's order.
func uniqueBenchmarkJobIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	unique := make([]string, 0, len(ids))

	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}

		if _, duplicate := seen[id]; duplicate {
			continue
		}

		seen[id] = struct{}{}
		unique = append(unique, id)
	}

	return unique
}

// SortBenchmarkSnapshotsByCapture orders snapshots newest first, breaking
// ties on job ID so repeated calls agree.
func SortBenchmarkSnapshotsByCapture(snapshots []BenchmarkSnapshot) {
	sort.SliceStable(snapshots, func(a, b int) bool {
		if !snapshots[a].CapturedAt.Equal(snapshots[b].CapturedAt) {
			return snapshots[a].CapturedAt.After(snapshots[b].CapturedAt)
		}

		return snapshots[a].JobID < snapshots[b].JobID
	})
}
