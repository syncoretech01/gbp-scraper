package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/gosom/google-maps-scraper/web"
)

var _ interface {
	JobPipelineFacts(context.Context, string) (web.JobPipelineFacts, error)
} = (*repo)(nil)

// JobPipelineFacts gathers the durable per-stage evidence for one job in a
// handful of bounded aggregate queries. Nothing loads a row set into Go memory,
// so the live monitor can refresh it while the job is still running.
//
// Every query that reaches a per-business table scopes itself with
// "business_id IN (SELECT business_id FROM business_sources WHERE job_id = ?)"
// rather than joining through business_sources. The join is a fan-out: one
// business observed by several queries or grid cells has several
// business_sources rows, so a joined COUNT(*) or SUM() multiplies each website
// by the number of times the job saw its business. On the acceptance job
// cfe2d653 that reported 60 websites checked where 25 were, and 110 pages
// crawled where 35 were.
func (repo *repo) JobPipelineFacts(ctx context.Context, jobID string) (web.JobPipelineFacts, error) {
	facts := web.JobPipelineFacts{
		EventsByType:          map[string]int64{},
		EmailRejectionReasons: map[string]int64{},
	}

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

	if err := repo.readJobWebsiteFacts(ctx, jobID, &facts); err != nil {
		return web.JobPipelineFacts{}, err
	}

	if err := repo.readJobEmailFacts(ctx, jobID, &facts); err != nil {
		return web.JobPipelineFacts{}, err
	}

	if err := repo.readJobEnrichmentFacts(ctx, jobID, &facts); err != nil {
		return web.JobPipelineFacts{}, err
	}

	return facts, nil
}

// readJobWebsiteFacts counts the crawl evidence once per website row. See the
// fan-out note on JobPipelineFacts for why this cannot join business_sources.
func (repo *repo) readJobWebsiteFacts(
	ctx context.Context,
	jobID string,
	facts *web.JobPipelineFacts,
) error {
	if err := repo.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN websites.status = 'active' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN websites.status IN ('inactive', 'error') THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(websites.pages_checked), 0),
			COALESCE(AVG(CASE WHEN websites.response_time_ms > 0 THEN websites.response_time_ms END), 0),
			COUNT(DISTINCT CASE WHEN websites.domain <> '' THEN websites.domain END),
			COALESCE((
				SELECT latest.http_status FROM websites AS latest
				WHERE latest.business_id IN (
					SELECT business_id FROM business_sources WHERE job_id = ?
				) AND latest.http_status IS NOT NULL
				ORDER BY latest.last_checked_at DESC, latest.id DESC LIMIT 1
			), 0)
		FROM websites
		WHERE websites.business_id IN (
			SELECT business_id FROM business_sources WHERE job_id = ?
		) AND websites.last_checked_at IS NOT NULL`, jobID, jobID,
	).Scan(
		&facts.WebsitesChecked,
		&facts.WebsitesActive,
		&facts.WebsitesInactive,
		&facts.PagesChecked,
		&facts.AverageResponseMS,
		&facts.DomainsChecked,
		&facts.LastHTTPStatus,
	); err != nil {
		return fmt.Errorf("read job website crawl facts: %w", err)
	}

	return nil
}

// readJobEmailFacts reads both the stored addresses and the extraction funnel
// that explains them. The funnel lives in the immutable per-audit result JSON,
// so a run that discovered candidates but stored none can still say why.
func (repo *repo) readJobEmailFacts(
	ctx context.Context,
	jobID string,
	facts *web.JobPipelineFacts,
) error {
	if err := repo.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT normalized_value)
		FROM emails
		WHERE business_id IN (SELECT business_id FROM business_sources WHERE job_id = ?)`,
		jobID,
	).Scan(&facts.EmailAddresses); err != nil {
		return fmt.Errorf("read job email address facts: %w", err)
	}

	if err := repo.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(json_extract(raw_result, '$.email_funnel.distinct')), 0),
			COALESCE(SUM(json_extract(raw_result, '$.email_funnel.accepted')), 0),
			COALESCE(SUM(json_extract(raw_result, '$.email_funnel.rejected')), 0),
			COALESCE(SUM(json_extract(raw_result, '$.email_funnel.repaired')), 0)
		FROM website_audits
		WHERE task_id IN (SELECT id FROM enrichment_tasks WHERE job_id = ?)
			AND json_valid(raw_result)`,
		jobID,
	).Scan(
		&facts.EmailCandidates,
		&facts.EmailsAccepted,
		&facts.EmailsRejected,
		&facts.EmailsRepaired,
	); err != nil {
		return fmt.Errorf("read job email funnel facts: %w", err)
	}

	rows, err := repo.db.QueryContext(ctx, `
		SELECT reason.key, SUM(reason.value)
		FROM website_audits,
			json_each(json_extract(website_audits.raw_result, '$.email_funnel.rejection_reasons')) AS reason
		WHERE website_audits.task_id IN (SELECT id FROM enrichment_tasks WHERE job_id = ?)
			AND json_valid(website_audits.raw_result)
			AND json_extract(website_audits.raw_result, '$.email_funnel.rejection_reasons') IS NOT NULL
		GROUP BY reason.key`, jobID)
	if err != nil {
		return fmt.Errorf("read job email rejection facts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			reason string
			count  int64
		)
		if err := rows.Scan(&reason, &count); err != nil {
			return fmt.Errorf("scan job email rejection facts: %w", err)
		}

		facts.EmailRejectionReasons[reason] += count
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate job email rejection facts: %w", err)
	}

	return nil
}

// readJobEnrichmentFacts reports the second pipeline stage as its own timed
// unit of work. Discovery and enrichment are separate phases with separate
// clocks: a job whose listing walk took six seconds and whose website audits
// took a further two and a half minutes must be able to say both, instead of
// reporting six seconds for a run the operator watched for three minutes.
func (repo *repo) readJobEnrichmentFacts(
	ctx context.Context,
	jobID string,
	facts *web.JobPipelineFacts,
) error {
	var (
		startedAt  sql.NullInt64
		finishedAt sql.NullInt64
	)

	if err := repo.db.QueryRowContext(ctx, `
		SELECT started_at, finished_at FROM job_runtime WHERE job_id = ?`, jobID,
	).Scan(&startedAt, &finishedAt); err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read job discovery timing: %w", err)
	}

	facts.DiscoveryStartedAt = startedAt.Int64
	facts.DiscoveryFinishedAt = finishedAt.Int64
	if startedAt.Valid && finishedAt.Valid && finishedAt.Int64 >= startedAt.Int64 {
		facts.DiscoveryDurationMS = (finishedAt.Int64 - startedAt.Int64) * 1000
	}

	var (
		enrichmentStarted  sql.NullInt64
		enrichmentFinished sql.NullInt64
	)

	if err := repo.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN state = 'queued' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'running' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'completed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END), 0),
			MIN(COALESCE(started_at, created_at)),
			MAX(finished_at)
		FROM enrichment_tasks WHERE job_id = ?`, jobID,
	).Scan(
		&facts.EnrichmentTasksTotal,
		&facts.EnrichmentQueued,
		&facts.EnrichmentRunning,
		&facts.EnrichmentCompleted,
		&facts.EnrichmentFailed,
		&enrichmentStarted,
		&enrichmentFinished,
	); err != nil {
		return fmt.Errorf("read job enrichment task facts: %w", err)
	}

	facts.EnrichmentStartedAt = enrichmentStarted.Int64
	facts.EnrichmentFinishedAt = enrichmentFinished.Int64
	facts.EnrichmentComplete = facts.EnrichmentTasksTotal > 0 &&
		facts.EnrichmentQueued == 0 && facts.EnrichmentRunning == 0

	if enrichmentStarted.Valid && enrichmentFinished.Valid &&
		enrichmentFinished.Int64 >= enrichmentStarted.Int64 {
		facts.EnrichmentDurationMS = (enrichmentFinished.Int64 - enrichmentStarted.Int64) * 1000
	}

	if err := repo.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM website_audits
		WHERE task_id IN (SELECT id FROM enrichment_tasks WHERE job_id = ?)
			AND json_valid(raw_result)
			AND json_extract(raw_result, '$.cache.reused_from_audit_id') IS NOT NULL`, jobID,
	).Scan(&facts.EnrichmentReused); err != nil {
		return fmt.Errorf("read job enrichment cache reuse facts: %w", err)
	}

	// End to end is the span the operator actually waited: from the moment
	// discovery started to whichever stage finished last.
	if startedAt.Valid {
		end := finishedAt.Int64
		if enrichmentFinished.Valid && enrichmentFinished.Int64 > end {
			end = enrichmentFinished.Int64
		}
		if end >= startedAt.Int64 {
			facts.TotalDurationMS = (end - startedAt.Int64) * 1000
		}
	}

	return nil
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
		// Severity is counted through the honest-severity policy rather than as
		// the emitter recorded it, so a run already in the workspace stops
		// reporting 118 warnings it never earned. See web/job_event_severity.go.
		switch web.HonestJobEventSeverity(eventType, severity) {
		case web.JobEventSeverityWarning:
			facts.Warnings += count
		case web.JobEventSeverityError:
			facts.Errors += count
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate job event facts: %w", err)
	}

	return nil
}
