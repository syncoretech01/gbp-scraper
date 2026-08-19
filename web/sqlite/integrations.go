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

func (repo *repo) SaveIntegration(
	ctx context.Context,
	record web.IntegrationRecord,
	secret string,
) error {
	key, err := repo.loadProxyKey()
	if err != nil {
		return fmt.Errorf("load local integration encryption key: %w", err)
	}
	encrypted, err := encryptProxyURL(key, secret)
	if err != nil {
		return fmt.Errorf("encrypt local integration configuration: %w", err)
	}
	now := record.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("start integration update: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx,
		`INSERT INTO integrations(
			id, name, kind, enabled, configuration, secret_configuration,
			last_error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, '', ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			kind = excluded.kind,
			enabled = excluded.enabled,
			configuration = excluded.configuration,
			secret_configuration = excluded.secret_configuration,
			updated_at = excluded.updated_at`,
		record.ID, record.Name, record.Kind, record.Enabled, record.Configuration,
		encrypted, createdAt.Unix(), now.Unix(),
	)
	if err != nil {
		return fmt.Errorf("save integration: %w", err)
	}
	details, _ := json.Marshal(map[string]any{
		"name": record.Name, "kind": record.Kind, "enabled": record.Enabled,
	})
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO audit_logs(action, entity_type, entity_id, details, created_at) VALUES ('integration_saved', 'integration', ?, ?, ?)",
		record.ID, string(details), now.Unix(),
	); err != nil {
		return fmt.Errorf("audit integration update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit integration update: %w", err)
	}
	return nil
}

func (repo *repo) ListIntegrations(
	ctx context.Context,
	enabledOnly bool,
	limit int,
) ([]web.IntegrationRecord, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	query := `SELECT id, name, kind, enabled, configuration, last_run_at,
		last_error, created_at, updated_at FROM integrations`
	if enabledOnly {
		query += " WHERE enabled = 1"
	}
	query += " ORDER BY name COLLATE NOCASE, id LIMIT ?"
	rows, err := repo.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("list integrations: %w", err)
	}
	defer rows.Close()
	records := make([]web.IntegrationRecord, 0)
	for rows.Next() {
		record, scanErr := scanIntegration(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list integrations: %w", err)
	}
	return records, nil
}

func (repo *repo) GetIntegrationSecret(ctx context.Context, id string) (web.IntegrationSecret, error) {
	row := repo.db.QueryRowContext(ctx,
		`SELECT id, name, kind, enabled, configuration, last_run_at,
		last_error, created_at, updated_at, secret_configuration
		FROM integrations WHERE id = ?`,
		id,
	)
	var record web.IntegrationRecord
	var enabled bool
	var lastRun sql.NullInt64
	var createdAt, updatedAt int64
	var encrypted string
	if err := row.Scan(
		&record.ID, &record.Name, &record.Kind, &enabled, &record.Configuration,
		&lastRun, &record.LastError, &createdAt, &updatedAt, &encrypted,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return web.IntegrationSecret{}, web.ErrIntegrationNotFound
		}
		return web.IntegrationSecret{}, fmt.Errorf("read integration: %w", err)
	}
	record.Enabled = enabled
	record.CreatedAt = time.Unix(createdAt, 0).UTC()
	record.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	if lastRun.Valid {
		value := time.Unix(lastRun.Int64, 0).UTC()
		record.LastRunAt = &value
	}
	key, err := repo.loadProxyKey()
	if err != nil {
		return web.IntegrationSecret{}, fmt.Errorf("load local integration encryption key: %w", err)
	}
	secret, err := decryptProxyURL(key, encrypted)
	if err != nil {
		return web.IntegrationSecret{}, fmt.Errorf("decrypt local integration configuration: %w", err)
	}
	return web.IntegrationSecret{Record: record, Secret: secret}, nil
}

func (repo *repo) DeleteIntegration(ctx context.Context, id string) error {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("start integration delete: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "DELETE FROM integrations WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete integration: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read integration delete result: %w", err)
	}
	if affected == 0 {
		return web.ErrIntegrationNotFound
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO audit_logs(action, entity_type, entity_id, details, created_at) VALUES ('integration_deleted', 'integration', ?, '{}', ?)",
		id, time.Now().UTC().Unix(),
	); err != nil {
		return fmt.Errorf("audit integration delete: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit integration delete: %w", err)
	}
	return nil
}

func (repo *repo) RecordIntegrationRun(
	ctx context.Context,
	id string,
	runAt time.Time,
	message string,
) error {
	result, err := repo.db.ExecContext(ctx,
		"UPDATE integrations SET last_run_at = ?, last_error = ?, updated_at = ? WHERE id = ?",
		runAt.Unix(), message, runAt.Unix(), id,
	)
	if err != nil {
		return fmt.Errorf("record integration run: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read integration run result: %w", err)
	}
	if affected == 0 {
		return web.ErrIntegrationNotFound
	}
	return nil
}

type integrationScanner interface {
	Scan(...any) error
}

func scanIntegration(scanner integrationScanner) (web.IntegrationRecord, error) {
	var record web.IntegrationRecord
	var enabled bool
	var lastRun sql.NullInt64
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&record.ID, &record.Name, &record.Kind, &enabled, &record.Configuration,
		&lastRun, &record.LastError, &createdAt, &updatedAt,
	); err != nil {
		return web.IntegrationRecord{}, fmt.Errorf("scan integration: %w", err)
	}
	record.Enabled = enabled
	record.CreatedAt = time.Unix(createdAt, 0).UTC()
	record.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	if lastRun.Valid {
		value := time.Unix(lastRun.Int64, 0).UTC()
		record.LastRunAt = &value
	}
	return record, nil
}
