package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

func TestJobListingKeysArePersistedIdempotentlyAndReadBack(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "listing-keys")
	defer closeDatabase()

	ctx := context.Background()
	job := lifecycleTestJob("listing-key-job", time.Now().UTC())

	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	inserted, err := repository.RememberJobListingKeys(ctx, job.ID, []string{"aaa", "bbb", "ccc"})
	if err != nil {
		t.Fatalf("remember listing keys: %v", err)
	}

	if inserted != 3 {
		t.Fatalf("first write inserted %d keys, want 3", inserted)
	}

	// A repeated write must converge rather than duplicate: this is what makes
	// a retried task and a restarted run safe.
	inserted, err = repository.RememberJobListingKeys(ctx, job.ID, []string{"bbb", "ccc", "ddd"})
	if err != nil {
		t.Fatalf("repeat listing keys: %v", err)
	}

	if inserted != 1 {
		t.Fatalf("repeat write inserted %d keys, want only the new one", inserted)
	}

	total, err := repository.CountJobListingKeys(ctx, job.ID)
	if err != nil {
		t.Fatalf("count listing keys: %v", err)
	}

	if total != 4 {
		t.Fatalf("stored %d listing keys, want 4", total)
	}

	keys, err := repository.JobListingKeys(ctx, job.ID, 10)
	if err != nil {
		t.Fatalf("read listing keys: %v", err)
	}

	if len(keys) != 4 {
		t.Fatalf("read %d listing keys, want 4", len(keys))
	}

	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		seen[key] = struct{}{}
	}

	for _, want := range []string{"aaa", "bbb", "ccc", "ddd"} {
		if _, found := seen[want]; !found {
			t.Errorf("listing key %q was not read back", want)
		}
	}

	// The bound is honoured so a resumed run cannot read an unbounded set.
	limited, err := repository.JobListingKeys(ctx, job.ID, 2)
	if err != nil {
		t.Fatalf("read bounded listing keys: %v", err)
	}

	if len(limited) != 2 {
		t.Fatalf("bounded read returned %d keys, want 2", len(limited))
	}
}

func TestJobListingKeysIgnoreEmptyInputAndUnknownJobs(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "listing-keys-empty")
	defer closeDatabase()

	ctx := context.Background()

	inserted, err := repository.RememberJobListingKeys(ctx, "", []string{"aaa"})
	if err != nil || inserted != 0 {
		t.Fatalf("write without a job = %d, %v", inserted, err)
	}

	keys, err := repository.JobListingKeys(ctx, "missing-job", 10)
	if err != nil {
		t.Fatalf("read for an unknown job: %v", err)
	}

	if len(keys) != 0 {
		t.Fatalf("unknown job returned %d keys", len(keys))
	}

	total, err := repository.CountJobListingKeys(ctx, "missing-job")
	if err != nil || total != 0 {
		t.Fatalf("count for an unknown job = %d, %v", total, err)
	}
}

func TestIntervalCheckpointBecomesTheLatestResumeBoundary(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "interval-checkpoint")
	defer closeDatabase()

	ctx := context.Background()
	job := lifecycleTestJob("interval-checkpoint-job", time.Now().UTC())

	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := repository.RecordJobIntervalCheckpoint(
		ctx, job.ID, `{"reason":"interval","tasks_completed":3}`,
	); err != nil {
		t.Fatalf("record interval checkpoint: %v", err)
	}

	execution, err := repository.GetJobExecution(ctx, job.ID)
	if err != nil {
		t.Fatalf("read job execution: %v", err)
	}

	if execution.Checkpoint == nil {
		t.Fatal("the interval checkpoint is not reported as a resume boundary")
	}

	if execution.Checkpoint.CreatedAt.IsZero() {
		t.Fatal("the interval checkpoint has no timestamp to display")
	}

	if string(execution.Checkpoint.Payload) != `{"reason":"interval","tasks_completed":3}` {
		t.Fatalf("checkpoint payload = %s", execution.Checkpoint.Payload)
	}

	// An empty payload must still produce a valid JSON object.
	if err := repository.RecordJobIntervalCheckpoint(ctx, job.ID, "  "); err != nil {
		t.Fatalf("record empty interval checkpoint: %v", err)
	}

	execution, err = repository.GetJobExecution(ctx, job.ID)
	if err != nil {
		t.Fatalf("read job execution after the empty payload: %v", err)
	}

	if execution.Checkpoint == nil || string(execution.Checkpoint.Payload) != "{}" {
		t.Fatalf("empty interval checkpoint payload = %v", execution.Checkpoint)
	}

	if err := repository.RecordJobIntervalCheckpoint(ctx, "", "{}"); err == nil {
		t.Fatal("an interval checkpoint without a job ID must be rejected")
	}
}
