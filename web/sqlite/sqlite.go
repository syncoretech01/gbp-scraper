package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // sqlite driver

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

type repo struct {
	db   *sql.DB
	path string
}

func New(path string) (web.JobRepository, error) {
	db, err := initDatabase(path)
	if err != nil {
		return nil, err
	}

	return &repo{db: db, path: path}, nil
}

// Close releases the underlying database handle. The long-running application
// keeps one repository for the process lifetime, but tests and tools that open a
// temporary database need a way to release the file.
func (repo *repo) Close() error {
	if repo == nil || repo.db == nil {
		return nil
	}

	return repo.db.Close()
}

func (repo *repo) Get(ctx context.Context, id string) (web.Job, error) {
	const q = `SELECT id, name, status, data, created_at, updated_at FROM jobs WHERE id = ?`

	row := repo.db.QueryRowContext(ctx, q, id)

	return rowToJob(row)
}

func (repo *repo) Create(ctx context.Context, job *web.Job) error {
	return repo.CreateWithState(ctx, job, lifecycleStateFromLegacyStatus(job.Status))
}

func (repo *repo) Delete(ctx context.Context, id string) error {
	const q = `DELETE FROM jobs WHERE id = ?`

	_, err := repo.db.ExecContext(ctx, q, id)

	return err
}

func (repo *repo) Select(ctx context.Context, params web.SelectParams) ([]web.Job, error) {
	q := `SELECT jobs.id, jobs.name, jobs.status, jobs.data, jobs.created_at, jobs.updated_at FROM jobs`

	var args []any

	if params.Status == web.StatusPending {
		q += ` JOIN job_runtime ON job_runtime.job_id = jobs.id
			WHERE jobs.status = ? AND job_runtime.state = 'queued'`

		args = append(args, params.Status)
	} else if params.Status != "" {
		q += ` WHERE jobs.status = ?`

		args = append(args, params.Status)
	}

	if params.Status == web.StatusPending {
		q += ` ORDER BY job_runtime.queue_seq, COALESCE(job_runtime.queued_at, jobs.created_at), jobs.created_at, jobs.id`
	} else {
		q += " ORDER BY jobs.created_at DESC"
	}

	if params.Limit > 0 {
		q += " LIMIT ?"

		args = append(args, params.Limit)
	}

	rows, err := repo.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var ans []web.Job

	for rows.Next() {
		job, err := rowToJob(rows)
		if err != nil {
			return nil, err
		}

		ans = append(ans, job)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return ans, nil
}

func (repo *repo) Update(ctx context.Context, job *web.Job) error {
	item, err := jobToRow(job)
	if err != nil {
		return err
	}

	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		_ = tx.Rollback()
	}()

	const q = `UPDATE jobs SET name = ?, status = ?, data = ?, updated_at = ? WHERE id = ?`

	result, err := tx.ExecContext(ctx, q, item.Name, item.Status, item.Data, item.UpdatedAt, item.ID)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		return tx.Commit()
	}

	if err := upsertJobFoundation(ctx, tx, item); err != nil {
		return err
	}

	return tx.Commit()
}

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func upsertJobFoundation(ctx context.Context, tx *sql.Tx, item job) error {
	incomingState := lifecycleStateFromLegacyStatus(item.Status)
	progress := lifecycleProgress(incomingState)
	stage := lifecycleStage(incomingState)

	queueSequence := int64(0)
	var queuedAt any
	if incomingState == jobruntime.StateQueued {
		var err error
		queueSequence, err = nextQueueSequence(ctx, tx)
		if err != nil {
			return err
		}
		queuedAt = item.CreatedAt
	}

	_, err := tx.ExecContext(
		ctx,
		`INSERT INTO job_runtime(
			job_id, state, stage, progress, queue_seq, queued_at,
			config_snapshot, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(job_id) DO UPDATE SET
			config_snapshot = excluded.config_snapshot,
			updated_at = excluded.updated_at`,
		item.ID,
		incomingState,
		stage,
		progress,
		queueSequence,
		queuedAt,
		item.Data,
		item.CreatedAt,
		item.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert job runtime foundation: %w", err)
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO job_config_versions(job_id, version, configuration, created_at)
		VALUES (?, 1, ?, ?)`,
		item.ID,
		item.Data,
		item.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert job configuration foundation: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO job_progress(job_id, stage, updated_at) VALUES (?, ?, ?)`,
		item.ID,
		stage,
		item.UpdatedAt,
	); err != nil {
		return fmt.Errorf("upsert job progress foundation: %w", err)
	}

	return applyLegacyRuntimeProjection(ctx, tx, item, incomingState)
}

func applyLegacyRuntimeProjection(
	ctx context.Context,
	tx *sql.Tx,
	item job,
	incomingState jobruntime.State,
) error {
	current, err := readRuntime(ctx, tx, item.ID)
	if err != nil {
		return err
	}

	nextState := current.State
	switch incomingState {
	case jobruntime.StateRunning:
		if current.State == jobruntime.StateQueued || current.State == jobruntime.StateStarting ||
			current.State == jobruntime.StateRunning {
			nextState = jobruntime.StateRunning
		}
	case jobruntime.StateCompleted:
		if current.State == jobruntime.StateStarting || current.State == jobruntime.StateRunning ||
			current.State == jobruntime.StateCompleted {
			nextState = jobruntime.StateCompleted
		}
	case jobruntime.StateFailed:
		if current.State.Active() || current.State == jobruntime.StateFailed {
			nextState = jobruntime.StateFailed
		}
	case jobruntime.StateQueued:
		// A legacy pending update must not turn a draft or paused job into a
		// runnable job. Explicit start/resume controls own those transitions.
		if current.State == jobruntime.StateQueued {
			nextState = jobruntime.StateQueued
		}
	}

	legacyStatus, err := jobruntime.LegacyStatusForState(nextState)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE jobs SET status = ? WHERE id = ?`,
		legacyStatus,
		item.ID,
	); err != nil {
		return fmt.Errorf("restore canonical legacy status projection: %w", err)
	}

	if nextState == current.State {
		return nil
	}

	stage := current.Stage
	message := current.Message
	progress := current.Progress
	requestedStop := current.RequestedStop
	outcomeReason := current.OutcomeReason
	startedAt := unixTimeValue(current.StartedAt)
	finishedAt := unixTimeValue(current.FinishedAt)
	switch nextState {
	case jobruntime.StateRunning:
		stage = jobruntime.StageSearchingMaps
		message = "job started"
		requestedStop = jobruntime.StopReasonNone
		outcomeReason = jobruntime.StopReasonNone
		if startedAt == nil {
			startedAt = item.UpdatedAt
		}
		finishedAt = nil
	case jobruntime.StateCompleted:
		stage = jobruntime.StageSavingExporting
		message = "job completed"
		progress = 100
		requestedStop = jobruntime.StopReasonNone
		outcomeReason = jobruntime.StopReasonCompleted
		finishedAt = item.UpdatedAt
	case jobruntime.StateFailed:
		message = "job failed"
		requestedStop = jobruntime.StopReasonNone
		outcomeReason = jobruntime.StopReasonFatalError
		finishedAt = item.UpdatedAt
	}

	result, err := tx.ExecContext(
		ctx,
		`UPDATE job_runtime SET
			state = ?, state_version = state_version + 1, stage = ?, message = ?,
			progress = ?, requested_stop = ?, outcome_reason = ?, recovery_required = 0,
			started_at = ?, finished_at = ?, updated_at = ?
		WHERE job_id = ? AND state_version = ?`,
		nextState,
		stage,
		message,
		progress,
		requestedStop,
		outcomeReason,
		startedAt,
		finishedAt,
		item.UpdatedAt,
		item.ID,
		current.StateVersion,
	)
	if err != nil {
		return fmt.Errorf("apply legacy runtime projection: %w", err)
	}
	if err := requireCASUpdate(result); err != nil {
		return err
	}

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE job_progress SET stage = ?, updated_at = ? WHERE job_id = ?`,
		stage,
		item.UpdatedAt,
		item.ID,
	); err != nil {
		return fmt.Errorf("update legacy job progress projection: %w", err)
	}

	severity := "information"
	if nextState == jobruntime.StateFailed {
		severity = "system-error"
	}
	return insertJobEvent(ctx, tx, jobEventInput{
		jobID:    item.ID,
		typeName: "state",
		severity: severity,
		stage:    stage,
		message:  message,
		context: map[string]any{
			"source":     "legacy_update",
			"from_state": current.State,
			"to_state":   nextState,
		},
		createdAt: item.UpdatedAt,
	})
}

func lifecycleStateFromLegacyStatus(status string) jobruntime.State {
	switch status {
	case web.StatusPending:
		return jobruntime.StateQueued
	case web.StatusWorking:
		return jobruntime.StateRunning
	case web.StatusOK:
		return jobruntime.StateCompleted
	case web.StatusFailed:
		return jobruntime.StateFailed
	default:
		return jobruntime.StateQueued
	}
}

type scannable interface {
	Scan(dest ...any) error
}

func rowToJob(row scannable) (web.Job, error) {
	var j job

	err := row.Scan(&j.ID, &j.Name, &j.Status, &j.Data, &j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		return web.Job{}, err
	}

	ans := web.Job{
		ID:     j.ID,
		Name:   j.Name,
		Status: j.Status,
		Date:   time.Unix(j.CreatedAt, 0).UTC(),
	}

	err = json.Unmarshal([]byte(j.Data), &ans.Data)
	if err != nil {
		return web.Job{}, err
	}

	return ans, nil
}

func jobToRow(item *web.Job) (job, error) {
	data, err := json.Marshal(item.Data)
	if err != nil {
		return job{}, err
	}

	return job{
		ID:        item.ID,
		Name:      item.Name,
		Status:    item.Status,
		Data:      string(data),
		CreatedAt: item.Date.Unix(),
		UpdatedAt: time.Now().UTC().Unix(),
	}, nil
}

type job struct {
	ID        string
	Name      string
	Status    string
	Data      string
	CreatedAt int64
	UpdatedAt int64
}

func initDatabase(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	fail := func(err error) (*sql.DB, error) {
		_ = db.Close()

		return nil, err
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	// SQLite pragmas such as foreign_keys are connection-local. Keep the one
	// configured connection for the lifetime of this local repository so those
	// invariants are not silently lost during connection recycling.
	db.SetConnMaxLifetime(0)

	_, err = db.Exec("PRAGMA busy_timeout = 5000")
	if err != nil {
		return fail(err)
	}

	_, err = db.Exec("PRAGMA foreign_keys=ON")
	if err != nil {
		return fail(err)
	}

	err = db.Ping()
	if err != nil {
		return fail(err)
	}

	// Reject a database created by a newer application before setting WAL mode.
	// Unlike the connection-local pragmas above, journal_mode is persisted in the
	// database file and therefore must not be changed for an unsupported schema.
	if _, err := supportedSchemaVersion(db); err != nil {
		return fail(err)
	}

	_, err = db.Exec("PRAGMA journal_mode=WAL")
	if err != nil {
		return fail(err)
	}

	_, err = db.Exec("PRAGMA synchronous=NORMAL")
	if err != nil {
		return fail(err)
	}

	_, err = db.Exec("PRAGMA cache_size=1000")
	if err != nil {
		return fail(err)
	}

	if err := migrateDatabase(db, path); err != nil {
		return fail(err)
	}

	return db, nil
}
