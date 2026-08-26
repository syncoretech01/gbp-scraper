package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// Several workers may share one job's durable plan. A worker owns a task only
// while it holds an unexpired lease on it, which gives three guarantees:
//
//   - Two workers never run the same task: the claim is a single conditional
//     UPDATE, so exactly one caller wins.
//   - A worker that dies cannot strand work: its lease expires and the task
//     returns to the queue.
//   - A worker whose lease was reclaimed cannot later overwrite the task state,
//     because finishing verifies ownership.

// ClaimNextJobTask atomically leases the next runnable task for one worker.
// It returns found=false when the job has no runnable task left, which is the
// signal for a worker to stop.
func (repo *repo) ClaimNextJobTask(
	ctx context.Context,
	jobID, owner string,
	lease time.Duration,
) (web.JobTask, bool, error) {
	if owner == "" {
		return web.JobTask{}, false, errors.New("a task lease requires an owner")
	}

	leaseSeconds := int64(lease.Seconds())
	if leaseSeconds < 1 {
		leaseSeconds = 1
	}

	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return web.JobTask{}, false, fmt.Errorf("begin task lease transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Unix()

	// Reclaim first so an expired lease becomes claimable in the same
	// transaction that is about to look for work.
	if _, err := reclaimExpiredTasks(ctx, tx, jobID, now); err != nil {
		return web.JobTask{}, false, err
	}

	var taskKey string

	// Eligibility honours failure-class backoff (not_before) and the claim
	// order prefers higher priorities; the default zero priority preserves
	// the historical FIFO order exactly.
	err = tx.QueryRowContext(
		ctx,
		`SELECT task_key FROM job_tasks
		WHERE job_id = ? AND state IN ('pending', 'failed') AND attempts < max_attempts
			AND (not_before IS NULL OR not_before <= ?)
		ORDER BY priority DESC, sequence, created_at, task_key
		LIMIT 1`,
		jobID,
		now,
	).Scan(&taskKey)

	if errors.Is(err, sql.ErrNoRows) {
		return web.JobTask{}, false, tx.Commit()
	}

	if err != nil {
		return web.JobTask{}, false, fmt.Errorf("select next task: %w", err)
	}

	task, err := readJobTask(ctx, tx, jobID, taskKey)
	if err != nil {
		return web.JobTask{}, false, err
	}

	result, err := tx.ExecContext(
		ctx,
		`UPDATE job_tasks SET
			state = 'running', attempts = attempts + 1, last_error = '',
			lease_owner = ?, lease_expires_at = ?, heartbeat_at = ?, not_before = NULL,
			started_at = ?, finished_at = NULL, updated_at = ?
		WHERE job_id = ? AND task_key = ? AND state IN ('pending', 'failed')
			AND attempts < max_attempts`,
		owner,
		now+leaseSeconds,
		now,
		now,
		now,
		jobID,
		taskKey,
	)
	if err != nil {
		return web.JobTask{}, false, fmt.Errorf("lease task %q: %w", taskKey, err)
	}

	// Another worker won the race between the select and the update. Report no
	// work rather than an error; the caller simply asks again.
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil {
		return web.JobTask{}, false, fmt.Errorf("lease task %q: %w", taskKey, affectedErr)
	} else if affected != 1 {
		return web.JobTask{}, false, tx.Commit()
	}

	if err := updateTaskAggregates(ctx, tx, jobID, now); err != nil {
		return web.JobTask{}, false, err
	}

	if err := insertJobEvent(ctx, tx, jobEventInput{
		jobID: jobID, typeName: "task-started", severity: "information",
		stage:   jobruntime.StageSearchingMaps,
		message: fmt.Sprintf("Started task %d: %s", task.Sequence+1, task.Query),
		context: map[string]any{
			"task_key": taskKey, "query": task.Query, "cell": task.SourceCell,
			"attempt": task.Attempts + 1, "max_attempts": task.MaxAttempts,
			"lease_owner": owner,
		},
		createdAt: now,
	}); err != nil {
		return web.JobTask{}, false, err
	}

	claimed, err := readJobTask(ctx, tx, jobID, taskKey)
	if err != nil {
		return web.JobTask{}, false, err
	}

	if err := tx.Commit(); err != nil {
		return web.JobTask{}, false, fmt.Errorf("commit task lease: %w", err)
	}

	return claimed, true, nil
}

// HeartbeatJobTask extends a lease the caller still owns. It fails once the
// lease has been reclaimed, which tells a stalled worker to abandon its task
// instead of finishing work another worker has already taken over.
func (repo *repo) HeartbeatJobTask(
	ctx context.Context,
	jobID, taskKey, owner string,
	lease time.Duration,
) error {
	leaseSeconds := int64(lease.Seconds())
	if leaseSeconds < 1 {
		leaseSeconds = 1
	}

	now := time.Now().UTC().Unix()

	result, err := repo.db.ExecContext(
		ctx,
		`UPDATE job_tasks SET heartbeat_at = ?, lease_expires_at = ?, updated_at = ?
		WHERE job_id = ? AND task_key = ? AND state = 'running' AND lease_owner = ?`,
		now,
		now+leaseSeconds,
		now,
		jobID,
		taskKey,
		owner,
	)
	if err != nil {
		return fmt.Errorf("heartbeat task %q: %w", taskKey, err)
	}

	if affected, affectedErr := result.RowsAffected(); affectedErr != nil {
		return fmt.Errorf("heartbeat task %q: %w", taskKey, affectedErr)
	} else if affected != 1 {
		return fmt.Errorf("%w: %s", web.ErrCheckpointLeaseLost, taskKey)
	}

	return nil
}

// ReleaseJobTask returns a leased task to the queue without consuming another
// attempt. Cancellation and shutdown use it so an interrupted task resumes
// exactly rather than counting as a failure.
func (repo *repo) ReleaseJobTask(ctx context.Context, jobID, taskKey, owner, reason string) error {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin task release transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Unix()

	result, err := tx.ExecContext(
		ctx,
		`UPDATE job_tasks SET
			state = 'pending', attempts = CASE WHEN attempts > 0 THEN attempts - 1 ELSE 0 END,
			last_error = ?, lease_owner = '', lease_expires_at = NULL,
			started_at = NULL, finished_at = NULL, updated_at = ?
		WHERE job_id = ? AND task_key = ? AND state = 'running' AND lease_owner = ?`,
		jobruntime.RedactString(reason),
		now,
		jobID,
		taskKey,
		owner,
	)
	if err != nil {
		return fmt.Errorf("release task %q: %w", taskKey, err)
	}

	if affected, affectedErr := result.RowsAffected(); affectedErr != nil {
		return fmt.Errorf("release task %q: %w", taskKey, affectedErr)
	} else if affected != 1 {
		// The lease was already reclaimed or the task finished. Both are safe.
		return tx.Commit()
	}

	if err := updateTaskAggregates(ctx, tx, jobID, now); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit task release: %w", err)
	}

	return nil
}

// ReclaimExpiredJobTasks returns tasks whose lease has lapsed to the queue and
// reports how many were recovered.
func (repo *repo) ReclaimExpiredJobTasks(ctx context.Context, jobID string) (int, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin task reclaim transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Unix()

	recovered, err := reclaimExpiredTasks(ctx, tx, jobID, now)
	if err != nil {
		return 0, err
	}

	if recovered == 0 {
		return 0, tx.Commit()
	}

	if err := updateTaskAggregates(ctx, tx, jobID, now); err != nil {
		return 0, err
	}

	if err := insertJobEvent(ctx, tx, jobEventInput{
		jobID: jobID, typeName: "task-lease-expired", severity: "warning",
		stage:     jobruntime.StageSearchingMaps,
		message:   fmt.Sprintf("Reclaimed %d task lease(s) from a worker that stopped reporting", recovered),
		context:   map[string]any{"reclaimed": recovered},
		createdAt: now,
	}); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit task reclaim: %w", err)
	}

	return recovered, nil
}

// ReclaimStaleJobTasks returns every running task whose lease has lapsed to
// the pending queue, across all jobs regardless of their lifecycle state, and
// reports how many were recovered. It closes the gap left by the implicit
// reclaim inside ClaimNextJobTask (which only runs while a job's plan is being
// worked) and by RecoverAbandonedJobs (which only touches jobs left in
// starting, running, or cancelling): a stale lease on a paused, partial, or
// cancelled job is otherwise never reclaimed. It is intended to be called once
// at process startup, next to RecoverAbandonedJobs.
func (repo *repo) ReclaimStaleJobTasks(ctx context.Context) (int, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin stale task reclaim transaction: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Unix()

	rows, err := tx.QueryContext(
		ctx,
		`SELECT job_id, COUNT(*) FROM job_tasks
		WHERE state = 'running' AND lease_expires_at IS NOT NULL AND lease_expires_at < ?
		GROUP BY job_id ORDER BY job_id`,
		now,
	)
	if err != nil {
		return 0, fmt.Errorf("find stale task leases: %w", err)
	}

	type staleJob struct {
		id    string
		count int
	}

	stale := make([]staleJob, 0)

	for rows.Next() {
		var job staleJob
		if err := rows.Scan(&job.id, &job.count); err != nil {
			_ = rows.Close()

			return 0, fmt.Errorf("scan stale task lease: %w", err)
		}

		stale = append(stale, job)
	}

	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close stale task leases: %w", err)
	}

	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate stale task leases: %w", err)
	}

	if len(stale) == 0 {
		return 0, tx.Commit()
	}

	result, err := tx.ExecContext(
		ctx,
		`UPDATE job_tasks SET
			state = 'pending', attempts = CASE WHEN attempts > 0 THEN attempts - 1 ELSE 0 END,
			last_error = 'Worker lease expired before the task reported progress',
			lease_owner = '', lease_expires_at = NULL, started_at = NULL, updated_at = ?
		WHERE state = 'running' AND lease_expires_at IS NOT NULL AND lease_expires_at < ?`,
		now,
		now,
	)
	if err != nil {
		return 0, fmt.Errorf("reclaim stale task leases: %w", err)
	}

	reclaimed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("reclaim stale task leases: %w", err)
	}

	for _, job := range stale {
		if err := updateTaskAggregates(ctx, tx, job.id, now); err != nil {
			return 0, err
		}

		if err := insertJobEvent(ctx, tx, jobEventInput{
			jobID: job.id, typeName: "task-lease-expired", severity: "warning",
			stage:     jobruntime.StageSearchingMaps,
			message:   fmt.Sprintf("Reclaimed %d stale task lease(s) left by a stopped worker", job.count),
			context:   map[string]any{"reclaimed": job.count, "startup_sweep": true},
			createdAt: now,
		}); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit stale task reclaim: %w", err)
	}

	return int(reclaimed), nil
}

// reclaimExpiredTasks is the shared statement. The attempt consumed by the lost
// lease is returned so a reclaimed task is not penalised for a worker crash.
func reclaimExpiredTasks(ctx context.Context, tx *sql.Tx, jobID string, now int64) (int, error) {
	result, err := tx.ExecContext(
		ctx,
		`UPDATE job_tasks SET
			state = 'pending', attempts = CASE WHEN attempts > 0 THEN attempts - 1 ELSE 0 END,
			last_error = 'Worker lease expired before the task reported progress',
			lease_owner = '', lease_expires_at = NULL, started_at = NULL, updated_at = ?
		WHERE job_id = ? AND state = 'running'
			AND lease_expires_at IS NOT NULL AND lease_expires_at < ?`,
		now,
		jobID,
		now,
	)
	if err != nil {
		return 0, fmt.Errorf("reclaim expired task leases: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("reclaim expired task leases: %w", err)
	}

	return int(affected), nil
}
