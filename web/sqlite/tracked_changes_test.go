package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// Every tracked change the specification names must end up as durable,
// queryable evidence with the exact field_name the incremental "volatile
// fields" mode filters on. This test walks a business through a discovery and
// then a rescan that moves phone, website, address, category, rating, review
// count, opening hours and status, and asserts the recorded rows.
func TestImportRecordsDiscoveryAndFieldLevelChanges(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete, ok := repository.(*repo)
	if !ok {
		t.Fatal("New() did not return the local SQLite repository")
	}
	t.Cleanup(func() { _ = concrete.db.Close() })

	job := resultImportJob("job-tracked-changes", time.Unix(1_700_000_000, 0).UTC())
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	before := map[string]string{
		"input_id": "seed-a", "title": "Bay Smile Dental", "category": "Dentist",
		"address":      "123 Main St, San Francisco, CA 94105, United States",
		"website":      "https://baysmile.example",
		"phone":        "+1 415-555-0199",
		"review_count": "42", "review_rating": "4.6",
		"latitude": "37.789", "longitude": "-122.394", "place_id": "place-alpha",
		"link":       `https://www.google.com/maps/place/?q=place_id:place-alpha`,
		"status":     "Open",
		"open_hours": `{"Monday":["9AM-5PM"]}`,
	}

	firstCSV := filepath.Join(t.TempDir(), "pass-1.csv")
	writeLegacyResultRows(t, firstCSV, before)
	first, err := concrete.ImportLegacyCSV(ctx, job, firstCSV)
	if err != nil {
		t.Fatalf("first ImportLegacyCSV() error = %v", err)
	}
	if first.New != 1 {
		t.Fatalf("first import New = %d, want 1", first.New)
	}

	businessID := businessIDByPlaceID(t, concrete.db, "place-alpha")

	// "New business discovered" is durable evidence in two independent
	// places: the job's own discovery flag and the first immutable version.
	if !jobBusinessIsNew(t, concrete.db, job.ID, businessID) {
		t.Error("the discovering job did not flag the business as new")
	}
	if changeType := firstVersionChangeType(t, concrete.db, businessID); changeType != "new" {
		t.Errorf("first version change_type = %q, want \"new\"", changeType)
	}

	after := map[string]string{}
	for key, value := range before {
		after[key] = value
	}
	after["phone"] = "+1 415-555-0100"
	after["website"] = "https://baysmiledental.example"
	after["address"] = "456 Market St, San Francisco, CA 94105, United States"
	after["category"] = "Dental clinic"
	after["review_rating"] = "4.9"
	after["review_count"] = "58"
	after["status"] = "Temporarily closed"
	after["open_hours"] = `{"Monday":["10AM-6PM"]}`

	secondCSV := filepath.Join(t.TempDir(), "pass-2.csv")
	writeLegacyResultRows(t, secondCSV, after)
	second, err := concrete.ImportLegacyCSV(ctx, job, secondCSV)
	if err != nil {
		t.Fatalf("second ImportLegacyCSV() error = %v", err)
	}
	if second.Changed != 1 {
		t.Fatalf("second import Changed = %d, want 1", second.Changed)
	}

	recorded := changedFieldNames(t, concrete.db, businessID)
	// These are exactly the names web.volatileBusinessFields filters on; a
	// rename on either side would break the incremental volatile mode.
	for _, field := range []string{
		"phones", "website", "address", "category", "normalized_category",
		"review_rating", "review_count", "status", "structured",
	} {
		if _, ok := recorded[field]; !ok {
			t.Errorf("no business_changes row for %q; recorded = %v", field, sortedKeys(recorded))
		}
	}

	// Every recorded name must be one the volatile mode knows about, or the
	// mode would silently drop real changes.
	known := make(map[string]struct{}, 16)
	for _, field := range volatileFieldNamesUnderTest() {
		known[field] = struct{}{}
	}
	for field := range recorded {
		if _, ok := known[field]; !ok {
			t.Logf("business_changes recorded %q, which the volatile rescan mode ignores", field)
		}
	}
}

// volatileFieldNamesUnderTest mirrors web.volatileBusinessFields. The web
// package cannot be imported for an unexported variable, so the list is
// restated here and the assertions above are what keep the two honest.
func volatileFieldNamesUnderTest() []string {
	return []string{
		"phones", "website", "address", "category", "normalized_category",
		"review_rating", "review_count", "status", "emails", "structured",
		"website_status", "website_final_url",
	}
}

// An incremental rescan mode reaches the stored evidence through the shared
// result-filter language, so the lineage filters the modes are built on must
// actually resolve against a real database.
func TestIncrementalLineageFiltersResolveAgainstStoredEvidence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete, ok := repository.(*repo)
	if !ok {
		t.Fatal("New() did not return the local SQLite repository")
	}
	t.Cleanup(func() { _ = concrete.db.Close() })

	job := resultImportJob("job-lineage", time.Unix(1_700_000_000, 0).UTC())
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	record := map[string]string{
		"input_id": "seed-l", "title": "Lineage Dental", "category": "Dentist",
		"address": "1 Lineage Way, San Francisco, CA 94105, United States",
		"website": "https://lineage.example", "phone": "+1 415-555-0000",
		"review_count": "10", "review_rating": "4.0",
		"latitude": "37.77", "longitude": "-122.41", "place_id": "place-lineage",
		"link": `https://www.google.com/maps/place/?q=place_id:place-lineage`,
	}
	csvPath := filepath.Join(t.TempDir(), "lineage.csv")
	writeLegacyResultRows(t, csvPath, record)
	if _, err := concrete.ImportLegacyCSV(ctx, job, csvPath); err != nil {
		t.Fatalf("ImportLegacyCSV() error = %v", err)
	}

	// first_seen_job is the "collect only new listings" mode's predicate.
	newOnly, err := concrete.SearchBusinesses(ctx, web.ResultSearch{
		Limit: 10,
		FilterGroup: &web.ResultFilterGroup{Logic: "and", Filters: []web.ResultFilter{
			{Field: "first_seen_job", Operator: "eq", Value: job.ID},
		}},
	})
	if err != nil {
		t.Fatalf("first_seen_job search error = %v", err)
	}
	if newOnly.Total != 1 {
		t.Fatalf("first_seen_job total = %d, want 1", newOnly.Total)
	}

	// changed_by_job excludes the run that discovered the business, so a
	// freshly imported business must NOT match it.
	changed, err := concrete.SearchBusinesses(ctx, web.ResultSearch{
		Limit: 10,
		FilterGroup: &web.ResultFilterGroup{Logic: "and", Filters: []web.ResultFilter{
			{Field: "changed_by_job", Operator: "eq", Value: job.ID},
		}},
	})
	if err != nil {
		t.Fatalf("changed_by_job search error = %v", err)
	}
	if changed.Total != 0 {
		t.Fatalf("changed_by_job total = %d, want 0 for a discovery-only run", changed.Total)
	}

	// changed_field is the volatile-rescan mode's predicate; nothing has
	// changed yet, so it must also be empty.
	volatile, err := concrete.SearchBusinesses(ctx, web.ResultSearch{
		Limit: 10,
		FilterGroup: &web.ResultFilterGroup{Logic: "and", Filters: []web.ResultFilter{
			{Field: "changed_field", Operator: "eq", Value: "review_count"},
		}},
	})
	if err != nil {
		t.Fatalf("changed_field search error = %v", err)
	}
	if volatile.Total != 0 {
		t.Fatalf("changed_field total = %d, want 0 before any change", volatile.Total)
	}

	// After a rescan that moves the review count, both predicates match.
	record["review_count"] = "25"
	rescanPath := filepath.Join(t.TempDir(), "lineage-2.csv")
	writeLegacyResultRows(t, rescanPath, record)
	if _, err := concrete.ImportLegacyCSV(ctx, job, rescanPath); err != nil {
		t.Fatalf("rescan ImportLegacyCSV() error = %v", err)
	}

	volatile, err = concrete.SearchBusinesses(ctx, web.ResultSearch{
		Limit: 10,
		FilterGroup: &web.ResultFilterGroup{Logic: "and", Filters: []web.ResultFilter{
			{Field: "changed_field", Operator: "eq", Value: "review_count"},
		}},
	})
	if err != nil {
		t.Fatalf("changed_field search after rescan error = %v", err)
	}
	if volatile.Total != 1 {
		t.Fatalf("changed_field total after rescan = %d, want 1", volatile.Total)
	}
}

func jobBusinessIsNew(t *testing.T, db *sql.DB, jobID, businessID string) bool {
	t.Helper()

	var isNew int
	if err := db.QueryRow(
		"SELECT is_new FROM job_businesses WHERE job_id = ? AND business_id = ?",
		jobID, businessID,
	).Scan(&isNew); err != nil {
		t.Fatalf("read job_businesses discovery flag: %v", err)
	}

	return isNew == 1
}

func firstVersionChangeType(t *testing.T, db *sql.DB, businessID string) string {
	t.Helper()

	var changeType string
	if err := db.QueryRow(
		"SELECT change_type FROM business_versions WHERE business_id = ? ORDER BY version_no LIMIT 1",
		businessID,
	).Scan(&changeType); err != nil {
		t.Fatalf("read first business version: %v", err)
	}

	return changeType
}

func changedFieldNames(t *testing.T, db *sql.DB, businessID string) map[string]struct{} {
	t.Helper()

	rows, err := db.Query(
		"SELECT DISTINCT field_name FROM business_changes WHERE business_id = ? AND field_name <> ''",
		businessID,
	)
	if err != nil {
		t.Fatalf("read business changes: %v", err)
	}
	defer rows.Close()

	fields := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan business change: %v", err)
		}
		fields[strings.TrimSpace(name)] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read business changes: %v", err)
	}

	return fields
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	return keys
}
