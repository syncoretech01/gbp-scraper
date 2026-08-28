package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// TestJobCellObservationsPairEveryBusinessWithTheCellThatFoundIt proves the
// evidence exists: the importer records the task key on each source row and
// the plan records the cell that key searched, so a collected business can
// always be traced back to the search that produced it.
//
// The second business is deliberately given three source rows for the same
// job. Joining business_sources on the job would return it three times and
// triple its weight in any distance measurement; anchoring on
// job_businesses.first_source_id returns it once, which is what this asserts.
func TestJobCellObservationsPairEveryBusinessWithTheCellThatFoundIt(t *testing.T) {
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

	job := resultImportJob("job-cell-geography", time.Now().UTC())
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if _, err := concrete.PrepareJobTasks(ctx, job.ID, []web.JobTaskDefinition{
		{Key: "task-a", Kind: "map-grid-cell", Sequence: 0, Query: "tattoo", SourceCell: "34.074506,-118.216777"},
		{Key: "task-b", Kind: "map-grid-cell", Sequence: 1, Query: "tattoo", SourceCell: "33.939760,-118.108355"},
		{Key: "task-c", Kind: "map-query", Sequence: 2, Query: "tattoo"},
	}, 2); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Unix()
	businesses := []struct {
		id        string
		latitude  float64
		longitude float64
	}{
		{"business-near", 34.0745, -118.2168},
		{"business-far", 33.8445, -117.9413},
		{"business-nocell", 34.0500, -118.2500},
	}
	for _, business := range businesses {
		if _, err := concrete.db.Exec(`INSERT INTO businesses(
			id, canonical_key, name, normalized_name, latitude, longitude,
			first_seen_at, last_seen_at, last_changed_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			business.id, "place:"+business.id, business.id, business.id,
			business.latitude, business.longitude, now, now, now, now, now,
		); err != nil {
			t.Fatal(err)
		}
	}

	sourceIDs := map[string]int64{}
	insertSource := func(businessID, taskKey, ingestKey string) int64 {
		result, err := concrete.db.Exec(`INSERT INTO business_sources(
			business_id, job_id, source_type, input_id, extracted_at, ingest_key
		) VALUES (?, ?, 'maps', ?, ?, ?)`, businessID, job.ID, taskKey, now, ingestKey)
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}

		return id
	}
	sourceIDs["business-near"] = insertSource("business-near", "task-a", "near-1")
	sourceIDs["business-far"] = insertSource("business-far", "task-b", "far-1")
	// The same business re-found by two more searches: three source rows, one
	// business, and exactly one of them is the first.
	insertSource("business-far", "task-a", "far-2")
	insertSource("business-far", "task-b", "far-3")
	sourceIDs["business-nocell"] = insertSource("business-nocell", "task-c", "nocell-1")

	for businessID, sourceID := range sourceIDs {
		if _, err := concrete.db.Exec(`INSERT INTO job_businesses(
			job_id, business_id, first_source_id, first_seen_at, last_seen_at, occurrence_count
		) VALUES (?, ?, ?, ?, ?, 1)`, job.ID, businessID, sourceID, now, now); err != nil {
			t.Fatal(err)
		}
	}

	observations, err := concrete.JobCellObservations(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobCellObservations() error = %v", err)
	}

	// business-nocell was found by a task with no cell, so it cannot be
	// attributed and is left out rather than attributed to a guess.
	if len(observations) != 2 {
		t.Fatalf("observations = %d (%+v), want 2 with no source fan-out", len(observations), observations)
	}

	byCell := map[string]web.JobCellObservation{}
	for _, observation := range observations {
		if _, repeated := byCell[observation.Cell]; repeated {
			t.Fatalf("cell %q appeared twice: the join is fanning out over sources", observation.Cell)
		}
		byCell[observation.Cell] = observation
	}
	if got := byCell["34.074506,-118.216777"]; got.Latitude != 34.0745 {
		t.Fatalf("near business paired with %+v", got)
	}
	if got := byCell["33.939760,-118.108355"]; got.Latitude != 33.8445 {
		t.Fatalf("far business paired with %+v", got)
	}

	// And the measurement built on it must attribute the far business to the
	// platform rather than to the plan: a 5 km cell reaches 3.5 km, and this
	// result landed far past that.
	spillover := web.NewService(repository, t.TempDir())
	report, err := spillover.JobCellSpillover(ctx, job.ID, 5)
	if err != nil {
		t.Fatalf("JobCellSpillover() error = %v", err)
	}
	if report.Measured != 2 || report.WithinOwnCell != 1 || report.Beyond != 1 {
		t.Fatalf("spillover = %+v, want 2 measured, 1 within, 1 beyond", report)
	}
}

func TestJobCellObservationsAreEmptyForAnUngriddedJob(t *testing.T) {
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

	job := resultImportJob("job-no-grid", time.Now().UTC())
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatal(err)
	}

	observations, err := concrete.JobCellObservations(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobCellObservations() error = %v", err)
	}
	if len(observations) != 0 {
		t.Fatalf("observations = %+v, want none", observations)
	}
}
