package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

var errInvalidScheduleConfiguration = errors.New("invalid schedule configuration")

const (
	scheduleRunStateQueued       = "queued"
	scheduleRunStateRetryPending = "retry_pending"
)

// scheduleSelectColumns is shared by every schedule read so scanSchedule stays
// aligned with one column order.
const scheduleSelectColumns = "schedules.id, schedules.name, COALESCE(schedules.template_id, ''), " +
	"COALESCE(templates.name, ''), schedules.timezone, schedules.enabled, schedules.configuration, " +
	"schedules.next_run_at, schedules.last_run_at, schedules.retry_count, schedules.retry_backoff_seconds, " +
	"schedules.auto_export_format, schedules.runs_retention_days, schedules.created_at, schedules.updated_at"

// scheduleRunSelectColumns is shared by the run-history reads so
// scanScheduleRun stays aligned with one column order.
const scheduleRunSelectColumns = "schedule_runs.id, schedule_runs.schedule_id, schedules.name, " +
	"COALESCE(schedule_runs.job_id, ''), COALESCE(job_runtime.state, schedule_runs.state), " +
	"schedule_runs.scheduled_for, COALESCE(job_runtime.started_at, schedule_runs.started_at), " +
	"COALESCE(job_runtime.finished_at, schedule_runs.finished_at), schedule_runs.attempt, schedule_runs.error"

func (repo *repo) ListSchedules(ctx context.Context) ([]web.ScheduleRecord, error) {
	rows, err := repo.db.QueryContext(ctx,
		"SELECT "+scheduleSelectColumns+" "+
			"FROM schedules LEFT JOIN templates ON templates.id = schedules.template_id "+
			"ORDER BY schedules.enabled DESC, schedules.next_run_at, schedules.name",
	)
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	defer rows.Close()
	schedules := make([]web.ScheduleRecord, 0)
	for rows.Next() {
		schedule, scanErr := scanSchedule(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		schedules = append(schedules, schedule)
	}
	return schedules, rows.Err()
}

func (repo *repo) ListScheduleRuns(ctx context.Context, limit int) ([]web.ScheduleRunRecord, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := repo.db.QueryContext(ctx,
		"SELECT "+scheduleRunSelectColumns+" "+
			"FROM schedule_runs JOIN schedules ON schedules.id = schedule_runs.schedule_id "+
			"LEFT JOIN job_runtime ON job_runtime.job_id = schedule_runs.job_id "+
			"ORDER BY schedule_runs.scheduled_for DESC, schedule_runs.id DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list schedule runs: %w", err)
	}
	defer rows.Close()
	runs := make([]web.ScheduleRunRecord, 0)
	for rows.Next() {
		run, scanErr := scanScheduleRun(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// ListScheduleRunsForSchedule returns one schedule's run history, newest
// first, including automatic retry attempts.
func (repo *repo) ListScheduleRunsForSchedule(
	ctx context.Context,
	scheduleID string,
	limit int,
) ([]web.ScheduleRunRecord, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := repo.db.QueryContext(ctx,
		"SELECT "+scheduleRunSelectColumns+" "+
			"FROM schedule_runs JOIN schedules ON schedules.id = schedule_runs.schedule_id "+
			"LEFT JOIN job_runtime ON job_runtime.job_id = schedule_runs.job_id "+
			"WHERE schedule_runs.schedule_id = ? "+
			"ORDER BY schedule_runs.scheduled_for DESC, schedule_runs.id DESC LIMIT ?",
		scheduleID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list schedule runs for %s: %w", scheduleID, err)
	}
	defer rows.Close()
	runs := make([]web.ScheduleRunRecord, 0)
	for rows.Next() {
		run, scanErr := scanScheduleRun(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// GetSchedule loads one schedule with its automation settings.
func (repo *repo) GetSchedule(ctx context.Context, id string) (web.ScheduleRecord, error) {
	return repo.getSchedule(ctx, repo.db, id)
}

func (repo *repo) SaveSchedule(ctx context.Context, schedule web.ScheduleRecord) error {
	configuration, err := json.Marshal(schedule.Spec)
	if err != nil {
		return fmt.Errorf("encode schedule: %w", err)
	}
	createdAt := schedule.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := schedule.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	var nextRunAt any
	if schedule.NextRunAt != nil {
		nextRunAt = schedule.NextRunAt.Unix()
	}
	cronExpression := schedule.Spec.Recurrence
	if schedule.Spec.Recurrence == "cron" {
		cronExpression = schedule.Spec.CustomCron
	}
	_, err = repo.db.ExecContext(ctx,
		"INSERT INTO schedules(id, name, template_id, cron_expression, timezone, enabled, overlap_policy, "+
			"missed_run_policy, configuration, next_run_at, retry_count, retry_backoff_seconds, "+
			"auto_export_format, runs_retention_days, created_at, updated_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT(id) DO UPDATE SET name = excluded.name, template_id = excluded.template_id, "+
			"cron_expression = excluded.cron_expression, timezone = excluded.timezone, enabled = excluded.enabled, "+
			"overlap_policy = excluded.overlap_policy, missed_run_policy = excluded.missed_run_policy, "+
			"configuration = excluded.configuration, next_run_at = excluded.next_run_at, "+
			"retry_count = excluded.retry_count, retry_backoff_seconds = excluded.retry_backoff_seconds, "+
			"auto_export_format = excluded.auto_export_format, runs_retention_days = excluded.runs_retention_days, "+
			"updated_at = excluded.updated_at",
		schedule.ID, schedule.Name, schedule.TemplateID, cronExpression, schedule.Timezone, schedule.Enabled,
		schedule.Spec.OverlapPolicy, schedule.Spec.MissedPolicy, string(configuration), nextRunAt,
		schedule.RetryCount, schedule.RetryBackoffSeconds, schedule.AutoExportFormat, schedule.RunsRetentionDays,
		createdAt.Unix(), updatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("save schedule: %w", err)
	}
	return nil
}

func (repo *repo) SetScheduleEnabled(ctx context.Context, id string, enabled bool) error {
	var nextRun any
	if enabled {
		schedule, err := repo.getSchedule(ctx, repo.db, id)
		if err != nil {
			return err
		}
		next, err := web.NextScheduleTime(schedule.Spec, schedule.Timezone, time.Now().UTC())
		if err != nil {
			return err
		}
		if next.IsZero() {
			return fmt.Errorf("schedule has no future occurrence")
		}
		nextRun = next.Unix()
	}
	result, err := repo.db.ExecContext(ctx,
		"UPDATE schedules SET enabled = ?, next_run_at = ?, updated_at = ? WHERE id = ?",
		enabled, nextRun, time.Now().UTC().Unix(), id,
	)
	return requireScheduleResult(result, err)
}

func (repo *repo) DeleteSchedule(ctx context.Context, id string) error {
	return requireScheduleResult(repo.db.ExecContext(ctx, "DELETE FROM schedules WHERE id = ?", id))
}

func (repo *repo) RunScheduleNow(ctx context.Context, id string, now time.Time) (web.Job, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return web.Job{}, fmt.Errorf("start manual schedule run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	schedule, err := repo.getSchedule(ctx, tx, id)
	if err != nil {
		return web.Job{}, err
	}
	job, err := repo.enqueueSchedule(ctx, tx, schedule, now.UTC())
	if err != nil {
		return web.Job{}, err
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE schedules SET last_run_at = ?, updated_at = ? WHERE id = ?",
		now.UTC().Unix(), now.UTC().Unix(), id,
	); err != nil {
		return web.Job{}, fmt.Errorf("update manual schedule run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return web.Job{}, fmt.Errorf("commit manual schedule run: %w", err)
	}
	return job, nil
}

func (repo *repo) StartDueSchedules(ctx context.Context, now time.Time, limit int) ([]web.Job, error) {
	if limit < 1 || limit > 100 {
		limit = 10
	}
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("start due schedules: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx,
		"SELECT id, next_run_at FROM schedules WHERE enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ? "+
			"ORDER BY next_run_at, id LIMIT ?",
		now.UTC().Unix(), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("find due schedules: %w", err)
	}
	type dueSchedule struct {
		id  string
		due time.Time
	}
	dueSchedules := make([]dueSchedule, 0)
	for rows.Next() {
		var item dueSchedule
		var dueUnix int64
		if err := rows.Scan(&item.id, &dueUnix); err != nil {
			_ = rows.Close()
			return nil, err
		}
		item.due = time.Unix(dueUnix, 0).UTC()
		dueSchedules = append(dueSchedules, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	jobs := make([]web.Job, 0, len(dueSchedules))
	for _, dueSchedule := range dueSchedules {
		schedule, err := repo.getSchedule(ctx, tx, dueSchedule.id)
		if err != nil {
			if errors.Is(err, web.ErrScheduleNotFound) {
				continue
			}
			if quarantineErr := quarantineDueSchedule(
				ctx, tx, dueSchedule.id, dueSchedule.due, now.UTC(), err,
			); quarantineErr != nil {
				return nil, quarantineErr
			}
			continue
		}
		due := dueSchedule.due
		next, err := web.NextScheduleTime(schedule.Spec, schedule.Timezone, now.UTC())
		if err != nil {
			if quarantineErr := quarantineDueSchedule(ctx, tx, schedule.ID, due, now.UTC(), err); quarantineErr != nil {
				return nil, quarantineErr
			}
			continue
		}
		skipReason := ""
		if schedule.Spec.MissedPolicy == "skip" && now.Sub(due) > time.Minute {
			skipReason = "missed while the scheduler was offline"
		}
		if skipReason == "" && schedule.Spec.OverlapPolicy == "skip" {
			active, activeErr := scheduleHasActiveJob(ctx, tx, schedule.ID)
			if activeErr != nil {
				return nil, activeErr
			}
			if active {
				skipReason = "previous scheduled job is still active"
			}
		}
		if skipReason != "" {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO schedule_runs(schedule_id, state, scheduled_for, finished_at, error) VALUES (?, 'skipped', ?, ?, ?)",
				schedule.ID, due.Unix(), now.UTC().Unix(), skipReason,
			); err != nil {
				return nil, fmt.Errorf("record skipped schedule: %w", err)
			}
		} else {
			job, err := repo.enqueueSchedule(ctx, tx, schedule, due)
			if err != nil {
				if errors.Is(err, errInvalidScheduleConfiguration) {
					if quarantineErr := quarantineDueSchedule(
						ctx, tx, schedule.ID, due, now.UTC(), err,
					); quarantineErr != nil {
						return nil, quarantineErr
					}
					continue
				}
				return nil, err
			}
			jobs = append(jobs, job)
		}

		enabled := schedule.Enabled && !next.IsZero()
		var nextValue any
		if enabled {
			nextValue = next.Unix()
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE schedules SET enabled = ?, next_run_at = ?, last_run_at = ?, updated_at = ? WHERE id = ? AND next_run_at = ?",
			enabled, nextValue, now.UTC().Unix(), now.UTC().Unix(), schedule.ID, due.Unix(),
		); err != nil {
			return nil, fmt.Errorf("advance schedule: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit due schedules: %w", err)
	}
	return jobs, nil
}

// CompleteScheduleRuns copies terminal job outcomes onto their schedule_runs
// rows exactly once and, inside the same transaction, records the next
// automatic retry attempt for failed runs still inside the retry budget.
func (repo *repo) CompleteScheduleRuns(ctx context.Context, now time.Time) ([]web.ScheduleRunCompletion, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("complete schedule runs: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx,
		"SELECT schedule_runs.id, schedule_runs.schedule_id, schedules.name, schedule_runs.job_id, "+
			"schedule_runs.attempt, job_runtime.state, job_runtime.started_at, job_runtime.finished_at, "+
			"job_runtime.message, schedules.retry_count, schedules.retry_backoff_seconds, schedules.auto_export_format "+
			"FROM schedule_runs "+
			"JOIN schedules ON schedules.id = schedule_runs.schedule_id "+
			"JOIN job_runtime ON job_runtime.job_id = schedule_runs.job_id "+
			"WHERE schedule_runs.state = ? AND schedule_runs.job_id IS NOT NULL "+
			"AND job_runtime.state IN ('completed','partial','failed','cancelled') "+
			"ORDER BY schedule_runs.id",
		scheduleRunStateQueued,
	)
	if err != nil {
		return nil, fmt.Errorf("find finished schedule runs: %w", err)
	}
	type finishedRun struct {
		completion             web.ScheduleRunCompletion
		startedAt, finishedAt  sql.NullInt64
		message                string
		retryCount, retryDelay int
	}
	finished := make([]finishedRun, 0)
	for rows.Next() {
		var run finishedRun
		if scanErr := rows.Scan(
			&run.completion.RunID, &run.completion.ScheduleID, &run.completion.ScheduleName,
			&run.completion.JobID, &run.completion.Attempt, &run.completion.State,
			&run.startedAt, &run.finishedAt, &run.message,
			&run.retryCount, &run.retryDelay, &run.completion.AutoExportFormat,
		); scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		finished = append(finished, run)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	completions := make([]web.ScheduleRunCompletion, 0, len(finished))
	for _, run := range finished {
		message := ""
		if run.completion.State == string(jobruntime.StateFailed) ||
			run.completion.State == string(jobruntime.StateCancelled) {
			message = jobruntime.RedactString(run.message)
		}
		finishedAt := now.UTC().Unix()
		if run.finishedAt.Valid {
			finishedAt = run.finishedAt.Int64
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE schedule_runs SET state = ?, started_at = ?, finished_at = ?, error = ? WHERE id = ? AND state = ?",
			run.completion.State, run.startedAt, finishedAt, message, run.completion.RunID, scheduleRunStateQueued,
		); err != nil {
			return nil, fmt.Errorf("finish schedule run %d: %w", run.completion.RunID, err)
		}
		if run.completion.State == string(jobruntime.StateFailed) &&
			web.ScheduleRetryAllowed(run.retryCount, run.completion.Attempt) {
			retryAt := now.UTC().Add(web.ScheduleRetryDelay(run.retryDelay, run.completion.Attempt))
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO schedule_runs(schedule_id, state, scheduled_for, attempt) VALUES (?, ?, ?, ?)",
				run.completion.ScheduleID, scheduleRunStateRetryPending, retryAt.Unix(), run.completion.Attempt+1,
			); err != nil {
				return nil, fmt.Errorf("queue schedule retry: %w", err)
			}
			run.completion.RetryQueued = true
			run.completion.NextRetryAt = &retryAt
		}
		completions = append(completions, run.completion)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit finished schedule runs: %w", err)
	}
	return completions, nil
}

// StartDueScheduleRetries turns due pending retry rows into queued jobs. The
// job attaches to the existing run row so attempt numbering stays durable.
func (repo *repo) StartDueScheduleRetries(ctx context.Context, now time.Time, limit int) ([]web.Job, error) {
	if limit < 1 || limit > 100 {
		limit = 10
	}
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("start schedule retries: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx,
		"SELECT id, schedule_id, scheduled_for, attempt FROM schedule_runs "+
			"WHERE state = ? AND scheduled_for <= ? ORDER BY scheduled_for, id LIMIT ?",
		scheduleRunStateRetryPending, now.UTC().Unix(), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("find due schedule retries: %w", err)
	}
	type dueRetry struct {
		id         int64
		scheduleID string
		due        int64
		attempt    int
	}
	retries := make([]dueRetry, 0)
	for rows.Next() {
		var retry dueRetry
		if scanErr := rows.Scan(&retry.id, &retry.scheduleID, &retry.due, &retry.attempt); scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		retries = append(retries, retry)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	jobs := make([]web.Job, 0, len(retries))
	for _, retry := range retries {
		schedule, err := repo.getSchedule(ctx, tx, retry.scheduleID)
		if err != nil {
			if errors.Is(err, web.ErrScheduleNotFound) {
				continue
			}
			return nil, err
		}
		job, err := repo.createScheduleJob(ctx, tx, schedule, time.Unix(retry.due, 0).UTC(), retry.attempt)
		if err != nil {
			if errors.Is(err, errInvalidScheduleConfiguration) {
				if _, updateErr := tx.ExecContext(ctx,
					"UPDATE schedule_runs SET state = 'failed', finished_at = ?, error = ? WHERE id = ?",
					now.UTC().Unix(), jobruntime.RedactString(err.Error()), retry.id,
				); updateErr != nil {
					return nil, fmt.Errorf("record invalid schedule retry: %w", updateErr)
				}
				continue
			}
			return nil, err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE schedule_runs SET job_id = ?, state = ?, error = '' WHERE id = ?",
			job.ID, scheduleRunStateQueued, retry.id,
		); err != nil {
			return nil, fmt.Errorf("attach schedule retry job: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE schedules SET last_run_at = ?, updated_at = ? WHERE id = ?",
			now.UTC().Unix(), now.UTC().Unix(), retry.scheduleID,
		); err != nil {
			return nil, fmt.Errorf("record schedule retry start: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit schedule retries: %w", err)
	}
	return jobs, nil
}

// expiredScheduleRunSelect is the one definition of "past its schedule's
// retention window". Queued and pending-retry rows are never expired, and a
// schedule with retention 0 keeps everything.
const expiredScheduleRunSelect = "SELECT schedule_runs.id AS run_id, schedule_runs.job_id AS job_id " +
	"FROM schedule_runs " +
	"JOIN schedules ON schedules.id = schedule_runs.schedule_id " +
	"WHERE schedules.runs_retention_days > 0 " +
	"AND schedule_runs.state NOT IN (?, ?) " +
	"AND COALESCE(schedule_runs.finished_at, schedule_runs.scheduled_for) < " +
	"? - (schedules.runs_retention_days * 86400)"

// PruneScheduleRuns deletes finished run-history rows older than each
// schedule's own runs_retention_days, together with the operational log those
// runs produced.
//
// What is deliberately NOT removed: the job row, its runtime counters, its
// normalized results, and its per-job CSV. Those are collected data. Only the
// run-history row and the job_events log — both reproducible operational
// evidence — expire with the window. Export FILES are removed by the service,
// which owns the data directory; ExpiredScheduleRunExports reports them.
func (repo *repo) PruneScheduleRuns(ctx context.Context, now time.Time) (int64, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("start schedule run prune: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	cutoff := now.UTC().Unix()
	// Logs first: once the run rows are gone the jobs are no longer
	// identifiable as expired, so ordering here is what makes the pass
	// idempotent rather than leaving orphaned logs behind forever.
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM job_events WHERE job_id IN (SELECT job_id FROM ("+expiredScheduleRunSelect+
			") WHERE job_id IS NOT NULL AND job_id <> '')",
		scheduleRunStateQueued, scheduleRunStateRetryPending, cutoff,
	); err != nil {
		return 0, fmt.Errorf("prune expired schedule run logs: %w", err)
	}

	result, err := tx.ExecContext(ctx,
		"DELETE FROM schedule_runs WHERE id IN (SELECT run_id FROM ("+expiredScheduleRunSelect+"))",
		scheduleRunStateQueued, scheduleRunStateRetryPending, cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("prune schedule runs: %w", err)
	}
	pruned, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count pruned schedule runs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit schedule run prune: %w", err)
	}

	return pruned, nil
}

// ExpiredScheduleRunExports lists completed exports produced from jobs whose
// schedule run has passed its retention window. The service deletes the files
// through its ordinary export-deletion path, which is the only code that may
// touch the data directory.
func (repo *repo) ExpiredScheduleRunExports(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := repo.db.QueryContext(ctx,
		"SELECT exports.id FROM exports "+
			"WHERE exports.source_type = 'job' AND exports.state = 'completed' "+
			"AND exports.source_id IN ("+
			"SELECT job_id FROM ("+expiredScheduleRunSelect+") WHERE job_id IS NOT NULL AND job_id <> '') "+
			"ORDER BY exports.id",
		scheduleRunStateQueued, scheduleRunStateRetryPending, now.UTC().Unix(),
	)
	if err != nil {
		return nil, fmt.Errorf("read expired schedule run exports: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan expired schedule run export: %w", err)
		}
		ids = append(ids, id)
	}

	return ids, rows.Err()
}

// ListDueReplaceableScheduleJobs returns still-active jobs belonging to due
// schedules whose overlap policy is replace, so the service can cancel them
// through the lifecycle control path before the new run is queued.
func (repo *repo) ListDueReplaceableScheduleJobs(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := repo.db.QueryContext(ctx,
		"SELECT DISTINCT schedule_runs.job_id FROM schedules "+
			"JOIN schedule_runs ON schedule_runs.schedule_id = schedules.id "+
			"JOIN job_runtime ON job_runtime.job_id = schedule_runs.job_id "+
			"WHERE schedules.enabled = 1 AND schedules.overlap_policy = ? "+
			"AND schedules.next_run_at IS NOT NULL AND schedules.next_run_at <= ? "+
			"AND job_runtime.state IN ('queued','starting','running','paused','cancelling') "+
			"ORDER BY schedule_runs.job_id",
		web.ScheduleOverlapReplace, now.UTC().Unix(),
	)
	if err != nil {
		return nil, fmt.Errorf("find replaceable scheduled jobs: %w", err)
	}
	defer rows.Close()
	jobIDs := make([]string, 0)
	for rows.Next() {
		var jobID string
		if scanErr := rows.Scan(&jobID); scanErr != nil {
			return nil, scanErr
		}
		jobIDs = append(jobIDs, jobID)
	}
	return jobIDs, rows.Err()
}

func quarantineDueSchedule(
	ctx context.Context,
	tx *sql.Tx,
	scheduleID string,
	due time.Time,
	now time.Time,
	cause error,
) error {
	message := jobruntime.RedactString(cause.Error())
	details, err := json.Marshal(map[string]string{"error": message})
	if err != nil {
		return fmt.Errorf("encode invalid schedule audit: %w", err)
	}
	result, err := tx.ExecContext(ctx,
		"UPDATE schedules SET enabled = 0, next_run_at = NULL, last_run_at = ?, updated_at = ? "+
			"WHERE id = ? AND enabled = 1 AND next_run_at = ?",
		now.Unix(), now.Unix(), scheduleID, due.Unix(),
	)
	if err != nil {
		return fmt.Errorf("quarantine invalid schedule: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read invalid schedule quarantine result: %w", err)
	}
	if affected == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schedule_runs(schedule_id, state, scheduled_for, finished_at, error) "+
			"VALUES (?, 'failed', ?, ?, ?)",
		scheduleID, due.Unix(), now.Unix(), message,
	); err != nil {
		return fmt.Errorf("record invalid schedule run: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO audit_logs(action, entity_type, entity_id, details, created_at) "+
			"VALUES ('schedule_quarantined', 'schedule', ?, ?, ?)",
		scheduleID, string(details), now.Unix(),
	); err != nil {
		return fmt.Errorf("audit invalid schedule: %w", err)
	}

	return nil
}

func (repo *repo) enqueueSchedule(
	ctx context.Context,
	tx *sql.Tx,
	schedule web.ScheduleRecord,
	scheduledFor time.Time,
) (web.Job, error) {
	job, err := repo.createScheduleJob(ctx, tx, schedule, scheduledFor, 1)
	if err != nil {
		return web.Job{}, err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schedule_runs(schedule_id, job_id, state, scheduled_for, attempt) VALUES (?, ?, 'queued', ?, 1)",
		schedule.ID, job.ID, scheduledFor.Unix(),
	); err != nil {
		return web.Job{}, fmt.Errorf("record scheduled job: %w", err)
	}
	return job, nil
}

// createScheduleJob creates the durable job rows for one scheduled attempt
// without recording a schedule_runs row, so retries can attach the job to
// their existing pending run.
func (repo *repo) createScheduleJob(
	ctx context.Context,
	tx *sql.Tx,
	schedule web.ScheduleRecord,
	scheduledFor time.Time,
	attempt int,
) (web.Job, error) {
	template, err := getTemplateConfiguration(ctx, tx, schedule.TemplateID)
	if err != nil {
		if errors.Is(err, web.ErrReusableNotFound) {
			return web.Job{}, fmt.Errorf("%w: scrape template is unavailable", errInvalidScheduleConfiguration)
		}
		return web.Job{}, err
	}
	now := time.Now().UTC()
	name := schedule.Name + " - " + scheduledFor.Format("2006-01-02 15:04")
	if attempt > 1 {
		name += fmt.Sprintf(" (retry %d)", attempt-1)
	}
	// An incremental-only schedule stamps its mode onto every run it creates,
	// overriding whatever mode the template stored. An empty schedule mode
	// leaves the template untouched, which is the historical behaviour.
	if mode := strings.TrimSpace(schedule.Spec.IncrementalMode); mode != "" {
		if !web.ValidIncrementalMode(mode) {
			return web.Job{}, fmt.Errorf("%w: unsupported incremental mode %q", errInvalidScheduleConfiguration, mode)
		}
		template.IncrementalMode = mode
	}
	// A parameterised template regenerates its query lines on every run, so
	// adding a city to the template changes the next run without editing
	// query text. A template without parameters is returned untouched.
	template, err = web.ApplyJobParameters(template)
	if err != nil {
		return web.Job{}, fmt.Errorf("%w: %v", errInvalidScheduleConfiguration, err)
	}
	template.TemplateID = schedule.TemplateID
	job := web.Job{
		ID: uuid.NewString(), Name: name,
		Date: now, Status: web.StatusPending, Data: template,
	}
	if err := job.Validate(); err != nil {
		return web.Job{}, fmt.Errorf("%w: scheduled template is invalid: %v", errInvalidScheduleConfiguration, err)
	}
	item, err := jobToRow(&job)
	if err != nil {
		return web.Job{}, err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO jobs(id, name, status, data, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		item.ID, item.Name, item.Status, item.Data, item.CreatedAt, item.UpdatedAt,
	); err != nil {
		return web.Job{}, fmt.Errorf("create scheduled job: %w", err)
	}
	if err := upsertJobFoundation(ctx, tx, item); err != nil {
		return web.Job{}, err
	}
	return job, nil
}

func scheduleHasActiveJob(ctx context.Context, tx *sql.Tx, scheduleID string) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schedule_runs JOIN job_runtime ON job_runtime.job_id = schedule_runs.job_id "+
			"WHERE schedule_runs.schedule_id = ? AND job_runtime.state IN ('queued','starting','running','paused','cancelling')",
		scheduleID,
	).Scan(&count)
	return count > 0, err
}

func getTemplateConfiguration(ctx context.Context, tx *sql.Tx, id string) (web.JobData, error) {
	var configuration string
	if err := tx.QueryRowContext(ctx, "SELECT configuration FROM templates WHERE id = ?", id).Scan(&configuration); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return web.JobData{}, web.ErrReusableNotFound
		}
		return web.JobData{}, err
	}
	var data web.JobData
	if err := json.Unmarshal([]byte(configuration), &data); err != nil {
		return web.JobData{}, fmt.Errorf("decode scheduled template: %w", err)
	}
	return data, nil
}

type scheduleQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (repo *repo) getSchedule(ctx context.Context, queryer scheduleQueryer, id string) (web.ScheduleRecord, error) {
	row := queryer.QueryRowContext(ctx,
		"SELECT "+scheduleSelectColumns+" "+
			"FROM schedules LEFT JOIN templates ON templates.id = schedules.template_id WHERE schedules.id = ?",
		id,
	)
	schedule, err := scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return web.ScheduleRecord{}, web.ErrScheduleNotFound
	}
	return schedule, err
}

type scheduleScanner interface {
	Scan(...any) error
}

func scanSchedule(scanner scheduleScanner) (web.ScheduleRecord, error) {
	var schedule web.ScheduleRecord
	var enabled int
	var configuration string
	var nextRun, lastRun sql.NullInt64
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&schedule.ID, &schedule.Name, &schedule.TemplateID, &schedule.TemplateName,
		&schedule.Timezone, &enabled, &configuration, &nextRun, &lastRun,
		&schedule.RetryCount, &schedule.RetryBackoffSeconds, &schedule.AutoExportFormat,
		&schedule.RunsRetentionDays, &createdAt, &updatedAt,
	); err != nil {
		return web.ScheduleRecord{}, err
	}
	if err := json.Unmarshal([]byte(configuration), &schedule.Spec); err != nil {
		return web.ScheduleRecord{}, fmt.Errorf("decode schedule %s: %w", schedule.ID, err)
	}
	schedule.Enabled = enabled != 0
	schedule.CreatedAt = time.Unix(createdAt, 0).UTC()
	schedule.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	if nextRun.Valid {
		value := time.Unix(nextRun.Int64, 0).UTC()
		schedule.NextRunAt = &value
	}
	if lastRun.Valid {
		value := time.Unix(lastRun.Int64, 0).UTC()
		schedule.LastRunAt = &value
	}
	return schedule, nil
}

func scanScheduleRun(scanner scheduleScanner) (web.ScheduleRunRecord, error) {
	var run web.ScheduleRunRecord
	var scheduledFor int64
	var startedAt, finishedAt sql.NullInt64
	if err := scanner.Scan(
		&run.ID, &run.ScheduleID, &run.ScheduleName, &run.JobID, &run.State,
		&scheduledFor, &startedAt, &finishedAt, &run.Attempt, &run.Error,
	); err != nil {
		return web.ScheduleRunRecord{}, err
	}
	run.ScheduledFor = time.Unix(scheduledFor, 0).UTC()
	if startedAt.Valid {
		value := time.Unix(startedAt.Int64, 0).UTC()
		run.StartedAt = &value
	}
	if finishedAt.Valid {
		value := time.Unix(finishedAt.Int64, 0).UTC()
		run.FinishedAt = &value
	}
	return run, nil
}

func requireScheduleResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return web.ErrScheduleNotFound
	}
	return nil
}
