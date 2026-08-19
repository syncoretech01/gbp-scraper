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

// maximumKeywordSetRows bounds a listing regardless of the requested limit so
// a runaway caller can never page the whole table into memory.
const maximumKeywordSetRows = 200

// ListKeywordSets returns saved sets, newest first.
func (repo *repo) ListKeywordSets(ctx context.Context, limit int) ([]web.KeywordSet, error) {
	if limit <= 0 || limit > maximumKeywordSetRows {
		limit = maximumKeywordSetRows
	}

	rows, err := repo.db.QueryContext(ctx,
		`SELECT id, name, description, keywords, use_count, last_used_at, created_at, updated_at
		FROM keyword_sets ORDER BY created_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list keyword sets: %w", err)
	}

	defer func() { _ = rows.Close() }()

	sets := make([]web.KeywordSet, 0)

	for rows.Next() {
		set, scanErr := scanKeywordSet(rows)
		if scanErr != nil {
			return nil, scanErr
		}

		sets = append(sets, set)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read keyword sets: %w", err)
	}

	return sets, nil
}

// SaveKeywordSet stores one set. The name is unique (case-insensitively), so
// saving an existing name updates that set's description and keywords while
// keeping its identity, creation time, and usage history.
func (repo *repo) SaveKeywordSet(ctx context.Context, set web.KeywordSet) (web.KeywordSet, error) {
	keywords, err := json.Marshal(set.Keywords)
	if err != nil {
		return web.KeywordSet{}, fmt.Errorf("encode keyword set: %w", err)
	}

	now := set.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}

	createdAt := set.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}

	if _, err := repo.db.ExecContext(ctx,
		`INSERT INTO keyword_sets(id, name, description, keywords, use_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, 0, ?, ?)
		ON CONFLICT(name) DO UPDATE SET description = excluded.description,
			keywords = excluded.keywords, updated_at = excluded.updated_at`,
		set.ID, set.Name, set.Description, string(keywords), createdAt.Unix(), now.Unix(),
	); err != nil {
		return web.KeywordSet{}, fmt.Errorf("save keyword set: %w", err)
	}

	// The row that survives an upsert-by-name keeps its original id, so the
	// stored record is read back rather than assumed.
	return repo.keywordSetByName(ctx, set.Name)
}

// DeleteKeywordSet removes one saved set.
func (repo *repo) DeleteKeywordSet(ctx context.Context, id string) error {
	result, err := repo.db.ExecContext(ctx, `DELETE FROM keyword_sets WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete keyword set: %w", err)
	}

	if affected, affectedErr := result.RowsAffected(); affectedErr == nil && affected == 0 {
		return fmt.Errorf("%w: %s", web.ErrKeywordSetNotFound, id)
	}

	return nil
}

// TouchKeywordSetUse increments the usage counter, stamps the last use, and
// returns the stored set so the caller can insert its keywords.
func (repo *repo) TouchKeywordSetUse(ctx context.Context, id string, usedAt time.Time) (web.KeywordSet, error) {
	result, err := repo.db.ExecContext(ctx,
		`UPDATE keyword_sets SET use_count = use_count + 1, last_used_at = ?, updated_at = ? WHERE id = ?`,
		usedAt.Unix(), usedAt.Unix(), id,
	)
	if err != nil {
		return web.KeywordSet{}, fmt.Errorf("record keyword set use: %w", err)
	}

	if affected, affectedErr := result.RowsAffected(); affectedErr == nil && affected == 0 {
		return web.KeywordSet{}, fmt.Errorf("%w: %s", web.ErrKeywordSetNotFound, id)
	}

	return repo.keywordSetByID(ctx, id)
}

func (repo *repo) keywordSetByID(ctx context.Context, id string) (web.KeywordSet, error) {
	set, err := scanKeywordSet(repo.db.QueryRowContext(ctx,
		`SELECT id, name, description, keywords, use_count, last_used_at, created_at, updated_at
		FROM keyword_sets WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return web.KeywordSet{}, fmt.Errorf("%w: %s", web.ErrKeywordSetNotFound, id)
	}

	return set, err
}

func (repo *repo) keywordSetByName(ctx context.Context, name string) (web.KeywordSet, error) {
	set, err := scanKeywordSet(repo.db.QueryRowContext(ctx,
		`SELECT id, name, description, keywords, use_count, last_used_at, created_at, updated_at
		FROM keyword_sets WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return web.KeywordSet{}, fmt.Errorf("%w: %s", web.ErrKeywordSetNotFound, name)
	}

	return set, err
}

type keywordSetScanner interface {
	Scan(destination ...any) error
}

func scanKeywordSet(row keywordSetScanner) (web.KeywordSet, error) {
	var (
		set        web.KeywordSet
		keywords   string
		lastUsedAt sql.NullInt64
		createdAt  int64
		updatedAt  int64
	)

	if err := row.Scan(
		&set.ID, &set.Name, &set.Description, &keywords,
		&set.UseCount, &lastUsedAt, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return web.KeywordSet{}, err
		}

		return web.KeywordSet{}, fmt.Errorf("scan keyword set: %w", err)
	}

	if err := json.Unmarshal([]byte(keywords), &set.Keywords); err != nil {
		return web.KeywordSet{}, fmt.Errorf("decode keyword set %s: %w", set.ID, err)
	}

	if lastUsedAt.Valid {
		used := time.Unix(lastUsedAt.Int64, 0).UTC()
		set.LastUsedAt = &used
	}

	set.CreatedAt = time.Unix(createdAt, 0).UTC()
	set.UpdatedAt = time.Unix(updatedAt, 0).UTC()

	return set, nil
}
