package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

const maximumStoredAPIRequestLogs = 10_000

func (repo *repo) CreateAPIKey(ctx context.Context, record web.APIKeyRecord, hash string) error {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("start API-key creation: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx,
		`INSERT INTO api_keys(id, name, key_hash, permission, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		record.ID, record.Name, hash, record.Permission, record.Enabled, record.CreatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("create API key: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO audit_logs(action, entity_type, entity_id, details, created_at)
		VALUES ('api_key_created', 'api_key', ?, json_object('name', ?, 'permission', ?), ?)`,
		record.ID, record.Name, record.Permission, time.Now().UTC().Unix(),
	); err != nil {
		return fmt.Errorf("audit API-key creation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit API-key creation: %w", err)
	}
	return nil
}

func (repo *repo) ListAPIKeys(ctx context.Context, limit int) ([]web.APIKeyRecord, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := repo.db.QueryContext(ctx,
		`SELECT id, name, permission, enabled, last_used_at, created_at
		FROM api_keys ORDER BY created_at DESC, id LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list API keys: %w", err)
	}
	defer rows.Close()
	records := make([]web.APIKeyRecord, 0)
	for rows.Next() {
		record, scanErr := scanAPIKey(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list API keys: %w", err)
	}
	return records, nil
}

func (repo *repo) EnabledAPIKeyCount(ctx context.Context) (int, error) {
	var count int
	if err := repo.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM api_keys WHERE enabled = 1").Scan(&count); err != nil {
		return 0, fmt.Errorf("count enabled API keys: %w", err)
	}
	return count, nil
}

func (repo *repo) AuthenticateAPIKey(
	ctx context.Context,
	hash string,
	usedAt time.Time,
) (web.APIKeyRecord, error) {
	row := repo.db.QueryRowContext(ctx,
		`SELECT id, name, permission, enabled, last_used_at, created_at
		FROM api_keys WHERE key_hash = ? AND enabled = 1`,
		hash,
	)
	record, err := scanAPIKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return web.APIKeyRecord{}, web.ErrAPIKeyNotFound
	}
	if err != nil {
		return web.APIKeyRecord{}, err
	}
	if _, err := repo.db.ExecContext(ctx, "UPDATE api_keys SET last_used_at = ? WHERE id = ?", usedAt.Unix(), record.ID); err != nil {
		return web.APIKeyRecord{}, fmt.Errorf("record API-key usage: %w", err)
	}
	record.LastUsedAt = &usedAt
	return record, nil
}

func (repo *repo) SetAPIKeyEnabled(ctx context.Context, id string, enabled bool) error {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("start API-key update: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE api_keys SET enabled = ? WHERE id = ?", enabled, id)
	if err != nil {
		return fmt.Errorf("update API key: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read API-key update result: %w", err)
	}
	if affected == 0 {
		return web.ErrAPIKeyNotFound
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO audit_logs(action, entity_type, entity_id, details, created_at)
		VALUES ('api_key_toggled', 'api_key', ?, json_object('enabled', ?), ?)`,
		id, enabled, time.Now().UTC().Unix(),
	); err != nil {
		return fmt.Errorf("audit API-key update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit API-key update: %w", err)
	}
	return nil
}

func (repo *repo) RecordAPIRequest(ctx context.Context, record web.APIRequestLog) error {
	result, err := repo.db.ExecContext(ctx,
		`INSERT INTO api_request_logs(method, path, status_code, duration_ms, api_key_id, created_at)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), ?)`,
		record.Method, record.Path, record.StatusCode, record.DurationMS, record.APIKeyID, record.CreatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("record API request: %w", err)
	}
	id, err := result.LastInsertId()
	if err == nil && id%100 == 0 {
		_, _ = repo.db.ExecContext(ctx,
			`DELETE FROM api_request_logs WHERE id <= COALESCE(
				(SELECT id FROM api_request_logs ORDER BY id DESC LIMIT 1 OFFSET ?), 0
			)`,
			maximumStoredAPIRequestLogs,
		)
	}
	return nil
}

func (repo *repo) ListAPIRequestLogs(ctx context.Context, limit int) ([]web.APIRequestLog, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := repo.db.QueryContext(ctx,
		`SELECT id, method, path, status_code, duration_ms, COALESCE(api_key_id, ''), created_at
		FROM api_request_logs ORDER BY id DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list API request logs: %w", err)
	}
	defer rows.Close()
	logs := make([]web.APIRequestLog, 0)
	for rows.Next() {
		var record web.APIRequestLog
		var createdAt int64
		if err := rows.Scan(
			&record.ID, &record.Method, &record.Path, &record.StatusCode,
			&record.DurationMS, &record.APIKeyID, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan API request log: %w", err)
		}
		record.CreatedAt = time.Unix(createdAt, 0).UTC()
		logs = append(logs, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list API request logs: %w", err)
	}
	return logs, nil
}

type apiKeyScanner interface {
	Scan(...any) error
}

func scanAPIKey(scanner apiKeyScanner) (web.APIKeyRecord, error) {
	var record web.APIKeyRecord
	var enabled bool
	var lastUsed sql.NullInt64
	var createdAt int64
	if err := scanner.Scan(
		&record.ID, &record.Name, &record.Permission, &enabled, &lastUsed, &createdAt,
	); err != nil {
		return web.APIKeyRecord{}, err
	}
	record.Enabled = enabled
	record.CreatedAt = time.Unix(createdAt, 0).UTC()
	if lastUsed.Valid {
		value := time.Unix(lastUsed.Int64, 0).UTC()
		record.LastUsedAt = &value
	}
	return record, nil
}
