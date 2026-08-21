package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

var _ interface {
	SaveBenchmarkSnapshot(context.Context, web.BenchmarkSnapshot) error
	GetBenchmarkSnapshot(context.Context, string) (web.BenchmarkSnapshot, error)
	ListBenchmarkSnapshots(context.Context, int) ([]web.BenchmarkSnapshot, error)
} = (*repo)(nil)

// benchmarkSnapshotColumns selects the stored scalars plus the job's current
// display name, which lives on the job row so a rename is reflected without
// rewriting history.
const benchmarkSnapshotColumns = `job_benchmark_snapshots.job_id,
	COALESCE(jobs.name, ''),
	job_benchmark_snapshots.captured_at,
	job_benchmark_snapshots.engine_version,
	job_benchmark_snapshots.schema_version,
	job_benchmark_snapshots.unique_businesses,
	job_benchmark_snapshots.rows_added,
	job_benchmark_snapshots.duplicates_skipped,
	job_benchmark_snapshots.duplicate_rate,
	job_benchmark_snapshots.tasks_completed,
	job_benchmark_snapshots.tasks_failed,
	job_benchmark_snapshots.tasks_skipped,
	job_benchmark_snapshots.retries,
	job_benchmark_snapshots.wall_seconds,
	job_benchmark_snapshots.new_per_minute,
	job_benchmark_snapshots.report`

// SaveBenchmarkSnapshot records or replaces one job's benchmark snapshot.
// Re-capturing a job overwrites its row rather than appending, so history
// stays one snapshot per run and can never grow without bound.
func (repo *repo) SaveBenchmarkSnapshot(ctx context.Context, snapshot web.BenchmarkSnapshot) error {
	capturedAt := snapshot.CapturedAt
	if capturedAt.IsZero() {
		capturedAt = time.Now().UTC()
	}

	report := snapshot.Report
	if report == "" {
		report = "{}"
	}

	_, err := repo.db.ExecContext(
		ctx,
		`INSERT INTO job_benchmark_snapshots(
			job_id, captured_at, engine_version, schema_version, unique_businesses,
			rows_added, duplicates_skipped, duplicate_rate, tasks_completed,
			tasks_failed, tasks_skipped, retries, wall_seconds, new_per_minute, report
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(job_id) DO UPDATE SET
			captured_at = excluded.captured_at,
			engine_version = excluded.engine_version,
			schema_version = excluded.schema_version,
			unique_businesses = excluded.unique_businesses,
			rows_added = excluded.rows_added,
			duplicates_skipped = excluded.duplicates_skipped,
			duplicate_rate = excluded.duplicate_rate,
			tasks_completed = excluded.tasks_completed,
			tasks_failed = excluded.tasks_failed,
			tasks_skipped = excluded.tasks_skipped,
			retries = excluded.retries,
			wall_seconds = excluded.wall_seconds,
			new_per_minute = excluded.new_per_minute,
			report = excluded.report`,
		snapshot.JobID,
		capturedAt.UTC().Unix(),
		snapshot.EngineVersion,
		snapshot.SchemaVersion,
		snapshot.UniqueBusinesses,
		snapshot.RowsAdded,
		snapshot.DuplicatesSkipped,
		snapshot.DuplicateRate,
		snapshot.TasksCompleted,
		snapshot.TasksFailed,
		snapshot.TasksSkipped,
		snapshot.Retries,
		snapshot.WallSeconds,
		snapshot.NewBusinessesPerMinute,
		report,
	)
	if err != nil {
		return fmt.Errorf("save benchmark snapshot for job %q: %w", snapshot.JobID, err)
	}

	return nil
}

// GetBenchmarkSnapshot reads one job's stored snapshot, reporting
// web.ErrBenchmarkSnapshotNotFound when none has been captured.
func (repo *repo) GetBenchmarkSnapshot(ctx context.Context, jobID string) (web.BenchmarkSnapshot, error) {
	row := repo.db.QueryRowContext(
		ctx,
		`SELECT `+benchmarkSnapshotColumns+`
		FROM job_benchmark_snapshots
		LEFT JOIN jobs ON jobs.id = job_benchmark_snapshots.job_id
		WHERE job_benchmark_snapshots.job_id = ?`,
		jobID,
	)

	snapshot, err := scanBenchmarkSnapshot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return web.BenchmarkSnapshot{}, fmt.Errorf("%w: %s", web.ErrBenchmarkSnapshotNotFound, jobID)
	}

	return snapshot, err
}

// ListBenchmarkSnapshots returns the most recently captured snapshots first.
func (repo *repo) ListBenchmarkSnapshots(ctx context.Context, limit int) ([]web.BenchmarkSnapshot, error) {
	if limit <= 0 {
		limit = web.DefaultBenchmarkHistoryLimit
	}

	if limit > web.MaximumBenchmarkHistoryLimit {
		limit = web.MaximumBenchmarkHistoryLimit
	}

	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT `+benchmarkSnapshotColumns+`
		FROM job_benchmark_snapshots
		LEFT JOIN jobs ON jobs.id = job_benchmark_snapshots.job_id
		ORDER BY job_benchmark_snapshots.captured_at DESC, job_benchmark_snapshots.job_id
		LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list benchmark snapshots: %w", err)
	}

	defer func() { _ = rows.Close() }()

	snapshots := make([]web.BenchmarkSnapshot, 0, limit)

	for rows.Next() {
		snapshot, scanErr := scanBenchmarkSnapshot(rows)
		if scanErr != nil {
			return nil, scanErr
		}

		snapshots = append(snapshots, snapshot)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate benchmark snapshots: %w", err)
	}

	return snapshots, nil
}

type benchmarkSnapshotScanner interface {
	Scan(...any) error
}

func scanBenchmarkSnapshot(scanner benchmarkSnapshotScanner) (web.BenchmarkSnapshot, error) {
	var (
		snapshot   web.BenchmarkSnapshot
		capturedAt int64
	)

	if err := scanner.Scan(
		&snapshot.JobID,
		&snapshot.JobName,
		&capturedAt,
		&snapshot.EngineVersion,
		&snapshot.SchemaVersion,
		&snapshot.UniqueBusinesses,
		&snapshot.RowsAdded,
		&snapshot.DuplicatesSkipped,
		&snapshot.DuplicateRate,
		&snapshot.TasksCompleted,
		&snapshot.TasksFailed,
		&snapshot.TasksSkipped,
		&snapshot.Retries,
		&snapshot.WallSeconds,
		&snapshot.NewBusinessesPerMinute,
		&snapshot.Report,
	); err != nil {
		return web.BenchmarkSnapshot{}, fmt.Errorf("scan benchmark snapshot: %w", err)
	}

	snapshot.CapturedAt = time.Unix(capturedAt, 0).UTC()

	return snapshot, nil
}
