package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

var _ interface {
	SetJobLabels(context.Context, string, web.JobLabels) error
	JobLabels(context.Context, string) (web.JobLabels, error)
	AllJobLabels(context.Context) (map[string]web.JobLabels, error)
} = (*repo)(nil)

// SetJobLabels replaces a job's folder, owner, and tag set in one transaction.
// Tags are shared rows, so a tag is created on first use and reused after
// that; the job's own membership is replaced wholesale, which is what makes
// removing a tag possible through the same call that adds one.
func (repo *repo) SetJobLabels(ctx context.Context, jobID string, labels web.JobLabels) error {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin job label transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(
		ctx,
		`UPDATE job_runtime SET folder = ?, owner_label = ?, updated_at = ? WHERE job_id = ?`,
		labels.Folder,
		labels.Owner,
		time.Now().UTC().Unix(),
		jobID,
	)
	if err != nil {
		return fmt.Errorf("update job labels: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect job label update: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("%w: %s", web.ErrLifecycleNotFound, jobID)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM job_tags WHERE job_id = ?`, jobID); err != nil {
		return fmt.Errorf("clear job tags: %w", err)
	}

	now := time.Now().UTC().Unix()
	for _, tag := range labels.Tags {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO tags(name, created_at) VALUES (?, ?)
			ON CONFLICT(name) DO NOTHING`,
			tag,
			now,
		); err != nil {
			return fmt.Errorf("create job tag: %w", err)
		}

		if _, err := tx.ExecContext(
			ctx,
			`INSERT OR IGNORE INTO job_tags(job_id, tag_id)
			SELECT ?, id FROM tags WHERE name = ?`,
			jobID,
			tag,
		); err != nil {
			return fmt.Errorf("attach job tag: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit job label transaction: %w", err)
	}

	return nil
}

// JobLabels reads one job's folder, owner, and tags.
func (repo *repo) JobLabels(ctx context.Context, jobID string) (web.JobLabels, error) {
	labels := web.JobLabels{JobID: jobID, Tags: []string{}}

	var folder, owner sql.NullString
	if err := repo.db.QueryRowContext(
		ctx,
		`SELECT folder, owner_label FROM job_runtime WHERE job_id = ?`,
		jobID,
	).Scan(&folder, &owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return web.JobLabels{}, fmt.Errorf("%w: %s", web.ErrLifecycleNotFound, jobID)
		}

		return web.JobLabels{}, fmt.Errorf("read job labels: %w", err)
	}

	labels.Folder = strings.TrimSpace(folder.String)
	labels.Owner = strings.TrimSpace(owner.String)

	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT tags.name FROM job_tags
		JOIN tags ON tags.id = job_tags.tag_id
		WHERE job_tags.job_id = ?
		ORDER BY tags.name COLLATE NOCASE`,
		jobID,
	)
	if err != nil {
		return web.JobLabels{}, fmt.Errorf("read job tags: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return web.JobLabels{}, fmt.Errorf("scan job tag: %w", err)
		}
		labels.Tags = append(labels.Tags, name)
	}
	if err := rows.Err(); err != nil {
		return web.JobLabels{}, fmt.Errorf("iterate job tags: %w", err)
	}

	return labels, nil
}

// AllJobLabels returns every job's labels in two queries so a list page never
// runs one lookup per row.
func (repo *repo) AllJobLabels(ctx context.Context) (map[string]web.JobLabels, error) {
	labels := map[string]web.JobLabels{}

	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT job_id, folder, owner_label FROM job_runtime
		WHERE folder <> '' OR owner_label <> ''`,
	)
	if err != nil {
		return nil, fmt.Errorf("read job folders: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var jobID, folder, owner string
		if err := rows.Scan(&jobID, &folder, &owner); err != nil {
			return nil, fmt.Errorf("scan job folder: %w", err)
		}
		labels[jobID] = web.JobLabels{
			JobID:  jobID,
			Folder: strings.TrimSpace(folder),
			Owner:  strings.TrimSpace(owner),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate job folders: %w", err)
	}

	tagRows, err := repo.db.QueryContext(
		ctx,
		`SELECT job_tags.job_id, tags.name FROM job_tags
		JOIN tags ON tags.id = job_tags.tag_id
		ORDER BY job_tags.job_id, tags.name COLLATE NOCASE`,
	)
	if err != nil {
		return nil, fmt.Errorf("read all job tags: %w", err)
	}
	defer tagRows.Close()

	for tagRows.Next() {
		var jobID, name string
		if err := tagRows.Scan(&jobID, &name); err != nil {
			return nil, fmt.Errorf("scan job tag row: %w", err)
		}
		entry := labels[jobID]
		entry.JobID = jobID
		entry.Tags = append(entry.Tags, name)
		labels[jobID] = entry
	}
	if err := tagRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate all job tags: %w", err)
	}

	for jobID, entry := range labels {
		sort.SliceStable(entry.Tags, func(left, right int) bool {
			return strings.ToLower(entry.Tags[left]) < strings.ToLower(entry.Tags[right])
		})
		labels[jobID] = entry
	}

	return labels, nil
}
