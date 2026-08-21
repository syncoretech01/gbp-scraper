package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// A merge may resolve conflicting fields instead of simply keeping the
// surviving record's values. These tests pin the three rules, the provenance
// that has to follow an adopted value, and the promise that nothing is lost.

func TestMergeWithCompletenessRuleAdoptsTheRicherValue(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	pair, leftID, rightID := seedDuplicatePair(t, repository)

	var before string
	if err := repository.db.QueryRowContext(ctx,
		`SELECT name FROM businesses WHERE id = ?`, leftID,
	).Scan(&before); err != nil {
		t.Fatalf("read surviving name: %v", err)
	}

	var merged string
	if err := repository.db.QueryRowContext(ctx,
		`SELECT name FROM businesses WHERE id = ?`, rightID,
	).Scan(&merged); err != nil {
		t.Fatalf("read merged name: %v", err)
	}

	if len(merged) <= len(before) {
		t.Skipf("fixture produced names %q and %q, which the completeness rule cannot separate", before, merged)
	}

	resolution, err := repository.ResolveDuplicateCandidate(ctx, web.DuplicateDecision{
		CandidateID:    pair.CandidateID,
		Action:         "merge",
		KeepBusinessID: leftID,
		FieldStrategy:  "completeness",
		Note:           "same practice, richer listing",
	})
	if err != nil {
		t.Fatalf("ResolveDuplicateCandidate() error = %v", err)
	}

	if resolution.FieldStrategy != "completeness" {
		t.Fatalf("field strategy = %q, want completeness", resolution.FieldStrategy)
	}

	if len(resolution.PreferredFields) == 0 {
		t.Fatal("no field was adopted, want at least the business name")
	}

	var after string
	if err := repository.db.QueryRowContext(ctx,
		`SELECT name FROM businesses WHERE id = ?`, leftID,
	).Scan(&after); err != nil {
		t.Fatalf("read merged-in name: %v", err)
	}

	if after != merged {
		t.Fatalf("surviving name = %q, want the adopted %q", after, merged)
	}

	// The replaced value survives as a change row, so the merge stays
	// explainable after the fact.
	var changes int
	if err := repository.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM business_changes
		WHERE business_id = ? AND change_kind = 'merge_preferred_completeness' AND field_name = 'name'`,
		leftID,
	).Scan(&changes); err != nil {
		t.Fatalf("count merge changes: %v", err)
	}

	if changes != 1 {
		t.Fatalf("merge change rows = %d, want 1", changes)
	}

	// The evidence follows the value: exactly one preferred provenance row for
	// the field, and it belongs to the surviving record.
	var preferred int
	if err := repository.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM field_provenance
		WHERE business_id = ? AND field_name = 'name' AND preferred = 1 AND superseded_at IS NULL`,
		leftID,
	).Scan(&preferred); err != nil {
		t.Fatalf("count preferred provenance: %v", err)
	}

	if preferred != 1 {
		t.Fatalf("preferred provenance rows = %d, want 1", preferred)
	}
}

func TestMergeWithoutAFieldRuleKeepsTheSurvivingValues(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	pair, leftID, _ := seedDuplicatePair(t, repository)

	var before string
	if err := repository.db.QueryRowContext(ctx,
		`SELECT name FROM businesses WHERE id = ?`, leftID,
	).Scan(&before); err != nil {
		t.Fatalf("read surviving name: %v", err)
	}

	resolution, err := repository.ResolveDuplicateCandidate(ctx, web.DuplicateDecision{
		CandidateID: pair.CandidateID, Action: "merge", KeepBusinessID: leftID,
	})
	if err != nil {
		t.Fatalf("ResolveDuplicateCandidate() error = %v", err)
	}

	if resolution.FieldStrategy != "" || len(resolution.PreferredFields) != 0 {
		t.Fatalf("resolution = %+v, want no field rule applied", resolution)
	}

	var after string
	if err := repository.db.QueryRowContext(ctx,
		`SELECT name FROM businesses WHERE id = ?`, leftID,
	).Scan(&after); err != nil {
		t.Fatalf("read name after merge: %v", err)
	}

	if after != before {
		t.Fatalf("surviving name = %q, want the unchanged %q", after, before)
	}
}

func TestPreferMergedValueImplementsEachRule(t *testing.T) {
	t.Parallel()

	older := time.Now().UTC().Add(-2 * time.Hour).Unix()
	newer := time.Now().UTC().Unix()

	tests := []struct {
		name     string
		strategy string
		source   mergeFieldObservation
		target   mergeFieldObservation
		want     bool
	}{
		{
			name: "an empty candidate never wins", strategy: "completeness",
			source: mergeFieldObservation{value: ""},
			target: mergeFieldObservation{value: "Harbor Dental"},
		},
		{
			name: "an empty survivor always loses", strategy: "confidence",
			source: mergeFieldObservation{value: "Harbor Dental"},
			target: mergeFieldObservation{value: ""},
			want:   true,
		},
		{
			name: "confidence prefers the better evidence", strategy: "confidence",
			source: mergeFieldObservation{value: "Harbor Dental Care", confidence: 0.9, hasEvidence: true},
			target: mergeFieldObservation{value: "Harbor Dental", confidence: 0.4, hasEvidence: true},
			want:   true,
		},
		{
			name: "confidence keeps the better evidenced survivor", strategy: "confidence",
			source: mergeFieldObservation{value: "Harbor Dental Care", confidence: 0.2, hasEvidence: true},
			target: mergeFieldObservation{value: "Harbor Dental", confidence: 0.8, hasEvidence: true},
		},
		{
			name: "recency prefers the later observation", strategy: "recency",
			source: mergeFieldObservation{value: "Harbor Dental Care", extractedAt: newer, hasEvidence: true},
			target: mergeFieldObservation{value: "Harbor Dental", extractedAt: older, hasEvidence: true},
			want:   true,
		},
		{
			name: "recency keeps the later survivor", strategy: "recency",
			source: mergeFieldObservation{value: "Harbor Dental Care", extractedAt: older, hasEvidence: true},
			target: mergeFieldObservation{value: "Harbor Dental", extractedAt: newer, hasEvidence: true},
		},
		{
			name: "completeness prefers the longer value", strategy: "completeness",
			source: mergeFieldObservation{value: "Harbor Dental Care"},
			target: mergeFieldObservation{value: "Harbor"},
			want:   true,
		},
		{
			name: "an unknown rule changes nothing", strategy: "coin-flip",
			source: mergeFieldObservation{value: "Harbor Dental Care"},
			target: mergeFieldObservation{value: "Harbor"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := preferMergedValue(test.strategy, test.source, test.target); got != test.want {
				t.Fatalf("preferMergedValue(%q) = %v, want %v", test.strategy, got, test.want)
			}
		})
	}
}

func TestMergePreferredFieldsCoverTheAdvertisedColumns(t *testing.T) {
	t.Parallel()

	want := map[string]bool{"name": true, "category": true, "address": true, "phone": true, "website": true}
	for _, field := range mergePreferredFieldNames() {
		if !want[field] {
			t.Errorf("unexpected mergeable field %q", field)
		}

		delete(want, field)
	}

	if len(want) != 0 {
		t.Fatalf("missing mergeable fields: %v", want)
	}
}
