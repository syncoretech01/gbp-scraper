package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// TestImportUsesExactObservationProvenance proves issue S: a row whose task
// provenance was recorded at merge time is imported with THAT query and cell,
// not the job's joined keyword list, and a second task that observed the same
// business is filed as a second observation instead of being lost.
func TestImportUsesExactObservationProvenance(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete := repository.(*repo)
	t.Cleanup(func() { _ = concrete.db.Close() })

	job := resultImportJob("job-prov", time.Unix(1_700_000_000, 0).UTC())
	job.Data.Keywords = []string{"tattoo artist", "tattoo shop", "tattoo studio"}
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// The pool recorded two tasks observing the same business, and none for
	// the second business — that one must fall back to the legacy behaviour.
	observed := time.Unix(1_700_000_100, 0).UTC()
	if err := concrete.RecordObservationProvenance(ctx, []web.ObservationProvenance{
		{JobID: job.ID, IdentityKey: "place:place-ink", TaskKey: "task-1", SourceQuery: "tattoo shop", SourceCell: "34.05,-118.24", ObservedAt: observed},
		{JobID: job.ID, IdentityKey: "place:place-ink", TaskKey: "task-2", SourceQuery: "tattoo studio", SourceCell: "34.10,-118.20", ObservedAt: observed.Add(time.Minute)},
		// Re-recording the same fact must be idempotent.
		{JobID: job.ID, IdentityKey: "place:place-ink", TaskKey: "task-1", SourceQuery: "tattoo shop", SourceCell: "34.05,-118.24", ObservedAt: observed},
	}); err != nil {
		t.Fatalf("RecordObservationProvenance() error = %v", err)
	}

	csvPath := filepath.Join(t.TempDir(), "job-prov.csv")
	writeLegacyResultRows(t, csvPath,
		map[string]string{
			"input_id": "seed-a", "title": "Ink Society", "category": "Tattoo shop",
			"address": "1 Sunset Blvd, Los Angeles, CA 90028, United States",
			"phone":   "+1 213-555-0100", "latitude": "34.05", "longitude": "-118.24",
			"place_id": "place-ink", "link": "https://www.google.com/maps/place/?q=place_id:place-ink",
		},
		map[string]string{
			"input_id": "seed-b", "title": "Lone Needle", "category": "Tattoo shop",
			"address": "2 Vine St, Los Angeles, CA 90028, United States",
			"phone":   "+1 213-555-0101", "latitude": "34.06", "longitude": "-118.25",
			"place_id": "place-lone", "link": "https://www.google.com/maps/place/?q=place_id:place-lone",
		},
	)

	if _, err := concrete.ImportLegacyCSV(ctx, job, csvPath); err != nil {
		t.Fatalf("ImportLegacyCSV() error = %v", err)
	}

	type source struct{ query, cell string }
	sourcesFor := func(placeID string) []source {
		rows, err := concrete.db.QueryContext(ctx, `
			SELECT s.source_query, s.source_cell FROM business_sources s
			JOIN businesses b ON b.id = s.business_id
			WHERE s.job_id = ? AND b.place_id = ? ORDER BY s.source_query`, job.ID, placeID)
		if err != nil {
			t.Fatalf("query sources: %v", err)
		}
		defer func() { _ = rows.Close() }()
		var out []source
		for rows.Next() {
			var item source
			if err := rows.Scan(&item.query, &item.cell); err != nil {
				t.Fatal(err)
			}
			out = append(out, item)
		}
		return out
	}

	ink := sourcesFor("place-ink")
	if len(ink) != 2 {
		t.Fatalf("Ink Society observations = %+v, want one per observing task", ink)
	}
	if ink[0] != (source{"tattoo shop", "34.05,-118.24"}) || ink[1] != (source{"tattoo studio", "34.10,-118.20"}) {
		t.Fatalf("Ink Society provenance = %+v, want the exact query and cell of each task", ink)
	}

	lone := sourcesFor("place-lone")
	if len(lone) != 1 || lone[0].query != "tattoo artist | tattoo shop | tattoo studio" {
		t.Fatalf("unrecorded business provenance = %+v, want the legacy joined keywords as a fallback", lone)
	}

	// A second import must not multiply observations.
	if _, err := concrete.ImportLegacyCSV(ctx, job, csvPath); err != nil {
		t.Fatalf("second ImportLegacyCSV() error = %v", err)
	}
	if again := sourcesFor("place-ink"); len(again) != 2 {
		t.Fatalf("re-import multiplied observations: %d", len(again))
	}
}
