package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// maximumListingKeyBatch bounds one durable write so a very large discovery
// burst is written in steady chunks rather than one enormous statement.
const maximumListingKeyBatch = 500

// intervalCheckpointStage is the stage an interval checkpoint is filed under.
// It matches the per-task checkpoint stage so the monitor's checkpoint card
// treats both kinds of resume boundary identically.
const intervalCheckpointStage = jobruntime.StageSearchingMaps

// RememberJobListingKeys records the listing identities a job has already
// discovered. Keys are one-way digests supplied by the caller, so the durable
// record can never become a second copy of the result set.
//
// The write is idempotent: a key already recorded for the job is ignored, so
// a restart, a retried task, and a duplicate discovery all converge on the
// same rows. It returns how many keys were genuinely new.
func (repo *repo) RememberJobListingKeys(ctx context.Context, jobID string, keys []string) (int, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" || len(keys) == 0 {
		return 0, nil
	}

	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Unix()
	inserted := 0

	for start := 0; start < len(keys); start += maximumListingKeyBatch {
		end := min(start+maximumListingKeyBatch, len(keys))

		placeholders := make([]string, 0, end-start)
		arguments := make([]any, 0, (end-start)*3)

		for _, key := range keys[start:end] {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}

			placeholders = append(placeholders, "(?, ?, ?)")
			arguments = append(arguments, jobID, key, now)
		}

		if len(placeholders) == 0 {
			continue
		}

		result, execErr := tx.ExecContext(
			ctx,
			`INSERT OR IGNORE INTO job_listing_keys(job_id, key_hash, created_at) VALUES `+
				strings.Join(placeholders, ", "),
			arguments...,
		)
		if execErr != nil {
			return 0, fmt.Errorf("record job listing keys: %w", execErr)
		}

		affected, affectedErr := result.RowsAffected()
		if affectedErr != nil {
			return 0, fmt.Errorf("record job listing keys: %w", affectedErr)
		}

		inserted += int(affected)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit job listing keys: %w", err)
	}

	return inserted, nil
}

// JobListingKeys returns the recorded listing identities for one job, oldest
// first, so a resumed run can rebuild its deduplication state exactly.
func (repo *repo) JobListingKeys(ctx context.Context, jobID string, limit int) ([]string, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" || limit <= 0 {
		return nil, nil
	}

	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT key_hash FROM job_listing_keys WHERE job_id = ?
		ORDER BY created_at, key_hash LIMIT ?`,
		jobID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("read job listing keys: %w", err)
	}
	defer func() { _ = rows.Close() }()

	keys := make([]string, 0, min(limit, maximumListingKeyBatch))

	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan job listing key: %w", err)
		}

		keys = append(keys, key)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read job listing keys: %w", err)
	}

	return keys, nil
}

// CountJobListingKeys reports how many listing identities a job has recorded.
func (repo *repo) CountJobListingKeys(ctx context.Context, jobID string) (int64, error) {
	var total int64
	if err := repo.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM job_listing_keys WHERE job_id = ?`,
		strings.TrimSpace(jobID),
	).Scan(&total); err != nil {
		return 0, fmt.Errorf("count job listing keys: %w", err)
	}

	return total, nil
}

// RecordJobIntervalCheckpoint appends a time-based safe resume boundary. It is
// the interval companion of the per-task checkpoint written by CompleteJobTask
// and FailJobTask: an interrupted run therefore reports how recently it was
// still making progress even between task boundaries.
func (repo *repo) RecordJobIntervalCheckpoint(ctx context.Context, jobID, payload string) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return fmt.Errorf("record interval checkpoint: job ID is required")
	}

	if strings.TrimSpace(payload) == "" {
		payload = "{}"
	}

	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Unix()

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO job_checkpoints(job_id, stage, payload, created_at) VALUES (?, ?, ?, ?)`,
		jobID, string(intervalCheckpointStage), payload, now,
	); err != nil {
		return fmt.Errorf("insert interval checkpoint: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE job_runtime SET last_checkpoint_at = ?, updated_at = ? WHERE job_id = ?`,
		now, now, jobID,
	); err != nil {
		return fmt.Errorf("update interval checkpoint runtime: %w", err)
	}

	return tx.Commit()
}
