package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// Live controls are durable requests the worker polls between tasks — the safe
// reconfiguration boundary. Nothing here signals a process directly; a control
// written while no worker is running simply applies on the next run.

// JobLiveControls reads the current control state for one job.
func (repo *repo) JobLiveControls(ctx context.Context, jobID string) (web.JobLiveControls, error) {
	var controls web.JobLiveControls

	err := repo.db.QueryRowContext(
		ctx,
		`SELECT extended_seconds, concurrency_override, proxy_pool_override, retry_current_requested
		FROM job_runtime WHERE job_id = ?`,
		jobID,
	).Scan(
		&controls.ExtendedSeconds,
		&controls.ConcurrencyOverride,
		&controls.ProxyPoolOverride,
		&controls.RetryCurrentRequested,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return web.JobLiveControls{}, fmt.Errorf("%w: %s", web.ErrLifecycleNotFound, jobID)
	}

	if err != nil {
		return web.JobLiveControls{}, fmt.Errorf("read live controls: %w", err)
	}

	controls.JobID = jobID

	return controls, nil
}

// ExtendJobRuntime accumulates extra allowed seconds for the current run.
func (repo *repo) ExtendJobRuntime(ctx context.Context, jobID string, seconds int64) error {
	return repo.applyLiveControl(ctx, jobID, "job_runtime_extended", map[string]any{"seconds": seconds},
		`UPDATE job_runtime SET extended_seconds = extended_seconds + ?, updated_at = ? WHERE job_id = ?`,
		seconds, time.Now().UTC().Unix(), jobID,
	)
}

// SetJobConcurrencyOverride stores a live concurrency target (0 clears it).
func (repo *repo) SetJobConcurrencyOverride(ctx context.Context, jobID string, concurrency int) error {
	return repo.applyLiveControl(ctx, jobID, "job_concurrency_override", map[string]any{"concurrency": concurrency},
		`UPDATE job_runtime SET concurrency_override = ?, updated_at = ? WHERE job_id = ?`,
		concurrency, time.Now().UTC().Unix(), jobID,
	)
}

// SetJobProxyPoolOverride stores a live proxy pool switch. The sentinel
// "direct" clears proxies entirely; "" clears the override.
func (repo *repo) SetJobProxyPoolOverride(ctx context.Context, jobID, poolID string) error {
	return repo.applyLiveControl(ctx, jobID, "job_proxy_pool_override", map[string]any{"pool_id": poolID},
		`UPDATE job_runtime SET proxy_pool_override = ?, updated_at = ? WHERE job_id = ?`,
		poolID, time.Now().UTC().Unix(), jobID,
	)
}

// RequestJobRetryCurrent asks the worker to abandon and requeue whatever tasks
// are currently in flight without consuming their attempts.
func (repo *repo) RequestJobRetryCurrent(ctx context.Context, jobID string) error {
	return repo.applyLiveControl(ctx, jobID, "job_retry_current_requested", nil,
		`UPDATE job_runtime SET retry_current_requested = 1, updated_at = ? WHERE job_id = ?`,
		time.Now().UTC().Unix(), jobID,
	)
}

// ConsumeJobRetryCurrent atomically claims a pending retry-current request.
// Exactly one caller observes true per request.
func (repo *repo) ConsumeJobRetryCurrent(ctx context.Context, jobID string) (bool, error) {
	result, err := repo.db.ExecContext(
		ctx,
		`UPDATE job_runtime SET retry_current_requested = 0, updated_at = ?
		WHERE job_id = ? AND retry_current_requested = 1`,
		time.Now().UTC().Unix(),
		jobID,
	)
	if err != nil {
		return false, fmt.Errorf("consume retry-current request: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}

	return affected == 1, nil
}

// ResetJobLiveControls clears every live control at the start of a fresh run,
// so a stale override from a previous run cannot silently reshape a new one.
func (repo *repo) ResetJobLiveControls(ctx context.Context, jobID string) error {
	if _, err := repo.db.ExecContext(
		ctx,
		`UPDATE job_runtime SET extended_seconds = 0, concurrency_override = 0,
			proxy_pool_override = '', retry_current_requested = 0, updated_at = ?
		WHERE job_id = ?`,
		time.Now().UTC().Unix(),
		jobID,
	); err != nil {
		return fmt.Errorf("reset live controls: %w", err)
	}

	return nil
}

func (repo *repo) applyLiveControl(
	ctx context.Context,
	jobID, action string,
	detail map[string]any,
	query string,
	arguments ...any,
) error {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin live control: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, query, arguments...)
	if err != nil {
		return fmt.Errorf("apply live control: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if affected == 0 {
		return fmt.Errorf("%w: %s", web.ErrLifecycleNotFound, jobID)
	}

	if err := insertAuditFromMap(ctx, tx, action, "job", jobID, detail); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit live control: %w", err)
	}

	return nil
}

func insertAuditFromMap(ctx context.Context, tx *sql.Tx, action, entityType, entityID string, detail map[string]any) error {
	payload := "{}"

	if len(detail) > 0 {
		encoded, err := json.Marshal(detail)
		if err != nil {
			return fmt.Errorf("encode live control audit: %w", err)
		}

		payload = string(encoded)
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO audit_logs(action, entity_type, entity_id, details, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		action, entityType, entityID, payload, time.Now().UTC().Unix(),
	); err != nil {
		return fmt.Errorf("audit live control: %w", err)
	}

	return nil
}
