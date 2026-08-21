package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

func TestJobLabelsRoundTripAndReplaceTagSet(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "job-labels-roundtrip")
	defer closeDatabase()
	ctx := context.Background()

	job := lifecycleTestJob("labelled", time.Unix(100, 0).UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	empty, err := repository.JobLabels(ctx, job.ID)
	if err != nil {
		t.Fatalf("read empty labels: %v", err)
	}
	if empty.Folder != "" || empty.Owner != "" || len(empty.Tags) != 0 {
		t.Fatalf("empty labels = %#v", empty)
	}

	if err := repository.SetJobLabels(ctx, job.ID, web.JobLabels{
		Folder: "Q3 campaigns",
		Owner:  "Dana",
		Tags:   []string{"austin", "plumbing"},
	}); err != nil {
		t.Fatalf("set labels: %v", err)
	}

	stored, err := repository.JobLabels(ctx, job.ID)
	if err != nil {
		t.Fatalf("read labels: %v", err)
	}
	if stored.Folder != "Q3 campaigns" || stored.Owner != "Dana" {
		t.Fatalf("stored labels = %#v", stored)
	}
	if len(stored.Tags) != 2 || stored.Tags[0] != "austin" || stored.Tags[1] != "plumbing" {
		t.Fatalf("stored tags = %#v", stored.Tags)
	}

	// Replacing the set is what makes removing a tag possible, so a second
	// write must not leave the first write's tags attached.
	if err := repository.SetJobLabels(ctx, job.ID, web.JobLabels{
		Folder: "",
		Owner:  "",
		Tags:   []string{"plumbing", "q4"},
	}); err != nil {
		t.Fatalf("replace labels: %v", err)
	}

	replaced, err := repository.JobLabels(ctx, job.ID)
	if err != nil {
		t.Fatalf("read replaced labels: %v", err)
	}
	if replaced.Folder != "" || replaced.Owner != "" {
		t.Fatalf("replaced labels kept folder or owner: %#v", replaced)
	}
	if len(replaced.Tags) != 2 || replaced.Tags[0] != "plumbing" || replaced.Tags[1] != "q4" {
		t.Fatalf("replaced tags = %#v", replaced.Tags)
	}

	// The shared tag rows are reused rather than duplicated.
	var tagCount int
	if err := repository.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tags`).Scan(&tagCount); err != nil {
		t.Fatalf("count tags: %v", err)
	}
	if tagCount != 3 {
		t.Fatalf("tag rows = %d, want 3 (austin, plumbing, q4)", tagCount)
	}
}

func TestAllJobLabelsGroupsEveryJobAndRejectsMissingJobs(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "job-labels-all")
	defer closeDatabase()
	ctx := context.Background()

	first := lifecycleTestJob("first", time.Unix(100, 0).UTC())
	second := lifecycleTestJob("second", time.Unix(200, 0).UTC())
	for _, job := range []*web.Job{&first, &second} {
		if err := repository.CreateWithState(ctx, job, jobruntime.StateQueued); err != nil {
			t.Fatalf("create job %s: %v", job.ID, err)
		}
	}

	if err := repository.SetJobLabels(ctx, first.ID, web.JobLabels{
		Folder: "Texas", Owner: "Dana", Tags: []string{"austin"},
	}); err != nil {
		t.Fatalf("label first job: %v", err)
	}
	if err := repository.SetJobLabels(ctx, second.ID, web.JobLabels{
		Tags: []string{"austin", "roofing"},
	}); err != nil {
		t.Fatalf("label second job: %v", err)
	}

	all, err := repository.AllJobLabels(ctx)
	if err != nil {
		t.Fatalf("read all labels: %v", err)
	}
	if all[first.ID].Folder != "Texas" || all[first.ID].Owner != "Dana" {
		t.Fatalf("first job labels = %#v", all[first.ID])
	}
	if len(all[first.ID].Tags) != 1 {
		t.Fatalf("first job tags = %#v", all[first.ID].Tags)
	}
	if len(all[second.ID].Tags) != 2 {
		t.Fatalf("second job tags = %#v", all[second.ID].Tags)
	}

	if err := repository.SetJobLabels(ctx, "missing", web.JobLabels{Folder: "Texas"}); !errors.Is(err, web.ErrLifecycleNotFound) {
		t.Fatalf("labelling a missing job = %v, want ErrLifecycleNotFound", err)
	}
	if _, err := repository.JobLabels(ctx, "missing"); !errors.Is(err, web.ErrLifecycleNotFound) {
		t.Fatalf("reading a missing job = %v, want ErrLifecycleNotFound", err)
	}
}
