package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

var _ interface {
	SaveJobCampaignLink(context.Context, web.JobCampaignLink) error
	GetJobCampaignLink(context.Context, string) (web.JobCampaignLink, error)
	CampaignLinks(context.Context, string) ([]web.JobCampaignLink, error)
	FindCampaignIdempotencyKey(context.Context, string, string) (web.JobCampaignLink, error)
} = (*repo)(nil)

const campaignLinkColumns = `job_id, campaign_id, root_job_id, source_job_id, mode,
	generation, idempotency_key, created_at`

// SaveJobCampaignLink records or refreshes one job's place in a rescan
// campaign. The job row must already exist, so lineage can never point at a
// run that was not created.
//
// Re-saving the same job is idempotent: the row is replaced in place, which
// is what lets a repeated rescan request settle on one lineage rather than
// accumulating duplicates.
func (repo *repo) SaveJobCampaignLink(ctx context.Context, link web.JobCampaignLink) error {
	createdAt := link.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	_, err := repo.db.ExecContext(
		ctx,
		`INSERT INTO job_campaigns(`+campaignLinkColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(job_id) DO UPDATE SET
			campaign_id = excluded.campaign_id,
			root_job_id = excluded.root_job_id,
			source_job_id = excluded.source_job_id,
			mode = excluded.mode,
			generation = excluded.generation,
			idempotency_key = excluded.idempotency_key`,
		link.JobID,
		link.CampaignID,
		link.RootJobID,
		link.SourceJobID,
		link.Mode,
		link.Generation,
		link.IdempotencyKey,
		createdAt.UTC().Unix(),
	)
	if err != nil {
		return fmt.Errorf("save campaign lineage for job %q: %w", link.JobID, err)
	}

	return nil
}

// GetJobCampaignLink reads one job's lineage. A job that was never part of a
// campaign reports web.ErrCampaignNotFound.
func (repo *repo) GetJobCampaignLink(ctx context.Context, jobID string) (web.JobCampaignLink, error) {
	row := repo.db.QueryRowContext(
		ctx,
		`SELECT `+campaignLinkColumns+` FROM job_campaigns WHERE job_id = ?`,
		jobID,
	)

	link, err := scanCampaignLink(row)
	if errors.Is(err, sql.ErrNoRows) {
		return web.JobCampaignLink{}, fmt.Errorf("%w: %s", web.ErrCampaignNotFound, jobID)
	}

	return link, err
}

// CampaignLinks reads every generation of one campaign, oldest first.
func (repo *repo) CampaignLinks(ctx context.Context, campaignID string) ([]web.JobCampaignLink, error) {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT `+campaignLinkColumns+` FROM job_campaigns
		WHERE campaign_id = ?
		ORDER BY generation, created_at, job_id`,
		campaignID,
	)
	if err != nil {
		return nil, fmt.Errorf("list campaign lineage: %w", err)
	}

	defer func() { _ = rows.Close() }()

	links := make([]web.JobCampaignLink, 0)

	for rows.Next() {
		link, scanErr := scanCampaignLink(rows)
		if scanErr != nil {
			return nil, scanErr
		}

		links = append(links, link)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate campaign lineage: %w", err)
	}

	return links, nil
}

// FindCampaignIdempotencyKey resolves a client-supplied rescan key to the
// run an earlier request already created, or web.ErrCampaignNotFound when
// this is the first request carrying it.
func (repo *repo) FindCampaignIdempotencyKey(
	ctx context.Context,
	campaignID, key string,
) (web.JobCampaignLink, error) {
	if key == "" {
		return web.JobCampaignLink{}, fmt.Errorf("%w: empty idempotency key", web.ErrCampaignNotFound)
	}

	row := repo.db.QueryRowContext(
		ctx,
		`SELECT `+campaignLinkColumns+` FROM job_campaigns
		WHERE campaign_id = ? AND idempotency_key = ?`,
		campaignID,
		key,
	)

	link, err := scanCampaignLink(row)
	if errors.Is(err, sql.ErrNoRows) {
		return web.JobCampaignLink{}, fmt.Errorf("%w: %s/%s", web.ErrCampaignNotFound, campaignID, key)
	}

	return link, err
}

type campaignLinkScanner interface {
	Scan(...any) error
}

func scanCampaignLink(scanner campaignLinkScanner) (web.JobCampaignLink, error) {
	var (
		link      web.JobCampaignLink
		createdAt int64
	)

	if err := scanner.Scan(
		&link.JobID,
		&link.CampaignID,
		&link.RootJobID,
		&link.SourceJobID,
		&link.Mode,
		&link.Generation,
		&link.IdempotencyKey,
		&createdAt,
	); err != nil {
		return web.JobCampaignLink{}, fmt.Errorf("scan campaign lineage: %w", err)
	}

	link.CreatedAt = time.Unix(createdAt, 0).UTC()

	return link, nil
}
