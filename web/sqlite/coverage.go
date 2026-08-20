package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

var _ interface {
	JobCoverageTasks(context.Context, string) ([]web.CoverageTaskRow, error)
	JobCoverageSeedState(context.Context, string) (web.CoverageSeedState, error)
	SkipPendingJobTasks(context.Context, string, string) (int, error)
	AppendJobTasks(context.Context, string, []web.JobTaskDefinition, int) ([]web.JobTask, error)
	DeferJobTask(context.Context, string, string, time.Time) error
} = (*repo)(nil)

// JobCoverageTasks reads every task of the job's durable plan with the
// checkpoint counters the coverage report needs, in plan order.
func (repo *repo) JobCoverageTasks(ctx context.Context, jobID string) ([]web.CoverageTaskRow, error) {
	if err := requireJob(ctx, repo.db, jobID); err != nil {
		return nil, err
	}

	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT task_key, query, origin, state, last_error, attempts, priority, sequence,
			COALESCE(json_extract(checkpoint, '$.rows_added'), 0),
			COALESCE(json_extract(checkpoint, '$.rows_replaced'), 0),
			COALESCE(json_extract(checkpoint, '$.duplicates_skipped'), 0),
			COALESCE(json_extract(checkpoint, '$.truncated'), 0),
			started_at, finished_at
		FROM job_tasks WHERE job_id = ?
		ORDER BY sequence, created_at, task_key`,
		jobID,
	)
	if err != nil {
		return nil, fmt.Errorf("read coverage tasks: %w", err)
	}

	defer func() { _ = rows.Close() }()

	tasks := make([]web.CoverageTaskRow, 0)

	for rows.Next() {
		var task web.CoverageTaskRow

		var startedAt, finishedAt sql.NullInt64

		// json_extract renders a JSON boolean as 1/0, and a checkpoint
		// written before the signal existed simply has no such key.
		var truncated int64

		if err := rows.Scan(
			&task.TaskKey,
			&task.Query,
			&task.Origin,
			&task.State,
			&task.LastError,
			&task.Attempts,
			&task.Priority,
			&task.Sequence,
			&task.RowsAdded,
			&task.RowsReplaced,
			&task.DuplicatesSkipped,
			&truncated,
			&startedAt,
			&finishedAt,
		); err != nil {
			return nil, fmt.Errorf("scan coverage task: %w", err)
		}

		task.Truncated = truncated != 0
		task.LastError = jobruntime.RedactString(task.LastError)
		task.StartedAt = nullableUnixTime(startedAt)
		task.FinishedAt = nullableUnixTime(finishedAt)

		tasks = append(tasks, task)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate coverage tasks: %w", err)
	}

	return tasks, nil
}

// JobCoverageSeedState summarises the durable plan for the mid-run coverage
// engine: every task query, the highest sequence, and how many expansion
// tasks already exist so the budget survives restarts.
func (repo *repo) JobCoverageSeedState(ctx context.Context, jobID string) (web.CoverageSeedState, error) {
	if err := requireJob(ctx, repo.db, jobID); err != nil {
		return web.CoverageSeedState{}, err
	}

	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT query, sequence, origin FROM job_tasks WHERE job_id = ?`,
		jobID,
	)
	if err != nil {
		return web.CoverageSeedState{}, fmt.Errorf("read coverage seed state: %w", err)
	}

	defer func() { _ = rows.Close() }()

	state := web.CoverageSeedState{MaxSequence: -1}

	for rows.Next() {
		var (
			query, origin string
			sequence      int
		)

		if err := rows.Scan(&query, &sequence, &origin); err != nil {
			return web.CoverageSeedState{}, fmt.Errorf("scan coverage seed state: %w", err)
		}

		state.Queries = append(state.Queries, query)

		if sequence > state.MaxSequence {
			state.MaxSequence = sequence
		}

		// Both kinds of engine-appended task draw on the same budget, so
		// both must be counted or a restart would hand out a fresh one.
		if strings.HasPrefix(origin, web.CoverageExpansionOriginPrefix) ||
			strings.HasPrefix(origin, web.CoverageRefinementOriginPrefix) {
			state.ExpansionTasks++
		}
	}

	if err := rows.Err(); err != nil {
		return web.CoverageSeedState{}, fmt.Errorf("iterate coverage seed state: %w", err)
	}

	return state, nil
}

// SkipPendingJobTasks terminally skips every still-pending task of the job.
// Skipped tasks carry the reason in last_error, are excluded from the
// unfinished/reclaim queries, and are preserved by a later plan upsert.
func (repo *repo) SkipPendingJobTasks(ctx context.Context, jobID, reason string) (int, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin task skip transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := requireJob(ctx, tx, jobID); err != nil {
		return 0, err
	}

	now := time.Now().UTC().Unix()

	result, err := tx.ExecContext(
		ctx,
		`UPDATE job_tasks SET state = 'skipped', last_error = ?,
			lease_owner = '', lease_expires_at = NULL, not_before = NULL,
			finished_at = ?, updated_at = ?
		WHERE job_id = ? AND state = 'pending'`,
		jobruntime.RedactString(reason),
		now,
		now,
		jobID,
	)
	if err != nil {
		return 0, fmt.Errorf("skip pending tasks: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("skip pending tasks: %w", err)
	}

	if affected > 0 {
		if err := updateTaskAggregates(ctx, tx, jobID, now); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit task skip: %w", err)
	}

	return int(affected), nil
}

// AppendJobTasks adds new pending tasks to an existing plan. An already
// present task key is left untouched, so repeating an expansion decision is
// idempotent. It returns only the tasks this call actually inserted.
func (repo *repo) AppendJobTasks(
	ctx context.Context,
	jobID string,
	definitions []web.JobTaskDefinition,
	maxAttempts int,
) ([]web.JobTask, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin task append transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := requireJob(ctx, tx, jobID); err != nil {
		return nil, err
	}

	now := time.Now().UTC().Unix()
	insertedKeys := make([]string, 0, len(definitions))

	for _, definition := range definitions {
		payload := definition.Payload
		if len(payload) == 0 {
			payload = json.RawMessage("{}")
		}

		result, err := tx.ExecContext(
			ctx,
			`INSERT INTO job_tasks(
				id, job_id, kind, state, sequence, query, source_cell, payload,
				attempts, max_attempts, created_at, updated_at, task_key, input_id,
				checkpoint, origin, priority
			) VALUES (?, ?, ?, 'pending', ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, '{}', ?, ?)
			ON CONFLICT(job_id, task_key) DO NOTHING`,
			durableTaskID(jobID, definition.Key),
			jobID,
			definition.Kind,
			definition.Sequence,
			jobruntime.RedactString(definition.Query),
			jobruntime.RedactString(definition.SourceCell),
			string(payload),
			maxAttempts,
			now,
			now,
			definition.Key,
			definition.InputID,
			definition.Origin,
			definition.Priority,
		)
		if err != nil {
			return nil, fmt.Errorf("append task %q: %w", definition.Key, err)
		}

		if affected, affectedErr := result.RowsAffected(); affectedErr != nil {
			return nil, fmt.Errorf("append task %q: %w", definition.Key, affectedErr)
		} else if affected == 1 {
			insertedKeys = append(insertedKeys, definition.Key)
		}
	}

	if len(insertedKeys) > 0 {
		if err := updateTaskAggregates(ctx, tx, jobID, now); err != nil {
			return nil, err
		}
	}

	inserted := make([]web.JobTask, 0, len(insertedKeys))

	for _, key := range insertedKeys {
		task, err := readJobTask(ctx, tx, jobID, key)
		if err != nil {
			return nil, err
		}

		inserted = append(inserted, task)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit task append: %w", err)
	}

	return inserted, nil
}

// DeferJobTask sets the earliest claim time of a pending task. A task that
// already finished, or is running, is left untouched.
func (repo *repo) DeferJobTask(ctx context.Context, jobID, taskKey string, until time.Time) error {
	_, err := repo.db.ExecContext(
		ctx,
		`UPDATE job_tasks SET not_before = ?, updated_at = ?
		WHERE job_id = ? AND task_key = ? AND state = 'pending'`,
		until.UTC().Unix(),
		time.Now().UTC().Unix(),
		jobID,
		taskKey,
	)
	if err != nil {
		return fmt.Errorf("defer task %q: %w", taskKey, err)
	}

	return nil
}
