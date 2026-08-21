package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// A merge can do more than hide one row: the operator may ask the surviving
// record to adopt the better value of each conflicting field. The rule is
// chosen per merge, applied field by field, and never destructive — the
// adopted value arrives with the provenance that justified it, the replaced
// value is superseded rather than deleted, and a business_changes row records
// the before/after pair so the merge stays explainable afterwards.

// mergePreferredField pairs a provenance field name with the businesses column
// it writes. Only fields that carry field-level provenance are eligible, so a
// preferred value can always be explained.
type mergePreferredField struct {
	field  string
	column string
}

// mergePreferredFields lists the fields a merge may resolve. Identity columns
// (place_id, cid, data_id) are deliberately absent: they decide identity and
// must not be swapped by a display-value rule.
//
//nolint:gochecknoglobals // Immutable lookup table, safe to share.
var mergePreferredFields = []mergePreferredField{
	{field: "name", column: "name"},
	{field: "category", column: "primary_category"},
	{field: "address", column: "address"},
	{field: "phone", column: "phone"},
	{field: "website", column: "website"},
}

// mergeFieldObservation is one side of a field-level merge comparison.
type mergeFieldObservation struct {
	value       string
	confidence  float64
	extractedAt int64
	provenance  int64
	hasEvidence bool
}

// applyPreferredValues resolves every eligible field between the merged record
// and the surviving one using the chosen rule. It returns the field names the
// surviving record actually adopted.
func applyPreferredValues(
	ctx context.Context,
	tx *sql.Tx,
	sourceID, targetID, strategy string,
	now int64,
) ([]string, error) {
	if strategy == "" {
		return nil, nil
	}

	adopted := make([]string, 0, len(mergePreferredFields))

	for _, candidate := range mergePreferredFields {
		source, err := readMergeFieldObservation(ctx, tx, sourceID, candidate)
		if err != nil {
			return nil, err
		}

		target, err := readMergeFieldObservation(ctx, tx, targetID, candidate)
		if err != nil {
			return nil, err
		}

		if !preferMergedValue(strategy, source, target) {
			continue
		}

		if err := adoptMergedFieldValue(ctx, tx, targetID, candidate, source, target, strategy, now); err != nil {
			return nil, err
		}

		adopted = append(adopted, candidate.field)
	}

	return adopted, nil
}

// readMergeFieldObservation reads one business's current value for a field
// together with the preferred provenance row backing it, if any.
func readMergeFieldObservation(
	ctx context.Context,
	tx *sql.Tx,
	businessID string,
	candidate mergePreferredField,
) (mergeFieldObservation, error) {
	var observation mergeFieldObservation

	err := tx.QueryRowContext(
		ctx,
		`SELECT COALESCE(`+candidate.column+`, '') FROM businesses WHERE id = ?`,
		businessID,
	).Scan(&observation.value)
	if err != nil {
		return mergeFieldObservation{}, fmt.Errorf("read merge field %q: %w", candidate.field, err)
	}

	observation.value = strings.TrimSpace(observation.value)

	var (
		provenanceID sql.NullInt64
		confidence   sql.NullFloat64
		extractedAt  sql.NullInt64
	)

	err = tx.QueryRowContext(
		ctx,
		`SELECT id, confidence, extracted_at FROM field_provenance
		WHERE business_id = ? AND field_name = ? AND preferred = 1 AND superseded_at IS NULL
		ORDER BY extracted_at DESC, id DESC LIMIT 1`,
		businessID,
		candidate.field,
	).Scan(&provenanceID, &confidence, &extractedAt)
	if err != nil && err != sql.ErrNoRows {
		return mergeFieldObservation{}, fmt.Errorf("read merge field provenance %q: %w", candidate.field, err)
	}

	if provenanceID.Valid {
		observation.hasEvidence = true
		observation.provenance = provenanceID.Int64
		observation.confidence = confidence.Float64
		observation.extractedAt = extractedAt.Int64
	}

	return observation, nil
}

// preferMergedValue reports whether the merged record's observation should
// replace the surviving record's under the chosen rule. An empty candidate
// never wins, and a tie always leaves the surviving record untouched.
func preferMergedValue(strategy string, source, target mergeFieldObservation) bool {
	if source.value == "" || source.value == target.value {
		return false
	}

	if target.value == "" {
		return true
	}

	switch strategy {
	case "confidence":
		return source.hasEvidence && (!target.hasEvidence || source.confidence > target.confidence)
	case "recency":
		return source.hasEvidence && (!target.hasEvidence || source.extractedAt > target.extractedAt)
	case "completeness":
		return len(source.value) > len(target.value)
	default:
		return false
	}
}

// adoptMergedFieldValue writes the winning value onto the surviving record,
// moves the evidence that justified it across, supersedes the value it
// replaced, and records the change.
func adoptMergedFieldValue(
	ctx context.Context,
	tx *sql.Tx,
	targetID string,
	candidate mergePreferredField,
	source, target mergeFieldObservation,
	strategy string,
	now int64,
) error {
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE businesses SET `+candidate.column+` = ?, last_changed_at = ?, updated_at = ? WHERE id = ?`,
		source.value,
		now,
		now,
		targetID,
	); err != nil {
		return fmt.Errorf("apply merged field %q: %w", candidate.field, err)
	}

	if target.hasEvidence {
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE field_provenance SET preferred = 0, superseded_at = ? WHERE id = ?`,
			now,
			target.provenance,
		); err != nil {
			return fmt.Errorf("supersede merged field %q: %w", candidate.field, err)
		}
	}

	if source.hasEvidence {
		// The evidence follows the value so the surviving record can still
		// explain where the adopted value came from.
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE field_provenance SET business_id = ?, preferred = 1, superseded_at = NULL WHERE id = ?`,
			targetID,
			source.provenance,
		); err != nil {
			return fmt.Errorf("move merged field provenance %q: %w", candidate.field, err)
		}
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO business_changes(
			business_id, field_name, before_value, after_value, change_kind, detected_at
		) VALUES (?, ?, ?, ?, ?, ?)`,
		targetID,
		candidate.field,
		mustJSON(target.value, `""`),
		mustJSON(source.value, `""`),
		"merge_preferred_"+strategy,
		now,
	); err != nil {
		return fmt.Errorf("record merged field change %q: %w", candidate.field, err)
	}

	return nil
}

// mergePreferredFieldNames lists the fields a preferred-value rule may resolve,
// so callers and tests share one vocabulary.
func mergePreferredFieldNames() []string {
	names := make([]string, 0, len(mergePreferredFields))
	for _, candidate := range mergePreferredFields {
		names = append(names, candidate.field)
	}

	return names
}
