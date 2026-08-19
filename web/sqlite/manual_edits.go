package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/resultimport"
)

// Manual field edits are stored non-destructively: the business column changes,
// while the previous value survives as superseded field provenance, a
// business_changes row records the before/after pair, and audit_logs records
// who asked for the edit and why. Everything happens in one transaction so a
// failure never leaves a value without its paper trail. The businesses_fts_*
// UPDATE trigger refreshes the search index automatically, so no manual FTS
// maintenance is needed here.

// manualEditColumns maps an editable field to the businesses column it updates.
//
//nolint:gochecknoglobals // Immutable lookup tables are safe to share.
var manualEditColumns = map[string]string{
	"name":     "name",
	"phone":    "phone",
	"website":  "website",
	"category": "primary_category",
}

// ApplyManualFieldEdit applies one validated operator correction.
func (repo *repo) ApplyManualFieldEdit(
	ctx context.Context,
	edit web.ManualFieldEdit,
) (web.ManualFieldEditResult, error) {
	column, ok := manualEditColumns[edit.Field]
	if !ok {
		return web.ManualFieldEditResult{}, fmt.Errorf("%w: unknown field %q", web.ErrInvalidManualEdit, edit.Field)
	}

	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return web.ManualFieldEditResult{}, fmt.Errorf("begin manual field edit: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var previous string

	err = tx.QueryRowContext(
		ctx,
		`SELECT `+column+` FROM businesses WHERE id = ? AND deleted_at IS NULL`,
		edit.BusinessID,
	).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return web.ManualFieldEditResult{}, fmt.Errorf("%w: %s", web.ErrBusinessNotFound, edit.BusinessID)
	}

	if err != nil {
		return web.ManualFieldEditResult{}, fmt.Errorf("read business field: %w", err)
	}

	now := time.Now().UTC().Unix()
	stored, assignments, arguments := manualEditAssignments(column, edit)
	arguments = append(arguments, now, now, edit.BusinessID)

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE businesses SET `+assignments+`, last_changed_at = ?, updated_at = ? WHERE id = ?`,
		arguments...,
	); err != nil {
		return web.ManualFieldEditResult{}, fmt.Errorf("update business field: %w", err)
	}

	if err := storeManualEditProvenance(ctx, tx, edit, previous, stored, now); err != nil {
		return web.ManualFieldEditResult{}, err
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO business_changes(
			business_id, field_name, before_value, after_value, change_kind, detected_at
		) VALUES (?, ?, ?, ?, 'manual_edit', ?)`,
		edit.BusinessID,
		edit.Field,
		mustJSON(previous, `""`),
		mustJSON(stored, `""`),
		now,
	); err != nil {
		return web.ManualFieldEditResult{}, fmt.Errorf("record manual edit change: %w", err)
	}

	details, err := json.Marshal(map[string]string{
		"field": edit.Field, "operator": edit.Operator, "reason": edit.Reason,
	})
	if err != nil {
		return web.ManualFieldEditResult{}, fmt.Errorf("encode manual edit audit detail: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO audit_logs(action, entity_type, entity_id, details, created_at)
		VALUES ('business_field_edited', 'business', ?, ?, ?)`,
		edit.BusinessID,
		string(details),
		now,
	); err != nil {
		return web.ManualFieldEditResult{}, fmt.Errorf("record manual edit audit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return web.ManualFieldEditResult{}, fmt.Errorf("commit manual field edit: %w", err)
	}

	return web.ManualFieldEditResult{
		BusinessID:    edit.BusinessID,
		Field:         edit.Field,
		Value:         stored,
		PreviousValue: previous,
	}, nil
}

// manualEditAssignments builds the SET clause for one edit and returns the
// value that ends up in the primary column. Derived columns are refreshed with
// the exact normalization the CSV import uses (package resultimport), which is
// reachable from here: normalized_phone via NormalizePhone and domain via
// NormalizeURL. When the normalizer rejects a value the raw input is stored and
// the derived column is left untouched rather than guessed at; an empty value
// clears the derived column together with the primary one.
func manualEditAssignments(column string, edit web.ManualFieldEdit) (string, string, []any) {
	stored := edit.Value
	assignments := column + " = ?"
	arguments := []any{}

	switch edit.Field {
	case "name":
		assignments += ", normalized_name = ?"
		arguments = append(arguments, stored, resultimport.NormalizeName(stored))
	case "phone":
		switch phone := resultimport.NormalizePhone(stored, ""); {
		case stored == "":
			assignments += ", normalized_phone = ''"
			arguments = append(arguments, stored)
		case phone.Valid:
			assignments += ", normalized_phone = ?"
			arguments = append(arguments, stored, phone.Normalized)
		default:
			arguments = append(arguments, stored)
		}
	case "website":
		switch site, err := resultimport.NormalizeURL(stored); {
		case stored == "":
			assignments += ", domain = ''"
			arguments = append(arguments, stored)
		case err == nil:
			stored = site.Canonical
			assignments += ", domain = ?"
			arguments = append(arguments, stored, site.Domain)
		default:
			arguments = append(arguments, stored)
		}
	default:
		arguments = append(arguments, stored)
	}

	return stored, assignments, arguments
}

// storeManualEditProvenance supersedes the current preferred observation for
// the field and inserts the manual edit as the new preferred value, keeping the
// previous value as original_value so the drawer can explain the change.
func storeManualEditProvenance(
	ctx context.Context,
	tx *sql.Tx,
	edit web.ManualFieldEdit,
	previous string,
	stored string,
	observedAt int64,
) error {
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE field_provenance SET preferred = 0, superseded_at = ?
		WHERE business_id = ? AND field_name = ? AND preferred = 1 AND superseded_at IS NULL`,
		observedAt,
		edit.BusinessID,
		edit.Field,
	); err != nil {
		return fmt.Errorf("supersede field provenance: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO field_provenance(
			business_id, field_name, original_value, normalized_value, preferred,
			source_type, source_url, source_query, source_cell, extraction_method,
			confidence, extracted_at, source_id, original_json, normalized_json, value_hash,
			operator, edit_reason
		) VALUES (?, ?, ?, ?, 1, 'manual_edit', '', '', '', 'manual_edit', 1, ?, NULL, ?, ?, ?, ?, ?)`,
		edit.BusinessID,
		edit.Field,
		previous,
		stored,
		observedAt,
		mustJSON(previous, `""`),
		mustJSON(stored, `""`),
		hashText(edit.Field+"\x00"+stored),
		edit.Operator,
		edit.Reason,
	); err != nil {
		return fmt.Errorf("insert manual edit provenance: %w", err)
	}

	return nil
}
