package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestJobOpenDuplicateCandidatesCountsOnlyThisJobsUndecidedPairs guards the two
// things the monitor's "Unresolved duplicate candidates" figure promises: it
// counts questions nobody has answered, and it counts only the ones this run
// produced. A pair that straddles two runs belongs to neither run's total, or
// every run that touched it would report it and the per-run figures would sum
// past the number of open pairs that exist.
func TestJobOpenDuplicateCandidatesCountsOnlyThisJobsUndecidedPairs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	concrete, ok := repository.(*repo)
	if !ok {
		t.Fatal("repository is not the sqlite implementation")
	}
	t.Cleanup(func() { _ = concrete.db.Close() })

	job := resultImportJob("job-duplicates", time.Now().UTC())
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatal(err)
	}
	other := resultImportJob("job-other", time.Now().UTC())
	if err := repository.Create(ctx, &other); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Unix()
	for _, id := range []string{"b1", "b2", "b3", "b4", "outside"} {
		if _, err := concrete.db.Exec(`INSERT INTO businesses(
			id, canonical_key, name, normalized_name,
			first_seen_at, last_seen_at, last_changed_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, "place:"+id, id, id, now, now, now, now, now,
		); err != nil {
			t.Fatal(err)
		}
	}
	link := func(jobID, businessID string) {
		if _, err := concrete.db.Exec(`INSERT INTO job_businesses(
			job_id, business_id, first_seen_at, last_seen_at, occurrence_count
		) VALUES (?, ?, ?, ?, 1)`, jobID, businessID, now, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"b1", "b2", "b3", "b4"} {
		link(job.ID, id)
	}
	link(other.ID, "outside")

	candidate := func(left, right, state string) {
		if _, err := concrete.db.Exec(`INSERT INTO duplicate_candidates(
			left_business_id, right_business_id, score, signals, state, created_at
		) VALUES (?, ?, 0.9, '[]', ?, ?)`, left, right, state, now); err != nil {
			t.Fatal(err)
		}
	}
	candidate("b1", "b2", "pending")
	candidate("b3", "b4", "pending")
	// Already decided, so it is no longer a question.
	candidate("b2", "b3", "merged")
	// Straddles two runs: a workspace-level question, not this run's.
	candidate("b1", "outside", "pending")

	count, err := concrete.JobOpenDuplicateCandidates(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobOpenDuplicateCandidates() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("open candidates = %d, want 2", count)
	}

	empty, err := concrete.JobOpenDuplicateCandidates(ctx, "job-with-nothing")
	if err != nil {
		t.Fatalf("JobOpenDuplicateCandidates() error = %v", err)
	}
	if empty != 0 {
		t.Fatalf("open candidates for an unknown job = %d, want 0", empty)
	}
}
