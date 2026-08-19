package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// Retention only ever removes reproducible artifacts: manual backups beyond the
// configured count and historical version snapshots beyond their window. It
// never touches pre-migration safety copies, job result CSVs, or current data.

// PruneManualBackups deletes manual backup rows beyond the newest keep,
// returning the pruned records so the caller can unlink their files.
// Pre-migration backups are never candidates: they are the safety net for
// schema upgrades and are excluded by kind.
func (repo *repo) PruneManualBackups(ctx context.Context, keep int) ([]web.BackupRecord, error) {
	if keep < 1 {
		return nil, fmt.Errorf("backup retention must keep at least one backup")
	}

	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin backup retention: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(
		ctx,
		`SELECT id, relative_path FROM backups
		WHERE kind = 'manual' AND state = 'completed'
		ORDER BY created_at DESC, id DESC
		LIMIT -1 OFFSET ?`,
		keep,
	)
	if err != nil {
		return nil, fmt.Errorf("list prunable backups: %w", err)
	}

	pruned := make([]web.BackupRecord, 0)

	for rows.Next() {
		var record web.BackupRecord
		if err := rows.Scan(&record.ID, &record.RelativePath); err != nil {
			_ = rows.Close()

			return nil, fmt.Errorf("scan prunable backup: %w", err)
		}

		pruned = append(pruned, record)
	}

	if err := rows.Close(); err != nil {
		return nil, err
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read prunable backups: %w", err)
	}

	if len(pruned) == 0 {
		return nil, tx.Commit()
	}

	for _, record := range pruned {
		if _, err := tx.ExecContext(
			ctx,
			`DELETE FROM backups WHERE id = ? AND kind = 'manual'`,
			record.ID,
		); err != nil {
			return nil, fmt.Errorf("prune backup %s: %w", record.ID, err)
		}
	}

	if err := recordRetentionAudit(ctx, tx, "backups", map[string]any{
		"kept": keep, "pruned": len(pruned),
	}); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit backup retention: %w", err)
	}

	return pruned, nil
}

// PruneBusinessVersions deletes version snapshots observed before the cutoff,
// always keeping each business's most recent snapshot so history never loses
// its head.
func (repo *repo) PruneBusinessVersions(ctx context.Context, cutoff time.Time) (int64, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin version retention: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(
		ctx,
		`DELETE FROM business_versions
		WHERE observed_at < ?
			AND id NOT IN (
				SELECT MAX(id) FROM business_versions GROUP BY business_id
			)`,
		cutoff.UTC().Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("prune business versions: %w", err)
	}

	pruned, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	if pruned == 0 {
		return 0, tx.Commit()
	}

	if err := recordRetentionAudit(ctx, tx, "business_versions", map[string]any{
		"cutoff": cutoff.UTC().Format(time.RFC3339), "pruned": pruned,
	}); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit version retention: %w", err)
	}

	return pruned, nil
}

// OldestCompletedExports lists completed exports oldest-first so a storage cap
// can free space starting with the least recent artifact.
func (repo *repo) OldestCompletedExports(ctx context.Context, limit int) ([]web.ExportRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT id, file_path, file_size FROM exports
		WHERE state = 'completed'
		ORDER BY created_at ASC, id ASC
		LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list oldest exports: %w", err)
	}

	defer func() { _ = rows.Close() }()

	records := make([]web.ExportRecord, 0, limit)

	for rows.Next() {
		var record web.ExportRecord
		if err := rows.Scan(&record.ID, &record.RelativePath, &record.FileSize); err != nil {
			return nil, fmt.Errorf("scan oldest export: %w", err)
		}

		records = append(records, record)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read oldest exports: %w", err)
	}

	return records, nil
}

func recordRetentionAudit(ctx context.Context, tx *sql.Tx, subject string, detail map[string]any) error {
	payload, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode retention audit: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO audit_logs(action, entity_type, entity_id, details, created_at)
		VALUES ('retention_applied', ?, '', ?, ?)`,
		subject,
		string(payload),
		time.Now().UTC().Unix(),
	); err != nil {
		return fmt.Errorf("record retention audit: %w", err)
	}

	return nil
}
