package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

func (repo *repo) SystemDatabaseSnapshot(ctx context.Context) (web.SystemDatabaseSnapshot, error) {
	snapshot := web.SystemDatabaseSnapshot{}
	if err := repo.db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&snapshot.SQLiteVersion); err != nil {
		return web.SystemDatabaseSnapshot{}, fmt.Errorf("read SQLite version: %w", err)
	}
	if err := repo.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&snapshot.SchemaVersion); err != nil {
		return web.SystemDatabaseSnapshot{}, fmt.Errorf("read schema version: %w", err)
	}
	if err := repo.db.QueryRowContext(ctx,
		"SELECT "+
			"(SELECT COUNT(*) FROM jobs), "+
			"(SELECT COUNT(*) FROM businesses WHERE deleted_at IS NULL), "+
			"(SELECT COUNT(*) FROM business_sources), "+
			"(SELECT COUNT(*) FROM exports), "+
			"(SELECT COUNT(*) FROM backups), "+
			"(SELECT COUNT(*) FROM job_runtime WHERE state = 'queued'), "+
			"(SELECT COUNT(*) FROM job_runtime WHERE state IN ('starting','running','cancelling')), "+
			"(SELECT COALESCE(SUM(browser_count), 0) FROM job_progress "+
			"JOIN job_runtime ON job_runtime.job_id = job_progress.job_id "+
			"WHERE job_runtime.state IN ('starting','running','cancelling')), "+
			"(SELECT COALESCE(SUM(active_pages), 0) FROM job_progress "+
			"JOIN job_runtime ON job_runtime.job_id = job_progress.job_id "+
			"WHERE job_runtime.state IN ('starting','running','cancelling')), "+
			"(SELECT COALESCE(SUM(website_queue), 0) FROM job_progress), "+
			"(SELECT COUNT(*) FROM proxies), "+
			"(SELECT COUNT(*) FROM proxies WHERE enabled = 1 AND status IN ('healthy','slow')), "+
			"(SELECT COUNT(*) FROM proxies WHERE status IN ('blocked','rate-limited'))",
	).Scan(
		&snapshot.JobCount, &snapshot.BusinessCount, &snapshot.SourceCount,
		&snapshot.ExportCount, &snapshot.BackupCount, &snapshot.QueuedJobs, &snapshot.RunningJobs,
		&snapshot.ActiveBrowsers, &snapshot.ActivePages, &snapshot.WebsiteQueue,
		&snapshot.ProxyTotal, &snapshot.ProxyHealthy, &snapshot.ProxyBlocked,
	); err != nil {
		return web.SystemDatabaseSnapshot{}, fmt.Errorf("read database counters: %w", err)
	}

	var lastWrite sql.NullInt64
	if err := repo.db.QueryRowContext(ctx,
		"SELECT MAX(updated_at) FROM ("+
			"SELECT MAX(updated_at) AS updated_at FROM jobs "+
			"UNION ALL SELECT MAX(updated_at) FROM job_runtime "+
			"UNION ALL SELECT MAX(updated_at) FROM settings "+
			"UNION ALL SELECT MAX(COALESCE(finished_at, created_at)) FROM exports"+
			")",
	).Scan(&lastWrite); err != nil {
		return web.SystemDatabaseSnapshot{}, fmt.Errorf("read last database write: %w", err)
	}
	if lastWrite.Valid {
		value := time.Unix(lastWrite.Int64, 0).UTC()
		snapshot.LastWriteAt = &value
	}
	var lastBrowser sql.NullInt64
	if err := repo.db.QueryRowContext(ctx, `
		SELECT MAX(updated_at) FROM job_progress
		WHERE browser_count > 0 OR active_pages > 0`).Scan(&lastBrowser); err != nil {
		return web.SystemDatabaseSnapshot{}, fmt.Errorf("read last browser activity: %w", err)
	}
	if lastBrowser.Valid {
		value := time.Unix(lastBrowser.Int64, 0).UTC()
		snapshot.LastBrowserAt = &value
	}

	for _, path := range []string{repo.path, repo.path + "-wal", repo.path + "-shm"} {
		info, err := os.Stat(path)
		if err == nil {
			snapshot.DatabaseBytes += info.Size()
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return web.SystemDatabaseSnapshot{}, fmt.Errorf("inspect database storage: %w", err)
		}
	}

	return snapshot, nil
}

func (repo *repo) CheckDatabaseWritable(ctx context.Context) error {
	transaction, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin database write probe: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(ctx,
		"INSERT INTO audit_logs(action, entity_type, details, created_at) "+
			"VALUES ('system_write_probe', 'system', '{}', ?)",
		time.Now().UTC().Unix(),
	); err != nil {
		return fmt.Errorf("execute database write probe: %w", err)
	}
	if err := transaction.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("roll back database write probe: %w", err)
	}

	return nil
}
