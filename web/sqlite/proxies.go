package sqlite

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

const proxyKeyFilename = ".proxy-master-key"

func (repo *repo) ListProxyPools(ctx context.Context) ([]web.ProxyPoolRecord, error) {
	rows, err := repo.db.QueryContext(ctx,
		"SELECT proxy_pools.id, proxy_pools.name, proxy_pools.strategy, "+
			"COUNT(proxies.id), COALESCE(SUM(CASE WHEN proxies.enabled = 1 THEN 1 ELSE 0 END), 0), "+
			"COALESCE(SUM(CASE WHEN proxies.enabled = 1 AND proxies.status IN ('healthy','slow','unknown') THEN 1 ELSE 0 END), 0), "+
			"proxy_pools.created_at, proxy_pools.updated_at "+
			"FROM proxy_pools LEFT JOIN proxy_pool_members ON proxy_pool_members.pool_id = proxy_pools.id "+
			"LEFT JOIN proxies ON proxies.id = proxy_pool_members.proxy_id "+
			"GROUP BY proxy_pools.id ORDER BY proxy_pools.name",
	)
	if err != nil {
		return nil, fmt.Errorf("list proxy pools: %w", err)
	}
	defer rows.Close()
	pools := make([]web.ProxyPoolRecord, 0)
	for rows.Next() {
		var pool web.ProxyPoolRecord
		var createdAt, updatedAt int64
		if err := rows.Scan(
			&pool.ID, &pool.Name, &pool.Strategy, &pool.TotalCount, &pool.EnabledCount,
			&pool.HealthyCount, &createdAt, &updatedAt,
		); err != nil {
			return nil, err
		}
		pool.CreatedAt = time.Unix(createdAt, 0).UTC()
		pool.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		pools = append(pools, pool)
	}
	return pools, rows.Err()
}

func (repo *repo) ListProxies(ctx context.Context, poolID string) ([]web.ProxyRecord, error) {
	statement := "SELECT proxies.id, proxy_pools.id, proxy_pools.name, proxies.url_masked, proxies.protocol, " +
		"proxies.enabled, proxies.status, proxies.latency_ms, proxies.success_count, proxies.failure_count, " +
		"proxies.block_count, proxies.usage_count, proxies.last_success_at, proxies.last_failure_at, " +
		"proxies.cooldown_until, proxies.created_at, proxies.updated_at " +
		"FROM proxies JOIN proxy_pool_members ON proxy_pool_members.proxy_id = proxies.id " +
		"JOIN proxy_pools ON proxy_pools.id = proxy_pool_members.pool_id"
	args := []any{}
	if poolID != "" {
		statement += " WHERE proxy_pools.id = ?"
		args = append(args, poolID)
	}
	statement += " ORDER BY proxy_pools.name, proxies.url_masked, proxies.id"
	rows, err := repo.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("list proxies: %w", err)
	}
	defer rows.Close()
	proxies := make([]web.ProxyRecord, 0)
	for rows.Next() {
		proxy, scanErr := scanProxy(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		proxies = append(proxies, proxy)
	}
	return proxies, rows.Err()
}

func (repo *repo) ImportProxyPool(
	ctx context.Context,
	name string,
	strategy string,
	values []string,
) (web.ProxyPoolRecord, int, error) {
	if strategy != "round_robin" && strategy != "random" && strategy != "fastest" && strategy != "lowest_failure" {
		return web.ProxyPoolRecord{}, 0, fmt.Errorf("unsupported proxy strategy")
	}
	key, err := repo.loadProxyKey()
	if err != nil {
		return web.ProxyPoolRecord{}, 0, err
	}
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{})
	for _, raw := range values {
		value, normalizeErr := normalizeProxyURL(raw)
		if normalizeErr != nil {
			return web.ProxyPoolRecord{}, 0, normalizeErr
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return web.ProxyPoolRecord{}, 0, fmt.Errorf("at least one proxy URL is required")
	}

	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return web.ProxyPoolRecord{}, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	poolID := uuid.NewString()
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO proxy_pools(id, name, strategy, settings, created_at, updated_at) VALUES (?, ?, ?, '{}', ?, ?) "+
			"ON CONFLICT(name) DO UPDATE SET strategy = excluded.strategy, updated_at = excluded.updated_at",
		poolID, name, strategy, now.Unix(), now.Unix(),
	); err != nil {
		return web.ProxyPoolRecord{}, 0, fmt.Errorf("save proxy pool: %w", err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT id FROM proxy_pools WHERE name = ? COLLATE NOCASE", name).Scan(&poolID); err != nil {
		return web.ProxyPoolRecord{}, 0, err
	}
	existing, err := poolSecrets(ctx, tx, poolID, key)
	if err != nil {
		return web.ProxyPoolRecord{}, 0, err
	}
	imported := 0
	for _, value := range normalized {
		if _, exists := existing[value]; exists {
			continue
		}
		encrypted, err := encryptProxyURL(key, value)
		if err != nil {
			return web.ProxyPoolRecord{}, 0, err
		}
		parsed, _ := url.Parse(value)
		id := uuid.NewString()
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO proxies(id, url_encrypted, url_masked, protocol, status, created_at, updated_at) "+
				"VALUES (?, ?, ?, ?, 'unknown', ?, ?)",
			id, encrypted, maskProxyURL(parsed), parsed.Scheme, now.Unix(), now.Unix(),
		); err != nil {
			return web.ProxyPoolRecord{}, 0, fmt.Errorf("save proxy: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO proxy_pool_members(pool_id, proxy_id) VALUES (?, ?)",
			poolID, id,
		); err != nil {
			return web.ProxyPoolRecord{}, 0, fmt.Errorf("add proxy to pool: %w", err)
		}
		imported++
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO audit_logs(action, entity_type, entity_id, details, created_at) "+
			"VALUES ('proxy_import', 'proxy_pool', ?, ?, ?)",
		poolID, fmt.Sprintf("{\"imported\":%d}", imported), now.Unix(),
	); err != nil {
		return web.ProxyPoolRecord{}, 0, err
	}
	if err := tx.Commit(); err != nil {
		return web.ProxyPoolRecord{}, 0, err
	}
	pools, err := repo.ListProxyPools(ctx)
	if err != nil {
		return web.ProxyPoolRecord{}, imported, err
	}
	for _, pool := range pools {
		if pool.ID == poolID {
			return pool, imported, nil
		}
	}
	return web.ProxyPoolRecord{}, imported, web.ErrProxyNotFound
}

func (repo *repo) ResolveProxyPool(ctx context.Context, id string) ([]string, error) {
	key, err := repo.loadProxyKey()
	if err != nil {
		return nil, err
	}
	var strategy string
	if err := repo.db.QueryRowContext(ctx, "SELECT strategy FROM proxy_pools WHERE id = ?", id).Scan(&strategy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, web.ErrProxyNotFound
		}
		return nil, err
	}
	const queryPrefix = "SELECT proxies.id, proxies.url_encrypted FROM proxies " +
		"JOIN proxy_pool_members ON proxy_pool_members.proxy_id = proxies.id " +
		"WHERE proxy_pool_members.pool_id = ? AND proxies.enabled = 1 " +
		"AND (proxies.cooldown_until IS NULL OR proxies.cooldown_until <= ?) " +
		"AND proxies.status NOT IN ('blocked','authentication-failed','offline') ORDER BY "

	query := queryPrefix + "proxies.id"
	switch strategy {
	case "random":
		query = queryPrefix + "random()"
	case "fastest":
		query = queryPrefix + "CASE WHEN proxies.latency_ms IS NULL THEN 1 ELSE 0 END, proxies.latency_ms, proxies.id"
	case "lowest_failure":
		query = queryPrefix + "proxies.failure_count, proxies.block_count, proxies.id"
	}
	rows, err := repo.db.QueryContext(ctx, query, id, time.Now().UTC().Unix())
	if err != nil {
		return nil, fmt.Errorf("resolve proxy pool: %w", err)
	}
	defer rows.Close()
	values := make([]string, 0)
	ids := make([]string, 0)
	for rows.Next() {
		var proxyID, encrypted string
		if err := rows.Scan(&proxyID, &encrypted); err != nil {
			return nil, err
		}
		value, err := decryptProxyURL(key, encrypted)
		if err != nil {
			return nil, err
		}
		ids = append(ids, proxyID)
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, proxyID := range ids {
		_, _ = repo.db.ExecContext(ctx, "UPDATE proxies SET usage_count = usage_count + 1 WHERE id = ?", proxyID)
	}
	return values, nil
}

func (repo *repo) GetProxySecret(ctx context.Context, id string) (string, error) {
	key, err := repo.loadProxyKey()
	if err != nil {
		return "", err
	}
	var encrypted string
	if err := repo.db.QueryRowContext(ctx, "SELECT url_encrypted FROM proxies WHERE id = ?", id).Scan(&encrypted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", web.ErrProxyNotFound
		}
		return "", err
	}
	return decryptProxyURL(key, encrypted)
}

func (repo *repo) RecordProxyTest(ctx context.Context, id string, result web.ProxyTestResult) error {
	result.Error = jobruntime.RedactString(result.Error)
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var latency any
	if result.LatencyMS != nil {
		latency = *result.LatencyMS
	}
	success := result.Status == "healthy" || result.Status == "slow"
	blocked := result.Status == "blocked" || result.Status == "rate-limited"
	var cooldown any
	if result.Status == "rate-limited" {
		cooldown = result.CheckedAt.Add(15 * time.Minute).Unix()
	}
	update := "UPDATE proxies SET status = ?, latency_ms = ?, exit_ip = ?, country = ?, " +
		"success_count = success_count + ?, failure_count = failure_count + ?, block_count = block_count + ?, " +
		"last_success_at = CASE WHEN ? THEN ? ELSE last_success_at END, " +
		"last_failure_at = CASE WHEN ? THEN last_failure_at ELSE ? END, cooldown_until = ?, updated_at = ? WHERE id = ?"
	resultSQL, err := tx.ExecContext(ctx, update,
		result.Status, latency, result.ExitIP, result.Country, boolInt(success), boolInt(!success), boolInt(blocked),
		success, result.CheckedAt.Unix(), success, result.CheckedAt.Unix(), cooldown, result.CheckedAt.Unix(), id,
	)
	if err := requireProxyResult(resultSQL, err); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO proxy_health(proxy_id, status, latency_ms, exit_ip, country, error, checked_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		id, result.Status, latency, result.ExitIP, result.Country, result.Error, result.CheckedAt.Unix(),
	); err != nil {
		return err
	}
	if result.Status == "authentication-failed" || result.Status == "offline" {
		if _, err := tx.ExecContext(ctx,
			"UPDATE proxies SET enabled = CASE WHEN failure_count >= 3 THEN 0 ELSE enabled END WHERE id = ?",
			id,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (repo *repo) SetProxyEnabled(ctx context.Context, id string, enabled bool) error {
	result, err := repo.db.ExecContext(ctx,
		"UPDATE proxies SET enabled = ?, status = CASE WHEN ? THEN 'unknown' ELSE status END, "+
			"cooldown_until = CASE WHEN ? THEN NULL ELSE cooldown_until END, updated_at = ? WHERE id = ?",
		enabled, enabled, enabled, time.Now().UTC().Unix(), id,
	)
	return requireProxyResult(result, err)
}

func (repo *repo) DeleteProxy(ctx context.Context, id string) error {
	return requireProxyResult(repo.db.ExecContext(ctx, "DELETE FROM proxies WHERE id = ?", id))
}

func (repo *repo) DeleteProxyPool(ctx context.Context, id string) error {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM proxies WHERE id IN (SELECT proxy_id FROM proxy_pool_members WHERE pool_id = ?)",
		id,
	); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM proxy_pools WHERE id = ?", id)
	if err := requireProxyResult(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

func (repo *repo) loadProxyKey() ([]byte, error) {
	path := filepath.Join(filepath.Dir(repo.path), proxyKeyFilename)
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != 32 {
			return nil, fmt.Errorf("proxy master key has an invalid length")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read proxy master key: %w", err)
	}
	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("create proxy master key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return repo.loadProxyKey()
	}
	if err != nil {
		return nil, fmt.Errorf("create proxy master key file: %w", err)
	}
	if _, err := file.Write(key); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write proxy master key: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("sync proxy master key: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return key, nil
}

func normalizeProxyURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("invalid proxy URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "socks5" {
		return "", fmt.Errorf("proxy scheme must be http, https, or socks5")
	}
	host := parsed.Hostname()
	if host == "" || net.ParseIP(host) == nil && strings.ContainsAny(host, " /\\") {
		return "", fmt.Errorf("invalid proxy host")
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("proxy URLs cannot contain paths, queries, or fragments")
	}
	parsed.Path = ""
	return parsed.String(), nil
}

func maskProxyURL(value *url.URL) string {
	copy := *value
	if value.User != nil {
		username := value.User.Username()
		if username != "" {
			copy.User = url.UserPassword(username, "REDACTED")
		} else {
			copy.User = url.User("REDACTED")
		}
	}
	return copy.String()
}

func encryptProxyURL(key []byte, value string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(value), []byte("gmaps-proxy-v1"))
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

func decryptProxyURL(key []byte, value string) (string, error) {
	encoded, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return "", fmt.Errorf("decode encrypted proxy: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(encoded) < gcm.NonceSize() {
		return "", fmt.Errorf("encrypted proxy is truncated")
	}
	plaintext, err := gcm.Open(nil, encoded[:gcm.NonceSize()], encoded[gcm.NonceSize():], []byte("gmaps-proxy-v1"))
	if err != nil {
		return "", fmt.Errorf("decrypt proxy: %w", err)
	}
	return string(plaintext), nil
}

func poolSecrets(ctx context.Context, tx *sql.Tx, poolID string, key []byte) (map[string]struct{}, error) {
	rows, err := tx.QueryContext(ctx,
		"SELECT proxies.url_encrypted FROM proxies JOIN proxy_pool_members ON proxy_pool_members.proxy_id = proxies.id "+
			"WHERE proxy_pool_members.pool_id = ?",
		poolID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make(map[string]struct{})
	for rows.Next() {
		var encrypted string
		if err := rows.Scan(&encrypted); err != nil {
			return nil, err
		}
		value, err := decryptProxyURL(key, encrypted)
		if err != nil {
			return nil, err
		}
		values[value] = struct{}{}
	}
	return values, rows.Err()
}

type proxyScanner interface {
	Scan(...any) error
}

func scanProxy(scanner proxyScanner) (web.ProxyRecord, error) {
	var proxy web.ProxyRecord
	var enabled int
	var latency, lastSuccess, lastFailure, cooldown sql.NullInt64
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&proxy.ID, &proxy.PoolID, &proxy.PoolName, &proxy.MaskedURL, &proxy.Protocol,
		&enabled, &proxy.Status, &latency, &proxy.SuccessCount, &proxy.FailureCount,
		&proxy.BlockCount, &proxy.UsageCount, &lastSuccess, &lastFailure, &cooldown,
		&createdAt, &updatedAt,
	); err != nil {
		return web.ProxyRecord{}, err
	}
	proxy.Enabled = enabled != 0
	proxy.LatencyMS = nullIntPointer(latency)
	proxy.LastSuccessAt = nullTimePointer(lastSuccess)
	proxy.LastFailureAt = nullTimePointer(lastFailure)
	proxy.CooldownUntil = nullTimePointer(cooldown)
	proxy.CreatedAt = time.Unix(createdAt, 0).UTC()
	proxy.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return proxy, nil
}

func nullTimePointer(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	result := time.Unix(value.Int64, 0).UTC()
	return &result
}

func requireProxyResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return web.ErrProxyNotFound
	}
	return nil
}
