package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/enrichment"
	"github.com/gosom/google-maps-scraper/web/resultimport"
)

// The local repository is the domain-audit cache. Every completed audit is
// already immutable evidence keyed to a business and a website row, so the
// cache is a lookup over that evidence rather than a second copy of it: last
// checked, live or dead, HTTP status, final URL, TLS, page evidence, and the
// contacts found all live in website_audits and its children.
var _ interface {
	ReusableDomainAudit(context.Context, string, time.Time, int) (web.DomainAuditReuse, bool, error)
	RefreshJobEnrichmentTotals(context.Context, string) (web.JobEnrichmentTotals, error)
	PendingEnrichmentTaskCount(context.Context, string) (int, error)
	JobBusinessContacts(context.Context, string) ([]web.JobBusinessContacts, error)
} = (*repo)(nil)

// maximumReuseCandidates bounds the domain lookup. A registrable domain with
// more recent audits than this is a shared host such as instagram.com, and the
// newest handful are the only ones that can match the requested page anyway.
const maximumReuseCandidates = 25

// ReusableDomainAudit returns the most recent completed audit that another
// business may reuse for websiteURL, or reports that a fresh crawl is needed.
//
// Three conditions must all hold. The audit must be newer than notBefore, so
// the operator's freshness window is respected. It must have been produced by
// at least auditVersion, so evidence from a worse extractor is re-crawled
// rather than propagated. And it must be for the same page, not merely the
// same registrable domain: a workspace holds many businesses whose website is a
// per-business page on a shared host, and reusing across those would attribute
// one business's contacts to another.
func (repo *repo) ReusableDomainAudit(
	ctx context.Context,
	websiteURL string,
	notBefore time.Time,
	auditVersion int,
) (web.DomainAuditReuse, bool, error) {
	wanted := enrichment.SiteKey(websiteURL)
	if wanted == "" {
		return web.DomainAuditReuse{}, false, nil
	}

	domain := resultimport.NormalizeDomain(websiteURL)
	if domain == "" {
		return web.DomainAuditReuse{}, false, nil
	}

	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT audits.id, websites.domain, websites.url, audits.requested_url,
			audits.completed_at, audits.raw_result
		FROM website_audits AS audits
		JOIN websites ON websites.id = audits.website_id
		WHERE websites.domain = ?
			AND audits.completed_at >= ?
			AND audits.error = ''
			AND json_valid(audits.raw_result)
			AND COALESCE(json_extract(audits.raw_result, '$.audit_version'), 0) >= ?
		ORDER BY audits.completed_at DESC, audits.id DESC
		LIMIT ?`,
		domain,
		notBefore.UTC().Unix(),
		auditVersion,
		maximumReuseCandidates,
	)
	if err != nil {
		return web.DomainAuditReuse{}, false, fmt.Errorf("read reusable domain audit: %w", err)
	}

	defer rows.Close()

	for rows.Next() {
		var (
			auditID      int64
			storedDomain string
			websiteRow   string
			requestedURL string
			completedAt  int64
			rawResult    string
		)

		if err := rows.Scan(
			&auditID, &storedDomain, &websiteRow, &requestedURL, &completedAt, &rawResult,
		); err != nil {
			return web.DomainAuditReuse{}, false, fmt.Errorf("scan reusable domain audit: %w", err)
		}

		if enrichment.SiteKey(websiteRow) != wanted && enrichment.SiteKey(requestedURL) != wanted {
			continue
		}

		var result enrichment.Result
		if err := json.Unmarshal([]byte(rawResult), &result); err != nil {
			// Unreadable evidence is not reusable evidence; crawl instead.
			continue
		}

		// A reused audit must never carry the original business's screenshot
		// paths or its own cache provenance forward.
		result.Cache = nil

		return web.DomainAuditReuse{
			AuditID:     auditID,
			Domain:      storedDomain,
			CompletedAt: time.Unix(completedAt, 0).UTC(),
			Result:      result,
		}, true, nil
	}

	if err := rows.Err(); err != nil {
		return web.DomainAuditReuse{}, false, fmt.Errorf("iterate reusable domain audits: %w", err)
	}

	return web.DomainAuditReuse{}, false, nil
}

// PendingEnrichmentTaskCount reports how many of one job's website audits have
// not reached a durable terminal state.
func (repo *repo) PendingEnrichmentTaskCount(ctx context.Context, jobID string) (int, error) {
	var pending int

	if err := repo.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM enrichment_tasks
		WHERE job_id = ? AND state IN ('queued', 'running')`,
		jobID,
	).Scan(&pending); err != nil {
		return 0, fmt.Errorf("count pending enrichment tasks: %w", err)
	}

	return pending, nil
}

// RefreshJobEnrichmentTotals recomputes the per-job counters that website
// enrichment can change after the scrape's own import already wrote them.
//
// The import runs before a single website has been crawled, so a job whose
// addresses all come from enrichment stored emails_found = 0 and kept it. The
// counters are recomputed from the same tables the import used, so the monitor
// headline and the job list agree with what an export can deliver.
func (repo *repo) RefreshJobEnrichmentTotals(
	ctx context.Context,
	jobID string,
) (web.JobEnrichmentTotals, error) {
	totals := web.JobEnrichmentTotals{}

	if err := repo.db.QueryRowContext(
		ctx,
		`SELECT
			(SELECT COUNT(*) FROM job_businesses
				JOIN businesses ON businesses.id = job_businesses.business_id
				WHERE job_businesses.job_id = ? AND businesses.website <> ''),
			(SELECT COUNT(DISTINCT emails.normalized_value) FROM job_businesses
				JOIN emails ON emails.business_id = job_businesses.business_id
				WHERE job_businesses.job_id = ?),
			(SELECT COUNT(DISTINCT job_businesses.business_id) FROM job_businesses
				JOIN emails ON emails.business_id = job_businesses.business_id
				WHERE job_businesses.job_id = ?)`,
		jobID, jobID, jobID,
	).Scan(&totals.WebsitesFound, &totals.EmailAddresses, &totals.BusinessesWithEmail); err != nil {
		return web.JobEnrichmentTotals{}, fmt.Errorf("read job enrichment totals: %w", err)
	}

	if _, err := repo.db.ExecContext(
		ctx,
		`UPDATE job_runtime SET
			websites_found = ?,
			emails_found = ?,
			updated_at = ?
		WHERE job_id = ?`,
		totals.WebsitesFound,
		totals.EmailAddresses,
		time.Now().UTC().Unix(),
		jobID,
	); err != nil {
		return web.JobEnrichmentTotals{}, fmt.Errorf("write job enrichment totals: %w", err)
	}

	return totals, nil
}

// JobBusinessContacts returns one row per business this job observed, with the
// addresses the workspace now holds and every identifier the legacy result
// file can be matched on.
func (repo *repo) JobBusinessContacts(
	ctx context.Context,
	jobID string,
) ([]web.JobBusinessContacts, error) {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT businesses.id, businesses.place_id, businesses.cid, businesses.data_id,
			businesses.name, businesses.address,
			COALESCE((
				SELECT group_concat(ordered.value, char(10)) FROM (
					SELECT emails.value AS value FROM emails
					WHERE emails.business_id = businesses.id
					ORDER BY emails.relevance DESC, emails.rank, emails.id
				) AS ordered
			), '')
		FROM job_businesses
		JOIN businesses ON businesses.id = job_businesses.business_id
		WHERE job_businesses.job_id = ? AND businesses.deleted_at IS NULL
		ORDER BY businesses.id`,
		jobID,
	)
	if err != nil {
		return nil, fmt.Errorf("read job business contacts: %w", err)
	}

	defer rows.Close()

	contacts := make([]web.JobBusinessContacts, 0, 64)

	for rows.Next() {
		var (
			record  web.JobBusinessContacts
			encoded string
		)

		if err := rows.Scan(
			&record.BusinessID, &record.PlaceID, &record.CID, &record.DataID,
			&record.Name, &record.Address, &encoded,
		); err != nil {
			return nil, fmt.Errorf("scan job business contacts: %w", err)
		}

		for _, value := range strings.Split(encoded, "\n") {
			if value = strings.TrimSpace(value); value != "" {
				record.Emails = append(record.Emails, value)
			}
		}

		contacts = append(contacts, record)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate job business contacts: %w", err)
	}

	return contacts, nil
}

// EnrichmentEmailHygieneReport classifies every stored address against the
// current hygiene rules and reports what a re-audit would change. It is a
// report, never a mutation: the operator decides whether to re-audit.
func (repo *repo) EnrichmentEmailHygieneReport(
	ctx context.Context,
) (web.EmailHygieneReport, error) {
	rows, err := repo.db.QueryContext(
		ctx,
		`SELECT emails.value, emails.business_id, emails.extraction_method
		FROM emails ORDER BY emails.value`,
	)
	if err != nil {
		return web.EmailHygieneReport{}, fmt.Errorf("read stored emails: %w", err)
	}

	defer rows.Close()

	report := web.EmailHygieneReport{
		Reasons: map[string]int64{},
		Methods: map[string]int64{},
	}

	for rows.Next() {
		var value, businessID, method string
		if err := rows.Scan(&value, &businessID, &method); err != nil {
			return web.EmailHygieneReport{}, fmt.Errorf("scan stored email: %w", err)
		}

		report.Total++

		verdict := enrichment.ClassifyStoredEmail(value)
		switch {
		case verdict.Rejected:
			report.Unusable++
			report.Reasons[verdict.Reason]++
			report.Methods[method]++

			if len(report.Samples) < web.MaximumEmailHygieneSamples {
				report.Samples = append(report.Samples, web.EmailHygieneSample{
					Value: value, Reason: verdict.Reason, BusinessID: businessID,
				})
			}
		case verdict.Repaired:
			report.Repairable++
			report.Methods[method]++

			if len(report.Samples) < web.MaximumEmailHygieneSamples {
				report.Samples = append(report.Samples, web.EmailHygieneSample{
					Value: value, Repaired: verdict.Address, BusinessID: businessID,
				})
			}
		}
	}

	if err := rows.Err(); err != nil {
		return web.EmailHygieneReport{}, fmt.Errorf("iterate stored emails: %w", err)
	}

	return report, nil
}
