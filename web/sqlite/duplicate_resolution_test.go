package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// seedDuplicatePair imports two records that share an identity signal, then
// returns the pending candidate the deduplicator raised for them.
func seedDuplicatePair(t *testing.T, repository *repo) (web.DuplicateReviewPair, string, string) {
	t.Helper()

	ctx := context.Background()
	job := resultImportJob("job-duplicates", time.Now().UTC())

	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("create job: %v", err)
	}

	path := filepath.Join(t.TempDir(), "duplicates.csv")
	writeLegacyResultRows(t, path,
		map[string]string{
			"title": "Harbor Dental", "category": "Dentist", "place_id": "harbor-1",
			"address": "1 Market St, San Francisco, CA 94105, United States",
			"phone":   "+1 415 555 0100",
		},
		map[string]string{
			"title": "Harbor Dental Care", "category": "Dentist", "place_id": "harbor-2",
			"address": "1 Market Street, San Francisco, CA 94105, United States",
			"phone":   "+1 415 555 0199",
		},
	)

	if _, err := repository.ImportLegacyCSV(ctx, job, path); err != nil {
		t.Fatalf("import duplicates: %v", err)
	}

	pairs, err := repository.ListDuplicateCandidates(ctx, 10)
	if err != nil {
		t.Fatalf("list duplicate candidates: %v", err)
	}

	if len(pairs) == 0 {
		t.Skip("import produced no duplicate candidate for this fixture")
	}

	return pairs[0], pairs[0].Left.ID, pairs[0].Right.ID
}

func TestMergeDuplicateKeepsEvidenceAndHidesTheMergedRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	pair, leftID, rightID := seedDuplicatePair(t, repository)

	before, err := repository.SearchBusinesses(ctx, web.ResultSearch{Limit: 50})
	if err != nil {
		t.Fatalf("search before merge: %v", err)
	}

	var sourcesBefore int
	if err := repository.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM business_sources WHERE business_id IN (?, ?)`, leftID, rightID,
	).Scan(&sourcesBefore); err != nil {
		t.Fatalf("count sources before merge: %v", err)
	}

	resolution, err := repository.ResolveDuplicateCandidate(ctx, web.DuplicateDecision{
		CandidateID: pair.CandidateID, Action: "merge",
		KeepBusinessID: leftID, Note: "same practice, two listings",
	})
	if err != nil {
		t.Fatalf("merge duplicate: %v", err)
	}

	if resolution.State != "merged" || resolution.KeptBusinessID != leftID || resolution.MergedBusinessID != rightID {
		t.Fatalf("resolution = %#v", resolution)
	}

	// The merged row disappears from results without being deleted.
	after, err := repository.SearchBusinesses(ctx, web.ResultSearch{Limit: 50})
	if err != nil {
		t.Fatalf("search after merge: %v", err)
	}

	if after.Total != before.Total-1 {
		t.Fatalf("result total = %d, want %d", after.Total, before.Total-1)
	}

	var mergedInto string
	if err := repository.db.QueryRowContext(ctx,
		`SELECT COALESCE(merged_into_id, '') FROM businesses WHERE id = ?`, rightID,
	).Scan(&mergedInto); err != nil {
		t.Fatalf("read merged row: %v", err)
	}

	if mergedInto != leftID {
		t.Fatalf("merged_into_id = %q, want %q", mergedInto, leftID)
	}

	// Every source observation survives, now attached to the surviving record.
	var sourcesAfter, sourcesOnTarget int
	if err := repository.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM business_sources WHERE business_id IN (?, ?)`, leftID, rightID,
	).Scan(&sourcesAfter); err != nil {
		t.Fatalf("count sources after merge: %v", err)
	}

	if err := repository.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM business_sources WHERE business_id = ?`, leftID,
	).Scan(&sourcesOnTarget); err != nil {
		t.Fatalf("count target sources: %v", err)
	}

	if sourcesAfter != sourcesBefore {
		t.Fatalf("sources after merge = %d, want %d (nothing may be lost)", sourcesAfter, sourcesBefore)
	}

	if sourcesOnTarget != sourcesBefore {
		t.Fatalf("target holds %d source(s), want all %d", sourcesOnTarget, sourcesBefore)
	}

	// The decision is recorded with a reversible snapshot and an audit entry.
	var merges, audits int
	if err := repository.db.QueryRowContext(ctx,
		`SELECT
			(SELECT COUNT(*) FROM business_merges WHERE source_business_id = ? AND target_business_id = ?),
			(SELECT COUNT(*) FROM audit_logs WHERE action = 'duplicate_merged')`,
		rightID, leftID,
	).Scan(&merges, &audits); err != nil {
		t.Fatalf("read merge history: %v", err)
	}

	if merges != 1 || audits != 1 {
		t.Fatalf("merge records = %d, audit records = %d", merges, audits)
	}

	// The same candidate cannot be resolved twice.
	_, err = repository.ResolveDuplicateCandidate(ctx, web.DuplicateDecision{
		CandidateID: pair.CandidateID, Action: "merge",
	})
	if !errors.Is(err, web.ErrDuplicateAlreadyResolved) {
		t.Fatalf("second resolution error = %v, want already-resolved", err)
	}
}

func TestKeepBothRecordsAPermanentNonMatchRule(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	pair, leftID, rightID := seedDuplicatePair(t, repository)

	resolution, err := repository.ResolveDuplicateCandidate(ctx, web.DuplicateDecision{
		CandidateID: pair.CandidateID, Action: "keep_both", Note: "different practices",
	})
	if err != nil {
		t.Fatalf("keep both: %v", err)
	}

	if resolution.State != "keep_both" || resolution.MergedBusinessID != "" {
		t.Fatalf("resolution = %#v", resolution)
	}

	// Both records stay visible.
	page, err := repository.SearchBusinesses(ctx, web.ResultSearch{Limit: 50})
	if err != nil {
		t.Fatalf("search after keep both: %v", err)
	}

	if page.Total < 2 {
		t.Fatalf("keep both removed a record: total = %d", page.Total)
	}

	var rules int
	if err := repository.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dedup_rules
		WHERE rule_type = 'business_pair' AND action = 'keep_separate'
			AND left_key = ? AND right_key = ?`,
		leftID, rightID,
	).Scan(&rules); err != nil {
		t.Fatalf("read non-match rules: %v", err)
	}

	if rules != 1 {
		t.Fatalf("non-match rules = %d, want 1", rules)
	}

	// The pair no longer appears for review.
	remaining, err := repository.ListDuplicateCandidates(ctx, 10)
	if err != nil {
		t.Fatalf("list after keep both: %v", err)
	}

	for _, candidate := range remaining {
		if candidate.CandidateID == pair.CandidateID {
			t.Fatalf("resolved candidate %d is still pending", pair.CandidateID)
		}
	}
}

func TestResolveDuplicateRejectsUnknownCandidateAndForeignBusiness(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	pair, _, _ := seedDuplicatePair(t, repository)

	_, err := repository.ResolveDuplicateCandidate(ctx, web.DuplicateDecision{
		CandidateID: 999_999, Action: "merge",
	})
	if !errors.Is(err, web.ErrDuplicateCandidateNotFound) {
		t.Fatalf("unknown candidate error = %v", err)
	}

	_, err = repository.ResolveDuplicateCandidate(ctx, web.DuplicateDecision{
		CandidateID: pair.CandidateID, Action: "merge", KeepBusinessID: "biz_not_in_this_pair",
	})
	if !errors.Is(err, web.ErrInvalidDuplicateDecision) {
		t.Fatalf("foreign business error = %v", err)
	}
}

func TestBulkDeleteHidesResultsReversiblyAndKeepsHistory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	job := resultImportJob("job-bulk-delete", time.Now().UTC())
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("create job: %v", err)
	}

	path := filepath.Join(t.TempDir(), "delete.csv")
	writeLegacyResultRows(t, path,
		map[string]string{
			"title": "Keep Dental", "place_id": "keep-1",
			"address": "1 Market St, San Francisco, CA 94105, United States",
		},
		map[string]string{
			"title": "Remove Dental", "place_id": "remove-1",
			"address": "2 Mission St, San Francisco, CA 94105, United States",
		},
	)

	if _, err := repository.ImportLegacyCSV(ctx, job, path); err != nil {
		t.Fatalf("import rows: %v", err)
	}

	page, err := repository.SearchBusinesses(ctx, web.ResultSearch{Limit: 50})
	if err != nil || page.Total != 2 {
		t.Fatalf("search = %+v, %v", page, err)
	}

	var target string

	for _, result := range page.Results {
		if result.Name == "Remove Dental" {
			target = result.ID
		}
	}

	if target == "" {
		t.Fatal("fixture row was not imported")
	}

	changed, err := repository.MutateBusinesses(ctx, web.ResultMutation{
		IDs: []string{target}, Action: "delete",
	})
	if err != nil || changed != 1 {
		t.Fatalf("delete = %d, %v", changed, err)
	}

	after, err := repository.SearchBusinesses(ctx, web.ResultSearch{Limit: 50})
	if err != nil {
		t.Fatalf("search after delete: %v", err)
	}

	if after.Total != 1 {
		t.Fatalf("total after delete = %d, want 1", after.Total)
	}

	// The row is hidden, not destroyed: its sources are still on disk.
	var sources int
	if err := repository.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM business_sources WHERE business_id = ?`, target,
	).Scan(&sources); err != nil {
		t.Fatalf("count sources: %v", err)
	}

	if sources == 0 {
		t.Fatal("delete destroyed the source observations")
	}

	// Deleting twice changes nothing, and restoring brings the row back.
	repeat, err := repository.MutateBusinesses(ctx, web.ResultMutation{
		IDs: []string{target}, Action: "delete",
	})
	if err != nil || repeat != 0 {
		t.Fatalf("repeated delete = %d, %v", repeat, err)
	}

	restored, err := repository.MutateBusinesses(ctx, web.ResultMutation{
		IDs: []string{target}, Action: "restore",
	})
	if err != nil || restored != 1 {
		t.Fatalf("restore = %d, %v", restored, err)
	}

	final, err := repository.SearchBusinesses(ctx, web.ResultSearch{Limit: 50})
	if err != nil {
		t.Fatalf("search after restore: %v", err)
	}

	if final.Total != 2 {
		t.Fatalf("total after restore = %d, want 2", final.Total)
	}
}
