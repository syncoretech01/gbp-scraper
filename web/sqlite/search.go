package sqlite

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/gosom/google-maps-scraper/web"
)

// GlobalSearch returns a bounded, deterministic mixture of local workspace
// entities. Business text uses FTS5; smaller metadata tables use bound LIKE
// queries with indexed recency ordering where available.
func (repo *repo) GlobalSearch(ctx context.Context, query string, limit int) ([]web.GlobalSearchItem, error) {
	items := make([]web.GlobalSearchItem, 0, limit)
	appendRows := func(entityType, statement string, arguments ...any) error {
		if len(items) >= limit {
			return nil
		}
		rows, err := repo.db.QueryContext(ctx, statement, arguments...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() && len(items) < limit {
			var title, subtitle, target string
			if err := rows.Scan(&title, &subtitle, &target); err != nil {
				return err
			}
			items = append(items, web.GlobalSearchItem{Type: entityType, Title: title, Subtitle: subtitle, URL: target})
		}

		return rows.Err()
	}

	if err := appendRows("Business", `
		SELECT b.name,
			trim(CASE WHEN b.primary_category <> '' THEN b.primary_category ELSE b.city END),
			'/app/results/' || b.id
		FROM businesses b
		WHERE b.deleted_at IS NULL AND b.merged_into_id IS NULL
			AND b.id IN (SELECT business_id FROM businesses_fts WHERE businesses_fts MATCH ?)
		ORDER BY b.updated_at DESC, b.id
		LIMIT ?`, ftsMatchQuery(query), limit); err != nil {
		return nil, fmt.Errorf("search businesses: %w", err)
	}

	like := "%" + escapeLike(strings.ToLower(query)) + "%"
	remaining := func() int { return max(0, limit-len(items)) }
	if err := appendRows("Job", `
		SELECT name, status, '/app/jobs/' || id
		FROM jobs WHERE lower(name) LIKE ? ESCAPE '\'
		ORDER BY updated_at DESC, id LIMIT ?`, like, remaining()); err != nil {
		return nil, fmt.Errorf("search jobs: %w", err)
	}
	if err := appendRows("Tag", `
		SELECT name, 'Result tag', ? || name
		FROM tags WHERE lower(name) LIKE ? ESCAPE '\'
		ORDER BY name COLLATE NOCASE LIMIT ?`,
		"/app/results?filter_field=tag&filter_operator=eq&filter_value=", like, remaining()); err != nil {
		return nil, fmt.Errorf("search tags: %w", err)
	}
	if err := appendRows("Template", `
		SELECT name, description, '/app/scrapes/new?template=' || id
		FROM templates WHERE lower(name) LIKE ? ESCAPE '\' OR lower(description) LIKE ? ESCAPE '\'
		ORDER BY pinned DESC, updated_at DESC, id LIMIT ?`, like, like, remaining()); err != nil {
		return nil, fmt.Errorf("search templates: %w", err)
	}
	if err := appendRows("Saved view", `
		SELECT name, 'Saved Results view', '/app/saved-searches?tab=views'
		FROM saved_views WHERE lower(name) LIKE ? ESCAPE '\'
		ORDER BY updated_at DESC, id LIMIT ?`, like, remaining()); err != nil {
		return nil, fmt.Errorf("search saved views: %w", err)
	}
	if err := appendRows("Export", `
		SELECT name, upper(format) || ' · ' || state, '/app/exports'
		FROM exports WHERE lower(name) LIKE ? ESCAPE '\'
		ORDER BY created_at DESC, id LIMIT ?`, like, remaining()); err != nil {
		return nil, fmt.Errorf("search exports: %w", err)
	}

	for index := range items {
		if items[index].Type == "Tag" {
			prefix := "/app/results?filter_field=tag&filter_operator=eq&filter_value="
			items[index].URL = prefix + url.QueryEscape(strings.TrimPrefix(items[index].URL, prefix))
		}
	}

	return items, nil
}

var _ interface {
	GlobalSearch(context.Context, string, int) ([]web.GlobalSearchItem, error)
} = (*repo)(nil)
