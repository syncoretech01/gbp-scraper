package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

var _ web.MapRepository = (*repo)(nil)

// MapCellActivity aggregates durable worker and normalized source evidence by
// source cell. Empty/failed coverage therefore survives browser refreshes and
// local service restarts.
func (repo *repo) MapCellActivity(ctx context.Context, jobID string) ([]web.MapCellActivity, error) {
	rows, err := repo.db.QueryContext(
		ctx,
		`WITH task_counts AS (
			SELECT source_cell,
				COUNT(*) AS task_count,
				SUM(CASE WHEN state = 'pending' THEN 1 ELSE 0 END) AS pending_tasks,
				SUM(CASE WHEN state = 'running' THEN 1 ELSE 0 END) AS running_tasks,
				SUM(CASE WHEN state = 'completed' THEN 1 ELSE 0 END) AS completed_tasks,
				SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END) AS failed_tasks,
				SUM(CASE WHEN state = 'failed' AND lower(last_error) LIKE '%block%' THEN 1 ELSE 0 END) AS blocked_tasks,
				SUM(CASE WHEN state = 'skipped' THEN 1 ELSE 0 END) AS skipped_tasks,
				SUM(CASE WHEN last_error <> '' THEN 1 ELSE 0 END) AS warning_count
			FROM job_tasks
			WHERE job_id = ? AND source_cell <> ''
			GROUP BY source_cell
		), source_counts AS (
			SELECT source_cell,
				COUNT(DISTINCT business_id) AS result_count,
				COUNT(*) AS raw_result_count
			FROM business_sources
			WHERE job_id = ? AND source_cell <> ''
			GROUP BY source_cell
		), cells AS (
			SELECT source_cell FROM task_counts
			UNION SELECT source_cell FROM source_counts
		)
		SELECT cells.source_cell,
			COALESCE(task_counts.task_count, 0), COALESCE(task_counts.pending_tasks, 0),
			COALESCE(task_counts.running_tasks, 0), COALESCE(task_counts.completed_tasks, 0),
			COALESCE(task_counts.failed_tasks, 0), COALESCE(task_counts.blocked_tasks, 0),
			COALESCE(task_counts.skipped_tasks, 0), COALESCE(task_counts.warning_count, 0),
			COALESCE(source_counts.result_count, 0), COALESCE(source_counts.raw_result_count, 0)
		FROM cells
		LEFT JOIN task_counts USING(source_cell)
		LEFT JOIN source_counts USING(source_cell)
		ORDER BY cells.source_cell`,
		jobID,
		jobID,
	)
	if err != nil {
		return nil, fmt.Errorf("read map cell coverage: %w", err)
	}
	defer rows.Close()

	activities := make([]web.MapCellActivity, 0)
	for rows.Next() {
		var activity web.MapCellActivity
		if err := rows.Scan(
			&activity.SourceCell,
			&activity.TaskCount,
			&activity.PendingTasks,
			&activity.RunningTasks,
			&activity.CompletedTasks,
			&activity.FailedTasks,
			&activity.BlockedTasks,
			&activity.SkippedTasks,
			&activity.WarningCount,
			&activity.ResultCount,
			&activity.RawResultCount,
		); err != nil {
			return nil, fmt.Errorf("scan map cell coverage: %w", err)
		}
		activities = append(activities, activity)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate map cell coverage: %w", err)
	}

	return activities, nil
}

// ListSavedAreas returns a bounded, deterministic list of local map features.
func (repo *repo) ListSavedAreas(ctx context.Context, limit int) ([]web.SavedArea, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := repo.db.QueryContext(ctx,
		`SELECT id, name, geojson, created_at, updated_at
		FROM saved_areas ORDER BY updated_at DESC, id LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list saved map areas: %w", err)
	}
	defer rows.Close()

	areas := make([]web.SavedArea, 0)
	for rows.Next() {
		area, scanErr := scanSavedArea(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		areas = append(areas, area)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list saved map areas: %w", err)
	}

	return areas, nil
}

// GetSavedArea returns one canonical local map feature.
func (repo *repo) GetSavedArea(ctx context.Context, id string) (web.SavedArea, error) {
	area, err := scanSavedArea(repo.db.QueryRowContext(ctx,
		`SELECT id, name, geojson, created_at, updated_at FROM saved_areas WHERE id = ?`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return web.SavedArea{}, web.ErrSavedAreaNotFound
	}
	if err != nil {
		return web.SavedArea{}, fmt.Errorf("get saved map area: %w", err)
	}

	return area, nil
}

// CreateSavedArea inserts a validated map feature without overwriting an
// existing identifier.
func (repo *repo) CreateSavedArea(ctx context.Context, area web.SavedArea) error {
	now := time.Now().UTC()
	if area.CreatedAt.IsZero() {
		area.CreatedAt = now
	}
	if area.UpdatedAt.IsZero() {
		area.UpdatedAt = now
	}
	normalized, _, err := web.NormalizeSavedArea(area)
	if err != nil {
		return err
	}
	result, err := repo.db.ExecContext(ctx,
		`INSERT INTO saved_areas(id, name, geojson, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		normalized.ID,
		normalized.Name,
		string(normalized.GeoJSON),
		normalized.CreatedAt.Unix(),
		normalized.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("create saved map area: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect saved map area insert: %w", err)
	}
	if affected == 0 {
		return web.ErrSavedAreaConflict
	}

	return nil
}

// UpdateSavedArea replaces the name and geometry for an existing identifier.
func (repo *repo) UpdateSavedArea(ctx context.Context, area web.SavedArea) error {
	if area.UpdatedAt.IsZero() {
		area.UpdatedAt = time.Now().UTC()
	}
	normalized, _, err := web.NormalizeSavedArea(area)
	if err != nil {
		return err
	}
	result, err := repo.db.ExecContext(ctx,
		`UPDATE saved_areas SET name = ?, geojson = ?, updated_at = ? WHERE id = ?`,
		normalized.Name,
		string(normalized.GeoJSON),
		normalized.UpdatedAt.Unix(),
		normalized.ID,
	)
	if err != nil {
		return fmt.Errorf("update saved map area: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect saved map area update: %w", err)
	}
	if affected == 0 {
		return web.ErrSavedAreaNotFound
	}

	return nil
}

// DeleteSavedArea removes only the saved geographic definition.
func (repo *repo) DeleteSavedArea(ctx context.Context, id string) error {
	result, err := repo.db.ExecContext(ctx, `DELETE FROM saved_areas WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete saved map area: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect saved map area delete: %w", err)
	}
	if affected == 0 {
		return web.ErrSavedAreaNotFound
	}

	return nil
}

// SearchBusinessesInArea applies the existing normalized ResultSearch first,
// then performs exact in-process geometry containment over its stable pages.
// This keeps all filter and source-row behavior in one query implementation.
func (repo *repo) SearchBusinessesInArea(
	ctx context.Context,
	search web.ResultSearch,
	geometry web.MapGeometry,
) (web.ResultPage, error) {
	if !geometry.Valid() {
		return web.ResultPage{}, fmt.Errorf("%w: empty geometry", web.ErrInvalidMapGeometry)
	}
	requestedLimit := search.Limit
	if requestedLimit <= 0 {
		requestedLimit = 25
	}
	requestedLimit = min(requestedLimit, 250)
	requestedOffset := max(search.Offset, 0)

	spatialResults := make([]web.BusinessResult, 0, requestedLimit)
	var spatialTotal int64
	_, err := repo.visitBusinessesInArea(ctx, search, geometry, 1_000_000, func(business web.BusinessResult) error {
		if spatialTotal >= int64(requestedOffset) && len(spatialResults) < requestedLimit {
			spatialResults = append(spatialResults, business)
		}
		spatialTotal++
		return nil
	})
	if err != nil {
		return web.ResultPage{}, err
	}

	return web.ResultPage{
		Results: spatialResults,
		Total:   spatialTotal,
		Limit:   requestedLimit,
		Offset:  requestedOffset,
	}, nil
}

// VisitBusinessesInArea visits all bounded spatial matches without repeatedly
// applying the page offset to an already-filtered set.
func (repo *repo) VisitBusinessesInArea(
	ctx context.Context,
	search web.ResultSearch,
	geometry web.MapGeometry,
	maximumRows int,
	visit func(web.BusinessResult) error,
) (int64, error) {
	return repo.visitBusinessesInArea(ctx, search, geometry, maximumRows, visit)
}

func (repo *repo) visitBusinessesInArea(
	ctx context.Context,
	search web.ResultSearch,
	geometry web.MapGeometry,
	maximumRows int,
	visit func(web.BusinessResult) error,
) (int64, error) {
	baseSearch := search
	baseSearch.Limit = 250
	baseSearch.Offset = 0
	var spatialTotal int64
	for {
		if err := ctx.Err(); err != nil {
			return spatialTotal, err
		}
		page, err := repo.SearchBusinesses(ctx, baseSearch)
		if err != nil {
			return spatialTotal, err
		}
		if page.Total > 1_000_000 {
			return spatialTotal, fmt.Errorf("%w: filter matches %d normalized rows", web.ErrMapSpatialQueryTooLarge, page.Total)
		}
		for _, business := range page.Results {
			if business.Latitude == nil || business.Longitude == nil ||
				!geometry.ContainsPoint(*business.Latitude, *business.Longitude) {
				continue
			}
			if spatialTotal >= int64(maximumRows) {
				return spatialTotal, fmt.Errorf("%w: area export matches more than %d rows", web.ErrMapSpatialQueryTooLarge, maximumRows)
			}
			if err := visit(business); err != nil {
				return spatialTotal, err
			}
			spatialTotal++
		}
		if len(page.Results) == 0 || baseSearch.Offset+len(page.Results) >= int(page.Total) {
			break
		}
		baseSearch.Offset += len(page.Results)
	}

	return spatialTotal, nil
}

type savedAreaScanner interface {
	Scan(...any) error
}

func scanSavedArea(scanner savedAreaScanner) (web.SavedArea, error) {
	var area web.SavedArea
	var geoJSON string
	var createdAt, updatedAt int64
	if err := scanner.Scan(&area.ID, &area.Name, &geoJSON, &createdAt, &updatedAt); err != nil {
		return web.SavedArea{}, err
	}
	area.GeoJSON = []byte(geoJSON)
	area.CreatedAt = time.Unix(createdAt, 0).UTC()
	area.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	normalized, _, err := web.NormalizeSavedArea(area)
	if err != nil {
		return web.SavedArea{}, fmt.Errorf("saved map area %s contains invalid GeoJSON: %w", area.ID, err)
	}

	return normalized, nil
}
