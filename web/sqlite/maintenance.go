package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/web"
)

func (repo *repo) MaintenanceSnapshot(ctx context.Context) (web.MaintenanceSnapshot, error) {
	snapshot := web.MaintenanceSnapshot{SchemaVersion: currentSchemaVersion}
	if err := repo.db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&snapshot.SQLiteVersion); err != nil {
		return web.MaintenanceSnapshot{}, fmt.Errorf("read sqlite version: %w", err)
	}
	if err := repo.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&snapshot.Integrity); err != nil {
		return web.MaintenanceSnapshot{}, fmt.Errorf("check database integrity: %w", err)
	}

	counts := []struct {
		query string
		value *int64
	}{
		{"SELECT COUNT(*) FROM jobs", &snapshot.JobCount},
		{"SELECT COUNT(*) FROM businesses WHERE deleted_at IS NULL", &snapshot.BusinessCount},
		{"SELECT COUNT(*) FROM business_sources", &snapshot.SourceCount},
		{"SELECT COUNT(*) FROM exports", &snapshot.ExportCount},
		{"SELECT COUNT(*) FROM backups", &snapshot.BackupCount},
	}
	for _, count := range counts {
		if err := repo.db.QueryRowContext(ctx, count.query).Scan(count.value); err != nil {
			return web.MaintenanceSnapshot{}, fmt.Errorf("read maintenance count: %w", err)
		}
	}

	if info, err := os.Stat(repo.path); err == nil {
		snapshot.DatabaseBytes = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return web.MaintenanceSnapshot{}, fmt.Errorf("inspect database file: %w", err)
	}

	return snapshot, nil
}

func (repo *repo) RunIntegrityCheck(ctx context.Context) (string, error) {
	var result string
	if err := repo.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return "", fmt.Errorf("run database integrity check: %w", err)
	}

	return result, nil
}

func (repo *repo) VacuumDatabase(ctx context.Context) error {
	if _, err := repo.db.ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("vacuum database: %w", err)
	}
	if _, err := repo.db.ExecContext(ctx,
		"INSERT INTO audit_logs(action, entity_type, details, created_at) "+
			"VALUES ('database_vacuum', 'database', '{}', ?)",
		time.Now().UTC().Unix(),
	); err != nil {
		return fmt.Errorf("audit database vacuum: %w", err)
	}

	return nil
}

func (repo *repo) CreateDatabaseBackup(ctx context.Context) (web.BackupRecord, error) {
	if repo.path == "" || repo.path == ":memory:" {
		return web.BackupRecord{}, fmt.Errorf("database backup requires an on-disk database")
	}

	backupDirectory := filepath.Join(filepath.Dir(repo.path), "backups")
	if err := os.MkdirAll(backupDirectory, 0o700); err != nil {
		return web.BackupRecord{}, fmt.Errorf("create backup directory: %w", err)
	}

	now := time.Now().UTC()
	id := uuid.NewString()
	filename := fmt.Sprintf("manual-%s-%s.db", now.Format("20060102T150405Z"), id)
	fullPath := filepath.Join(backupDirectory, filename)
	if _, err := repo.db.ExecContext(ctx, "VACUUM INTO ?", fullPath); err != nil {
		return web.BackupRecord{}, fmt.Errorf("create database backup: %w", err)
	}
	if err := verifySQLiteDatabase(fullPath); err != nil {
		_ = os.Remove(fullPath)
		return web.BackupRecord{}, fmt.Errorf("verify database backup: %w", err)
	}

	checksum, size, err := checksumFile(fullPath)
	if err != nil {
		_ = os.Remove(fullPath)
		return web.BackupRecord{}, fmt.Errorf("checksum database backup: %w", err)
	}

	relativePath, err := filepath.Rel(filepath.Dir(repo.path), fullPath)
	if err != nil {
		_ = os.Remove(fullPath)
		return web.BackupRecord{}, fmt.Errorf("resolve backup path: %w", err)
	}
	record := web.BackupRecord{
		ID:            id,
		Kind:          "manual",
		State:         "completed",
		RelativePath:  filepath.ToSlash(relativePath),
		SchemaVersion: currentSchemaVersion,
		FileSize:      size,
		Checksum:      checksum,
		CreatedAt:     now,
		FinishedAt:    &now,
	}
	if _, err := repo.db.ExecContext(ctx,
		"INSERT INTO backups(id, kind, state, relative_path, schema_version, file_size, checksum, created_at, finished_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		record.ID, record.Kind, record.State, record.RelativePath, record.SchemaVersion,
		record.FileSize, record.Checksum, now.Unix(), now.Unix(),
	); err != nil {
		_ = os.Remove(fullPath)
		return web.BackupRecord{}, fmt.Errorf("register database backup: %w", err)
	}
	if _, err := repo.db.ExecContext(ctx,
		"INSERT INTO audit_logs(action, entity_type, entity_id, details, created_at) "+
			"VALUES ('backup_created', 'backup', ?, '{}', ?)",
		record.ID, now.Unix(),
	); err != nil {
		return web.BackupRecord{}, fmt.Errorf("audit database backup: %w", err)
	}

	return record, nil
}

func (repo *repo) ListDatabaseBackups(ctx context.Context, limit int) ([]web.BackupRecord, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := repo.db.QueryContext(ctx,
		"SELECT id, kind, state, relative_path, schema_version, file_size, checksum, "+
			"created_at, finished_at, error FROM backups ORDER BY created_at DESC, id DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list database backups: %w", err)
	}
	defer rows.Close()

	records := make([]web.BackupRecord, 0)
	for rows.Next() {
		record, scanErr := scanBackupRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list database backups: %w", err)
	}

	return records, nil
}

func (repo *repo) GetDatabaseBackup(ctx context.Context, id string) (web.BackupRecord, error) {
	row := repo.db.QueryRowContext(ctx,
		"SELECT id, kind, state, relative_path, schema_version, file_size, checksum, "+
			"created_at, finished_at, error FROM backups WHERE id = ?",
		id,
	)
	return scanBackupRecord(row)
}

type backupScanner interface {
	Scan(...any) error
}

func scanBackupRecord(scanner backupScanner) (web.BackupRecord, error) {
	var record web.BackupRecord
	var createdAt int64
	var finishedAt sql.NullInt64
	if err := scanner.Scan(
		&record.ID, &record.Kind, &record.State, &record.RelativePath,
		&record.SchemaVersion, &record.FileSize, &record.Checksum,
		&createdAt, &finishedAt, &record.Error,
	); err != nil {
		return web.BackupRecord{}, fmt.Errorf("read database backup: %w", err)
	}
	record.CreatedAt = time.Unix(createdAt, 0).UTC()
	if finishedAt.Valid {
		value := time.Unix(finishedAt.Int64, 0).UTC()
		record.FinishedAt = &value
	}

	return record, nil
}
