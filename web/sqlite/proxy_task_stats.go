package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

var _ interface {
	UpsertProxyTaskStat(context.Context, web.ProxyTaskStatInput) error
	ProxyTaskHealthByURL(context.Context, string) (map[string]web.ProxyTaskHealth, error)
} = (*repo)(nil)

// UpsertProxyTaskStat folds one finished task into the proxy's aggregate
// history. A success resets the consecutive-failure streak; a failure
// extends it and records the redacted error.
func (repo *repo) UpsertProxyTaskStat(ctx context.Context, input web.ProxyTaskStatInput) error {
	now := time.Now().UTC().Unix()

	var (
		successes, failures  int64
		successAt, failureAt any
		lastError            string
		durationSeconds      = input.DurationSeconds
	)

	if durationSeconds < 0 {
		durationSeconds = 0
	}

	if input.Success {
		successes = 1
		successAt = now
	} else {
		failures = 1
		failureAt = now
		lastError = jobruntime.RedactString(input.LastError)
	}

	_, err := repo.db.ExecContext(
		ctx,
		`INSERT INTO proxy_task_stats(
			proxy_id, pool_id, task_successes, task_failures, consecutive_failures,
			total_task_seconds, last_success_at, last_failure_at, last_error, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(proxy_id) DO UPDATE SET
			pool_id = excluded.pool_id,
			task_successes = proxy_task_stats.task_successes + excluded.task_successes,
			task_failures = proxy_task_stats.task_failures + excluded.task_failures,
			consecutive_failures = CASE
				WHEN excluded.task_successes > 0 THEN 0
				ELSE proxy_task_stats.consecutive_failures + excluded.task_failures
			END,
			total_task_seconds = proxy_task_stats.total_task_seconds + excluded.total_task_seconds,
			last_success_at = COALESCE(excluded.last_success_at, proxy_task_stats.last_success_at),
			last_failure_at = COALESCE(excluded.last_failure_at, proxy_task_stats.last_failure_at),
			last_error = CASE
				WHEN excluded.task_failures > 0 THEN excluded.last_error
				ELSE proxy_task_stats.last_error
			END,
			updated_at = excluded.updated_at`,
		input.ProxyID,
		input.PoolID,
		successes,
		failures,
		failures,
		durationSeconds,
		successAt,
		failureAt,
		lastError,
		now,
	)
	if err != nil {
		return fmt.Errorf("upsert proxy task stats: %w", err)
	}

	return nil
}

// ProxyTaskHealthByURL returns the aggregate task history of every proxy of
// the pool, keyed by decrypted proxy URL. Proxies without any recorded task
// yet appear with zero counters, so callers always get an identity mapping
// for the whole pool.
func (repo *repo) ProxyTaskHealthByURL(ctx context.Context, poolID string) (map[string]web.ProxyTaskHealth, error) {
	key, err := repo.loadProxyKey()
	if err != nil {
		return nil, err
	}

	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT proxies.id, proxies.url_encrypted,
			COALESCE(proxy_task_stats.consecutive_failures, 0),
			COALESCE(proxy_task_stats.task_successes, 0),
			COALESCE(proxy_task_stats.task_failures, 0)
		FROM proxies
		JOIN proxy_pool_members ON proxy_pool_members.proxy_id = proxies.id
		LEFT JOIN proxy_task_stats ON proxy_task_stats.proxy_id = proxies.id
		WHERE proxy_pool_members.pool_id = ?`,
		poolID,
	)
	if err != nil {
		return nil, fmt.Errorf("read proxy task health: %w", err)
	}

	defer func() { _ = rows.Close() }()

	health := make(map[string]web.ProxyTaskHealth)

	for rows.Next() {
		var (
			entry     web.ProxyTaskHealth
			encrypted string
		)

		if err := rows.Scan(
			&entry.ProxyID,
			&encrypted,
			&entry.ConsecutiveFailures,
			&entry.Successes,
			&entry.Failures,
		); err != nil {
			return nil, fmt.Errorf("scan proxy task health: %w", err)
		}

		url, decryptErr := decryptProxyURL(key, encrypted)
		if decryptErr != nil {
			return nil, decryptErr
		}

		health[url] = entry
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate proxy task health: %w", err)
	}

	return health, nil
}
