package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web/prospect"
)

// prospectFixturePlaces maps a stable place_id to the fixture business it
// identifies: one with no website, one social-only, one on a free builder,
// and one with a real custom-domain website (inconclusive until audited).
var prospectFixturePlaces = []string{
	"prospect-no-website",
	"prospect-social",
	"prospect-builder",
	"prospect-dead",
}

func newProspectRepository(t *testing.T) *repo {
	t.Helper()

	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete := repository.(*repo)
	t.Cleanup(func() { _ = concrete.db.Close() })

	return concrete
}

func importProspectFixture(t *testing.T, ctx context.Context, concrete *repo, jobID string) map[string]string {
	t.Helper()

	job := resultImportJob(jobID, time.Unix(1_800_000_000, 0).UTC())
	if err := concrete.Create(ctx, &job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), jobID+".csv")
	writeLegacyResultRows(t, path,
		map[string]string{
			"input_id": "seed-nw", "title": "No Website Bakery", "category": "Bakery",
			"address": "10 Pine St, Portland, OR 97205", "phone": "+1 503-555-0101",
			"latitude": "45.5202", "longitude": "-122.6742", "place_id": "prospect-no-website",
		},
		map[string]string{
			"input_id": "seed-social", "title": "Social Cafe", "category": "Cafe",
			"address":  "22 Oak Ave, Portland, OR 97209",
			"website":  "https://www.facebook.com/socialcafe",
			"latitude": "45.5301", "longitude": "-122.6851", "place_id": "prospect-social",
		},
		map[string]string{
			"input_id": "seed-builder", "title": "Builder Salon", "category": "Salon",
			"address":  "31 Elm Rd, Portland, OR 97211",
			"website":  "https://buildersalon.wixsite.com/home",
			"latitude": "45.5405", "longitude": "-122.6503", "place_id": "prospect-builder",
		},
		map[string]string{
			"input_id": "seed-dead", "title": "Dead Plumbing", "category": "Plumber",
			"address":  "44 Cedar Blvd, Portland, OR 97214",
			"website":  "https://stalepipes-plumbing.net",
			"latitude": "45.5122", "longitude": "-122.6301", "place_id": "prospect-dead",
		},
	)
	if _, err := concrete.ImportLegacyCSV(ctx, job, path); err != nil {
		t.Fatalf("ImportLegacyCSV() error = %v", err)
	}

	ids := make(map[string]string, len(prospectFixturePlaces))
	for _, placeID := range prospectFixturePlaces {
		var id string
		if err := concrete.db.QueryRowContext(
			ctx, `SELECT id FROM businesses WHERE place_id = ?`, placeID,
		).Scan(&id); err != nil {
			t.Fatalf("read business for %s: %v", placeID, err)
		}
		ids[placeID] = id
	}

	return ids
}

type prospectState struct {
	status  string
	tier    string
	reasons string
	signals string
	score   sql.NullFloat64
	updated sql.NullInt64
}

func readProspectState(t *testing.T, concrete *repo, businessID string) prospectState {
	t.Helper()

	var state prospectState
	if err := concrete.db.QueryRow(
		`SELECT prospect_status, prospect_tier, prospect_reasons, prospect_signals,
			prospect_score, prospect_updated_at
		FROM businesses WHERE id = ?`,
		businessID,
	).Scan(&state.status, &state.tier, &state.reasons, &state.signals, &state.score, &state.updated); err != nil {
		t.Fatalf("read prospect state for %s: %v", businessID, err)
	}

	return state
}

func prospectChangeCount(t *testing.T, concrete *repo, businessID string) int64 {
	t.Helper()

	var count int64
	if err := concrete.db.QueryRow(
		`SELECT COUNT(*) FROM business_changes
		WHERE business_id = ? AND change_kind = 'prospect_status_changed'`,
		businessID,
	).Scan(&count); err != nil {
		t.Fatalf("count prospect change rows for %s: %v", businessID, err)
	}

	return count
}

func assertScoredProspect(t *testing.T, state prospectState, wantStatus string) {
	t.Helper()

	if state.status != wantStatus {
		t.Fatalf("prospect status = %q, want %q", state.status, wantStatus)
	}
	if !state.score.Valid || state.score.Float64 <= 0 {
		t.Fatalf("prospect score for %s = %+v, want a positive stored score", wantStatus, state.score)
	}
	if state.tier == "" {
		t.Fatalf("prospect tier for %s is empty", wantStatus)
	}
	if state.reasons == "" || state.reasons == "[]" {
		t.Fatalf("prospect reasons for %s = %q, want non-empty JSON", wantStatus, state.reasons)
	}
	if state.signals == "" || state.signals == "{}" {
		t.Fatalf("prospect signals for %s = %q, want recorded signals JSON", wantStatus, state.signals)
	}
	if !state.updated.Valid {
		t.Fatalf("prospect_updated_at for %s not set", wantStatus)
	}
}

func clearProspectColumns(t *testing.T, ctx context.Context, concrete *repo) {
	t.Helper()

	if _, err := concrete.db.ExecContext(
		ctx,
		`UPDATE businesses SET prospect_status = '', prospect_score = NULL,
			prospect_tier = '', prospect_signals = '{}', prospect_reasons = '[]',
			prospect_updated_at = NULL`,
	); err != nil {
		t.Fatalf("clear prospect columns: %v", err)
	}
}

func TestImportHookClassifiesStaticProspectStatuses(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	concrete := newProspectRepository(t)
	ids := importProspectFixture(t, ctx, concrete, "prospect-import-job")

	assertScoredProspect(t, readProspectState(t, concrete, ids["prospect-no-website"]), "NO_WEBSITE")
	assertScoredProspect(t, readProspectState(t, concrete, ids["prospect-social"]), "SOCIAL_ONLY")
	assertScoredProspect(t, readProspectState(t, concrete, ids["prospect-builder"]), "FREE_BUILDER")

	// The custom-domain business has no audit yet, so classification is
	// inconclusive: no status, no score, but the pass is still recorded.
	unaudited := readProspectState(t, concrete, ids["prospect-dead"])
	if unaudited.status != "" || unaudited.score.Valid || unaudited.tier != "" {
		t.Fatalf("unaudited prospect state = %+v, want empty status without score", unaudited)
	}
	if unaudited.reasons != "[]" {
		t.Fatalf("unaudited prospect reasons = %q, want []", unaudited.reasons)
	}
	if !unaudited.updated.Valid {
		t.Fatalf("unaudited business was not processed by the import hook")
	}

	// Each conclusive classification records exactly one status transition;
	// the inconclusive business records none.
	for _, placeID := range []string{"prospect-no-website", "prospect-social", "prospect-builder"} {
		if count := prospectChangeCount(t, concrete, ids[placeID]); count != 1 {
			t.Fatalf("prospect change rows for %s = %d, want 1", placeID, count)
		}
	}
	if count := prospectChangeCount(t, concrete, ids["prospect-dead"]); count != 0 {
		t.Fatalf("prospect change rows for unaudited business = %d, want 0", count)
	}

	var recomputes int64
	if err := concrete.db.QueryRow(
		`SELECT COUNT(*) FROM audit_logs WHERE action = 'prospects_recomputed'`,
	).Scan(&recomputes); err != nil {
		t.Fatalf("count prospects_recomputed audit rows: %v", err)
	}
	if recomputes != 1 {
		t.Fatalf("prospects_recomputed audit rows = %d, want exactly 1 per import", recomputes)
	}
}

func TestRecomputeProspectsFlipsDeadStatusOnceAndSummarises(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	concrete := newProspectRepository(t)
	ids := importProspectFixture(t, ctx, concrete, "prospect-dead-job")
	deadID := ids["prospect-dead"]

	auditedAt := time.Unix(1_800_100_000, 0).UTC()
	if _, err := concrete.db.ExecContext(
		ctx,
		`INSERT INTO website_audits(
			business_id, requested_url, final_url, reachable, status_code,
			started_at, completed_at
		) VALUES (?, 'https://stalepipes-plumbing.net', '', 0, 0, ?, ?)`,
		deadID,
		auditedAt.Unix(),
		auditedAt.Unix(),
	); err != nil {
		t.Fatalf("seed website audit: %v", err)
	}

	processed, err := concrete.RecomputeProspects(ctx, prospect.DefaultScoreWeights(), nil)
	if err != nil {
		t.Fatalf("RecomputeProspects() error = %v", err)
	}
	if processed != 4 {
		t.Fatalf("RecomputeProspects() processed = %d, want 4", processed)
	}

	assertScoredProspect(t, readProspectState(t, concrete, deadID), "DEAD")
	if count := prospectChangeCount(t, concrete, deadID); count != 1 {
		t.Fatalf("prospect change rows after flip = %d, want 1", count)
	}

	// A second recompute is idempotent: same status, no duplicate change row.
	processed, err = concrete.RecomputeProspects(ctx, prospect.DefaultScoreWeights(), nil)
	if err != nil {
		t.Fatalf("second RecomputeProspects() error = %v", err)
	}
	if processed != 4 {
		t.Fatalf("second RecomputeProspects() processed = %d, want 4", processed)
	}
	assertScoredProspect(t, readProspectState(t, concrete, deadID), "DEAD")
	if count := prospectChangeCount(t, concrete, deadID); count != 1 {
		t.Fatalf("prospect change rows after idempotent rerun = %d, want 1", count)
	}

	// Losing the audit makes classification inconclusive again, and the
	// stored audit-dependent status survives rather than being discarded.
	if _, err := concrete.db.ExecContext(ctx, `DELETE FROM website_audits WHERE business_id = ?`, deadID); err != nil {
		t.Fatalf("delete website audit: %v", err)
	}
	if _, err := concrete.RecomputeProspects(ctx, prospect.DefaultScoreWeights(), nil); err != nil {
		t.Fatalf("RecomputeProspects() after audit removal error = %v", err)
	}
	assertScoredProspect(t, readProspectState(t, concrete, deadID), "DEAD")
	if count := prospectChangeCount(t, concrete, deadID); count != 1 {
		t.Fatalf("prospect change rows after inconclusive rerun = %d, want 1", count)
	}

	summary, err := concrete.ProspectSummary(ctx)
	if err != nil {
		t.Fatalf("ProspectSummary() error = %v", err)
	}
	byStatus := make(map[string]int64, len(summary.ByStatus))
	for _, point := range summary.ByStatus {
		byStatus[point.Label] = point.Value
	}
	for _, status := range []string{"NO_WEBSITE", "SOCIAL_ONLY", "FREE_BUILDER", "DEAD"} {
		if byStatus[status] != 1 {
			t.Fatalf("ProspectSummary() ByStatus[%s] = %d, want 1 (%+v)", status, byStatus[status], summary.ByStatus)
		}
	}
	if summary.Scored != 4 {
		t.Fatalf("ProspectSummary() Scored = %d, want 4", summary.Scored)
	}
	var tiered int64
	for _, point := range summary.ByTier {
		if point.Label == "" {
			t.Fatalf("ProspectSummary() ByTier includes an empty tier: %+v", summary.ByTier)
		}
		tiered += point.Value
	}
	if tiered != 4 {
		t.Fatalf("ProspectSummary() ByTier total = %d, want 4 (%+v)", tiered, summary.ByTier)
	}
}

func TestRecomputeProspectsSkipsDeletedAndMergedAndHonoursExplicitIDs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	concrete := newProspectRepository(t)
	ids := importProspectFixture(t, ctx, concrete, "prospect-scope-job")
	keepID := ids["prospect-no-website"]
	deletedID := ids["prospect-social"]
	mergedID := ids["prospect-builder"]
	unauditedID := ids["prospect-dead"]

	now := time.Now().UTC().Unix()
	if _, err := concrete.db.ExecContext(
		ctx, `UPDATE businesses SET deleted_at = ? WHERE id = ?`, now, deletedID,
	); err != nil {
		t.Fatalf("soft delete business: %v", err)
	}
	if _, err := concrete.db.ExecContext(
		ctx, `UPDATE businesses SET merged_into_id = ? WHERE id = ?`, keepID, mergedID,
	); err != nil {
		t.Fatalf("merge business: %v", err)
	}

	clearProspectColumns(t, ctx, concrete)
	processed, err := concrete.RecomputeProspects(ctx, prospect.DefaultScoreWeights(), nil)
	if err != nil {
		t.Fatalf("RecomputeProspects() error = %v", err)
	}
	if processed != 2 {
		t.Fatalf("RecomputeProspects() processed = %d, want 2 live businesses", processed)
	}
	for label, id := range map[string]string{"deleted": deletedID, "merged": mergedID} {
		state := readProspectState(t, concrete, id)
		if state.status != "" || state.score.Valid || state.updated.Valid {
			t.Fatalf("%s business was reclassified: %+v", label, state)
		}
	}
	assertScoredProspect(t, readProspectState(t, concrete, keepID), "NO_WEBSITE")

	// The summary only ever reports live businesses.
	summary, err := concrete.ProspectSummary(ctx)
	if err != nil {
		t.Fatalf("ProspectSummary() error = %v", err)
	}
	if len(summary.ByStatus) != 1 || summary.ByStatus[0].Label != "NO_WEBSITE" || summary.ByStatus[0].Value != 1 {
		t.Fatalf("ProspectSummary() ByStatus = %+v, want only NO_WEBSITE x1", summary.ByStatus)
	}
	if summary.Scored != 1 {
		t.Fatalf("ProspectSummary() Scored = %d, want 1", summary.Scored)
	}

	// An explicit id list touches only those businesses, and a deleted
	// business stays skipped even when requested directly.
	clearProspectColumns(t, ctx, concrete)
	processed, err = concrete.RecomputeProspects(ctx, prospect.DefaultScoreWeights(), []string{keepID})
	if err != nil {
		t.Fatalf("scoped RecomputeProspects() error = %v", err)
	}
	if processed != 1 {
		t.Fatalf("scoped RecomputeProspects() processed = %d, want 1", processed)
	}
	assertScoredProspect(t, readProspectState(t, concrete, keepID), "NO_WEBSITE")
	if state := readProspectState(t, concrete, unauditedID); state.status != "" || state.updated.Valid {
		t.Fatalf("out-of-scope business was touched: %+v", state)
	}

	processed, err = concrete.RecomputeProspects(ctx, prospect.DefaultScoreWeights(), []string{deletedID})
	if err != nil {
		t.Fatalf("RecomputeProspects(deleted) error = %v", err)
	}
	if processed != 0 {
		t.Fatalf("RecomputeProspects(deleted) processed = %d, want 0", processed)
	}
}
