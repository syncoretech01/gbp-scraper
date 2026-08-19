package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

var errInvalidScheduleConfiguration = errors.New("invalid schedule configuration")

func (repo *repo) ListSchedules(ctx context.Context) ([]web.ScheduleRecord, error) {
	rows, err := repo.db.QueryContext(ctx,
		"SELECT schedules.id, schedules.name, COALESCE(schedules.template_id, ''), COALESCE(templates.name, ''), "+
			"schedules.timezone, schedules.enabled, schedules.configuration, schedules.next_run_at, "+
			"schedules.last_run_at, schedules.created_at, schedules.updated_at "+
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
		"SELECT schedule_runs.id, schedule_runs.schedule_id, schedules.name, "+
			"COALESCE(schedule_runs.job_id, ''), COALESCE(job_runtime.state, schedule_runs.state), schedule_runs.scheduled_for, "+
			"COALESCE(job_runtime.started_at, schedule_runs.started_at), "+
			"COALESCE(job_runtime.finished_at, schedule_runs.finished_at), schedule_runs.error "+
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
			"missed_run_policy, configuration, next_run_at, created_at, updated_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT(id) DO UPDATE SET name = excluded.name, template_id = excluded.template_id, "+
			"cron_expression = excluded.cron_expression, timezone = excluded.timezone, enabled = excluded.enabled, "+
			"overlap_policy = excluded.overlap_policy, missed_run_policy = excluded.missed_run_policy, "+
			"configuration = excluded.configuration, next_run_at = excluded.next_run_at, updated_at = excluded.updated_at",
		schedule.ID, schedule.Name, schedule.TemplateID, cronExpression, schedule.Timezone, schedule.Enabled,
		schedule.Spec.OverlapPolicy, schedule.Spec.MissedPolicy, string(configuration), nextRunAt,
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
	template, err := getTemplateConfiguration(ctx, tx, schedule.TemplateID)
	if err != nil {
		if errors.Is(err, web.ErrReusableNotFound) {
			return web.Job{}, fmt.Errorf("%w: scrape template is unavailable", errInvalidScheduleConfiguration)
		}
		return web.Job{}, err
	}
	now := time.Now().UTC()
	job := web.Job{
		ID: uuid.NewString(), Name: schedule.Name + " - " + scheduledFor.Format("2006-01-02 15:04"),
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
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schedule_runs(schedule_id, job_id, state, scheduled_for) VALUES (?, ?, 'queued', ?)",
		schedule.ID, job.ID, scheduledFor.Unix(),
	); err != nil {
		return web.Job{}, fmt.Errorf("record scheduled job: %w", err)
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
		"SELECT schedules.id, schedules.name, COALESCE(schedules.template_id, ''), COALESCE(templates.name, ''), "+
			"schedules.timezone, schedules.enabled, schedules.configuration, schedules.next_run_at, "+
			"schedules.last_run_at, schedules.created_at, schedules.updated_at "+
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
		&schedule.Timezone, &enabled, &configuration, &nextRun, &lastRun, &createdAt, &updatedAt,
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
		&scheduledFor, &startedAt, &finishedAt, &run.Error,
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
