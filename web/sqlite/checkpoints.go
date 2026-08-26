package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

const (
	taskStatePending   = "pending"
	taskStateRunning   = "running"
	taskStateCompleted = "completed"
	taskStateFailed    = "failed"
	taskStateSkipped   = "skipped"
)

var _ interface {
	PrepareJobTasks(context.Context, string, []web.JobTaskDefinition, int) ([]web.JobTask, error)
	StartJobTask(context.Context, string, string) (web.JobTask, error)
	ClaimNextJobTask(context.Context, string, string, time.Duration) (web.JobTask, bool, error)
	HeartbeatJobTask(context.Context, string, string, string, time.Duration) error
	ReleaseJobTask(context.Context, string, string, string, string) error
	ReclaimExpiredJobTasks(context.Context, string) (int, error)
	ReclaimStaleJobTasks(context.Context) (int, error)
	CompleteJobTask(context.Context, string, string, web.JobTaskCheckpoint) error
	CompleteJobTaskAs(context.Context, string, string, string, web.JobTaskCheckpoint) error
	FailJobTask(context.Context, string, string, error, bool, web.JobTaskCheckpoint) error
	FailJobTaskAs(context.Context, string, string, string, error, bool, web.JobTaskCheckpoint) error
	UpdateJobWorkerProgress(context.Context, string, web.JobWorkerProgress) error
	RecordJobWorkerEvent(context.Context, string, string, string, string, map[string]any) error
	GetJobExecution(context.Context, string) (web.JobExecutionSnapshot, error)
	RecoverAbandonedJobs(context.Context) (int, error)
} = (*repo)(nil)

// PrepareJobTasks upserts a deterministic plan while preserving completed
// tasks. A manually restarted failed job gets fresh outer attempts, but its
// previous attempt count remains available through checkpoint/event history.
func (repo *repo) PrepareJobTasks(
	ctx context.Context,
	jobID string,
	definitions []web.JobTaskDefinition,
	maxAttempts int,
) ([]web.JobTask, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin task plan transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := requireJob(ctx, tx, jobID); err != nil {
		return nil, err
	}

	now := time.Now().UTC().Unix()
	for _, definition := range definitions {
		payload := definition.Payload
		if len(payload) == 0 {
			payload = json.RawMessage("{}")
		}
		taskID := durableTaskID(jobID, definition.Key)
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO job_tasks(
				id, job_id, kind, state, sequence, query, source_cell, payload,
				attempts, max_attempts, created_at, updated_at, task_key, input_id, checkpoint
			) VALUES (?, ?, ?, 'pending', ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, '{}')
			ON CONFLICT(job_id, task_key) DO UPDATE SET
				kind = excluded.kind,
				sequence = excluded.sequence,
				query = excluded.query,
				source_cell = excluded.source_cell,
				payload = excluded.payload,
				input_id = excluded.input_id,
				max_attempts = excluded.max_attempts,
				state = CASE
					WHEN job_tasks.state IN ('completed', 'skipped') THEN job_tasks.state
					ELSE 'pending'
				END,
				attempts = CASE
					WHEN job_tasks.state IN ('completed', 'skipped') THEN job_tasks.attempts
					WHEN job_tasks.state = 'failed' OR job_tasks.attempts >= excluded.max_attempts THEN 0
					ELSE job_tasks.attempts
				END,
				last_error = CASE WHEN job_tasks.state IN ('completed', 'skipped') THEN job_tasks.last_error ELSE '' END,
				started_at = CASE WHEN job_tasks.state IN ('completed', 'skipped') THEN job_tasks.started_at ELSE NULL END,
				finished_at = CASE WHEN job_tasks.state IN ('completed', 'skipped') THEN job_tasks.finished_at ELSE NULL END,
				not_before = CASE WHEN job_tasks.state IN ('completed', 'skipped') THEN job_tasks.not_before ELSE NULL END,
				updated_at = excluded.updated_at`,
			taskID,
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
		); err != nil {
			return nil, fmt.Errorf("persist task %q: %w", definition.Key, err)
		}
	}

	if err := updateTaskAggregates(ctx, tx, jobID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit task plan: %w", err)
	}

	return repo.unfinishedJobTasks(ctx, jobID)
}

// StartJobTask claims a task and increments its durable outer-attempt count.
func (repo *repo) StartJobTask(ctx context.Context, jobID, taskKey string) (web.JobTask, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return web.JobTask{}, fmt.Errorf("begin task attempt transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	task, err := readJobTask(ctx, tx, jobID, taskKey)
	if err != nil {
		return web.JobTask{}, err
	}
	if task.State == taskStateCompleted || task.State == taskStateSkipped {
		return task, nil
	}
	if task.State == taskStateRunning {
		return web.JobTask{}, fmt.Errorf("task %q is already running", taskKey)
	}
	if task.Attempts >= task.MaxAttempts {
		return web.JobTask{}, fmt.Errorf("task %q exhausted %d attempts", taskKey, task.MaxAttempts)
	}

	now := time.Now().UTC().Unix()
	result, err := tx.ExecContext(
		ctx,
		`UPDATE job_tasks SET
			state = 'running', attempts = attempts + 1, last_error = '',
			lease_owner = '', lease_expires_at = NULL, heartbeat_at = ?,
			started_at = ?, finished_at = NULL, updated_at = ?
		WHERE job_id = ? AND task_key = ? AND state IN ('pending', 'failed') AND attempts < max_attempts`,
		now,
		now,
		now,
		jobID,
		taskKey,
	)
	if err != nil {
		return web.JobTask{}, fmt.Errorf("claim task %q: %w", taskKey, err)
	}
	if err := requireCASUpdate(result); err != nil {
		return web.JobTask{}, fmt.Errorf("claim task %q: %w", taskKey, err)
	}
	if err := updateTaskAggregates(ctx, tx, jobID, now); err != nil {
		return web.JobTask{}, err
	}
	if err := insertJobEvent(ctx, tx, jobEventInput{
		jobID: jobID, typeName: "task-started", severity: "information",
		stage:   jobruntime.StageSearchingMaps,
		message: fmt.Sprintf("Started task %d: %s", task.Sequence+1, task.Query),
		context: map[string]any{
			"task_key": taskKey, "query": task.Query, "cell": task.SourceCell,
			"attempt": task.Attempts + 1, "max_attempts": task.MaxAttempts,
		},
		createdAt: now,
	}); err != nil {
		return web.JobTask{}, err
	}

	claimed, err := readJobTask(ctx, tx, jobID, taskKey)
	if err != nil {
		return web.JobTask{}, err
	}
	if err := tx.Commit(); err != nil {
		return web.JobTask{}, fmt.Errorf("commit task attempt: %w", err)
	}

	return claimed, nil
}

// CompleteJobTask records that the corresponding run CSV was merged before
// advancing the safe resume boundary. The empty owner matches only lease-less
// rows, i.e. rows put into the running state by StartJobTask, which stores an
// empty lease_owner; tasks claimed by lease must finish via CompleteJobTaskAs.
func (repo *repo) CompleteJobTask(
	ctx context.Context,
	jobID, taskKey string,
	checkpoint web.JobTaskCheckpoint,
) error {
	return repo.CompleteJobTaskAs(ctx, jobID, taskKey, "", checkpoint)
}

// CompleteJobTaskAs records that the corresponding run CSV was merged before
// advancing the safe resume boundary, but only while owner still holds the
// task. Once the lease was reclaimed it persists nothing and reports
// web.ErrCheckpointLeaseLost.
func (repo *repo) CompleteJobTaskAs(
	ctx context.Context,
	jobID, taskKey, owner string,
	checkpoint web.JobTaskCheckpoint,
) error {
	return repo.finishJobTask(ctx, jobID, taskKey, owner, nil, false, checkpoint)
}

// FailJobTask returns interrupted/retryable work to pending, or records an
// exhausted task as failed. The empty owner matches only lease-less rows (see
// CompleteJobTask); tasks claimed by lease must finish via FailJobTaskAs.
func (repo *repo) FailJobTask(
	ctx context.Context,
	jobID, taskKey string,
	runErr error,
	retryable bool,
	checkpoint web.JobTaskCheckpoint,
) error {
	return repo.FailJobTaskAs(ctx, jobID, taskKey, "", runErr, retryable, checkpoint)
}

// FailJobTaskAs returns interrupted/retryable work to pending, or records an
// exhausted task as failed, but only while owner still holds the task. Once
// the lease was reclaimed it persists nothing and reports
// web.ErrCheckpointLeaseLost.
func (repo *repo) FailJobTaskAs(
	ctx context.Context,
	jobID, taskKey, owner string,
	runErr error,
	retryable bool,
	checkpoint web.JobTaskCheckpoint,
) error {
	if runErr == nil {
		runErr = errors.New("task attempt failed")
	}

	return repo.finishJobTask(ctx, jobID, taskKey, owner, runErr, retryable, checkpoint)
}

func (repo *repo) finishJobTask(
	ctx context.Context,
	jobID, taskKey, owner string,
	runErr error,
	retryable bool,
	checkpoint web.JobTaskCheckpoint,
) error {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin task checkpoint transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	task, err := readJobTask(ctx, tx, jobID, taskKey)
	if err != nil {
		return err
	}
	if runErr == nil && task.State == taskStateCompleted {
		return nil
	}
	if task.State != taskStateRunning {
		return fmt.Errorf("task %q is %s, not running", taskKey, task.State)
	}

	state := taskStateCompleted
	message := "Task completed at a safe CSV checkpoint"
	severity := "information"
	lastError := ""
	if runErr != nil {
		state = taskStateFailed
		if retryable {
			state = taskStatePending
		}
		lastError = jobruntime.RedactString(runErr.Error())
		message = "Task attempt stopped; merged rows remain safe to resume"
		severity = "warning"
	}
	checkpoint.State = state
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("encode task checkpoint: %w", err)
	}

	now := time.Now().UTC().Unix()
	var finishedAt any = now
	if state == taskStatePending {
		finishedAt = nil
	}
	result, err := tx.ExecContext(
		ctx,
		`UPDATE job_tasks SET state = ?, last_error = ?, checkpoint = ?,
			lease_owner = '', lease_expires_at = NULL,
			finished_at = ?, updated_at = ?
		WHERE job_id = ? AND task_key = ? AND state = 'running' AND lease_owner = ?`,
		state,
		lastError,
		string(payload),
		finishedAt,
		now,
		jobID,
		taskKey,
		owner,
	)
	if err != nil {
		return fmt.Errorf("finish task %q: %w", taskKey, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("finish task %q: %w", taskKey, err)
	}
	if affected != 1 {
		// The pre-read in this transaction saw the task running, so the only
		// way the guarded update can miss is an ownership mismatch: the lease
		// was reclaimed and possibly handed to another worker. The deferred
		// rollback discards the transaction, so nothing below ever persisted.
		return fmt.Errorf("%w: %s", web.ErrCheckpointLeaseLost, taskKey)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO job_checkpoints(job_id, stage, payload, created_at, task_id)
		VALUES (?, ?, ?, ?, ?)`,
		jobID,
		jobruntime.StageSearchingMaps,
		string(payload),
		now,
		task.ID,
	); err != nil {
		return fmt.Errorf("insert task checkpoint: %w", err)
	}
	if err := updateTaskAggregates(ctx, tx, jobID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE job_runtime SET last_checkpoint_at = ?, last_heartbeat_at = ?, updated_at = ? WHERE job_id = ?`,
		now,
		now,
		now,
		jobID,
	); err != nil {
		return fmt.Errorf("update task checkpoint runtime: %w", err)
	}
	if err := insertJobEvent(ctx, tx, jobEventInput{
		jobID: jobID, typeName: "task-checkpoint", severity: severity,
		stage: jobruntime.StageSearchingMaps, message: message,
		context: map[string]any{
			"task_key": taskKey, "state": state, "attempts": task.Attempts,
			"last_error": lastError, "checkpoint": checkpoint,
		},
		createdAt: now,
	}); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit task checkpoint: %w", err)
	}

	return nil
}

// UpdateJobWorkerProgress persists a lightweight replaceable sample and a
// heartbeat without appending noisy per-sample events.
func (repo *repo) UpdateJobWorkerProgress(
	ctx context.Context,
	jobID string,
	progress web.JobWorkerProgress,
) error {
	if !progress.Stage.Valid() {
		return fmt.Errorf("%w: %q", jobruntime.ErrInvalidStage, progress.Stage)
	}
	now := progress.UpdatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var eta any
	if progress.ETASeconds != nil && *progress.ETASeconds >= 0 {
		eta = *progress.ETASeconds
	}

	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin worker progress transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE job_progress SET
			stage = ?, active_tasks = ?, retries = ?, places_per_minute = ?, eta_seconds = ?,
			current_query = ?, current_cell = ?, current_domain = ?, browser_count = ?,
			active_pages = ?, cpu_percent = ?, memory_bytes = ?, disk_free_bytes = ?,
			database_writes = ?, website_queue = ?, updated_at = ?
		WHERE job_id = ?`,
		progress.Stage,
		max(int64(0), progress.ActiveTasks),
		max(int64(0), progress.Retries),
		max(float64(0), progress.PlacesPerMinute),
		eta,
		jobruntime.RedactString(progress.CurrentQuery),
		jobruntime.RedactString(progress.CurrentCell),
		jobruntime.RedactString(progress.CurrentDomain),
		max(int64(0), progress.BrowserCount),
		max(int64(0), progress.ActivePages),
		max(float64(0), progress.CPUPercent),
		progress.MemoryBytes,
		progress.DiskFreeBytes,
		max(int64(0), progress.DatabaseWrites),
		max(int64(0), progress.WebsiteQueue),
		now.Unix(),
		jobID,
	); err != nil {
		return fmt.Errorf("update worker progress: %w", err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE job_runtime SET
			stage = ?, last_heartbeat_at = ?, desired_concurrency = ?,
			effective_concurrency = ?, updated_at = ?
		WHERE job_id = ?`,
		progress.Stage,
		now.Unix(),
		max(int64(0), progress.DesiredWorkers),
		max(int64(0), progress.EffectiveWorkers),
		now.Unix(),
		jobID,
	); err != nil {
		return fmt.Errorf("update worker heartbeat: %w", err)
	}

	return tx.Commit()
}

// RecordJobWorkerEvent appends bounded, redacted worker evidence.
func (repo *repo) RecordJobWorkerEvent(
	ctx context.Context,
	jobID, eventType, severity, message string,
	fields map[string]any,
) error {
	eventType = strings.TrimSpace(eventType)
	severity = strings.TrimSpace(severity)
	if eventType == "" || len(eventType) > 64 {
		return errors.New("worker event type is invalid")
	}
	if severity == "" || len(severity) > 64 {
		return errors.New("worker event severity is invalid")
	}

	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin worker event transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := requireJob(ctx, tx, jobID); err != nil {
		return err
	}
	if err := insertJobEvent(ctx, tx, jobEventInput{
		jobID: jobID, typeName: eventType, severity: severity,
		stage:   jobruntime.StageSearchingMaps,
		message: message, context: fields, createdAt: time.Now().UTC().Unix(),
	}); err != nil {
		return err
	}

	return tx.Commit()
}

// GetJobExecution joins aggregates with the latest checkpoint and resource
// sample. It intentionally does not return every task row.
func (repo *repo) GetJobExecution(ctx context.Context, jobID string) (web.JobExecutionSnapshot, error) {
	var snapshot web.JobExecutionSnapshot
	if err := repo.db.QueryRowContext(
		ctx,
		`SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN state = 'pending' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'running' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'completed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'skipped' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN attempts > 1 THEN attempts - 1 ELSE 0 END), 0)
		FROM job_tasks WHERE job_id = ?`,
		jobID,
	).Scan(
		&snapshot.Tasks.Total,
		&snapshot.Tasks.Pending,
		&snapshot.Tasks.Running,
		&snapshot.Tasks.Completed,
		&snapshot.Tasks.Failed,
		&snapshot.Tasks.Skipped,
		&snapshot.Tasks.Retries,
	); err != nil {
		return web.JobExecutionSnapshot{}, fmt.Errorf("read task summary: %w", err)
	}

	var recovery int
	if err := repo.db.QueryRowContext(
		ctx,
		`SELECT recovery_required FROM job_runtime WHERE job_id = ?`,
		jobID,
	).Scan(&recovery); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return web.JobExecutionSnapshot{}, fmt.Errorf("%w: %s", web.ErrLifecycleNotFound, jobID)
		}
		return web.JobExecutionSnapshot{}, fmt.Errorf("read checkpoint recovery state: %w", err)
	}
	snapshot.RecoveryRequired = recovery != 0

	var progressStage string
	var eta sql.NullInt64
	var updatedAt int64
	if err := repo.db.QueryRowContext(
		ctx,
		`SELECT job_progress.stage, active_tasks, retries, places_per_minute, eta_seconds,
			current_query, current_cell, current_domain, browser_count, active_pages,
			cpu_percent, memory_bytes, disk_free_bytes, database_writes, website_queue,
			job_runtime.desired_concurrency, job_runtime.effective_concurrency, job_progress.updated_at
		FROM job_progress
		JOIN job_runtime ON job_runtime.job_id = job_progress.job_id
		WHERE job_progress.job_id = ?`,
		jobID,
	).Scan(
		&progressStage,
		&snapshot.Progress.ActiveTasks,
		&snapshot.Progress.Retries,
		&snapshot.Progress.PlacesPerMinute,
		&eta,
		&snapshot.Progress.CurrentQuery,
		&snapshot.Progress.CurrentCell,
		&snapshot.Progress.CurrentDomain,
		&snapshot.Progress.BrowserCount,
		&snapshot.Progress.ActivePages,
		&snapshot.Progress.CPUPercent,
		&snapshot.Progress.MemoryBytes,
		&snapshot.Progress.DiskFreeBytes,
		&snapshot.Progress.DatabaseWrites,
		&snapshot.Progress.WebsiteQueue,
		&snapshot.Progress.DesiredWorkers,
		&snapshot.Progress.EffectiveWorkers,
		&updatedAt,
	); err != nil {
		return web.JobExecutionSnapshot{}, fmt.Errorf("read worker progress: %w", err)
	}
	snapshot.Progress.Stage = jobruntime.Stage(progressStage)
	if eta.Valid {
		value := eta.Int64
		snapshot.Progress.ETASeconds = &value
	}
	snapshot.Progress.UpdatedAt = time.Unix(updatedAt, 0).UTC()

	checkpoint, err := repo.latestJobCheckpoint(ctx, jobID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return web.JobExecutionSnapshot{}, err
	}
	if err == nil {
		snapshot.Checkpoint = &checkpoint
	}

	return snapshot, nil
}

// RecoverAbandonedJobs is deliberately repeatable. Only active states are
// changed, so a second launch produces no duplicate recovery events.
func (repo *repo) RecoverAbandonedJobs(ctx context.Context) (int, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin abandoned job recovery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(
		ctx,
		`SELECT job_id, stage FROM job_runtime WHERE state IN ('starting', 'running', 'cancelling') ORDER BY job_id`,
	)
	if err != nil {
		return 0, fmt.Errorf("find abandoned jobs: %w", err)
	}
	type abandonedJob struct {
		id    string
		stage jobruntime.Stage
	}
	jobs := make([]abandonedJob, 0)
	for rows.Next() {
		var job abandonedJob
		var stage string
		if err := rows.Scan(&job.id, &stage); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan abandoned job: %w", err)
		}
		job.stage = jobruntime.Stage(stage)
		if !job.stage.Valid() {
			job.stage = jobruntime.StagePreparingQueries
		}
		jobs = append(jobs, job)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close abandoned jobs: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate abandoned jobs: %w", err)
	}
	if len(jobs) == 0 {
		return 0, tx.Commit()
	}

	now := time.Now().UTC().Unix()
	for _, job := range jobs {
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE job_runtime SET
				state = 'paused', state_version = state_version + 1,
				requested_stop = '', outcome_reason = 'shutdown', recovery_required = 1,
				message = 'Previous process stopped; resume from the last safe checkpoint.',
				last_heartbeat_at = ?, finished_at = NULL, updated_at = ?
			WHERE job_id = ? AND state IN ('starting', 'running', 'cancelling')`,
			now,
			now,
			job.id,
		); err != nil {
			return 0, fmt.Errorf("recover job %s: %w", job.id, err)
		}
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE jobs SET status = 'pending', updated_at = ? WHERE id = ?`,
			now,
			job.id,
		); err != nil {
			return 0, fmt.Errorf("recover legacy job %s: %w", job.id, err)
		}
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE job_tasks SET state = 'pending', finished_at = NULL,
				lease_owner = '', lease_expires_at = NULL, started_at = NULL,
				last_error = 'Interrupted by local service shutdown', updated_at = ?
			WHERE job_id = ? AND state = 'running'`,
			now,
			job.id,
		); err != nil {
			return 0, fmt.Errorf("release tasks for job %s: %w", job.id, err)
		}
		if err := updateTaskAggregates(ctx, tx, job.id, now); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE job_progress SET active_tasks = 0, updated_at = ? WHERE job_id = ?`,
			now,
			job.id,
		); err != nil {
			return 0, fmt.Errorf("reset progress for job %s: %w", job.id, err)
		}
		if err := insertJobEvent(ctx, tx, jobEventInput{
			jobID: job.id, typeName: "recovery", severity: "warning", stage: job.stage,
			message:   "Recovered abandoned active job at its last safe checkpoint",
			context:   map[string]any{"state": "paused", "reason": "shutdown", "recovery_required": true},
			createdAt: now,
		}); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit abandoned job recovery: %w", err)
	}

	return len(jobs), nil
}

func (repo *repo) unfinishedJobTasks(ctx context.Context, jobID string) ([]web.JobTask, error) {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT id, job_id, task_key, kind, state, sequence, query, source_cell, input_id,
			payload, checkpoint, attempts, max_attempts, last_error, origin, priority, started_at, finished_at, updated_at
		FROM job_tasks
		WHERE job_id = ? AND state IN ('pending', 'running')
		ORDER BY sequence, created_at, id`,
		jobID,
	)
	if err != nil {
		return nil, fmt.Errorf("read unfinished tasks: %w", err)
	}
	defer rows.Close()

	tasks := make([]web.JobTask, 0)
	for rows.Next() {
		task, err := scanJobTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unfinished tasks: %w", err)
	}

	return tasks, nil
}

type checkpointQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// MapCellTasks returns every task of a job that carries a source cell, with
// its checkpoint payload, so the Map Explorer can aggregate per-cell duplicate
// evidence from the durable plan.
func (repo *repo) MapCellTasks(ctx context.Context, jobID string) ([]web.JobTask, error) {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT id, job_id, task_key, kind, state, sequence, query, source_cell, input_id,
			payload, checkpoint, attempts, max_attempts, last_error, origin, priority, started_at, finished_at, updated_at
		FROM job_tasks WHERE job_id = ? AND source_cell <> ''
		ORDER BY sequence, task_key`,
		jobID,
	)
	if err != nil {
		return nil, fmt.Errorf("list map cell tasks: %w", err)
	}

	defer func() { _ = rows.Close() }()

	tasks := make([]web.JobTask, 0)

	for rows.Next() {
		task, scanErr := scanJobTask(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan map cell task: %w", scanErr)
		}

		tasks = append(tasks, task)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read map cell tasks: %w", err)
	}

	return tasks, nil
}

func readJobTask(ctx context.Context, queryer checkpointQueryer, jobID, taskKey string) (web.JobTask, error) {
	row := queryer.QueryRowContext(
		ctx,
		`SELECT id, job_id, task_key, kind, state, sequence, query, source_cell, input_id,
			payload, checkpoint, attempts, max_attempts, last_error, origin, priority, started_at, finished_at, updated_at
		FROM job_tasks WHERE job_id = ? AND task_key = ?`,
		jobID,
		taskKey,
	)
	task, err := scanJobTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return web.JobTask{}, fmt.Errorf("%w: %s", web.ErrCheckpointTaskNotFound, taskKey)
	}

	return task, err
}

type checkpointScanner interface {
	Scan(...any) error
}

func scanJobTask(scanner checkpointScanner) (web.JobTask, error) {
	var task web.JobTask
	var payload, checkpoint string
	var startedAt, finishedAt sql.NullInt64
	var updatedAt int64
	if err := scanner.Scan(
		&task.ID,
		&task.JobID,
		&task.Key,
		&task.Kind,
		&task.State,
		&task.Sequence,
		&task.Query,
		&task.SourceCell,
		&task.InputID,
		&payload,
		&checkpoint,
		&task.Attempts,
		&task.MaxAttempts,
		&task.LastError,
		&task.Origin,
		&task.Priority,
		&startedAt,
		&finishedAt,
		&updatedAt,
	); err != nil {
		return web.JobTask{}, err
	}
	task.Payload = json.RawMessage(payload)
	task.Checkpoint = json.RawMessage(checkpoint)
	task.LastError = jobruntime.RedactString(task.LastError)
	task.StartedAt = nullableUnixTime(startedAt)
	task.FinishedAt = nullableUnixTime(finishedAt)
	task.UpdatedAt = time.Unix(updatedAt, 0).UTC()

	return task, nil
}

func (repo *repo) latestJobCheckpoint(ctx context.Context, jobID string) (web.JobCheckpoint, error) {
	var checkpoint web.JobCheckpoint
	var taskID, taskKey sql.NullString
	var stage, payload string
	var createdAt int64
	err := repo.db.QueryRowContext(
		ctx,
		`SELECT job_checkpoints.id, job_checkpoints.job_id, job_checkpoints.task_id,
			job_tasks.task_key, job_checkpoints.stage, job_checkpoints.payload, job_checkpoints.created_at
		FROM job_checkpoints
		LEFT JOIN job_tasks ON job_tasks.id = job_checkpoints.task_id
		WHERE job_checkpoints.job_id = ?
		ORDER BY job_checkpoints.created_at DESC, job_checkpoints.id DESC LIMIT 1`,
		jobID,
	).Scan(
		&checkpoint.ID,
		&checkpoint.JobID,
		&taskID,
		&taskKey,
		&stage,
		&payload,
		&createdAt,
	)
	if err != nil {
		return web.JobCheckpoint{}, err
	}
	checkpoint.TaskID = taskID.String
	checkpoint.TaskKey = taskKey.String
	checkpoint.Stage = jobruntime.Stage(stage)
	checkpoint.Payload = json.RawMessage(payload)
	checkpoint.CreatedAt = time.Unix(createdAt, 0).UTC()

	return checkpoint, nil
}

func updateTaskAggregates(ctx context.Context, tx *sql.Tx, jobID string, now int64) error {
	var summary web.JobTaskSummary
	if err := tx.QueryRowContext(
		ctx,
		`SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN state = 'pending' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'running' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'completed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = 'skipped' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN attempts > 1 THEN attempts - 1 ELSE 0 END), 0)
		FROM job_tasks WHERE job_id = ?`,
		jobID,
	).Scan(
		&summary.Total,
		&summary.Pending,
		&summary.Running,
		&summary.Completed,
		&summary.Failed,
		&summary.Skipped,
		&summary.Retries,
	); err != nil {
		return fmt.Errorf("aggregate job tasks: %w", err)
	}
	progress := float64(0)
	if summary.Total > 0 {
		progress = float64(summary.Completed+summary.Failed+summary.Skipped) * 100 / float64(summary.Total)
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE job_runtime SET total_tasks = ?, completed_tasks = ?, failed_tasks = ?,
			progress = ?, last_heartbeat_at = ?, updated_at = ? WHERE job_id = ?`,
		summary.Total,
		summary.Completed,
		summary.Failed,
		progress,
		now,
		now,
		jobID,
	); err != nil {
		return fmt.Errorf("update task runtime aggregate: %w", err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE job_progress SET skipped_tasks = ?, active_tasks = ?, retries = ?, updated_at = ? WHERE job_id = ?`,
		summary.Skipped,
		summary.Running,
		summary.Retries,
		now,
		jobID,
	); err != nil {
		return fmt.Errorf("update task progress aggregate: %w", err)
	}

	return nil
}

func requireJob(ctx context.Context, queryer checkpointQueryer, jobID string) error {
	var exists int
	if err := queryer.QueryRowContext(ctx, `SELECT 1 FROM jobs WHERE id = ?`, jobID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", web.ErrLifecycleNotFound, jobID)
		}
		return fmt.Errorf("find checkpoint job: %w", err)
	}

	return nil
}

func durableTaskID(jobID, taskKey string) string {
	digest := sha256.Sum256([]byte(jobID + "\x00" + taskKey))

	return hex.EncodeToString(digest[:16])
}
