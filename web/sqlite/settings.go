package sqlite

import (
	"context"
	"fmt"
	"sort"
	"time"
)

func (repo *repo) LoadSettings(ctx context.Context) (map[string]string, error) {
	rows, err := repo.db.QueryContext(ctx, "SELECT key, value FROM settings ORDER BY key")
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	defer rows.Close()

	values := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("read setting: %w", err)
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}

	return values, nil
}

func (repo *repo) SaveSettings(ctx context.Context, values map[string]string) error {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("start settings update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Unix()
	for _, key := range keys {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO settings(key, value, secret, updated_at, value_type, version, updated_by) "+
				"VALUES (?, ?, 0, ?, 'string', 1, 'local-ui') "+
				"ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, "+
				"version = settings.version + 1, updated_by = excluded.updated_by",
			key, values[key], now,
		); err != nil {
			return fmt.Errorf("save setting %s: %w", key, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO audit_logs(action, entity_type, details, created_at) "+
			"VALUES ('settings_updated', 'settings', ?, ?)",
		fmt.Sprintf("{\"count\":%d}", len(values)), now,
	); err != nil {
		return fmt.Errorf("audit settings update: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit settings update: %w", err)
	}
	return nil
}
