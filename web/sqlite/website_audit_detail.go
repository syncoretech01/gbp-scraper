package sqlite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/enrichment"
)

// attachLatestWebsiteAudits copies the newest immutable audit evidence onto
// each of a business's website rows: the basic content audit, the postal
// addresses the crawl found, the signature detections with their confidence,
// and the optional error capture.
//
// The current websites row deliberately keeps only names and flags, because
// the advanced filters query it with json_each. The richer evidence therefore
// comes from website_audits, which stores it immutably per run.
func attachLatestWebsiteAudits(ctx context.Context, repo *repo, id string, detail *web.BusinessDetail) error {
	if len(detail.Websites) == 0 {
		return nil
	}

	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT website_id, technologies, trackers, error_screenshot_path, raw_result
		FROM website_audits
		WHERE business_id = ? AND website_id IS NOT NULL
		ORDER BY completed_at DESC, id DESC`,
		id,
	)
	if err != nil {
		return fmt.Errorf("read latest website audits: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type auditEvidence struct {
		technologies        []enrichment.Detection
		trackers            []enrichment.Detection
		errorScreenshotPath string
		contentAudit        enrichment.ContentAudit
		addresses           []enrichment.PostalAddress
	}

	newest := make(map[int64]auditEvidence, len(detail.Websites))

	for rows.Next() {
		var (
			websiteID                                           int64
			technologiesJSON, trackersJSON, errorScreenshotPath string
			rawJSON                                             string
		)

		if err := rows.Scan(
			&websiteID, &technologiesJSON, &trackersJSON, &errorScreenshotPath, &rawJSON,
		); err != nil {
			return fmt.Errorf("scan latest website audit: %w", err)
		}

		// The query is newest first, so the first row per website wins.
		if _, seen := newest[websiteID]; seen {
			continue
		}

		evidence := auditEvidence{errorScreenshotPath: errorScreenshotPath}
		_ = json.Unmarshal([]byte(technologiesJSON), &evidence.technologies)
		_ = json.Unmarshal([]byte(trackersJSON), &evidence.trackers)

		var raw enrichment.Result
		if json.Unmarshal([]byte(rawJSON), &raw) == nil {
			evidence.contentAudit = raw.ContentAudit
			evidence.addresses = raw.Addresses
		}

		newest[websiteID] = evidence
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("read latest website audits: %w", err)
	}

	for index := range detail.Websites {
		evidence, found := newest[detail.Websites[index].ID]
		if !found {
			continue
		}

		detail.Websites[index].Detections = evidence.technologies
		detail.Websites[index].TrackerDetections = evidence.trackers
		detail.Websites[index].ErrorScreenshotPath = evidence.errorScreenshotPath
		detail.Websites[index].ContentAudit = evidence.contentAudit
		detail.Websites[index].Addresses = evidence.addresses
	}

	return nil
}
