package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/gosom/google-maps-scraper/web"
)

// JobCellObservations pairs every business this job kept with the grid cell
// whose search first returned it.
//
// The join is deliberately anchored on job_businesses.first_source_id rather
// than on business_sources.job_id. A business found by several searches has
// several source rows, and joining on the job would multiply that business
// once per source — the same fan-out that inflates other per-job aggregates.
// first_source_id names exactly one row, so this query returns exactly one row
// per business and its counts can be compared with the business count without
// further explanation.
//
// business_sources.input_id carries the task key the importer recorded, and
// job_tasks.source_cell carries the cell that key was searched at, so the
// pairing is stored evidence rather than a nearest-cell guess. Rows whose task
// carried no cell (an ungridded job) and rows with no position are left out;
// the caller reports an unavailable measurement rather than a partial one.
func (repo *repo) JobCellObservations(
	ctx context.Context,
	jobID string,
) ([]web.JobCellObservation, error) {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT job_tasks.source_cell, businesses.latitude, businesses.longitude
		FROM job_businesses
		JOIN business_sources ON business_sources.id = job_businesses.first_source_id
		JOIN job_tasks
			ON job_tasks.job_id = business_sources.job_id
			AND job_tasks.task_key = business_sources.input_id
		JOIN businesses ON businesses.id = job_businesses.business_id
		WHERE job_businesses.job_id = ?
			AND job_tasks.source_cell <> ''
			AND businesses.latitude IS NOT NULL
			AND businesses.longitude IS NOT NULL
			AND businesses.deleted_at IS NULL`,
		jobID,
	)
	if err != nil {
		return nil, fmt.Errorf("read job cell observations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	observations := make([]web.JobCellObservation, 0, 128)
	for rows.Next() {
		var (
			observation web.JobCellObservation
			latitude    sql.NullFloat64
			longitude   sql.NullFloat64
		)
		if err := rows.Scan(&observation.Cell, &latitude, &longitude); err != nil {
			return nil, fmt.Errorf("scan job cell observation: %w", err)
		}
		if !latitude.Valid || !longitude.Valid {
			continue
		}
		observation.Latitude = latitude.Float64
		observation.Longitude = longitude.Float64
		observations = append(observations, observation)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read job cell observations: %w", err)
	}

	return observations, nil
}
