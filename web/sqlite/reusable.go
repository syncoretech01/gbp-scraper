package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

func (repo *repo) ListScrapeTemplates(ctx context.Context, query string) ([]web.ScrapeTemplate, error) {
	statement := "SELECT id, name, description, configuration, tags, folder, pinned, use_count, last_run_at, created_at, updated_at FROM templates"
	args := []any{}
	if query = strings.TrimSpace(query); query != "" {
		statement += " WHERE name LIKE ? ESCAPE '\\' OR description LIKE ? ESCAPE '\\' OR folder LIKE ? ESCAPE '\\'"
		pattern := "%" + escapeLike(query) + "%"
		args = append(args, pattern, pattern, pattern)
	}
	statement += " ORDER BY pinned DESC, updated_at DESC, id"
	rows, err := repo.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("list scrape templates: %w", err)
	}
	defer rows.Close()
	templates := make([]web.ScrapeTemplate, 0)
	for rows.Next() {
		template, scanErr := scanScrapeTemplate(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		templates = append(templates, template)
	}
	return templates, rows.Err()
}

func (repo *repo) GetScrapeTemplate(ctx context.Context, id string) (web.ScrapeTemplate, error) {
	row := repo.db.QueryRowContext(ctx,
		"SELECT id, name, description, configuration, tags, folder, pinned, use_count, last_run_at, created_at, updated_at FROM templates WHERE id = ?",
		id,
	)
	template, err := scanScrapeTemplate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return web.ScrapeTemplate{}, web.ErrReusableNotFound
	}
	return template, err
}

func (repo *repo) SaveScrapeTemplate(ctx context.Context, template web.ScrapeTemplate) error {
	configuration, err := json.Marshal(template.Configuration)
	if err != nil {
		return fmt.Errorf("encode scrape template: %w", err)
	}
	tags, err := json.Marshal(template.Tags)
	if err != nil {
		return fmt.Errorf("encode template tags: %w", err)
	}
	now := template.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	createdAt := template.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	_, err = repo.db.ExecContext(ctx,
		"INSERT INTO templates(id, name, description, configuration, tags, folder, pinned, use_count, created_at, updated_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT(id) DO UPDATE SET name = excluded.name, description = excluded.description, "+
			"configuration = excluded.configuration, tags = excluded.tags, folder = excluded.folder, "+
			"pinned = excluded.pinned, updated_at = excluded.updated_at",
		template.ID, template.Name, template.Description, string(configuration), string(tags),
		template.Folder, template.Pinned, template.UseCount, createdAt.Unix(), now.Unix(),
	)
	if err != nil {
		return fmt.Errorf("save scrape template: %w", err)
	}
	return nil
}

func (repo *repo) DeleteScrapeTemplate(ctx context.Context, id string) error {
	return requireReusableDelete(repo.db.ExecContext(ctx, "DELETE FROM templates WHERE id = ?", id))
}

func (repo *repo) SetScrapeTemplatePinned(ctx context.Context, id string, pinned bool) error {
	result, err := repo.db.ExecContext(ctx,
		"UPDATE templates SET pinned = ?, updated_at = ? WHERE id = ?",
		pinned, time.Now().UTC().Unix(), id,
	)
	return requireReusableDelete(result, err)
}

func (repo *repo) RecordScrapeTemplateUse(ctx context.Context, id string, usedAt time.Time) error {
	result, err := repo.db.ExecContext(ctx,
		"UPDATE templates SET use_count = use_count + 1, last_run_at = ?, updated_at = ? WHERE id = ?",
		usedAt.Unix(), usedAt.Unix(), id,
	)
	return requireReusableDelete(result, err)
}

func (repo *repo) ListSavedResultViews(ctx context.Context, query string) ([]web.SavedResultView, error) {
	statement := "SELECT id, name, filters, columns, grouping, created_at, updated_at " +
		"FROM saved_views WHERE entity_type = 'businesses'"
	args := []any{}
	if query = strings.TrimSpace(query); query != "" {
		statement += " AND name LIKE ? ESCAPE '\\'"
		args = append(args, "%"+escapeLike(query)+"%")
	}
	statement += " ORDER BY updated_at DESC, id"
	rows, err := repo.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("list saved result views: %w", err)
	}
	defer rows.Close()
	views := make([]web.SavedResultView, 0)
	for rows.Next() {
		view, scanErr := scanSavedResultView(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		views = append(views, view)
	}
	return views, rows.Err()
}

func (repo *repo) GetSavedResultView(ctx context.Context, id string) (web.SavedResultView, error) {
	row := repo.db.QueryRowContext(ctx,
		"SELECT id, name, filters, columns, grouping, created_at, updated_at "+
			"FROM saved_views WHERE id = ? AND entity_type = 'businesses'",
		id,
	)
	view, err := scanSavedResultView(row)
	if errors.Is(err, sql.ErrNoRows) {
		return web.SavedResultView{}, web.ErrReusableNotFound
	}
	return view, err
}

func (repo *repo) SaveResultView(ctx context.Context, view web.SavedResultView) error {
	search, err := json.Marshal(view.Search)
	if err != nil {
		return fmt.Errorf("encode saved result view: %w", err)
	}
	now := view.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	createdAt := view.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	// The table layout the view was saved with lives in the columns and
	// grouping columns the schema already provides, so no migration is needed
	// to reopen a view with its own columns, order, and grouping.
	columns, group := web.NormalizeSavedViewLayout(view.Columns, view.Group)
	encodedColumns, err := json.Marshal(columns)
	if err != nil {
		return fmt.Errorf("encode saved result view columns: %w", err)
	}
	encodedGroup, err := json.Marshal([]string{group})
	if err != nil {
		return fmt.Errorf("encode saved result view grouping: %w", err)
	}

	_, err = repo.db.ExecContext(ctx,
		"INSERT INTO saved_views(id, name, entity_type, filters, columns, sort, grouping, created_at, updated_at) "+
			"VALUES (?, ?, 'businesses', ?, ?, '[]', ?, ?, ?) "+
			"ON CONFLICT(id) DO UPDATE SET name = excluded.name, filters = excluded.filters, "+
			"columns = excluded.columns, grouping = excluded.grouping, updated_at = excluded.updated_at",
		view.ID, view.Name, string(search), string(encodedColumns), string(encodedGroup),
		createdAt.Unix(), now.Unix(),
	)
	if err != nil {
		return fmt.Errorf("save result view: %w", err)
	}
	return nil
}

func (repo *repo) DeleteSavedResultView(ctx context.Context, id string) error {
	return requireReusableDelete(repo.db.ExecContext(ctx, "DELETE FROM saved_views WHERE id = ?", id))
}

type scrapeTemplateScanner interface {
	Scan(...any) error
}

func scanScrapeTemplate(scanner scrapeTemplateScanner) (web.ScrapeTemplate, error) {
	var template web.ScrapeTemplate
	var configuration, tags string
	var pinned int
	var lastRun sql.NullInt64
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&template.ID, &template.Name, &template.Description, &configuration, &tags,
		&template.Folder, &pinned, &template.UseCount, &lastRun, &createdAt, &updatedAt,
	); err != nil {
		return web.ScrapeTemplate{}, err
	}
	if err := json.Unmarshal([]byte(configuration), &template.Configuration); err != nil {
		return web.ScrapeTemplate{}, fmt.Errorf("decode scrape template %s: %w", template.ID, err)
	}
	if err := json.Unmarshal([]byte(tags), &template.Tags); err != nil {
		return web.ScrapeTemplate{}, fmt.Errorf("decode scrape template tags %s: %w", template.ID, err)
	}
	template.Pinned = pinned != 0
	template.CreatedAt = time.Unix(createdAt, 0).UTC()
	template.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	if lastRun.Valid {
		value := time.Unix(lastRun.Int64, 0).UTC()
		template.LastRunAt = &value
	}
	return template, nil
}

type savedResultViewScanner interface {
	Scan(...any) error
}

func scanSavedResultView(scanner savedResultViewScanner) (web.SavedResultView, error) {
	var view web.SavedResultView
	var search, columns, grouping string
	var createdAt, updatedAt int64
	if err := scanner.Scan(&view.ID, &view.Name, &search, &columns, &grouping, &createdAt, &updatedAt); err != nil {
		return web.SavedResultView{}, err
	}
	if err := json.Unmarshal([]byte(search), &view.Search); err != nil {
		return web.SavedResultView{}, fmt.Errorf("decode saved result view %s: %w", view.ID, err)
	}
	var storedColumns, storedGrouping []string
	if err := json.Unmarshal([]byte(columns), &storedColumns); err != nil {
		return web.SavedResultView{}, fmt.Errorf("decode saved result view columns %s: %w", view.ID, err)
	}
	if err := json.Unmarshal([]byte(grouping), &storedGrouping); err != nil {
		return web.SavedResultView{}, fmt.Errorf("decode saved result view grouping %s: %w", view.ID, err)
	}
	group := ""
	if len(storedGrouping) > 0 {
		group = storedGrouping[0]
	}
	view.Columns, view.Group = web.NormalizeSavedViewLayout(storedColumns, group)
	view.CreatedAt = time.Unix(createdAt, 0).UTC()
	view.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return view, nil
}

func requireReusableDelete(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return web.ErrReusableNotFound
	}
	return nil
}
