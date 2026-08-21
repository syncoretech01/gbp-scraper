package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// Duplicate resolution is deliberately non-destructive. Merging marks the source
// row as merged into a target and keeps every source observation, version and
// provenance record, so the decision can be explained afterwards and the raw
// evidence never disappears.

// ResolveDuplicateCandidate applies an operator decision to one candidate pair.
func (repo *repo) ResolveDuplicateCandidate(
	ctx context.Context,
	decision web.DuplicateDecision,
) (web.DuplicateResolution, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return web.DuplicateResolution{}, fmt.Errorf("begin duplicate resolution: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var (
		leftID, rightID, state string
		candidateID            int64
	)

	err = tx.QueryRowContext(
		ctx,
		`SELECT id, left_business_id, right_business_id, state
		FROM duplicate_candidates WHERE id = ?`,
		decision.CandidateID,
	).Scan(&candidateID, &leftID, &rightID, &state)

	if errors.Is(err, sql.ErrNoRows) {
		return web.DuplicateResolution{}, fmt.Errorf("%w: candidate %d", web.ErrDuplicateCandidateNotFound, decision.CandidateID)
	}

	if err != nil {
		return web.DuplicateResolution{}, fmt.Errorf("read duplicate candidate: %w", err)
	}

	if state != "pending" {
		return web.DuplicateResolution{}, fmt.Errorf(
			"%w: candidate %d is already %s", web.ErrDuplicateAlreadyResolved, decision.CandidateID, state,
		)
	}

	keepID, mergeID, err := orientDuplicateDecision(decision, leftID, rightID)
	if err != nil {
		return web.DuplicateResolution{}, err
	}

	now := time.Now().UTC().Unix()
	resolution := web.DuplicateResolution{
		CandidateID: candidateID, Action: decision.Action,
		KeptBusinessID: keepID, MergedBusinessID: mergeID,
	}

	switch decision.Action {
	case "merge":
		if err := mergeBusinessInto(ctx, tx, candidateID, mergeID, keepID, decision, now); err != nil {
			return web.DuplicateResolution{}, err
		}

		// The operator may also ask the surviving record to adopt the better
		// value of each conflicting field. Nothing is lost either way: the
		// replaced value is superseded, not deleted.
		adopted, err := applyPreferredValues(ctx, tx, mergeID, keepID, decision.FieldStrategy, now)
		if err != nil {
			return web.DuplicateResolution{}, err
		}

		resolution.FieldStrategy = decision.FieldStrategy
		resolution.PreferredFields = adopted
		resolution.State = "merged"
	case "keep_both":
		// A permanent non-match rule stops the same pair being suggested again.
		if err := insertNonMatchRule(ctx, tx, leftID, rightID, decision.Note, now); err != nil {
			return web.DuplicateResolution{}, err
		}

		resolution.State = "keep_both"
		resolution.MergedBusinessID = ""
	case "ignore":
		resolution.State = "ignored"
		resolution.MergedBusinessID = ""
	default:
		return web.DuplicateResolution{}, fmt.Errorf(
			"%w: action must be merge, keep_both or ignore", web.ErrInvalidDuplicateDecision,
		)
	}

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE duplicate_candidates SET state = ?, resolved_at = ?, resolution_note = ?
		WHERE id = ? AND state = 'pending'`,
		resolution.State,
		now,
		truncateResolutionNote(decision.Note),
		candidateID,
	); err != nil {
		return web.DuplicateResolution{}, fmt.Errorf("resolve duplicate candidate: %w", err)
	}

	details, err := json.Marshal(map[string]any{
		"candidate_id": candidateID, "kept": keepID, "merged": resolution.MergedBusinessID,
		"note":           truncateResolutionNote(decision.Note),
		"field_strategy": resolution.FieldStrategy, "preferred_fields": resolution.PreferredFields,
	})
	if err != nil {
		return web.DuplicateResolution{}, fmt.Errorf("encode duplicate audit detail: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO audit_logs(action, entity_type, entity_id, details, created_at)
		VALUES (?, 'business', ?, ?, ?)`,
		"duplicate_"+resolution.State,
		keepID,
		string(details),
		now,
	); err != nil {
		return web.DuplicateResolution{}, fmt.Errorf("record duplicate audit: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return web.DuplicateResolution{}, fmt.Errorf("commit duplicate resolution: %w", err)
	}

	return resolution, nil
}

// orientDuplicateDecision decides which row survives. The operator may name the
// row to keep; otherwise the record with more evidence wins.
func orientDuplicateDecision(decision web.DuplicateDecision, leftID, rightID string) (string, string, error) {
	keep := strings.TrimSpace(decision.KeepBusinessID)

	switch keep {
	case "":
		return leftID, rightID, nil
	case leftID:
		return leftID, rightID, nil
	case rightID:
		return rightID, leftID, nil
	default:
		return "", "", fmt.Errorf(
			"%w: the chosen business is not part of candidate %d",
			web.ErrInvalidDuplicateDecision, decision.CandidateID,
		)
	}
}

// mergeBusinessInto folds one business into another without deleting anything.
func mergeBusinessInto(
	ctx context.Context,
	tx *sql.Tx,
	candidateID int64,
	sourceID, targetID string,
	decision web.DuplicateDecision,
	now int64,
) error {
	snapshot, err := businessMergeSnapshot(ctx, tx, sourceID)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO business_merges(
			source_business_id, target_business_id, candidate_id,
			source_snapshot, reason, operator, merged_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sourceID,
		targetID,
		candidateID,
		snapshot,
		truncateResolutionNote(decision.Note),
		strings.TrimSpace(decision.Operator),
		now,
	); err != nil {
		return fmt.Errorf("record business merge: %w", err)
	}

	// Source observations move to the surviving record so no query, cell or
	// timestamp is lost by the merge.
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE business_sources SET business_id = ? WHERE business_id = ?`,
		targetID,
		sourceID,
	); err != nil {
		return fmt.Errorf("move merged business sources: %w", err)
	}

	if err := mergeJobBusinessLinks(ctx, tx, sourceID, targetID); err != nil {
		return err
	}

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE businesses SET merged_into_id = ?, change_status = 'merged', updated_at = ?
		WHERE id = ? AND merged_into_id IS NULL`,
		targetID,
		now,
		sourceID,
	); err != nil {
		return fmt.Errorf("mark business as merged: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE businesses SET updated_at = ? WHERE id = ?`,
		now,
		targetID,
	); err != nil {
		return fmt.Errorf("touch merge target: %w", err)
	}

	return nil
}

// mergeJobBusinessLinks folds the merged record's per-job links into the
// surviving record. job_businesses is keyed by (job_id, business_id), so a job
// that saw both records needs its two links combined rather than moved, keeping
// the earliest sighting, the latest sighting and the summed occurrence count.
func mergeJobBusinessLinks(ctx context.Context, tx *sql.Tx, sourceID, targetID string) error {
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE job_businesses AS target SET
			first_seen_at = MIN(target.first_seen_at, source.first_seen_at),
			last_seen_at = MAX(target.last_seen_at, source.last_seen_at),
			occurrence_count = target.occurrence_count + source.occurrence_count,
			is_new = MAX(target.is_new, source.is_new),
			is_changed = MAX(target.is_changed, source.is_changed)
		FROM job_businesses AS source
		WHERE source.business_id = ? AND target.business_id = ? AND target.job_id = source.job_id`,
		sourceID,
		targetID,
	); err != nil {
		return fmt.Errorf("fold merged job links: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM job_businesses
		WHERE business_id = ?
			AND EXISTS (
				SELECT 1 FROM job_businesses AS target
				WHERE target.business_id = ? AND target.job_id = job_businesses.job_id
			)`,
		sourceID,
		targetID,
	); err != nil {
		return fmt.Errorf("drop folded job links: %w", err)
	}

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE job_businesses SET business_id = ? WHERE business_id = ?`,
		targetID,
		sourceID,
	); err != nil {
		return fmt.Errorf("move merged job links: %w", err)
	}

	return nil
}

func businessMergeSnapshot(ctx context.Context, tx *sql.Tx, businessID string) (string, error) {
	var raw sql.NullString

	err := tx.QueryRowContext(
		ctx,
		`SELECT raw_json FROM businesses WHERE id = ?`,
		businessID,
	).Scan(&raw)

	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", web.ErrBusinessNotFound, businessID)
	}

	if err != nil {
		return "", fmt.Errorf("read business snapshot: %w", err)
	}

	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return "{}", nil
	}

	if !json.Valid([]byte(raw.String)) {
		return "{}", nil
	}

	return raw.String, nil
}

// insertNonMatchRule records a permanent "these are different businesses"
// decision so the same pair is never suggested again.
func insertNonMatchRule(ctx context.Context, tx *sql.Tx, leftID, rightID, note string, now int64) error {
	if _, err := tx.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO dedup_rules(rule_type, left_key, right_key, action, reason, created_at)
		VALUES ('business_pair', ?, ?, 'keep_separate', ?, ?)`,
		leftID,
		rightID,
		truncateResolutionNote(note),
		now,
	); err != nil {
		return fmt.Errorf("record non-match rule: %w", err)
	}

	return nil
}

func truncateResolutionNote(note string) string {
	note = strings.TrimSpace(note)
	if len(note) > 500 {
		return note[:500]
	}

	return note
}

// ListDuplicateCandidates returns pending review pairs, highest score first.
func (repo *repo) ListDuplicateCandidates(ctx context.Context, limit int) ([]web.DuplicateReviewPair, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT candidate.id, candidate.score, candidate.signals, candidate.created_at,
			left_business.id, left_business.name, left_business.address, left_business.domain,
			right_business.id, right_business.name, right_business.address, right_business.domain
		FROM duplicate_candidates candidate
		JOIN businesses left_business ON left_business.id = candidate.left_business_id
		JOIN businesses right_business ON right_business.id = candidate.right_business_id
		WHERE candidate.state = 'pending'
			AND left_business.deleted_at IS NULL AND left_business.merged_into_id IS NULL
			AND right_business.deleted_at IS NULL AND right_business.merged_into_id IS NULL
		ORDER BY candidate.score DESC, candidate.id
		LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list duplicate candidates: %w", err)
	}

	defer func() { _ = rows.Close() }()

	pairs := make([]web.DuplicateReviewPair, 0)

	for rows.Next() {
		var (
			pair      web.DuplicateReviewPair
			createdAt int64
		)

		if err := rows.Scan(
			&pair.CandidateID, &pair.Score, &pair.Signals, &createdAt,
			&pair.Left.ID, &pair.Left.Name, &pair.Left.Address, &pair.Left.Domain,
			&pair.Right.ID, &pair.Right.Name, &pair.Right.Address, &pair.Right.Domain,
		); err != nil {
			return nil, fmt.Errorf("scan duplicate candidate: %w", err)
		}

		pair.CreatedAt = time.Unix(createdAt, 0).UTC()
		pairs = append(pairs, pair)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read duplicate candidates: %w", err)
	}

	return pairs, nil
}
