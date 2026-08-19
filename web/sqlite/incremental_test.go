package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

type incrementalSummary struct {
	New             int64  `json:"new"`
	Changed         int64  `json:"changed"`
	Unchanged       int64  `json:"unchanged"`
	Disappeared     int64  `json:"disappeared"`
	Reappeared      int64  `json:"reappeared"`
	Rescan          bool   `json:"rescan"`
	IncrementalMode string `json:"incremental_mode"`
}

func TestRescanFlagsDisappearedAndRestoresReappearedBusinesses(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete := repository.(*repo)
	t.Cleanup(func() { _ = concrete.db.Close() })

	job := resultImportJob("job-rescan", time.Unix(1_700_000_000, 0).UTC())
	job.Data.IncrementalMode = web.IncrementalModeNewChanged
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	alpha := map[string]string{
		"input_id": "seed-a", "title": "Bay Smile Dental", "category": "Dentist",
		"address": "123 Main St, San Francisco, CA 94105, United States",
		"website": "https://baysmile.example", "phone": "+1 415-555-0199",
		"review_count": "42", "review_rating": "4.6",
		"latitude": "37.789", "longitude": "-122.394", "place_id": "place-alpha",
		"link": "https://www.google.com/maps/place/?q=place_id:place-alpha",
	}
	alphaChanged := map[string]string{}
	for key, value := range alpha {
		alphaChanged[key] = value
	}
	alphaChanged["review_count"] = "55"
	beta := map[string]string{
		"input_id": "seed-b", "title": "Mission Ortho Care", "category": "Orthodontist",
		"address": "500 Valencia St, San Francisco, CA 94110, United States",
		"website": "https://missionortho.example", "phone": "+1 415-555-0142",
		"review_count": "12", "review_rating": "4.1",
		"latitude": "37.764", "longitude": "-122.421", "place_id": "place-beta",
		"link": "https://www.google.com/maps/place/?q=place_id:place-beta",
	}

	// First import establishes the baseline: both businesses are new.
	firstCSV := filepath.Join(t.TempDir(), "pass-1.csv")
	writeLegacyResultRows(t, firstCSV, alpha, beta)
	first, err := concrete.ImportLegacyCSV(ctx, job, firstCSV)
	if err != nil {
		t.Fatalf("first ImportLegacyCSV() error = %v", err)
	}
	if first.New != 2 || first.Changed != 0 || first.Unchanged != 0 || first.Disappeared != 0 {
		t.Fatalf("first import counts = %+v", first)
	}

	// The rescan no longer sees beta: it must be flagged as evidence, not deleted.
	secondCSV := filepath.Join(t.TempDir(), "pass-2.csv")
	writeLegacyResultRows(t, secondCSV, alpha)
	second, err := concrete.ImportLegacyCSV(ctx, job, secondCSV)
	if err != nil {
		t.Fatalf("second ImportLegacyCSV() error = %v", err)
	}
	if second.New != 0 || second.Changed != 0 || second.Unchanged != 1 || second.Disappeared != 1 {
		t.Fatalf("second import counts = %+v", second)
	}

	betaID := businessIDByPlaceID(t, concrete.db, "place-beta")
	status, deleted := businessChangeState(t, concrete.db, betaID)
	if status != "possibly_removed" || deleted {
		t.Fatalf("after rescan beta status = %q, deleted = %v", status, deleted)
	}
	if count := recordChangeCount(t, concrete.db, betaID, "not_seen_in_rescan"); count != 1 {
		t.Fatalf("not_seen_in_rescan rows after second import = %d, want 1", count)
	}

	// Another rescan still missing beta must not stack a duplicate change row.
	thirdCSV := filepath.Join(t.TempDir(), "pass-3.csv")
	writeLegacyResultRows(t, thirdCSV, alphaChanged)
	third, err := concrete.ImportLegacyCSV(ctx, job, thirdCSV)
	if err != nil {
		t.Fatalf("third ImportLegacyCSV() error = %v", err)
	}
	if third.New != 0 || third.Changed != 1 || third.Unchanged != 0 || third.Disappeared != 1 {
		t.Fatalf("third import counts = %+v", third)
	}
	if count := recordChangeCount(t, concrete.db, betaID, "not_seen_in_rescan"); count != 1 {
		t.Fatalf("not_seen_in_rescan rows after third import = %d, want 1", count)
	}

	// Beta is seen again: restore it as changed with a reappeared change row.
	fourthCSV := filepath.Join(t.TempDir(), "pass-4.csv")
	writeLegacyResultRows(t, fourthCSV, alphaChanged, beta)
	fourth, err := concrete.ImportLegacyCSV(ctx, job, fourthCSV)
	if err != nil {
		t.Fatalf("fourth ImportLegacyCSV() error = %v", err)
	}
	if fourth.New != 0 || fourth.Changed != 1 || fourth.Unchanged != 1 || fourth.Disappeared != 0 {
		t.Fatalf("fourth import counts = %+v", fourth)
	}

	status, deleted = businessChangeState(t, concrete.db, betaID)
	if status != "changed" || deleted {
		t.Fatalf("after reappearance beta status = %q, deleted = %v", status, deleted)
	}
	if count := recordChangeCount(t, concrete.db, betaID, "reappeared"); count != 1 {
		t.Fatalf("reappeared rows = %d, want 1", count)
	}

	summaries := incrementalSummaries(t, concrete.db, job.ID)
	if len(summaries) != 4 {
		t.Fatalf("incremental-summary events = %d, want 4", len(summaries))
	}
	if summaries[0].Rescan || summaries[0].New != 2 || summaries[0].IncrementalMode != web.IncrementalModeNewChanged {
		t.Fatalf("first summary event = %+v", summaries[0])
	}
	if !summaries[1].Rescan || summaries[1].Disappeared != 1 || summaries[1].Unchanged != 1 {
		t.Fatalf("second summary event = %+v", summaries[1])
	}
	if summaries[3].Reappeared != 1 || summaries[3].Changed != 1 || summaries[3].Disappeared != 0 {
		t.Fatalf("fourth summary event = %+v", summaries[3])
	}

	// A byte-identical re-import is still skipped and reports durable counts.
	skipped, err := concrete.ImportLegacyCSV(ctx, job, fourthCSV)
	if err != nil {
		t.Fatalf("skipped ImportLegacyCSV() error = %v", err)
	}
	if !skipped.SkippedUnchanged || skipped.New != 2 || skipped.Disappeared != 0 {
		t.Fatalf("skipped import summary = %+v", skipped)
	}
}

func businessIDByPlaceID(t *testing.T, db *sql.DB, placeID string) string {
	t.Helper()

	var id string
	if err := db.QueryRow(`SELECT id FROM businesses WHERE place_id = ?`, placeID).Scan(&id); err != nil {
		t.Fatalf("read business by place_id %q: %v", placeID, err)
	}

	return id
}

func businessChangeState(t *testing.T, db *sql.DB, businessID string) (string, bool) {
	t.Helper()

	var status string
	var deletedAt sql.NullInt64
	if err := db.QueryRow(
		`SELECT change_status, deleted_at FROM businesses WHERE id = ?`,
		businessID,
	).Scan(&status, &deletedAt); err != nil {
		t.Fatalf("read business change state: %v", err)
	}

	return status, deletedAt.Valid
}

func recordChangeCount(t *testing.T, db *sql.DB, businessID, changeKind string) int64 {
	t.Helper()

	var count int64
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM business_changes
		WHERE business_id = ? AND change_kind = ? AND field_name = ''`,
		businessID,
		changeKind,
	).Scan(&count); err != nil {
		t.Fatalf("count %s change rows: %v", changeKind, err)
	}

	return count
}

func incrementalSummaries(t *testing.T, db *sql.DB, jobID string) []incrementalSummary {
	t.Helper()

	rows, err := db.Query(
		`SELECT context FROM job_events WHERE job_id = ? AND type = 'incremental-summary' ORDER BY id`,
		jobID,
	)
	if err != nil {
		t.Fatalf("read incremental-summary events: %v", err)
	}
	defer rows.Close()

	summaries := make([]incrementalSummary, 0, 4)
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			t.Fatalf("scan incremental-summary event: %v", err)
		}
		var summary incrementalSummary
		if err := json.Unmarshal([]byte(encoded), &summary); err != nil {
			t.Fatalf("decode incremental-summary context %q: %v", encoded, err)
		}
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read incremental-summary events: %v", err)
	}

	return summaries
}
