package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

func TestSavedAreasPersistCanonicalGeoJSONAcrossRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "jobs.db")
	repository, err := New(databasePath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete := repository.(*repo)

	now := time.Unix(1_800_000_000, 0).UTC()
	area := web.SavedArea{
		ID: "sf-core", Name: "San Francisco core",
		GeoJSON:   []byte(`{"type":"Feature","properties":{"shape":"bbox","bbox":[-122.52,37.70,-122.35,37.84]},"geometry":null}`),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := concrete.CreateSavedArea(ctx, area); err != nil {
		t.Fatalf("CreateSavedArea() error = %v", err)
	}
	if err := concrete.CreateSavedArea(ctx, area); !errors.Is(err, web.ErrSavedAreaConflict) {
		t.Fatalf("duplicate CreateSavedArea() error = %v", err)
	}

	stored, err := concrete.GetSavedArea(ctx, area.ID)
	if err != nil {
		t.Fatalf("GetSavedArea() error = %v", err)
	}
	geometry, err := web.ParseMapGeometry(stored.GeoJSON)
	if err != nil || geometry.Kind() != "bbox" || stored.Name != area.Name || !stored.CreatedAt.Equal(now) {
		t.Fatalf("stored area = %+v, kind = %q, error = %v", stored, geometry.Kind(), err)
	}
	areas, err := concrete.ListSavedAreas(ctx, 10)
	if err != nil || len(areas) != 1 || areas[0].ID != area.ID {
		t.Fatalf("ListSavedAreas() = %+v, %v", areas, err)
	}

	area.Name = "Renamed core"
	area.GeoJSON = []byte(`{"type":"Feature","properties":{"shape":"circle","radius_m":2500},"geometry":{"type":"Point","coordinates":[-122.4194,37.7749]}}`)
	area.UpdatedAt = now.Add(time.Hour)
	if err := concrete.UpdateSavedArea(ctx, area); err != nil {
		t.Fatalf("UpdateSavedArea() error = %v", err)
	}
	if err := concrete.UpdateSavedArea(ctx, web.SavedArea{
		ID: "missing-area", Name: "Missing", GeoJSON: area.GeoJSON,
		CreatedAt: now, UpdatedAt: now,
	}); !errors.Is(err, web.ErrSavedAreaNotFound) {
		t.Fatalf("missing UpdateSavedArea() error = %v", err)
	}
	if err := concrete.db.Close(); err != nil {
		t.Fatalf("close first database: %v", err)
	}

	reopenedRepository, err := New(databasePath)
	if err != nil {
		t.Fatalf("reopen New() error = %v", err)
	}
	reopened := reopenedRepository.(*repo)
	t.Cleanup(func() { _ = reopened.db.Close() })
	stored, err = reopened.GetSavedArea(ctx, area.ID)
	if err != nil || stored.Name != "Renamed core" {
		t.Fatalf("reopened area = %+v, %v", stored, err)
	}
	geometry, err = web.ParseMapGeometry(stored.GeoJSON)
	if err != nil || geometry.Kind() != "circle" {
		t.Fatalf("reopened geometry kind = %q, error = %v", geometry.Kind(), err)
	}
	if err := reopened.DeleteSavedArea(ctx, area.ID); err != nil {
		t.Fatalf("DeleteSavedArea() error = %v", err)
	}
	if _, err := reopened.GetSavedArea(ctx, area.ID); !errors.Is(err, web.ErrSavedAreaNotFound) {
		t.Fatalf("deleted GetSavedArea() error = %v", err)
	}
	if err := reopened.DeleteSavedArea(ctx, area.ID); !errors.Is(err, web.ErrSavedAreaNotFound) {
		t.Fatalf("second DeleteSavedArea() error = %v", err)
	}
}

func TestSearchBusinessesInAreaUsesNormalizedFiltersAndExactContainment(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete := repository.(*repo)
	t.Cleanup(func() { _ = concrete.db.Close() })

	job := resultImportJob("job-map-spatial", time.Unix(1_800_000_000, 0).UTC())
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("Create(job) error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "spatial.csv")
	writeLegacyResultRows(t, path,
		map[string]string{
			"title": "Inside Alpha Dental", "category": "Dentist", "place_id": "inside-alpha",
			"address":          "1 Market St, San Francisco, CA 94105, United States",
			"complete_address": `{"city":"San Francisco","state":"CA","postal_code":"94105","country":"US"}`,
			"latitude":         "37.7749", "longitude": "-122.4194", "review_rating": "4.8",
		},
		map[string]string{
			"title": "Inside Beta Dental", "category": "Dentist", "place_id": "inside-beta",
			"address":          "2 Mission St, San Francisco, CA 94105, United States",
			"complete_address": `{"city":"San Francisco","state":"CA","postal_code":"94105","country":"US"}`,
			"latitude":         "37.7790", "longitude": "-122.4100", "review_rating": "4.5",
		},
		map[string]string{
			"title": "Outside Oakland Dental", "category": "Dentist", "place_id": "outside-oakland",
			"address":          "3 Broadway, Oakland, CA 94607, United States",
			"complete_address": `{"city":"Oakland","state":"CA","postal_code":"94607","country":"US"}`,
			"latitude":         "37.8044", "longitude": "-122.2712", "review_rating": "4.9",
		},
		map[string]string{
			"title": "Missing Coordinates Dental", "category": "Dentist", "place_id": "missing-coordinates",
			"address":          "4 Unknown St, San Francisco, CA, United States",
			"complete_address": `{"city":"San Francisco","state":"CA","country":"US"}`,
		},
	)
	if _, err := concrete.ImportLegacyCSV(ctx, job, path); err != nil {
		t.Fatalf("ImportLegacyCSV() error = %v", err)
	}

	geometry, err := web.ParseMapGeometry([]byte(`{"type":"Feature","properties":{"shape":"bbox","bbox":[-122.45,37.75,-122.39,37.79]},"geometry":null}`))
	if err != nil {
		t.Fatalf("ParseMapGeometry() error = %v", err)
	}
	page, err := concrete.SearchBusinessesInArea(ctx, web.ResultSearch{
		Filters: []web.ResultFilter{{Field: "city", Operator: "eq", Value: "San Francisco"}},
		Sort:    "name_asc", Limit: 1,
	}, geometry)
	if err != nil {
		t.Fatalf("SearchBusinessesInArea() error = %v", err)
	}
	if page.Total != 2 || page.Limit != 1 || page.Offset != 0 || len(page.Results) != 1 || page.Results[0].Name != "Inside Alpha Dental" {
		t.Fatalf("first spatial page = %+v", page)
	}
	page, err = concrete.SearchBusinessesInArea(ctx, web.ResultSearch{
		Filters: []web.ResultFilter{{Field: "city", Operator: "eq", Value: "San Francisco"}},
		Sort:    "name_asc", Limit: 1, Offset: 1,
	}, geometry)
	if err != nil || page.Total != 2 || len(page.Results) != 1 || page.Results[0].Name != "Inside Beta Dental" {
		t.Fatalf("second spatial page = %+v, %v", page, err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := concrete.SearchBusinessesInArea(cancelled, web.ResultSearch{Limit: 25}, geometry); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled spatial query error = %v", err)
	}
}

func TestMapCellActivityAggregatesTasksResultsAndDuplicates(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	concrete := repository.(*repo)
	t.Cleanup(func() { _ = concrete.db.Close() })
	job := resultImportJob("job-map-coverage", time.Now().UTC())
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatal(err)
	}
	_, err = concrete.PrepareJobTasks(ctx, job.ID, []web.JobTaskDefinition{
		{Key: "one", Kind: "map-grid-cell", Sequence: 0, Query: "dentist", SourceCell: "cell-one"},
		{Key: "two", Kind: "map-grid-cell", Sequence: 1, Query: "dentist", SourceCell: "cell-one"},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := concrete.db.Exec(`UPDATE job_tasks SET state = CASE task_key WHEN 'one' THEN 'completed' ELSE 'failed' END,
		last_error = CASE task_key WHEN 'two' THEN 'Google blocked request' ELSE '' END WHERE job_id = ?`, job.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	if _, err := concrete.db.Exec(`INSERT INTO businesses(
		id, canonical_key, name, normalized_name, first_seen_at, last_seen_at, last_changed_at, created_at, updated_at
	) VALUES ('business-map', 'place:map', 'Map Dental', 'map dental', ?, ?, ?, ?, ?)`, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	for _, ingestKey := range []string{"map-one", "map-two"} {
		if _, err := concrete.db.Exec(`INSERT INTO business_sources(
			business_id, job_id, source_type, source_cell, extracted_at, ingest_key
		) VALUES ('business-map', ?, 'maps', 'cell-one', ?, ?)`, job.ID, now, ingestKey); err != nil {
			t.Fatal(err)
		}
	}
	activity, err := concrete.MapCellActivity(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(activity) != 1 || activity[0].TaskCount != 2 || activity[0].CompletedTasks != 1 ||
		activity[0].FailedTasks != 1 || activity[0].BlockedTasks != 1 ||
		activity[0].ResultCount != 1 || activity[0].DuplicateCount() != 1 {
		t.Fatalf("MapCellActivity() = %+v", activity)
	}
}
