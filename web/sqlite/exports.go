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
			"file_path, preset_id, saved_view_id, relative_path, record_count, file_size, checksum, error, options, created_at, started_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?)",
		record.ID, record.Name, record.Format, record.State, record.SourceType, record.SourceID,
		record.Filters, record.Columns, record.PresetID, record.SavedViewID, record.RelativePath,
		record.RecordCount, record.FileSize, record.Checksum, record.Error, defaultExportOptions(record.Options),
		record.CreatedAt.Unix(), startedAt,
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
			"preset_id = NULLIF(?, ''), saved_view_id = NULLIF(?, ''), filters = ?, columns = ?, options = ?, "+
			"relative_path = ?, record_count = ?, file_size = ?, checksum = ?, error = ?, started_at = ?, finished_at = ? WHERE id = ?",
		record.Name, record.Format, record.State, record.SourceType, record.SourceID,
		record.PresetID, record.SavedViewID, record.Filters, record.Columns, defaultExportOptions(record.Options),
		record.RelativePath, record.RecordCount, record.FileSize, record.Checksum, record.Error,
		startedAt, finishedAt, record.ID,
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
		"SELECT id, name, format, state, source_type, source_id, COALESCE(preset_id, ''), COALESCE(saved_view_id, ''), filters, columns, options, relative_path, "+
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
		"SELECT id, name, format, state, source_type, source_id, COALESCE(preset_id, ''), COALESCE(saved_view_id, ''), filters, columns, options, relative_path, "+
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
		&record.SourceID, &record.PresetID, &record.SavedViewID, &record.Filters, &record.Columns,
		&record.Options, &record.RelativePath,
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

func defaultExportOptions(value string) string {
	if value == "" {
		return "{}"
	}
	return value
}

// SaveExportPreset creates a new preset or updates an existing preset with
// the same case-insensitive name while retaining its stable ID.
func (repo *repo) SaveExportPreset(ctx context.Context, preset web.ExportPreset) (web.ExportPreset, error) {
	now := preset.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	createdAt := preset.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	_, err := repo.db.ExecContext(ctx,
		"INSERT INTO export_presets(id, name, format, columns, filters, sort, options, created_at, updated_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT(name) DO UPDATE SET format = excluded.format, columns = excluded.columns, "+
			"filters = excluded.filters, sort = excluded.sort, options = excluded.options, updated_at = excluded.updated_at",
		preset.ID, preset.Name, preset.Format, preset.Columns, preset.Filters, preset.Sort,
		defaultExportOptions(preset.Options), createdAt.Unix(), now.Unix(),
	)
	if err != nil {
		return web.ExportPreset{}, fmt.Errorf("save export preset: %w", err)
	}
	row := repo.db.QueryRowContext(ctx,
		"SELECT id, name, format, columns, filters, sort, options, created_at, updated_at "+
			"FROM export_presets WHERE name = ? COLLATE NOCASE",
		preset.Name,
	)
	result, err := scanExportPreset(row)
	if err != nil {
		return web.ExportPreset{}, fmt.Errorf("read saved export preset: %w", err)
	}
	return result, nil
}

func (repo *repo) ListExportPresets(ctx context.Context, limit int) ([]web.ExportPreset, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := repo.db.QueryContext(ctx,
		"SELECT id, name, format, columns, filters, sort, options, created_at, updated_at "+
			"FROM export_presets ORDER BY updated_at DESC, id LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list export presets: %w", err)
	}
	defer rows.Close()
	presets := make([]web.ExportPreset, 0)
	for rows.Next() {
		preset, scanErr := scanExportPreset(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		presets = append(presets, preset)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list export presets: %w", err)
	}
	return presets, nil
}

func (repo *repo) GetExportPreset(ctx context.Context, id string) (web.ExportPreset, error) {
	row := repo.db.QueryRowContext(ctx,
		"SELECT id, name, format, columns, filters, sort, options, created_at, updated_at "+
			"FROM export_presets WHERE id = ?",
		id,
	)
	preset, err := scanExportPreset(row)
	if errors.Is(err, sql.ErrNoRows) {
		return web.ExportPreset{}, web.ErrExportNotFound
	}
	return preset, err
}

func (repo *repo) DeleteExportPreset(ctx context.Context, id string) error {
	result, err := repo.db.ExecContext(ctx, "DELETE FROM export_presets WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete export preset: %w", err)
	}
	return requireExportAffected(result)
}

func (repo *repo) ReplaceExportParts(ctx context.Context, exportID string, parts []web.ExportPart) error {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin export part update: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM export_parts WHERE export_id = ?", exportID); err != nil {
		return fmt.Errorf("clear export parts: %w", err)
	}
	for _, part := range parts {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO export_parts(export_id, part_number, relative_path, record_count, file_size, checksum) VALUES (?, ?, ?, ?, ?, ?)",
			exportID, part.PartNumber, part.RelativePath, part.RecordCount, part.FileSize, part.Checksum,
		); err != nil {
			return fmt.Errorf("insert export part %d: %w", part.PartNumber, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit export parts: %w", err)
	}
	return nil
}

func (repo *repo) ListExportParts(ctx context.Context, exportID string) ([]web.ExportPart, error) {
	rows, err := repo.db.QueryContext(ctx,
		"SELECT export_id, part_number, relative_path, record_count, file_size, checksum "+
			"FROM export_parts WHERE export_id = ? ORDER BY part_number",
		exportID,
	)
	if err != nil {
		return nil, fmt.Errorf("list export parts: %w", err)
	}
	defer rows.Close()
	parts := make([]web.ExportPart, 0)
	for rows.Next() {
		var part web.ExportPart
		if err := rows.Scan(&part.ExportID, &part.PartNumber, &part.RelativePath,
			&part.RecordCount, &part.FileSize, &part.Checksum); err != nil {
			return nil, fmt.Errorf("scan export part: %w", err)
		}
		parts = append(parts, part)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list export parts: %w", err)
	}
	return parts, nil
}

func requireExportAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read export delete result: %w", err)
	}
	if affected == 0 {
		return web.ErrExportNotFound
	}
	return nil
}

type exportPresetScanner interface {
	Scan(...any) error
}

func scanExportPreset(scanner exportPresetScanner) (web.ExportPreset, error) {
	var preset web.ExportPreset
	var createdAt, updatedAt int64
	if err := scanner.Scan(&preset.ID, &preset.Name, &preset.Format, &preset.Columns,
		&preset.Filters, &preset.Sort, &preset.Options, &createdAt, &updatedAt); err != nil {
		return web.ExportPreset{}, err
	}
	preset.CreatedAt = time.Unix(createdAt, 0).UTC()
	preset.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return preset, nil
}
