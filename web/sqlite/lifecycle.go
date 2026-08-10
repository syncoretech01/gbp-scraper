package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

const (
	defaultEventLimit = 100
	maximumEventLimit = 1000
)

var _ web.LifecycleRepository = (*repo)(nil)

// CreateWithState atomically creates a legacy-compatible job together with
// its canonical runtime, initial progress row, configuration version, and
// creation event.
func (repo *repo) CreateWithState(ctx context.Context, item *web.Job, state jobruntime.State) error {
	if !state.Valid() {
		return fmt.Errorf("%w: %q", jobruntime.ErrInvalidState, state)
	}

	legacyStatus, err := jobruntime.LegacyStatusForState(state)
	if err != nil {
		return err
	}

	copyItem := *item
	copyItem.Status = string(legacyStatus)
	row, err := jobToRow(&copyItem)
	if err != nil {
		return fmt.Errorf("encode job: %w", err)
	}

	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create job transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	queueSequence := int64(0)
	var queuedAt any
	if state == jobruntime.StateQueued {
		queueSequence, err = nextQueueSequence(ctx, tx)
		if err != nil {
			return err
		}
		queuedAt = row.CreatedAt
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO jobs (id, name, status, data, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		row.ID,
		row.Name,
		row.Status,
		row.Data,
		row.CreatedAt,
		row.UpdatedAt,
	); err != nil {
		return fmt.Errorf("insert job: %w", err)
	}

	stage := lifecycleStage(state)
	progress := lifecycleProgress(state)
	finishedAt := nullableTerminalTime(state, row.UpdatedAt)
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO job_runtime(
			job_id, state, state_version, stage, message, progress,
			requested_stop, outcome_reason, recovery_required, queue_seq,
			queued_at, finished_at, config_snapshot, created_at, updated_at
		) VALUES (?, ?, 0, ?, ?, ?, '', '', 0, ?, ?, ?, ?, ?, ?)`,
		row.ID,
		state,
		stage,
		creationMessage(state),
		progress,
		queueSequence,
		queuedAt,
		finishedAt,
		row.Data,
		row.CreatedAt,
		row.UpdatedAt,
	); err != nil {
		return fmt.Errorf("insert job runtime: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO job_config_versions(job_id, version, configuration, created_at)
		VALUES (?, 1, ?, ?)`,
		row.ID,
		row.Data,
		row.CreatedAt,
	); err != nil {
		return fmt.Errorf("insert job configuration version: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO job_progress(job_id, stage, updated_at) VALUES (?, ?, ?)`,
		row.ID,
		stage,
		row.UpdatedAt,
	); err != nil {
		return fmt.Errorf("insert job progress: %w", err)
	}

	if err := insertJobEvent(ctx, tx, jobEventInput{
		jobID:    row.ID,
		typeName: "created",
		severity: "information",
		stage:    stage,
		message:  creationMessage(state),
		context: map[string]any{
			"state": state,
		},
		createdAt: row.CreatedAt,
	}); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create job transaction: %w", err)
	}

	return nil
}

// GetRuntime returns the canonical durable runtime projection for a job.
func (repo *repo) GetRuntime(ctx context.Context, id string) (web.JobRuntime, error) {
	runtime, err := readRuntime(ctx, repo.db, id)
	if err != nil {
		return web.JobRuntime{}, err
	}

	return runtime, nil
}

// ApplyControl evaluates an operator command and persists the decision using
// an optimistic state-version comparison. Repeated satisfied commands are
// returned as no-ops without incrementing the version or duplicating events.
func (repo *repo) ApplyControl(
	ctx context.Context,
	id string,
	control jobruntime.Control,
) (web.JobRuntime, jobruntime.ControlDecision, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return web.JobRuntime{}, jobruntime.ControlDecision{}, fmt.Errorf("begin job control transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	current, err := readRuntime(ctx, tx, id)
	if err != nil {
		return web.JobRuntime{}, jobruntime.ControlDecision{}, err
	}

	decision, err := jobruntime.DecideControl(current.State, current.RequestedStop, control)
	if err != nil {
		return current, jobruntime.ControlDecision{}, err
	}
	if err := decision.Error(); err != nil {
		return current, decision, err
	}
	if !decision.Changed() {
		return current, decision, nil
	}

	now := time.Now().UTC().Unix()
	queueSequence := int64(0)
	var queuedAt any
	if decision.NextState == jobruntime.StateQueued && current.State != jobruntime.StateQueued {
		queueSequence, err = nextQueueSequence(ctx, tx)
		if err != nil {
			return current, decision, err
		}
		queuedAt = now
	}

	stage := current.Stage
	if stage == jobruntime.StageNone || current.State != decision.NextState {
		stage = controlStage(stage, decision.NextState)
	}

	outcomeReason := current.OutcomeReason
	finishedAt := unixTimeValue(current.FinishedAt)
	if decision.NextState == jobruntime.StateQueued {
		outcomeReason = jobruntime.StopReasonNone
		finishedAt = nil
	}
	if decision.NextState == jobruntime.StateCancelled {
		outcomeReason = jobruntime.StopReasonUserCancelled
		finishedAt = now
	}

	legacyStatus, err := jobruntime.LegacyStatusForState(decision.NextState)
	if err != nil {
		return current, decision, err
	}

	query := `UPDATE job_runtime SET
		state = ?,
		state_version = state_version + 1,
		stage = ?,
		message = ?,
		requested_stop = ?,
		outcome_reason = ?,
		recovery_required = 0,
		queue_seq = CASE WHEN ? > 0 THEN ? ELSE queue_seq END,
		queued_at = CASE WHEN ? IS NOT NULL THEN ? ELSE queued_at END,
		finished_at = ?,
		updated_at = ?
	WHERE job_id = ? AND state_version = ?`
	result, err := tx.ExecContext(
		ctx,
		query,
		decision.NextState,
		stage,
		jobruntime.RedactString(decision.Message),
		decision.RequestedStop,
		outcomeReason,
		queueSequence,
		queueSequence,
		queuedAt,
		queuedAt,
		finishedAt,
		now,
		id,
		current.StateVersion,
	)
	if err != nil {
		return current, decision, fmt.Errorf("persist job control: %w", err)
	}
	if err := requireCASUpdate(result); err != nil {
		return current, decision, err
	}

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE jobs SET status = ?, updated_at = ? WHERE id = ?`,
		legacyStatus,
		now,
		id,
	); err != nil {
		return current, decision, fmt.Errorf("update legacy job status: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE job_progress SET stage = ?, updated_at = ? WHERE job_id = ?`,
		stage,
		now,
		id,
	); err != nil {
		return current, decision, fmt.Errorf("update job progress: %w", err)
	}

	if err := insertJobEvent(ctx, tx, jobEventInput{
		jobID:    id,
		typeName: "control",
		severity: "information",
		stage:    stage,
		message:  decision.Message,
		context: map[string]any{
			"control":        decision.Control,
			"disposition":    decision.Disposition,
			"from_state":     decision.CurrentState,
			"to_state":       decision.NextState,
			"eventual_state": decision.EventualState,
			"requested_stop": decision.RequestedStop,
		},
		createdAt: now,
	}); err != nil {
		return current, decision, err
	}

	updated, err := readRuntime(ctx, tx, id)
	if err != nil {
		return current, decision, err
	}
	if err := tx.Commit(); err != nil {
		return current, decision, fmt.Errorf("commit job control transaction: %w", err)
	}

	return updated, decision, nil
}

// SetOutcome atomically persists a deterministic executor outcome and its
// legacy status projection. Exact repeated outcomes are idempotent.
func (repo *repo) SetOutcome(
	ctx context.Context,
	id string,
	outcome jobruntime.Outcome,
	message string,
) (web.JobRuntime, error) {
	if err := validateOutcome(outcome); err != nil {
		return web.JobRuntime{}, err
	}

	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return web.JobRuntime{}, fmt.Errorf("begin job outcome transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	current, err := readRuntime(ctx, tx, id)
	if err != nil {
		return web.JobRuntime{}, err
	}
	if current.State == outcome.State && current.OutcomeReason == outcome.Reason && current.RequestedStop == jobruntime.StopReasonNone {
		return current, nil
	}
	if !validOutcomeTransition(current.State, outcome) {
		return current, fmt.Errorf(
			"%w: %s -> %s for %s",
			jobruntime.ErrInvalidTransition,
			current.State,
			outcome.State,
			outcome.Reason,
		)
	}

	now := time.Now().UTC().Unix()
	stage := current.Stage
	if outcome.State.Terminal() {
		stage = jobruntime.StageSavingExporting
	} else if outcome.State == jobruntime.StateQueued && stage == jobruntime.StageNone {
		stage = jobruntime.StagePreparingQueries
	}
	progress := current.Progress
	if outcome.State == jobruntime.StateCompleted {
		progress = 100
	}

	queueSequence := int64(0)
	var queuedAt any
	if outcome.State == jobruntime.StateQueued {
		queueSequence, err = nextQueueSequence(ctx, tx)
		if err != nil {
			return current, err
		}
		queuedAt = now
	}

	var finishedAt any
	if outcome.State.Terminal() {
		finishedAt = now
	}
	legacyStatus, err := jobruntime.LegacyStatusForState(outcome.State)
	if err != nil {
		return current, err
	}

	result, err := tx.ExecContext(
		ctx,
		`UPDATE job_runtime SET
			state = ?,
			state_version = state_version + 1,
			stage = ?,
			message = ?,
			progress = ?,
			requested_stop = '',
			outcome_reason = ?,
			recovery_required = 0,
			queue_seq = CASE WHEN ? > 0 THEN ? ELSE queue_seq END,
			queued_at = CASE WHEN ? IS NOT NULL THEN ? ELSE queued_at END,
			finished_at = ?,
			updated_at = ?
		WHERE job_id = ? AND state_version = ?`,
		outcome.State,
		stage,
		jobruntime.RedactString(message),
		progress,
		outcome.Reason,
		queueSequence,
		queueSequence,
		queuedAt,
		queuedAt,
		finishedAt,
		now,
		id,
		current.StateVersion,
	)
	if err != nil {
		return current, fmt.Errorf("persist job outcome: %w", err)
	}
	if err := requireCASUpdate(result); err != nil {
		return current, err
	}

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE jobs SET status = ?, updated_at = ? WHERE id = ?`,
		legacyStatus,
		now,
		id,
	); err != nil {
		return current, fmt.Errorf("update legacy job outcome: %w", err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE job_progress SET stage = ?, updated_at = ? WHERE job_id = ?`,
		stage,
		now,
		id,
	); err != nil {
		return current, fmt.Errorf("update job outcome progress: %w", err)
	}

	severity := "information"
	if outcome.State == jobruntime.StatePartial || outcome.State == jobruntime.StateCancelled || outcome.State == jobruntime.StatePaused {
		severity = "warning"
	}
	if outcome.State == jobruntime.StateFailed {
		severity = "system-error"
	}
	if err := insertJobEvent(ctx, tx, jobEventInput{
		jobID:    id,
		typeName: "outcome",
		severity: severity,
		stage:    stage,
		message:  message,
		context: map[string]any{
			"state":               outcome.State,
			"reason":              outcome.Reason,
			"recoverable":         outcome.Recoverable,
			"has_partial_results": outcome.HasPartialResults,
		},
		createdAt: now,
	}); err != nil {
		return current, err
	}

	updated, err := readRuntime(ctx, tx, id)
	if err != nil {
		return current, err
	}
	if err := tx.Commit(); err != nil {
		return current, fmt.Errorf("commit job outcome transaction: %w", err)
	}

	return updated, nil
}

// EventsAfter returns redacted events ordered by their durable cursor.
func (repo *repo) EventsAfter(ctx context.Context, id string, after int64, limit int) ([]web.JobEvent, error) {
	if after < 0 {
		return nil, fmt.Errorf("event cursor must be non-negative")
	}
	if limit <= 0 {
		limit = defaultEventLimit
	}
	if limit > maximumEventLimit {
		limit = maximumEventLimit
	}

	var exists int
	if err := repo.db.QueryRowContext(ctx, `SELECT 1 FROM jobs WHERE id = ?`, id).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", web.ErrLifecycleNotFound, id)
		}

		return nil, fmt.Errorf("find job for events: %w", err)
	}

	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT id, job_id, type, severity, stage, message, context, created_at
		FROM job_events
		WHERE job_id = ? AND id > ?
		ORDER BY id
		LIMIT ?`,
		id,
		after,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("read job events: %w", err)
	}
	defer rows.Close()

	events := make([]web.JobEvent, 0)
	for rows.Next() {
		var event web.JobEvent
		var stage string
		var createdAt int64
		if err := rows.Scan(
			&event.ID,
			&event.JobID,
			&event.Type,
			&event.Severity,
			&stage,
			&event.Message,
			&event.Context,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan job event: %w", err)
		}

		event.Stage = jobruntime.Stage(stage)
		if !event.Stage.Valid() {
			return nil, fmt.Errorf("read job event %d: %w: %q", event.ID, jobruntime.ErrInvalidStage, stage)
		}
		event.Message = jobruntime.RedactString(event.Message)
		event.Context = redactStoredContext(event.Context)
		event.OccurredAt = time.Unix(createdAt, 0).UTC()
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read job events: %w", err)
	}

	return events, nil
}

type runtimeQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readRuntime(ctx context.Context, queryer runtimeQueryer, id string) (web.JobRuntime, error) {
	var runtime web.JobRuntime
	var state, stage, requestedStop, outcomeReason string
	var startedAt, finishedAt sql.NullInt64
	var updatedAt int64
	err := queryer.QueryRowContext(
		ctx,
		`SELECT job_id, state, state_version, stage, requested_stop, outcome_reason,
			progress, message, total_tasks, completed_tasks, failed_tasks,
			raw_records, unique_records, emails_found, warnings, errors,
			started_at, finished_at, updated_at
		FROM job_runtime WHERE job_id = ?`,
		id,
	).Scan(
		&runtime.JobID,
		&state,
		&runtime.StateVersion,
		&stage,
		&requestedStop,
		&outcomeReason,
		&runtime.Progress,
		&runtime.Message,
		&runtime.TotalTasks,
		&runtime.Completed,
		&runtime.Failed,
		&runtime.RawRecords,
		&runtime.UniqueRecords,
		&runtime.Emails,
		&runtime.Warnings,
		&runtime.Errors,
		&startedAt,
		&finishedAt,
		&updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return web.JobRuntime{}, fmt.Errorf("%w: %s", web.ErrLifecycleNotFound, id)
		}

		return web.JobRuntime{}, fmt.Errorf("read job runtime: %w", err)
	}

	parsedState, err := jobruntime.ParseState(state)
	if err != nil {
		return web.JobRuntime{}, fmt.Errorf("read job runtime: %w", err)
	}
	runtime.State = parsedState
	runtime.Stage = jobruntime.Stage(stage)
	if !runtime.Stage.Valid() {
		return web.JobRuntime{}, fmt.Errorf("read job runtime: %w: %q", jobruntime.ErrInvalidStage, stage)
	}
	runtime.RequestedStop = jobruntime.StopReason(requestedStop)
	if !runtime.RequestedStop.Valid() {
		return web.JobRuntime{}, fmt.Errorf("read job runtime: %w: %q", jobruntime.ErrInvalidStopReason, requestedStop)
	}
	runtime.OutcomeReason = jobruntime.StopReason(outcomeReason)
	if !runtime.OutcomeReason.Valid() {
		return web.JobRuntime{}, fmt.Errorf("read job runtime: %w: %q", jobruntime.ErrInvalidStopReason, outcomeReason)
	}
	runtime.Message = jobruntime.RedactString(runtime.Message)
	runtime.StartedAt = nullableUnixTime(startedAt)
	runtime.FinishedAt = nullableUnixTime(finishedAt)
	runtime.UpdatedAt = time.Unix(updatedAt, 0).UTC()

	return runtime, nil
}

func nextQueueSequence(ctx context.Context, tx *sql.Tx) (int64, error) {
	var sequence int64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COALESCE(MAX(queue_seq), 0) + 1 FROM job_runtime`,
	).Scan(&sequence); err != nil {
		return 0, fmt.Errorf("allocate queue sequence: %w", err)
	}
	if sequence <= 0 {
		return 0, fmt.Errorf("allocate queue sequence: invalid value %d", sequence)
	}

	return sequence, nil
}

func requireCASUpdate(result sql.Result) error {
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect lifecycle update: %w", err)
	}
	if rowsAffected != 1 {
		return web.ErrLifecycleConflict
	}

	return nil
}

type jobEventInput struct {
	jobID     string
	typeName  string
	severity  string
	stage     jobruntime.Stage
	message   string
	context   map[string]any
	createdAt int64
}

func insertJobEvent(ctx context.Context, tx *sql.Tx, event jobEventInput) error {
	redactedContext := jobruntime.RedactValue(event.context)
	encodedContext, err := json.Marshal(redactedContext)
	if err != nil {
		return fmt.Errorf("encode job event context: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO job_events(job_id, type, severity, stage, message, context, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		event.jobID,
		event.typeName,
		event.severity,
		event.stage,
		jobruntime.RedactString(event.message),
		string(encodedContext),
		event.createdAt,
	); err != nil {
		return fmt.Errorf("insert job event: %w", err)
	}

	return nil
}

func validateOutcome(outcome jobruntime.Outcome) error {
	if !outcome.State.Valid() {
		return fmt.Errorf("%w: %q", jobruntime.ErrInvalidState, outcome.State)
	}
	if !outcome.Reason.Valid() {
		return fmt.Errorf("%w: %q", jobruntime.ErrInvalidStopReason, outcome.Reason)
	}

	valid := false
	switch outcome.State {
	case jobruntime.StateCompleted:
		valid = outcome.Reason == jobruntime.StopReasonCompleted
	case jobruntime.StatePartial:
		valid = outcome.Reason == jobruntime.StopReasonRuntimeLimit ||
			outcome.Reason == jobruntime.StopReasonMaximumRecords ||
			outcome.Reason == jobruntime.StopReasonTaskFailures ||
			outcome.Reason == jobruntime.StopReasonTasksIncomplete
	case jobruntime.StatePaused:
		valid = outcome.Reason == jobruntime.StopReasonPauseRequested ||
			outcome.Reason == jobruntime.StopReasonLowDisk ||
			outcome.Reason == jobruntime.StopReasonShutdown ||
			outcome.Reason == jobruntime.StopReasonProxiesUnavailable
	case jobruntime.StateCancelled:
		valid = outcome.Reason == jobruntime.StopReasonUserCancelled
	case jobruntime.StateQueued:
		valid = outcome.Reason == jobruntime.StopReasonReconfigure
	case jobruntime.StateFailed:
		valid = outcome.Reason == jobruntime.StopReasonFatalError
	}
	if !valid {
		return fmt.Errorf("invalid outcome state/reason pair: %s/%s", outcome.State, outcome.Reason)
	}

	return nil
}

func validOutcomeTransition(current jobruntime.State, outcome jobruntime.Outcome) bool {
	if jobruntime.CanTransition(current, outcome.State) {
		return true
	}

	return outcome.State == jobruntime.StateQueued &&
		outcome.Reason == jobruntime.StopReasonReconfigure &&
		current.Active()
}

func lifecycleStage(state jobruntime.State) jobruntime.Stage {
	switch state {
	case jobruntime.StateStarting, jobruntime.StateRunning, jobruntime.StateCancelling:
		return jobruntime.StageSearchingMaps
	case jobruntime.StateCompleted, jobruntime.StatePartial, jobruntime.StateCancelled:
		return jobruntime.StageSavingExporting
	default:
		return jobruntime.StagePreparingQueries
	}
}

func controlStage(current jobruntime.Stage, next jobruntime.State) jobruntime.Stage {
	if next == jobruntime.StateCancelled {
		return jobruntime.StageSavingExporting
	}
	if next == jobruntime.StateQueued && current == jobruntime.StageNone {
		return jobruntime.StagePreparingQueries
	}
	if current.Valid() {
		return current
	}

	return lifecycleStage(next)
}

func lifecycleProgress(state jobruntime.State) float64 {
	if state == jobruntime.StateCompleted {
		return 100
	}

	return 0
}

func creationMessage(state jobruntime.State) string {
	if state == jobruntime.StateDraft {
		return "job saved as draft"
	}
	if state == jobruntime.StateQueued {
		return "job queued"
	}

	return "job created"
}

func nullableTerminalTime(state jobruntime.State, timestamp int64) any {
	if state.Terminal() {
		return timestamp
	}

	return nil
}

func nullableUnixTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}

	timestamp := time.Unix(value.Int64, 0).UTC()

	return &timestamp
}

func unixTimeValue(value *time.Time) any {
	if value == nil {
		return nil
	}

	return value.Unix()
}

func redactStoredContext(value string) string {
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return jobruntime.RedactString(value)
	}

	redacted, err := json.Marshal(jobruntime.RedactValue(decoded))
	if err != nil {
		return "{}"
	}

	return string(redacted)
}
