package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// The entity-resolution tests drive the real import path end to end: every
// scenario writes a legacy CSV, imports it, and asserts on the stored
// businesses, identity keys, provenance columns, and duplicate candidates.

var entityResolutionBase = time.Unix(1_750_000_000, 0).UTC()

func importEntityRows(
	t *testing.T,
	repository *repo,
	jobID string,
	offset time.Duration,
	rows ...map[string]string,
) web.ResultFileImport {
	t.Helper()

	ctx := context.Background()
	job := resultImportJob(jobID, entityResolutionBase.Add(offset))
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("create job %s: %v", jobID, err)
	}
	path := filepath.Join(t.TempDir(), jobID+".csv")
	writeLegacyResultRows(t, path, rows...)
	summary, err := repository.ImportLegacyCSV(ctx, job, path)
	if err != nil {
		t.Fatalf("import %s: %v", jobID, err)
	}

	return summary
}

func liveBusinessCount(t *testing.T, repository *repo) int {
	t.Helper()

	var count int
	if err := repository.db.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM businesses WHERE deleted_at IS NULL AND merged_into_id IS NULL`,
	).Scan(&count); err != nil {
		t.Fatalf("count businesses: %v", err)
	}

	return count
}

func businessIdentity(t *testing.T, repository *repo, id string) (method string, confidence float64, evidence string) {
	t.Helper()

	if err := repository.db.QueryRowContext(
		context.Background(),
		`SELECT COALESCE(identity_method, ''), COALESCE(identity_confidence, 0),
			COALESCE(identity_evidence, '[]')
		FROM businesses WHERE id = ?`,
		id,
	).Scan(&method, &confidence, &evidence); err != nil {
		t.Fatalf("read identity provenance for %s: %v", id, err)
	}

	return method, confidence, evidence
}

func businessIDByPlaceKey(t *testing.T, repository *repo, placeID string) string {
	t.Helper()

	var id string
	if err := repository.db.QueryRowContext(
		context.Background(),
		`SELECT business_id FROM business_identity_keys
		WHERE key_type = 'place_id' AND key_value = ? ORDER BY created_at, business_id LIMIT 1`,
		placeID,
	).Scan(&id); err != nil {
		t.Fatalf("find business by place_id %s: %v", placeID, err)
	}

	return id
}

func pendingCandidateCount(t *testing.T, repository *repo, leftID, rightID string) int {
	t.Helper()

	var count int
	if err := repository.db.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM duplicate_candidates
		WHERE state = 'pending' AND (
			(left_business_id = ? AND right_business_id = ?)
			OR (left_business_id = ? AND right_business_id = ?)
		)`,
		leftID, rightID, rightID, leftID,
	).Scan(&count); err != nil {
		t.Fatalf("count pending candidates: %v", err)
	}

	return count
}

func identityDriftChangeCount(t *testing.T, repository *repo, businessID string) int {
	t.Helper()

	var count int
	if err := repository.db.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM business_changes
		WHERE business_id = ? AND change_kind = 'identity_drift'`,
		businessID,
	).Scan(&count); err != nil {
		t.Fatalf("count identity drift changes: %v", err)
	}

	return count
}

func TestRediscoveryWithoutPlaceIDAttachesWithProvenance(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	original := map[string]string{
		"title": "Harbor Dental", "category": "Dentist", "place_id": "redisc-p1",
		"address": "1 Market St, San Francisco, CA 94105, United States",
		"phone":   "+1 415 555 0100", "latitude": "37.7890", "longitude": "-122.3940",
	}
	rediscovered := map[string]string{
		"title": "Harbor Dental", "category": "Dentist",
		"address": "1 Market St, San Francisco, CA 94105, United States",
		"phone":   "+1 415 555 0100", "latitude": "37.7890", "longitude": "-122.3940",
	}

	importEntityRows(t, repository, "er-redisc-1", 0, original)
	importEntityRows(t, repository, "er-redisc-2", time.Hour, rediscovered)

	if count := liveBusinessCount(t, repository); count != 1 {
		t.Fatalf("business count = %d, want 1 (rediscovery must attach)", count)
	}

	id := businessIDByPlaceKey(t, repository, "redisc-p1")
	method, confidence, evidence := businessIdentity(t, repository, id)
	if method != "phone_corroborated" {
		t.Fatalf("identity_method = %q, want phone_corroborated", method)
	}
	if confidence != 0.9 {
		t.Fatalf("identity_confidence = %v, want 0.9", confidence)
	}
	if !strings.Contains(evidence, `"phone"`) || !strings.Contains(evidence, `"name_similarity"`) {
		t.Fatalf("identity_evidence = %s, want phone and name_similarity signals", evidence)
	}

	detail, err := repository.GetBusiness(context.Background(), id)
	if err != nil {
		t.Fatalf("GetBusiness() error = %v", err)
	}
	if detail.IdentityMethod != "phone_corroborated" || detail.IdentityConfidence == nil ||
		*detail.IdentityConfidence != 0.9 || detail.IdentityEvidence == "" {
		t.Fatalf("detail identity = %q/%v/%q", detail.IdentityMethod, detail.IdentityConfidence, detail.IdentityEvidence)
	}

	// Re-importing the same rediscovery CSV under a new job stays a no-op:
	// same single business, same provenance, still no review candidates.
	importEntityRows(t, repository, "er-redisc-3", 2*time.Hour, rediscovered)
	if count := liveBusinessCount(t, repository); count != 1 {
		t.Fatalf("business count after re-import = %d, want 1", count)
	}
	methodAfter, confidenceAfter, evidenceAfter := businessIdentity(t, repository, id)
	if methodAfter != method || confidenceAfter != confidence || evidenceAfter != evidence {
		t.Fatalf("re-import flipped provenance: %q/%v/%s", methodAfter, confidenceAfter, evidenceAfter)
	}
	var pending int
	if err := repository.db.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM duplicate_candidates WHERE state = 'pending'`,
	).Scan(&pending); err != nil {
		t.Fatalf("count pending candidates: %v", err)
	}
	if pending != 0 {
		t.Fatalf("pending candidates = %d, want 0", pending)
	}
}

func TestPlaceIDDriftAttachesRecordsBothKeysAndStaysIdempotent(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	shared := map[string]string{
		"title": "Golden Gate Optometry", "category": "Optometrist",
		"address": "800 Golden Gate Ave, San Francisco, CA 94102, United States",
		"phone":   "+1 415 555 0111", "latitude": "37.7810", "longitude": "-122.4220",
	}
	withPlaceID := func(placeID string) map[string]string {
		row := make(map[string]string, len(shared)+1)
		for key, value := range shared {
			row[key] = value
		}
		row["place_id"] = placeID

		return row
	}

	importEntityRows(t, repository, "er-drift-1", 0, withPlaceID("drift-p1"))
	importEntityRows(t, repository, "er-drift-2", time.Hour, withPlaceID("drift-p2"))

	if count := liveBusinessCount(t, repository); count != 1 {
		t.Fatalf("business count = %d, want 1 (drift must attach)", count)
	}

	id := businessIDByPlaceKey(t, repository, "drift-p1")
	if other := businessIDByPlaceKey(t, repository, "drift-p2"); other != id {
		t.Fatalf("place_id keys resolve to different rows: %s vs %s", id, other)
	}

	var storedPlaceID string
	if err := repository.db.QueryRowContext(
		context.Background(),
		`SELECT COALESCE(place_id, '') FROM businesses WHERE id = ?`, id,
	).Scan(&storedPlaceID); err != nil {
		t.Fatalf("read stored place_id: %v", err)
	}
	if storedPlaceID != "drift-p2" {
		t.Fatalf("businesses.place_id = %q, want drift-p2", storedPlaceID)
	}

	method, confidence, evidence := businessIdentity(t, repository, id)
	if method != "phone_corroborated" || confidence != 0.9 {
		t.Fatalf("identity = %q/%v, want phone_corroborated/0.9", method, confidence)
	}
	if !strings.Contains(evidence, "identity_keys_added") {
		t.Fatalf("identity_evidence = %s, want identity_keys_added entry", evidence)
	}
	if drifts := identityDriftChangeCount(t, repository, id); drifts != 1 {
		t.Fatalf("identity_drift changes = %d, want 1", drifts)
	}

	// A later import with either place_id resolves exactly to the same row
	// and never rewrites the recorded provenance.
	oldOnly := map[string]string{
		"title": "Golden Gate Optometry", "category": "Optometrist",
		"place_id": "drift-p1",
		"address":  "800 Golden Gate Ave, San Francisco, CA 94102, United States",
	}
	newOnly := map[string]string{
		"title": "Golden Gate Optometry", "category": "Optometrist",
		"place_id": "drift-p2",
		"address":  "800 Golden Gate Ave, San Francisco, CA 94102, United States",
	}
	importEntityRows(t, repository, "er-drift-3", 2*time.Hour, oldOnly)
	importEntityRows(t, repository, "er-drift-4", 3*time.Hour, newOnly)
	if count := liveBusinessCount(t, repository); count != 1 {
		t.Fatalf("business count after key follow-ups = %d, want 1", count)
	}
	if methodAfter, _, _ := businessIdentity(t, repository, id); methodAfter != "phone_corroborated" {
		t.Fatalf("exact re-import rewrote identity_method to %q", methodAfter)
	}

	// Re-importing the drifted row is idempotent: no second drift entry.
	importEntityRows(t, repository, "er-drift-5", 4*time.Hour, withPlaceID("drift-p2"))
	if drifts := identityDriftChangeCount(t, repository, id); drifts != 1 {
		t.Fatalf("identity_drift changes after re-import = %d, want 1", drifts)
	}
}

func TestChainSharedContactPointNeverAutoMerges(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	denver := map[string]string{
		"title": "Acme Plumbing", "category": "Plumber",
		"address": "100 Denver Way, Denver, CO 80014, United States",
		"phone":   "+1 303 555 0100", "website": "https://acmeplumbing.example",
		"latitude": "39.7000", "longitude": "-104.9000",
	}
	boulder := map[string]string{
		"title": "Acme Plumbing", "category": "Plumber",
		"address": "200 Boulder Ave, Boulder, CO 80301, United States",
		"phone":   "+1 303 555 0100", "website": "https://acmeplumbing.example",
		"latitude": "39.7500", "longitude": "-104.9000",
	}

	importEntityRows(t, repository, "er-chain-1", 0, denver)
	importEntityRows(t, repository, "er-chain-2", time.Hour, boulder)

	if count := liveBusinessCount(t, repository); count != 2 {
		t.Fatalf("business count = %d, want 2 (chain locations must stay separate)", count)
	}

	var denverID, boulderID string
	if err := repository.db.QueryRowContext(
		context.Background(),
		`SELECT id FROM businesses WHERE normalized_address LIKE '%denver%'`,
	).Scan(&denverID); err != nil {
		t.Fatalf("find denver row: %v", err)
	}
	if err := repository.db.QueryRowContext(
		context.Background(),
		`SELECT id FROM businesses WHERE normalized_address LIKE '%boulder%'`,
	).Scan(&boulderID); err != nil {
		t.Fatalf("find boulder row: %v", err)
	}

	// Identical unclaimed names with a shared contact point earn a review
	// candidate, never a merge.
	if count := pendingCandidateCount(t, repository, denverID, boulderID); count == 0 {
		t.Fatalf("expected a pending review candidate for the chain pair")
	}

	// New rows without authoritative keys carry weaker identity provenance.
	method, confidence, _ := businessIdentity(t, repository, boulderID)
	if method != "new" || confidence != 0.7 {
		t.Fatalf("chain row identity = %q/%v, want new/0.7", method, confidence)
	}

	// A third location with a distinct name and the same phone/domain also
	// stays separate even though the phone tier alone would score 0.9.
	louisville := map[string]string{
		"title": "Acme Plumbing Louisville", "category": "Plumber",
		"address": "300 Main St, Louisville, CO 80027, United States",
		"phone":   "+1 303 555 0100", "website": "https://acmeplumbing.example",
		"latitude": "39.9800", "longitude": "-105.1300",
	}
	importEntityRows(t, repository, "er-chain-3", 2*time.Hour, louisville)
	if count := liveBusinessCount(t, repository); count != 3 {
		t.Fatalf("business count = %d, want 3 (distinct chain location must stay separate)", count)
	}
}

func TestConflictingPlaceIDsFileCandidateInsteadOfMerging(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	first := map[string]string{
		"title": "Harbor Dental", "category": "Dentist", "place_id": "conf-p1",
		"address": "500 Mission St, San Francisco, CA 94105, United States",
	}
	second := map[string]string{
		"title": "Harbor Dental Care", "category": "Dentist", "place_id": "conf-p2",
		"address": "500 Mission St, San Francisco, CA 94105, United States",
	}

	importEntityRows(t, repository, "er-conf-1", 0, first)
	importEntityRows(t, repository, "er-conf-2", time.Hour, second)

	if count := liveBusinessCount(t, repository); count != 2 {
		t.Fatalf("business count = %d, want 2 (conflicting place_ids must not merge)", count)
	}

	firstID := businessIDByPlaceKey(t, repository, "conf-p1")
	secondID := businessIDByPlaceKey(t, repository, "conf-p2")
	if firstID == secondID {
		t.Fatalf("conflicting place_ids collapsed into one row %s", firstID)
	}
	if count := pendingCandidateCount(t, repository, firstID, secondID); count == 0 {
		t.Fatalf("expected a pending review candidate for the conflicting pair")
	}
}

func TestNearDuplicateNamesBecomeReviewCandidateAndRespectRules(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	firstRow := map[string]string{
		"title": "Luna Cafe", "category": "Cafe",
		"address": "12 Pine St, Portland, OR 97204, United States",
		"latitude": "45.5200", "longitude": "-122.6800",
	}
	secondRow := map[string]string{
		"title": "Luna Cafe", "category": "Cafe",
		"address": "14 Pine St, Portland, OR 97204, United States",
		"latitude": "45.5209", "longitude": "-122.6800",
	}

	importEntityRows(t, repository, "er-name-1", 0, firstRow)
	importEntityRows(t, repository, "er-name-2", time.Hour, secondRow)

	if count := liveBusinessCount(t, repository); count != 2 {
		t.Fatalf("business count = %d, want 2 (0.75 name proximity is review-only)", count)
	}

	var firstID, secondID string
	if err := repository.db.QueryRowContext(
		ctx, `SELECT id FROM businesses WHERE normalized_address LIKE '12 pine%'`,
	).Scan(&firstID); err != nil {
		t.Fatalf("find first cafe: %v", err)
	}
	if err := repository.db.QueryRowContext(
		ctx, `SELECT id FROM businesses WHERE normalized_address LIKE '14 pine%'`,
	).Scan(&secondID); err != nil {
		t.Fatalf("find second cafe: %v", err)
	}
	if count := pendingCandidateCount(t, repository, firstID, secondID); count == 0 {
		t.Fatalf("expected a pending review candidate for the near-duplicate names")
	}

	// A human keep-both decision creates a permanent non-match rule.
	var candidateID int64
	if err := repository.db.QueryRowContext(
		ctx,
		`SELECT id FROM duplicate_candidates WHERE state = 'pending'
			AND ((left_business_id = ? AND right_business_id = ?)
				OR (left_business_id = ? AND right_business_id = ?))`,
		firstID, secondID, secondID, firstID,
	).Scan(&candidateID); err != nil {
		t.Fatalf("read candidate id: %v", err)
	}
	if _, err := repository.ResolveDuplicateCandidate(ctx, web.DuplicateDecision{
		CandidateID: candidateID, Action: "keep_both", Note: "two storefronts",
	}); err != nil {
		t.Fatalf("resolve keep_both: %v", err)
	}

	// Even with the resolved candidate gone entirely, the dedup rule keeps
	// re-imports from filing the pair again.
	if _, err := repository.db.ExecContext(
		ctx, `DELETE FROM duplicate_candidates WHERE id = ?`, candidateID,
	); err != nil {
		t.Fatalf("clear resolved candidate: %v", err)
	}
	importEntityRows(t, repository, "er-name-3", 2*time.Hour, secondRow)
	if count := liveBusinessCount(t, repository); count != 2 {
		t.Fatalf("business count after rule = %d, want 2", count)
	}
	if count := pendingCandidateCount(t, repository, firstID, secondID); count != 0 {
		t.Fatalf("keep_separate rule ignored: %d pending candidates re-filed", count)
	}
}

func TestMovedBusinessSamePlaceIDFollowsUpdatePath(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	before := map[string]string{
		"title": "Bay Books", "category": "Book store", "place_id": "move-p1",
		"address": "1 Old Rd, Oakland, CA 94607, United States",
		"phone":   "+1 510 555 0100", "latitude": "37.8040", "longitude": "-122.2710",
	}
	after := map[string]string{
		"title": "Bay Books", "category": "Book store", "place_id": "move-p1",
		"address": "9 New Ave, Oakland, CA 94612, United States",
		"phone":   "+1 510 555 0100", "latitude": "37.8090", "longitude": "-122.2680",
	}

	importEntityRows(t, repository, "er-move-1", 0, before)
	importEntityRows(t, repository, "er-move-2", time.Hour, after)

	if count := liveBusinessCount(t, repository); count != 1 {
		t.Fatalf("business count = %d, want 1 (same place_id must stay exact)", count)
	}

	id := businessIDByPlaceKey(t, repository, "move-p1")
	var address string
	if err := repository.db.QueryRowContext(
		context.Background(),
		`SELECT address FROM businesses WHERE id = ?`, id,
	).Scan(&address); err != nil {
		t.Fatalf("read moved address: %v", err)
	}
	if !strings.HasPrefix(address, "9 New Ave") {
		t.Fatalf("address = %q, want the new location", address)
	}

	// The exact fast path never rewrites the creation provenance.
	method, confidence, _ := businessIdentity(t, repository, id)
	if method != "new" || confidence != 1 {
		t.Fatalf("identity after move = %q/%v, want new/1", method, confidence)
	}
}

func TestKeepSeparateRuleVetoesCorroboratedAttach(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	roseAnchor := map[string]string{
		"title": "Rose Bakery", "category": "Bakery", "place_id": "rose-p1",
		"address": "3 Rose St, Seattle, WA 98101, United States",
		"phone":   "+1 206 555 0100", "latitude": "47.6000", "longitude": "-122.3300",
	}
	roseRediscovered := map[string]string{
		"title": "Rose Bakery", "category": "Bakery",
		"address": "3 Rose St, Seattle, WA 98101, United States",
		"phone":   "+1 206 555 0100", "latitude": "47.6000", "longitude": "-122.3300",
	}

	// Business IDs are deterministic content hashes, so a scratch repository
	// reveals the row ID the rediscovered record will use everywhere.
	scratch, _, closeScratch := newLocalFeatureRepository(t)
	importEntityRows(t, scratch, "er-rule-scratch", 0, roseRediscovered)
	var rediscoveredID string
	if err := scratch.db.QueryRowContext(ctx, `SELECT id FROM businesses`).Scan(&rediscoveredID); err != nil {
		t.Fatalf("read scratch business id: %v", err)
	}
	closeScratch()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	importEntityRows(t, repository, "er-rule-1", 0, roseAnchor)
	anchorID := businessIDByPlaceKey(t, repository, "rose-p1")

	if _, err := repository.db.ExecContext(
		ctx,
		`INSERT INTO dedup_rules(rule_type, left_key, right_key, action, reason, created_at)
		VALUES ('business_pair', ?, ?, 'keep_separate', 'operator decision', ?)`,
		anchorID,
		rediscoveredID,
		entityResolutionBase.Unix(),
	); err != nil {
		t.Fatalf("insert keep_separate rule: %v", err)
	}

	importEntityRows(t, repository, "er-rule-2", time.Hour, roseRediscovered)

	if count := liveBusinessCount(t, repository); count != 2 {
		t.Fatalf("business count = %d, want 2 (rule must veto the 0.9 attach)", count)
	}
	var kept int
	if err := repository.db.QueryRowContext(
		ctx, `SELECT COUNT(*) FROM businesses WHERE id = ?`, rediscoveredID,
	).Scan(&kept); err != nil {
		t.Fatalf("read vetoed row: %v", err)
	}
	if kept != 1 {
		t.Fatalf("rediscovered row id %s missing after veto", rediscoveredID)
	}
	if count := pendingCandidateCount(t, repository, anchorID, rediscoveredID); count != 0 {
		t.Fatalf("keep_separate pair re-filed %d candidates", count)
	}
}

func TestReobservedMergedListingLandsOnSurvivingRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	kept := map[string]string{
		"title": "Cedar Clinic", "category": "Clinic", "place_id": "cedar-keep",
		"address": "10 Cedar Rd, Austin, TX 78701, United States",
		"phone":   "+1 512 555 0100", "latitude": "30.2670", "longitude": "-97.7430",
	}
	duplicate := map[string]string{
		"title": "Cedar Clinic", "category": "Clinic", "place_id": "cedar-dupe",
		"address": "10 Cedar Rd, Austin, TX 78701, United States",
		"phone":   "+1 512 555 0199", "latitude": "30.2670", "longitude": "-97.7430",
	}

	importEntityRows(t, repository, "er-merge-1", 0, kept)
	importEntityRows(t, repository, "er-merge-2", time.Hour, duplicate)

	keptID := businessIDByPlaceKey(t, repository, "cedar-keep")
	dupeID := businessIDByPlaceKey(t, repository, "cedar-dupe")
	if keptID == dupeID {
		t.Fatalf("fixture collapsed prematurely")
	}

	var candidateID int64
	if err := repository.db.QueryRowContext(
		ctx,
		`SELECT id FROM duplicate_candidates WHERE state = 'pending'
			AND ((left_business_id = ? AND right_business_id = ?)
				OR (left_business_id = ? AND right_business_id = ?))`,
		keptID, dupeID, dupeID, keptID,
	).Scan(&candidateID); err != nil {
		t.Fatalf("read candidate for merge fixture: %v", err)
	}
	if _, err := repository.ResolveDuplicateCandidate(ctx, web.DuplicateDecision{
		CandidateID: candidateID, Action: "merge", KeepBusinessID: keptID,
	}); err != nil {
		t.Fatalf("merge fixture pair: %v", err)
	}

	// A rediscovery matching only the merged row's phone must follow the
	// human merge to the surviving record instead of resurrecting the row.
	reobserved := map[string]string{
		"title": "Cedar Clinic", "category": "Clinic",
		"address": "10 Cedar Rd, Austin, TX 78701, United States",
		"phone":   "+1 512 555 0199", "latitude": "30.2670", "longitude": "-97.7430",
	}
	importEntityRows(t, repository, "er-merge-3", 2*time.Hour, reobserved)

	if count := liveBusinessCount(t, repository); count != 1 {
		t.Fatalf("business count = %d, want 1 (merged listing must not resurrect)", count)
	}
	var mergedInto string
	if err := repository.db.QueryRowContext(
		ctx, `SELECT COALESCE(merged_into_id, '') FROM businesses WHERE id = ?`, dupeID,
	).Scan(&mergedInto); err != nil {
		t.Fatalf("read merged row: %v", err)
	}
	if mergedInto != keptID {
		t.Fatalf("merged row now points at %q, want %q", mergedInto, keptID)
	}
}
