package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

func TestJobScraperVersionIsStampedOnceAndReadBack(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "scraper-version-stamp")
	defer closeDatabase()
	ctx := context.Background()

	job := lifecycleTestJob("versioned", time.Unix(100, 0).UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	recorded, err := repository.JobScraperVersion(ctx, job.ID)
	if err != nil {
		t.Fatalf("read unstamped version: %v", err)
	}
	if recorded != "" {
		t.Fatalf("unstamped version = %q, want empty", recorded)
	}

	if err := repository.RecordJobScraperVersion(ctx, job.ID, "v1.17.3"); err != nil {
		t.Fatalf("record version: %v", err)
	}

	// A restart under a newer build must not rewrite the identity that
	// produced the committed rows.
	if err := repository.RecordJobScraperVersion(ctx, job.ID, "v9.9.9"); err != nil {
		t.Fatalf("re-record version: %v", err)
	}
	// An empty version is ignored rather than blanking a recorded one.
	if err := repository.RecordJobScraperVersion(ctx, job.ID, "   "); err != nil {
		t.Fatalf("record blank version: %v", err)
	}

	recorded, err = repository.JobScraperVersion(ctx, job.ID)
	if err != nil {
		t.Fatalf("read stamped version: %v", err)
	}
	if recorded != "v1.17.3" {
		t.Fatalf("stamped version = %q, want v1.17.3", recorded)
	}
}

func TestJobScraperVersionBoundsLengthAndReportsMissingJob(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "scraper-version-bounds")
	defer closeDatabase()
	ctx := context.Background()

	job := lifecycleTestJob("bounded", time.Unix(200, 0).UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := repository.RecordJobScraperVersion(ctx, job.ID, strings.Repeat("v", 500)); err != nil {
		t.Fatalf("record oversized version: %v", err)
	}

	recorded, err := repository.JobScraperVersion(ctx, job.ID)
	if err != nil {
		t.Fatalf("read bounded version: %v", err)
	}
	if len(recorded) != maximumScraperVersionLength {
		t.Fatalf("bounded version length = %d, want %d", len(recorded), maximumScraperVersionLength)
	}

	if _, err := repository.JobScraperVersion(ctx, "missing"); !errors.Is(err, web.ErrLifecycleNotFound) {
		t.Fatalf("missing job error = %v, want ErrLifecycleNotFound", err)
	}
}
