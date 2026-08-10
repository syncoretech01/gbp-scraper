package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

func (repo *repo) CreateExport(ctx context.Context, record web.ExportRecord) error {
	var startedAt any
	if record.StartedAt != nil {
		startedAt = record.StartedAt.Unix()
	}
	_, err := repo.db.ExecContext(ctx,
		"INSERT INTO exports(id, name, format, state, source_type, source_id, filters, columns, "+
			"file_path, relative_path, record_count, file_size, checksum, error, options, created_at, started_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, '{}', ?, ?)",
		record.ID, record.Name, record.Format, record.State, record.SourceType, record.SourceID,
		record.Filters, record.Columns, record.RelativePath, record.RecordCount, record.FileSize,
		record.Checksum, record.Error, record.CreatedAt.Unix(), startedAt,
	)
	if err != nil {
		return fmt.Errorf("create export record: %w", err)
	}
	return nil
}

func (repo *repo) UpdateExport(ctx context.Context, record web.ExportRecord) error {
	var startedAt, finishedAt any
	if record.StartedAt != nil {
		startedAt = record.StartedAt.Unix()
	}
	if record.FinishedAt != nil {
		finishedAt = record.FinishedAt.Unix()
	}
	result, err := repo.db.ExecContext(ctx,
		"UPDATE exports SET name = ?, format = ?, state = ?, source_type = ?, source_id = ?, "+
			"filters = ?, columns = ?, relative_path = ?, record_count = ?, file_size = ?, checksum = ?, "+
			"error = ?, started_at = ?, finished_at = ? WHERE id = ?",
		record.Name, record.Format, record.State, record.SourceType, record.SourceID,
		record.Filters, record.Columns, record.RelativePath, record.RecordCount, record.FileSize,
		record.Checksum, record.Error, startedAt, finishedAt, record.ID,
	)
	if err != nil {
		return fmt.Errorf("update export record: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read export update result: %w", err)
	}
	if affected == 0 {
		return web.ErrExportNotFound
	}
	return nil
}

func (repo *repo) ListExports(ctx context.Context, limit int) ([]web.ExportRecord, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := repo.db.QueryContext(ctx,
		"SELECT id, name, format, state, source_type, source_id, filters, columns, relative_path, "+
			"record_count, file_size, checksum, error, created_at, started_at, finished_at "+
			"FROM exports ORDER BY created_at DESC, id DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list exports: %w", err)
	}
	defer rows.Close()

	records := make([]web.ExportRecord, 0)
	for rows.Next() {
		record, scanErr := scanExport(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list exports: %w", err)
	}
	return records, nil
}

func (repo *repo) GetExport(ctx context.Context, id string) (web.ExportRecord, error) {
	row := repo.db.QueryRowContext(ctx,
		"SELECT id, name, format, state, source_type, source_id, filters, columns, relative_path, "+
			"record_count, file_size, checksum, error, created_at, started_at, finished_at "+
			"FROM exports WHERE id = ?",
		id,
	)
	record, err := scanExport(row)
	if errors.Is(err, sql.ErrNoRows) {
		return web.ExportRecord{}, web.ErrExportNotFound
	}
	return record, err
}

func (repo *repo) DeleteExport(ctx context.Context, id string) error {
	result, err := repo.db.ExecContext(ctx, "DELETE FROM exports WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete export: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read export delete result: %w", err)
	}
	if affected == 0 {
		return web.ErrExportNotFound
	}
	return nil
}

type exportScanner interface {
	Scan(...any) error
}

func scanExport(scanner exportScanner) (web.ExportRecord, error) {
	var record web.ExportRecord
	var createdAt int64
	var startedAt, finishedAt sql.NullInt64
	if err := scanner.Scan(
		&record.ID, &record.Name, &record.Format, &record.State, &record.SourceType,
		&record.SourceID, &record.Filters, &record.Columns, &record.RelativePath,
		&record.RecordCount, &record.FileSize, &record.Checksum, &record.Error,
		&createdAt, &startedAt, &finishedAt,
	); err != nil {
		return web.ExportRecord{}, err
	}
	record.CreatedAt = time.Unix(createdAt, 0).UTC()
	if startedAt.Valid {
		value := time.Unix(startedAt.Int64, 0).UTC()
		record.StartedAt = &value
	}
	if finishedAt.Valid {
		value := time.Unix(finishedAt.Int64, 0).UTC()
		record.FinishedAt = &value
	}
	return record, nil
}
