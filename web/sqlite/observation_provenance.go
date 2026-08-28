package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

var _ interface {
	RecordObservationProvenance(context.Context, []web.ObservationProvenance) error
	ObservationProvenanceFor(context.Context, string, []string) ([]web.ObservationProvenance, error)
} = (*repo)(nil)

// RecordObservationProvenance stores which task observed which rows. It is
// idempotent per (job, identity, task): re-merging a task after a retry writes
// the same facts again.
func (repo *repo) RecordObservationProvenance(ctx context.Context, rows []web.ObservationProvenance) error {
	if len(rows) == 0 {
		return nil
	}

	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin observation provenance: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, row := range rows {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO job_observation_provenance(
				job_id, identity_key, task_key, source_query, source_cell, observed_at
			) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(job_id, identity_key, task_key) DO UPDATE SET
				source_query = excluded.source_query,
				source_cell = excluded.source_cell,
				observed_at = excluded.observed_at`,
			row.JobID, row.IdentityKey, row.TaskKey, row.SourceQuery, row.SourceCell, row.ObservedAt.Unix(),
		); err != nil {
			return fmt.Errorf("record observation provenance: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit observation provenance: %w", err)
	}

	return nil
}

// ObservationProvenanceFor returns every recorded observation of the given
// identity keys within one job, oldest first, so a business seen by several
// tasks lists every query and cell that found it.
func (repo *repo) ObservationProvenanceFor(ctx context.Context, jobID string, keys []string) ([]web.ObservationProvenance, error) {
	return observationProvenanceFor(ctx, repo.db, jobID, keys)
}

type provenanceQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func observationProvenanceFor(ctx context.Context, db provenanceQuerier, jobID string, keys []string) ([]web.ObservationProvenance, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
	args := make([]any, 0, len(keys)+1)
	args = append(args, jobID)

	for _, key := range keys {
		args = append(args, key)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT job_id, identity_key, task_key, source_query, source_cell, observed_at
		FROM job_observation_provenance
		WHERE job_id = ? AND identity_key IN (`+placeholders+`)
		ORDER BY observed_at ASC, task_key ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("read observation provenance: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []web.ObservationProvenance

	for rows.Next() {
		var row web.ObservationProvenance
		var observed int64

		if err := rows.Scan(&row.JobID, &row.IdentityKey, &row.TaskKey, &row.SourceQuery, &row.SourceCell, &observed); err != nil {
			return nil, fmt.Errorf("scan observation provenance: %w", err)
		}

		row.ObservedAt = time.Unix(observed, 0).UTC()
		out = append(out, row)
	}

	return out, rows.Err()
}
