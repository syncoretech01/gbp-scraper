package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

func TestRenameJobKeepsLifecycleStateUntouched(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	job := lifecycleTestJob("job-rename", time.Now().UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	before, err := repository.GetRuntime(ctx, job.ID)
	if err != nil {
		t.Fatalf("read runtime before rename: %v", err)
	}

	if err := repository.RenameJob(ctx, job.ID, "San Francisco dentists, second pass"); err != nil {
		t.Fatalf("rename job: %v", err)
	}

	renamed, err := repository.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("read renamed job: %v", err)
	}

	if renamed.Name != "San Francisco dentists, second pass" {
		t.Fatalf("name = %q", renamed.Name)
	}

	// Renaming is metadata only: the durable state and its version must not move.
	after, err := repository.GetRuntime(ctx, job.ID)
	if err != nil {
		t.Fatalf("read runtime after rename: %v", err)
	}

	if after.State != before.State || after.StateVersion != before.StateVersion {
		t.Fatalf("rename disturbed lifecycle: before %s/%d after %s/%d",
			before.State, before.StateVersion, after.State, after.StateVersion)
	}

	var audits int
	if err := repository.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_logs WHERE action = 'job_renamed' AND entity_id = ?`, job.ID,
	).Scan(&audits); err != nil {
		t.Fatalf("count rename audits: %v", err)
	}

	if audits != 1 {
		t.Fatalf("rename audits = %d, want 1", audits)
	}
}

func TestArchiveRefusesActiveJobsAndRoundTripsFinishedOnes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	job := lifecycleTestJob("job-archive", time.Now().UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	// A queued job is not finished, so archiving it would hide live work.
	if err := repository.SetJobArchived(ctx, job.ID, true); !errors.Is(err, web.ErrInvalidJobOrganisation) {
		t.Fatalf("archiving a queued job returned %v, want a refusal", err)
	}

	finished := lifecycleTestJob("job-archive-done", time.Now().UTC())
	if err := repository.CreateWithState(ctx, &finished, jobruntime.StateCompleted); err != nil {
		t.Fatalf("create finished job: %v", err)
	}

	if err := repository.SetJobArchived(ctx, finished.ID, true); err != nil {
		t.Fatalf("archive finished job: %v", err)
	}

	archived, err := repository.ArchivedJobIDs(ctx)
	if err != nil {
		t.Fatalf("list archived: %v", err)
	}

	if _, ok := archived[finished.ID]; !ok {
		t.Fatalf("archived set = %v, want %s", archived, finished.ID)
	}

	if _, ok := archived[job.ID]; ok {
		t.Fatalf("queued job %s was archived", job.ID)
	}

	// Archiving twice is a no-op rather than an error.
	if err := repository.SetJobArchived(ctx, finished.ID, true); err != nil {
		t.Fatalf("repeat archive: %v", err)
	}

	if err := repository.SetJobArchived(ctx, finished.ID, false); err != nil {
		t.Fatalf("restore job: %v", err)
	}

	restored, err := repository.ArchivedJobIDs(ctx)
	if err != nil {
		t.Fatalf("list archived after restore: %v", err)
	}

	if _, ok := restored[finished.ID]; ok {
		t.Fatalf("job %s is still archived after restore", finished.ID)
	}

	organisation, err := repository.JobOrganisation(ctx, finished.ID)
	if err != nil {
		t.Fatalf("read organisation: %v", err)
	}

	if organisation.Archived {
		t.Fatalf("organisation = %#v, want not archived", organisation)
	}
}

func TestJobNotesPersistAndAreAudited(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	job := lifecycleTestJob("job-notes", time.Now().UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := repository.SetJobNotes(ctx, job.ID, "Rerun after the proxy pool is topped up."); err != nil {
		t.Fatalf("set notes: %v", err)
	}

	organisation, err := repository.JobOrganisation(ctx, job.ID)
	if err != nil {
		t.Fatalf("read organisation: %v", err)
	}

	if organisation.Notes != "Rerun after the proxy pool is topped up." {
		t.Fatalf("notes = %q", organisation.Notes)
	}

	var audits int
	if err := repository.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_logs WHERE action = 'job_notes_updated' AND entity_id = ?`, job.ID,
	).Scan(&audits); err != nil {
		t.Fatalf("count note audits: %v", err)
	}

	if audits != 1 {
		t.Fatalf("note audits = %d, want 1", audits)
	}
}

func TestJobOrganisationRejectsUnknownJobs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	if err := repository.RenameJob(ctx, "missing-job", "Anything"); !errors.Is(err, web.ErrNotFound) {
		t.Fatalf("rename unknown job = %v, want not found", err)
	}

	if _, err := repository.JobOrganisation(ctx, "missing-job"); !errors.Is(err, web.ErrLifecycleNotFound) {
		t.Fatalf("read unknown organisation = %v, want lifecycle not found", err)
	}
}
