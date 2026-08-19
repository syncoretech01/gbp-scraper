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
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// Job organisation is metadata only. Renaming, archiving and annotating a job
// never touch its durable lifecycle state, its task plan, or its result file,
// so none of it can disturb a run in progress.

// RenameJob changes a job's display name.
func (repo *repo) RenameJob(ctx context.Context, jobID, name string) error {
	return repo.updateJobOrganisation(ctx, jobID, "job_renamed", func(ctx context.Context, tx *sql.Tx, now int64) error {
		result, err := tx.ExecContext(
			ctx,
			`UPDATE jobs SET name = ?, updated_at = ? WHERE id = ?`,
			name,
			now,
			jobID,
		)
		if err != nil {
			return fmt.Errorf("rename job: %w", err)
		}

		return requireCASUpdate(result)
	}, map[string]any{"name": name})
}

// SetJobArchived hides a finished job from the default queue view or restores it.
func (repo *repo) SetJobArchived(ctx context.Context, jobID string, archived bool) error {
	action := "job_archived"
	if !archived {
		action = "job_restored"
	}

	return repo.updateJobOrganisation(ctx, jobID, action, func(ctx context.Context, tx *sql.Tx, now int64) error {
		var state string

		err := tx.QueryRowContext(ctx, `SELECT state FROM job_runtime WHERE job_id = ?`, jobID).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", web.ErrLifecycleNotFound, jobID)
		}

		if err != nil {
			return fmt.Errorf("read job state: %w", err)
		}

		// Archiving an active job would hide work the operator still needs to
		// watch, so it is only offered once the job has stopped.
		if archived && !jobruntime.State(state).Terminal() {
			return fmt.Errorf("%w: a %s job cannot be archived", web.ErrInvalidJobOrganisation, state)
		}

		flag := 0
		if archived {
			flag = 1
		}

		result, err := tx.ExecContext(
			ctx,
			`UPDATE job_runtime SET archived = ?, updated_at = ? WHERE job_id = ? AND archived <> ?`,
			flag,
			now,
			jobID,
			flag,
		)
		if err != nil {
			return fmt.Errorf("archive job: %w", err)
		}

		// Setting the same value twice is not an error; it just changes nothing.
		if affected, affectedErr := result.RowsAffected(); affectedErr == nil && affected == 0 {
			return errNoJobOrganisationChange
		}

		return nil
	}, map[string]any{"archived": archived})
}

// SetJobNotes stores an operator note against a job.
func (repo *repo) SetJobNotes(ctx context.Context, jobID, notes string) error {
	return repo.updateJobOrganisation(ctx, jobID, "job_notes_updated", func(ctx context.Context, tx *sql.Tx, now int64) error {
		result, err := tx.ExecContext(
			ctx,
			`UPDATE job_runtime SET notes = ?, updated_at = ? WHERE job_id = ?`,
			notes,
			now,
			jobID,
		)
		if err != nil {
			return fmt.Errorf("update job notes: %w", err)
		}

		return requireCASUpdate(result)
	}, map[string]any{"length": len(notes)})
}

var errNoJobOrganisationChange = errors.New("no job organisation change")

func (repo *repo) updateJobOrganisation(
	ctx context.Context,
	jobID, action string,
	apply func(context.Context, *sql.Tx, int64) error,
	detail map[string]any,
) error {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin job organisation update: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM jobs WHERE id = ?`, jobID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", web.ErrNotFound, jobID)
		}

		return fmt.Errorf("read job: %w", err)
	}

	now := time.Now().UTC().Unix()

	if err := apply(ctx, tx, now); err != nil {
		if errors.Is(err, errNoJobOrganisationChange) {
			return tx.Commit()
		}

		return err
	}

	details, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode job organisation audit: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO audit_logs(action, entity_type, entity_id, details, created_at)
		VALUES (?, 'job', ?, ?, ?)`,
		action,
		jobID,
		string(details),
		now,
	); err != nil {
		return fmt.Errorf("audit job organisation update: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit job organisation update: %w", err)
	}

	return nil
}

// JobOrganisation reads the metadata that is not part of lifecycle state.
func (repo *repo) JobOrganisation(ctx context.Context, jobID string) (web.JobOrganisation, error) {
	var (
		organisation web.JobOrganisation
		archived     int
		folder       sql.NullString
		notes        sql.NullString
	)

	err := repo.db.QueryRowContext(
		ctx,
		`SELECT archived, folder, notes FROM job_runtime WHERE job_id = ?`,
		jobID,
	).Scan(&archived, &folder, &notes)

	if errors.Is(err, sql.ErrNoRows) {
		return web.JobOrganisation{}, fmt.Errorf("%w: %s", web.ErrLifecycleNotFound, jobID)
	}

	if err != nil {
		return web.JobOrganisation{}, fmt.Errorf("read job organisation: %w", err)
	}

	organisation.JobID = jobID
	organisation.Archived = archived != 0
	organisation.Folder = strings.TrimSpace(folder.String)
	organisation.Notes = notes.String

	return organisation, nil
}

// ArchivedJobIDs lists the jobs currently hidden from the default queue view.
func (repo *repo) ArchivedJobIDs(ctx context.Context) (map[string]struct{}, error) {
	rows, err := repo.db.QueryContext(ctx, `SELECT job_id FROM job_runtime WHERE archived = 1`)
	if err != nil {
		return nil, fmt.Errorf("list archived jobs: %w", err)
	}

	defer func() { _ = rows.Close() }()

	archived := make(map[string]struct{})

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan archived job: %w", err)
		}

		archived[id] = struct{}{}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read archived jobs: %w", err)
	}

	return archived, nil
}
