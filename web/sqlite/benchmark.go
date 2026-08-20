package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// benchmarkEventLimit bounds how many warning/error events a single report
// reads; a run producing more than this is already conclusively unhealthy.
const benchmarkEventLimit = 10000

var _ interface {
	JobBenchmarkEvidence(context.Context, string) (web.BenchmarkEvidence, error)
} = (*repo)(nil)

// JobBenchmarkEvidence gathers, read-only, everything a benchmark report for
// one job is computed from: the job and runtime row, every durable task with
// its merged checkpoint counters, warning/error events, aggregates over the
// businesses linked to the job, and per-proxy task stats (empty when the run
// used no proxies).
func (repo *repo) JobBenchmarkEvidence(ctx context.Context, jobID string) (web.BenchmarkEvidence, error) {
	evidence, err := repo.benchmarkJobHeader(ctx, jobID)
	if err != nil {
		return web.BenchmarkEvidence{}, err
	}
	if err := repo.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&evidence.SchemaVersion); err != nil {
		return web.BenchmarkEvidence{}, fmt.Errorf("read schema version: %w", err)
	}
	if evidence.Tasks, err = repo.benchmarkTasks(ctx, jobID); err != nil {
		return web.BenchmarkEvidence{}, err
	}
	if evidence.Events, err = repo.benchmarkEvents(ctx, jobID); err != nil {
		return web.BenchmarkEvidence{}, err
	}
	if evidence.Businesses, err = repo.benchmarkBusinesses(ctx, jobID); err != nil {
		return web.BenchmarkEvidence{}, err
	}
	if evidence.Proxies, err = repo.benchmarkProxies(ctx); err != nil {
		return web.BenchmarkEvidence{}, err
	}

	return evidence, nil
}

func (repo *repo) benchmarkJobHeader(ctx context.Context, jobID string) (web.BenchmarkEvidence, error) {
	var evidence web.BenchmarkEvidence
	var startedAt, finishedAt sql.NullInt64
	err := repo.db.QueryRowContext(
		ctx,
		`SELECT jobs.id, jobs.name, jobs.created_at,
			COALESCE(job_runtime.started_at, 0), COALESCE(job_runtime.finished_at, 0),
			COALESCE(job_runtime.raw_records, 0), COALESCE(job_runtime.unique_records, 0),
			COALESCE(job_runtime.duplicate_records, 0), COALESCE(job_runtime.scraper_version, '')
		FROM jobs
		LEFT JOIN job_runtime ON job_runtime.job_id = jobs.id
		WHERE jobs.id = ?`,
		jobID,
	).Scan(
		&evidence.JobID,
		&evidence.JobName,
		&evidence.CreatedAt,
		&startedAt,
		&finishedAt,
		&evidence.RawRecords,
		&evidence.UniqueRecords,
		&evidence.DuplicateRecords,
		&evidence.ScraperVersion,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return web.BenchmarkEvidence{}, fmt.Errorf("%w: %s", web.ErrLifecycleNotFound, jobID)
	}
	if err != nil {
		return web.BenchmarkEvidence{}, fmt.Errorf("read benchmark job: %w", err)
	}
	evidence.StartedAt = startedAt.Int64
	evidence.FinishedAt = finishedAt.Int64

	return evidence, nil
}

func (repo *repo) benchmarkTasks(ctx context.Context, jobID string) ([]web.BenchmarkTaskEvidence, error) {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT task_key, query, origin, state, sequence, attempts, last_error,
			COALESCE(finished_at, 0), checkpoint
		FROM job_tasks WHERE job_id = ?
		ORDER BY sequence, task_key`,
		jobID,
	)
	if err != nil {
		return nil, fmt.Errorf("read benchmark tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	tasks := make([]web.BenchmarkTaskEvidence, 0)
	for rows.Next() {
		var task web.BenchmarkTaskEvidence
		var checkpoint string
		if err := rows.Scan(
			&task.Key,
			&task.Query,
			&task.Origin,
			&task.State,
			&task.Sequence,
			&task.Attempts,
			&task.LastError,
			&task.FinishedAt,
			&checkpoint,
		); err != nil {
			return nil, fmt.Errorf("scan benchmark task: %w", err)
		}
		task.LastError = jobruntime.RedactString(task.LastError)
		var merged web.JobTaskCheckpoint
		if err := json.Unmarshal([]byte(checkpoint), &merged); err == nil {
			task.RowsAdded = merged.RowsAdded
			task.RowsReplaced = merged.RowsReplaced
			task.DuplicatesSkipped = merged.DuplicatesSkipped
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate benchmark tasks: %w", err)
	}

	return tasks, nil
}

func (repo *repo) benchmarkEvents(ctx context.Context, jobID string) ([]web.BenchmarkEventEvidence, error) {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT type, severity, message, context
		FROM job_events
		WHERE job_id = ? AND severity IN ('warning', 'error')
		ORDER BY id LIMIT ?`,
		jobID,
		benchmarkEventLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("read benchmark events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	events := make([]web.BenchmarkEventEvidence, 0)
	for rows.Next() {
		var event web.BenchmarkEventEvidence
		if err := rows.Scan(&event.Type, &event.Severity, &event.Message, &event.Context); err != nil {
			return nil, fmt.Errorf("scan benchmark event: %w", err)
		}
		event.Message = jobruntime.RedactString(event.Message)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate benchmark events: %w", err)
	}

	return events, nil
}

func (repo *repo) benchmarkBusinesses(ctx context.Context, jobID string) (web.BenchmarkBusinessEvidence, error) {
	evidence := web.BenchmarkBusinessEvidence{
		WebsiteStatus:  make(map[string]int64),
		ProspectTier:   make(map[string]int64),
		ProspectStatus: make(map[string]int64),
	}

	if err := repo.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*),
			COALESCE(SUM(has_email), 0),
			COALESCE(SUM(has_phone), 0),
			COALESCE(SUM(CASE WHEN has_email = 1 AND has_phone = 1 THEN 1 ELSE 0 END), 0)
		FROM (
			SELECT
				CASE WHEN businesses.emails <> ''
					OR EXISTS(SELECT 1 FROM emails WHERE emails.business_id = businesses.id)
				THEN 1 ELSE 0 END AS has_email,
				CASE WHEN businesses.phone <> ''
					OR EXISTS(SELECT 1 FROM phones WHERE phones.business_id = businesses.id)
				THEN 1 ELSE 0 END AS has_phone
			FROM job_businesses
			JOIN businesses ON businesses.id = job_businesses.business_id
			WHERE job_businesses.job_id = ?
		)`,
		jobID,
	).Scan(&evidence.Unique, &evidence.WithEmail, &evidence.WithPhone, &evidence.WithBoth); err != nil {
		return web.BenchmarkBusinessEvidence{}, fmt.Errorf("aggregate benchmark contacts: %w", err)
	}

	distributions := []struct {
		column string
		into   map[string]int64
	}{
		{column: "website_status", into: evidence.WebsiteStatus},
		{column: "prospect_tier", into: evidence.ProspectTier},
		{column: "prospect_status", into: evidence.ProspectStatus},
	}
	for _, distribution := range distributions {
		// The column name comes from the fixed list above, never from input.
		query := fmt.Sprintf(
			`SELECT businesses.%s, COUNT(*)
			FROM job_businesses
			JOIN businesses ON businesses.id = job_businesses.business_id
			WHERE job_businesses.job_id = ?
			GROUP BY businesses.%s`,
			distribution.column,
			distribution.column,
		)
		if err := repo.benchmarkCounts(ctx, query, jobID, distribution.into); err != nil {
			return web.BenchmarkBusinessEvidence{}, fmt.Errorf("aggregate %s: %w", distribution.column, err)
		}
	}

	return evidence, nil
}

func (repo *repo) benchmarkCounts(ctx context.Context, query, jobID string, into map[string]int64) error {
	rows, err := repo.db.QueryContext(ctx, query, jobID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var label string
		var count int64
		if err := rows.Scan(&label, &count); err != nil {
			return err
		}
		if label == "" {
			label = "unclassified"
		}
		into[label] += count
	}

	return rows.Err()
}

func (repo *repo) benchmarkProxies(ctx context.Context) ([]web.BenchmarkProxyEvidence, error) {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT proxy_task_stats.proxy_id, COALESCE(proxies.name, ''), proxy_task_stats.pool_id,
			proxy_task_stats.task_successes, proxy_task_stats.task_failures,
			proxy_task_stats.consecutive_failures, proxy_task_stats.total_task_seconds,
			COALESCE(proxy_task_stats.last_success_at, 0),
			COALESCE(proxy_task_stats.last_failure_at, 0), proxy_task_stats.last_error
		FROM proxy_task_stats
		LEFT JOIN proxies ON proxies.id = proxy_task_stats.proxy_id
		ORDER BY proxy_task_stats.proxy_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("read benchmark proxy stats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	proxies := make([]web.BenchmarkProxyEvidence, 0)
	for rows.Next() {
		var proxy web.BenchmarkProxyEvidence
		if err := rows.Scan(
			&proxy.ProxyID,
			&proxy.ProxyName,
			&proxy.PoolID,
			&proxy.TaskSuccesses,
			&proxy.TaskFailures,
			&proxy.ConsecutiveFailures,
			&proxy.TotalTaskSeconds,
			&proxy.LastSuccessAt,
			&proxy.LastFailureAt,
			&proxy.LastError,
		); err != nil {
			return nil, fmt.Errorf("scan benchmark proxy stats: %w", err)
		}
		proxy.LastError = jobruntime.RedactString(proxy.LastError)
		proxies = append(proxies, proxy)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate benchmark proxy stats: %w", err)
	}

	return proxies, nil
}
