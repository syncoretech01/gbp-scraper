package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/prospect"
)

// prospectBatchSize bounds one recompute transaction so a huge workspace can
// never hold a write transaction open for the whole recompute.
const prospectBatchSize = 500

// staticProspectStatuses are the classes prospect.Classify decides from the
// website URL alone. Every other non-empty stored status is audit-dependent,
// so an inconclusive pass keeps it instead of discarding audit evidence.
var staticProspectStatuses = map[string]struct{}{
	prospect.StatusNoWebsite:   {},
	prospect.StatusSocialOnly:  {},
	prospect.StatusFreeBuilder: {},
}

// RecomputeProspects refreshes the stored prospect classification for the
// given businesses, or for every live business when businessIDs is empty.
// It returns how many businesses were processed and records one audit_logs
// row for the whole call.
func (repo *repo) RecomputeProspects(
	ctx context.Context,
	weights prospect.ScoreWeights,
	businessIDs []string,
) (int64, error) {
	if err := weights.Validate(); err != nil {
		return 0, fmt.Errorf("validate prospect score weights: %w", err)
	}
	processed, changed, err := repo.recomputeProspects(ctx, weights, businessIDs)
	if err != nil {
		return processed, err
	}
	if err := repo.writeProspectAuditLog(ctx, "prospects_recomputed", map[string]any{
		"processed": processed,
		"changed":   changed,
	}); err != nil {
		return processed, err
	}

	return processed, nil
}

// ProspectSummary aggregates the stored prospect columns for the dashboard:
// live businesses grouped by status and tier, plus how many carry a score.
func (repo *repo) ProspectSummary(ctx context.Context) (web.ProspectSummary, error) {
	summary := web.ProspectSummary{}

	byStatus, err := repo.prospectCounts(ctx, "prospect_status")
	if err != nil {
		return web.ProspectSummary{}, fmt.Errorf("count prospects by status: %w", err)
	}
	summary.ByStatus = byStatus

	byTier, err := repo.prospectCounts(ctx, "prospect_tier")
	if err != nil {
		return web.ProspectSummary{}, fmt.Errorf("count prospects by tier: %w", err)
	}
	summary.ByTier = byTier

	if err := repo.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM businesses
		WHERE deleted_at IS NULL AND merged_into_id IS NULL AND prospect_score IS NOT NULL`,
	).Scan(&summary.Scored); err != nil {
		return web.ProspectSummary{}, fmt.Errorf("count scored prospects: %w", err)
	}

	return summary, nil
}

// prospectCounts groups live businesses by one of the two prospect label
// columns. The column name is a compile-time constant, never user input.
func (repo *repo) prospectCounts(ctx context.Context, column string) ([]web.DashboardCountPoint, error) {
	query := fmt.Sprintf(
		`SELECT %s, COUNT(*) FROM businesses
		WHERE deleted_at IS NULL AND merged_into_id IS NULL AND %s <> ''
		GROUP BY %s ORDER BY COUNT(*) DESC, %s`,
		column, column, column, column,
	)
	rows, err := repo.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	points := make([]web.DashboardCountPoint, 0)
	for rows.Next() {
		var point web.DashboardCountPoint
		if err := rows.Scan(&point.Label, &point.Value); err != nil {
			_ = rows.Close()

			return nil, err
		}
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()

		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	return points, nil
}

// reclassifyProspectsForJob refreshes the prospect columns for every business
// one import touched. It never fails the caller: any recompute error becomes
// a prospects_recompute_failed audit row and is swallowed.
func (repo *repo) reclassifyProspectsForJob(ctx context.Context, jobID string, weights prospect.ScoreWeights) {
	ids, err := repo.jobProspectBusinessIDs(ctx, jobID)
	if err == nil && len(ids) == 0 {
		return
	}
	var processed, changed int64
	if err == nil {
		processed, changed, err = repo.recomputeProspects(ctx, weights, ids)
	}
	if err != nil {
		_ = repo.writeProspectAuditLog(ctx, "prospects_recompute_failed", map[string]any{
			"job_id": jobID,
			"error":  err.Error(),
		})

		return
	}
	_ = repo.writeProspectAuditLog(ctx, "prospects_recomputed", map[string]any{
		"processed": processed,
		"changed":   changed,
		"job_id":    jobID,
	})
}

// reclassifyProspectsForBusiness refreshes one business after a stored
// website audit. Like the job hook it is error-tolerant: a failure becomes a
// prospects_recompute_failed audit row instead of failing the audit write.
func (repo *repo) reclassifyProspectsForBusiness(ctx context.Context, businessID string) {
	processed, changed, err := repo.recomputeProspects(ctx, prospect.DefaultScoreWeights(), []string{businessID})
	if err != nil {
		_ = repo.writeProspectAuditLog(ctx, "prospects_recompute_failed", map[string]any{
			"business_id": businessID,
			"error":       err.Error(),
		})

		return
	}
	_ = repo.writeProspectAuditLog(ctx, "prospects_recomputed", map[string]any{
		"processed":   processed,
		"changed":     changed,
		"business_id": businessID,
	})
}

// recomputeProspects is the shared engine behind the public recompute and
// both hooks. An empty id list means every live business. Deleted and merged
// businesses are always skipped; the processed count only covers rows that
// were actually classified.
func (repo *repo) recomputeProspects(
	ctx context.Context,
	weights prospect.ScoreWeights,
	businessIDs []string,
) (processed, changed int64, err error) {
	ids := make([]string, 0, len(businessIDs))
	seen := make(map[string]struct{}, len(businessIDs))
	for _, id := range businessIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		ids, err = repo.liveBusinessIDs(ctx)
		if err != nil {
			return 0, 0, err
		}
	}

	currentYear := time.Now().UTC().Year()
	for start := 0; start < len(ids); start += prospectBatchSize {
		end := min(start+prospectBatchSize, len(ids))
		batchProcessed, batchChanged, batchErr := repo.recomputeProspectBatch(ctx, weights, ids[start:end], currentYear)
		if batchErr != nil {
			return processed, changed, batchErr
		}
		processed += batchProcessed
		changed += batchChanged
	}

	return processed, changed, nil
}

// prospectInput is one business's assembled evidence plus the stored status
// used for change detection.
type prospectInput struct {
	businessID   string
	storedStatus string
	signals      prospect.Signals
}

// recomputeProspectBatch classifies one bounded batch inside its own write
// transaction. It only ever writes the six prospect columns and the change
// history: content hashes, versions, deleted_at, and the FTS-indexed columns
// stay untouched (the businesses_fts triggers index name/category/address
// style columns only, so rewriting prospect columns never alters FTS input).
func (repo *repo) recomputeProspectBatch(
	ctx context.Context,
	weights prospect.ScoreWeights,
	ids []string,
	currentYear int,
) (int64, int64, error) {
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	inputs, err := readProspectSignals(ctx, tx, ids, currentYear)
	if err != nil {
		return 0, 0, err
	}

	now := time.Now().UTC().Unix()
	var changed int64
	for _, input := range inputs {
		status, conclusive := prospect.Classify(input.signals)
		if !conclusive {
			// Static classes are recomputable at any time, so only an
			// audit-dependent stored status survives an inconclusive pass.
			_, static := staticProspectStatuses[input.storedStatus]
			if input.storedStatus != "" && !static {
				status = input.storedStatus
			} else {
				status = ""
			}
		}

		var scoreValue any
		tier := ""
		reasonsJSON := "[]"
		if status != "" {
			score, scoredTier, reasons := prospect.Score(status, input.signals, weights)
			scoreValue = score
			tier = scoredTier
			if len(reasons) > 0 {
				reasonsJSON = mustJSON(reasons, "[]")
			}
		}

		if _, err := tx.ExecContext(
			ctx,
			`UPDATE businesses SET
				prospect_status = ?, prospect_score = ?, prospect_tier = ?,
				prospect_signals = ?, prospect_reasons = ?, prospect_updated_at = ?
			WHERE id = ?`,
			status,
			scoreValue,
			tier,
			mustJSON(input.signals, "{}"),
			reasonsJSON,
			now,
			input.businessID,
		); err != nil {
			return 0, 0, fmt.Errorf("store prospect classification: %w", err)
		}

		if status != "" && status != input.storedStatus {
			if err := insertRecordLevelChange(
				ctx, tx, input.businessID, "prospect_status_changed",
				input.storedStatus, status, now,
			); err != nil {
				return 0, 0, err
			}
			changed++
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit prospect recompute batch: %w", err)
	}

	return int64(len(inputs)), changed, nil
}

// readProspectSignals assembles the classification evidence for one batch
// with a single query: the business columns, the latest website audit, email
// presence (mx_status stores the enrichment package values, where a present
// MX record is exactly 'present'), tracker evidence, and the newest
// copyright year seen on the audited pages.
func readProspectSignals(
	ctx context.Context,
	tx *sql.Tx,
	ids []string,
	currentYear int,
) ([]prospectInput, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	query := fmt.Sprintf(
		`SELECT
			businesses.id,
			businesses.website,
			businesses.maps_url,
			businesses.phone,
			businesses.normalized_phone,
			COALESCE(businesses.rating, 0),
			COALESCE(businesses.review_count, 0),
			businesses.prospect_status,
			COALESCE(audits.id, 0),
			COALESCE(audits.reachable, 0),
			COALESCE(audits.status_code, 0),
			COALESCE(audits.https, 0),
			COALESCE(audits.tls_valid, 0),
			COALESCE(audits.certificate_error, ''),
			COALESCE(audits.parked, 0),
			COALESCE(audits.coming_soon, 0),
			COALESCE(audits.placeholder, 0),
			COALESCE(audits.trackers, '[]'),
			EXISTS(SELECT 1 FROM emails WHERE emails.business_id = businesses.id),
			EXISTS(SELECT 1 FROM emails WHERE emails.business_id = businesses.id AND emails.mx_status = 'present'),
			COALESCE((SELECT MAX(pages.copyright_year) FROM website_audit_pages pages WHERE pages.audit_id = audits.id), 0),
			COALESCE((SELECT SUM(pages.size_bytes) FROM website_audit_pages pages WHERE pages.audit_id = audits.id), 0)
		FROM businesses
		LEFT JOIN website_audits audits ON audits.id = (
			SELECT candidate.id FROM website_audits candidate
			WHERE candidate.business_id = businesses.id
			ORDER BY candidate.completed_at DESC, candidate.id DESC
			LIMIT 1
		)
		WHERE businesses.id IN (%s)
			AND businesses.deleted_at IS NULL
			AND businesses.merged_into_id IS NULL
		ORDER BY businesses.id`,
		placeholders,
	)
	args := make([]any, len(ids))
	for index, id := range ids {
		args[index] = id
	}

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read prospect signals: %w", err)
	}
	inputs := make([]prospectInput, 0, len(ids))
	for rows.Next() {
		var (
			input                            prospectInput
			phone, normalizedPhone, trackers string
			auditID                          int64
			reachable, https, tlsValid       int
			parked, comingSoon, placeholder  int
			emailFound, mxPresent            int
			statusCode, copyrightYear        int
			contentBytes                     int64
		)
		if err := rows.Scan(
			&input.businessID,
			&input.signals.WebsiteURL,
			&input.signals.MapsURL,
			&phone,
			&normalizedPhone,
			&input.signals.Rating,
			&input.signals.ReviewCount,
			&input.storedStatus,
			&auditID,
			&reachable,
			&statusCode,
			&https,
			&tlsValid,
			&input.signals.CertificateError,
			&parked,
			&comingSoon,
			&placeholder,
			&trackers,
			&emailFound,
			&mxPresent,
			&copyrightYear,
			&contentBytes,
		); err != nil {
			_ = rows.Close()

			return nil, fmt.Errorf("scan prospect signals: %w", err)
		}
		input.signals.AuditPerformed = auditID > 0
		input.signals.Reachable = reachable != 0
		input.signals.StatusCode = statusCode
		input.signals.HTTPS = https != 0
		input.signals.TLSValid = tlsValid != 0
		input.signals.Parked = parked != 0
		input.signals.ComingSoon = comingSoon != 0
		input.signals.Placeholder = placeholder != 0
		input.signals.ContentBytes = contentBytes
		input.signals.PhonePresent = strings.TrimSpace(phone) != "" || strings.TrimSpace(normalizedPhone) != ""
		input.signals.EmailFound = emailFound != 0
		input.signals.MXPresent = mxPresent != 0
		trackers = strings.TrimSpace(trackers)
		input.signals.HasAdsTag = trackers != "" && trackers != "[]"
		input.signals.CopyrightYear = copyrightYear
		input.signals.CurrentYear = currentYear
		inputs = append(inputs, input)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()

		return nil, fmt.Errorf("read prospect signals: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close prospect signals: %w", err)
	}

	return inputs, nil
}

func (repo *repo) liveBusinessIDs(ctx context.Context) ([]string, error) {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT id FROM businesses
		WHERE deleted_at IS NULL AND merged_into_id IS NULL
		ORDER BY id`,
	)
	if err != nil {
		return nil, fmt.Errorf("read live business IDs: %w", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()

			return nil, fmt.Errorf("scan live business ID: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()

		return nil, fmt.Errorf("read live business IDs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close live business IDs: %w", err)
	}

	return ids, nil
}

func (repo *repo) jobProspectBusinessIDs(ctx context.Context, jobID string) ([]string, error) {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT business_id FROM job_businesses WHERE job_id = ? ORDER BY business_id`,
		jobID,
	)
	if err != nil {
		return nil, fmt.Errorf("read job prospect businesses: %w", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()

			return nil, fmt.Errorf("scan job prospect business: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()

		return nil, fmt.Errorf("read job prospect businesses: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close job prospect businesses: %w", err)
	}

	return ids, nil
}

func (repo *repo) writeProspectAuditLog(ctx context.Context, action string, details map[string]any) error {
	if _, err := repo.db.ExecContext(
		ctx,
		`INSERT INTO audit_logs(action, entity_type, entity_id, details, created_at)
		VALUES (?, 'business', '', ?, ?)`,
		action,
		mustJSON(details, "{}"),
		time.Now().UTC().Unix(),
	); err != nil {
		return fmt.Errorf("write prospect audit log: %w", err)
	}

	return nil
}
