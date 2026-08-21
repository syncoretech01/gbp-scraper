package sqlite

import (
	"context"
	"fmt"

	"github.com/gosom/google-maps-scraper/web"
)

var _ interface {
	JobPipelineFacts(context.Context, string) (web.JobPipelineFacts, error)
} = (*repo)(nil)

// JobPipelineFacts gathers the durable per-stage evidence for one job in four
// bounded aggregate queries. Nothing loads a row set into Go memory, so the
// live monitor can refresh it while the job is still running.
func (repo *repo) JobPipelineFacts(ctx context.Context, jobID string) (web.JobPipelineFacts, error) {
	facts := web.JobPipelineFacts{EventsByType: map[string]int64{}}

	if err := repo.db.QueryRowContext(ctx, `
		SELECT
			COUNT(DISTINCT CASE WHEN query <> '' THEN query END),
			COUNT(DISTINCT CASE WHEN source_cell <> '' THEN source_cell END),
			COUNT(*),
			COALESCE(SUM(CASE WHEN state = 'completed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'skipped' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'running' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'pending' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(attempts), 0),
			COALESCE(SUM(CASE WHEN attempts > 1 THEN attempts - 1 ELSE 0 END), 0)
		FROM job_tasks WHERE job_id = ?`, jobID,
	).Scan(
		&facts.QueriesPlanned,
		&facts.CellsPlanned,
		&facts.TasksTotal,
		&facts.TasksCompleted,
		&facts.TasksFailed,
		&facts.TasksSkipped,
		&facts.TasksRunning,
		&facts.TasksPending,
		&facts.Attempts,
		&facts.Retries,
	); err != nil {
		return web.JobPipelineFacts{}, fmt.Errorf("read job task plan facts: %w", err)
	}

	if err := repo.readJobEventFacts(ctx, jobID, &facts); err != nil {
		return web.JobPipelineFacts{}, err
	}

	if err := repo.db.QueryRowContext(ctx, `
		SELECT
			COUNT(DISTINCT businesses.id),
			COUNT(DISTINCT CASE WHEN businesses.website <> '' THEN businesses.id END),
			COUNT(DISTINCT CASE WHEN EXISTS (
				SELECT 1 FROM emails WHERE emails.business_id = businesses.id) THEN businesses.id END),
			COUNT(DISTINCT CASE WHEN businesses.normalized_phone <> '' OR EXISTS (
				SELECT 1 FROM phones WHERE phones.business_id = businesses.id) THEN businesses.id END),
			COUNT(DISTINCT CASE WHEN EXISTS (
				SELECT 1 FROM social_profiles WHERE social_profiles.business_id = businesses.id)
				THEN businesses.id END),
			COUNT(DISTINCT CASE WHEN businesses.merged_into_id IS NOT NULL THEN businesses.id END)
		FROM business_sources
		JOIN businesses ON businesses.id = business_sources.business_id
		WHERE business_sources.job_id = ? AND businesses.deleted_at IS NULL`, jobID,
	).Scan(
		&facts.UniqueBusinesses,
		&facts.WithWebsite,
		&facts.WithEmail,
		&facts.WithPhone,
		&facts.WithSocial,
		&facts.Merged,
	); err != nil {
		return web.JobPipelineFacts{}, fmt.Errorf("read job business facts: %w", err)
	}

	if err := repo.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN websites.status = 'active' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN websites.status IN ('inactive', 'error') THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(websites.pages_checked), 0),
			COALESCE(AVG(CASE WHEN websites.response_time_ms > 0 THEN websites.response_time_ms END), 0),
			COALESCE((
				SELECT latest.http_status FROM websites AS latest
				JOIN business_sources AS source ON source.business_id = latest.business_id
				WHERE source.job_id = ? AND latest.http_status IS NOT NULL
				ORDER BY latest.last_checked_at DESC, latest.id DESC LIMIT 1
			), 0)
		FROM websites
		JOIN business_sources ON business_sources.business_id = websites.business_id
		WHERE business_sources.job_id = ? AND websites.last_checked_at IS NOT NULL`, jobID, jobID,
	).Scan(
		&facts.WebsitesChecked,
		&facts.WebsitesActive,
		&facts.WebsitesInactive,
		&facts.PagesChecked,
		&facts.AverageResponseMS,
		&facts.LastHTTPStatus,
	); err != nil {
		return web.JobPipelineFacts{}, fmt.Errorf("read job website crawl facts: %w", err)
	}

	return facts, nil
}

func (repo *repo) readJobEventFacts(ctx context.Context, jobID string, facts *web.JobPipelineFacts) error {
	rows, err := repo.db.QueryContext(ctx, `
		SELECT type, severity, COUNT(*)
		FROM job_events WHERE job_id = ?
		GROUP BY type, severity`, jobID)
	if err != nil {
		return fmt.Errorf("read job event facts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			eventType string
			severity  string
			count     int64
		)
		if err := rows.Scan(&eventType, &severity, &count); err != nil {
			return fmt.Errorf("scan job event facts: %w", err)
		}

		facts.EventsByType[eventType] += count
		switch severity {
		case "warning":
			facts.Warnings += count
		case "error":
			facts.Errors += count
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate job event facts: %w", err)
	}

	return nil
}
