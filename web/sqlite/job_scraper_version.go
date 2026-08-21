package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/gosom/google-maps-scraper/web"
)

var _ interface {
	RecordJobScraperVersion(context.Context, string, string) error
	JobScraperVersion(context.Context, string) (string, error)
} = (*repo)(nil)

// maximumScraperVersionLength bounds a stored build identity so a malformed
// link-time override cannot grow the runtime row without limit.
const maximumScraperVersionLength = 64

// RecordJobScraperVersion stamps the build identity of the binary that is
// running a job onto its durable runtime row.
//
// The write is idempotent and first-writer-wins: an already recorded version
// is never overwritten, so restarting a job under a newer build keeps the
// version that actually produced the committed rows. An empty version is
// ignored rather than blanking a recorded one.
func (repo *repo) RecordJobScraperVersion(ctx context.Context, jobID, version string) error {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil
	}

	if len(version) > maximumScraperVersionLength {
		version = version[:maximumScraperVersionLength]
	}

	if _, err := repo.db.ExecContext(
		ctx,
		`UPDATE job_runtime SET scraper_version = ?
		WHERE job_id = ? AND scraper_version = ''`,
		version,
		jobID,
	); err != nil {
		return fmt.Errorf("record job scraper version: %w", err)
	}

	return nil
}

// JobScraperVersion returns the recorded build identity for a job, or an empty
// string when the run predates version stamping.
func (repo *repo) JobScraperVersion(ctx context.Context, jobID string) (string, error) {
	var version string
	if err := repo.db.QueryRowContext(
		ctx,
		`SELECT scraper_version FROM job_runtime WHERE job_id = ?`,
		jobID,
	).Scan(&version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("%w: %s", web.ErrLifecycleNotFound, jobID)
		}

		return "", fmt.Errorf("read job scraper version: %w", err)
	}

	return strings.TrimSpace(version), nil
}
