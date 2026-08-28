package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web/enrichment"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// seedAuditedBusiness writes one business, its crawled website row, and one
// completed audit carrying the supplied result.
func seedAuditedBusiness(
	t *testing.T,
	repository *repo,
	businessID string,
	websiteURL string,
	domain string,
	completedAt time.Time,
	result enrichment.Result,
) int64 {
	t.Helper()

	ctx := context.Background()
	stamp := completedAt.UTC().Unix()

	if _, err := repository.db.ExecContext(ctx,
		`INSERT INTO businesses(
			id, canonical_key, name, normalized_name, website, place_id,
			address, first_seen_at, last_seen_at, last_changed_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		businessID, "key-"+businessID, "Shop "+businessID, "shop "+businessID,
		websiteURL, "place-"+businessID, "1 Market St",
		stamp, stamp, stamp, stamp, stamp,
	); err != nil {
		t.Fatalf("seed business %s: %v", businessID, err)
	}

	if _, err := repository.db.ExecContext(ctx,
		`INSERT INTO websites(business_id, url, final_url, domain, status, last_checked_at, pages_checked)
		VALUES (?, ?, ?, ?, 'active', ?, 1)`,
		businessID, websiteURL, websiteURL, domain, stamp,
	); err != nil {
		t.Fatalf("seed website for %s: %v", businessID, err)
	}

	var websiteID int64
	if err := repository.db.QueryRowContext(ctx,
		`SELECT id FROM websites WHERE business_id = ? AND url = ?`, businessID, websiteURL,
	).Scan(&websiteID); err != nil {
		t.Fatalf("read seeded website: %v", err)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode seeded result: %v", err)
	}

	outcome, err := repository.db.ExecContext(ctx,
		`INSERT INTO website_audits(
			business_id, website_id, task_id, requested_url, final_url, reachable,
			status_code, raw_result, error, started_at, completed_at
		) VALUES (?, ?, '', ?, ?, 1, 200, ?, '', ?, ?)`,
		businessID, websiteID, websiteURL, websiteURL, string(encoded), stamp, stamp,
	)
	if err != nil {
		t.Fatalf("seed audit for %s: %v", businessID, err)
	}

	auditID, err := outcome.LastInsertId()
	if err != nil {
		t.Fatalf("read seeded audit id: %v", err)
	}

	return auditID
}

// TestReusableDomainAuditReusesOnlyTheSamePage guards the correctness boundary
// of the domain cache. The live workspace holds 28 businesses whose website is
// a per-business instagram.com path; reusing one business's audit for another's
// path would attribute one shop's contacts to a different shop.
func TestReusableDomainAuditReusesOnlyTheSamePage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	observed := time.Now().UTC().Add(-time.Hour)
	auditID := seedAuditedBusiness(t, repository, "biz-esto",
		"https://www.instagram.com/esto_lts", "instagram.com", observed,
		enrichment.Result{
			RequestedURL: "https://www.instagram.com/esto_lts",
			FinalURL:     "https://www.instagram.com/esto_lts",
			Reachable:    true,
			AuditVersion: enrichment.AuditVersion,
			Emails:       []enrichment.Email{{Address: "shop@esto.example"}},
		})

	notBefore := time.Now().UTC().Add(-24 * time.Hour)

	// The same page, written differently, reuses the evidence.
	entry, found, err := repository.ReusableDomainAudit(
		ctx, "http://instagram.com/esto_lts/", notBefore, enrichment.AuditVersion,
	)
	if err != nil {
		t.Fatalf("ReusableDomainAudit() error = %v", err)
	}
	if !found || entry.AuditID != auditID {
		t.Fatalf("the same page was not reused: found=%v entry=%+v", found, entry)
	}
	if len(entry.Result.Emails) != 1 || entry.Result.Emails[0].Address != "shop@esto.example" {
		t.Fatalf("reused evidence lost its contacts: %+v", entry.Result)
	}

	// A different page on the same registrable domain must be crawled.
	if _, found, err := repository.ReusableDomainAudit(
		ctx, "https://www.instagram.com/a_different_shop", notBefore, enrichment.AuditVersion,
	); err != nil || found {
		t.Fatalf("a different page on the same host was reused: found=%v err=%v", found, err)
	}

	// Evidence from an older extraction ruleset must never be reused.
	if _, found, err := repository.ReusableDomainAudit(
		ctx, "https://www.instagram.com/esto_lts", notBefore, enrichment.AuditVersion+1,
	); err != nil || found {
		t.Fatalf("evidence from an older audit version was reused: found=%v err=%v", found, err)
	}

	// Evidence older than the freshness window must never be reused.
	if _, found, err := repository.ReusableDomainAudit(
		ctx, "https://www.instagram.com/esto_lts",
		time.Now().UTC().Add(-time.Minute), enrichment.AuditVersion,
	); err != nil || found {
		t.Fatalf("stale evidence was reused: found=%v err=%v", found, err)
	}
}

// TestRefreshJobEnrichmentTotalsCorrectsCountersWrittenBeforeEnrichment is the
// headline half of the acceptance defect: the scrape's import writes
// emails_found before a single website has been crawled, and nothing used to
// correct it afterwards.
func TestRefreshJobEnrichmentTotalsCorrectsCountersWrittenBeforeEnrichment(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	job := lifecycleTestJob("job-enrichment-totals", time.Now().UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("CreateWithState() error = %v", err)
	}

	observed := time.Now().UTC()
	seedAuditedBusiness(t, repository, "biz-totals", "https://totals.example",
		"totals.example", observed, enrichment.Result{AuditVersion: enrichment.AuditVersion})

	if _, err := repository.db.ExecContext(ctx,
		`INSERT INTO job_businesses(job_id, business_id, first_seen_at, last_seen_at)
		VALUES (?, 'biz-totals', ?, ?)`,
		job.ID, observed.Unix(), observed.Unix(),
	); err != nil {
		t.Fatalf("link business to job: %v", err)
	}

	// The state after the scrape's own import: a website was found, no email
	// was, because enrichment had not run yet.
	if _, err := repository.db.ExecContext(ctx,
		`UPDATE job_runtime SET websites_found = 1, emails_found = 0 WHERE job_id = ?`, job.ID,
	); err != nil {
		t.Fatalf("seed job runtime counters: %v", err)
	}

	if _, err := repository.db.ExecContext(ctx,
		`INSERT INTO emails(business_id, value, normalized_value, kind, status)
		VALUES ('biz-totals', 'inquiries@totals.example', 'inquiries@totals.example', 'unknown', 'mx-present')`,
	); err != nil {
		t.Fatalf("seed enriched email: %v", err)
	}

	totals, err := repository.RefreshJobEnrichmentTotals(ctx, job.ID)
	if err != nil {
		t.Fatalf("RefreshJobEnrichmentTotals() error = %v", err)
	}

	if totals.EmailAddresses != 1 || totals.WebsitesFound != 1 || totals.BusinessesWithEmail != 1 {
		t.Fatalf("totals = %+v", totals)
	}

	var stored int64
	if err := repository.db.QueryRowContext(ctx,
		`SELECT emails_found FROM job_runtime WHERE job_id = ?`, job.ID,
	).Scan(&stored); err != nil {
		t.Fatalf("read job runtime counters: %v", err)
	}

	if stored != 1 {
		t.Fatalf("job_runtime.emails_found = %d, want 1", stored)
	}

	pending, err := repository.PendingEnrichmentTaskCount(ctx, job.ID)
	if err != nil || pending != 0 {
		t.Fatalf("PendingEnrichmentTaskCount() = %d, %v", pending, err)
	}
}

// TestJobBusinessContactsCarriesEveryMatchIdentifier proves the export
// backfill can find the right CSV row for a business.
func TestJobBusinessContactsCarriesEveryMatchIdentifier(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	job := lifecycleTestJob("job-contacts", time.Now().UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("CreateWithState() error = %v", err)
	}

	observed := time.Now().UTC()
	seedAuditedBusiness(t, repository, "biz-contacts", "https://contacts.example",
		"contacts.example", observed, enrichment.Result{AuditVersion: enrichment.AuditVersion})

	if _, err := repository.db.ExecContext(ctx,
		`INSERT INTO job_businesses(job_id, business_id, first_seen_at, last_seen_at)
		VALUES (?, 'biz-contacts', ?, ?)`,
		job.ID, observed.Unix(), observed.Unix(),
	); err != nil {
		t.Fatalf("link business to job: %v", err)
	}

	for _, address := range []string{"first@contacts.example", "second@contacts.example"} {
		if _, err := repository.db.ExecContext(ctx,
			`INSERT INTO emails(business_id, value, normalized_value, kind, status, relevance)
			VALUES ('biz-contacts', ?, ?, 'unknown', 'syntax-valid', 50)`,
			address, address,
		); err != nil {
			t.Fatalf("seed email %s: %v", address, err)
		}
	}

	contacts, err := repository.JobBusinessContacts(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobBusinessContacts() error = %v", err)
	}

	if len(contacts) != 1 {
		t.Fatalf("contacts = %d, want 1", len(contacts))
	}
	if contacts[0].PlaceID != "place-biz-contacts" || contacts[0].Address != "1 Market St" {
		t.Fatalf("contact identifiers = %+v", contacts[0])
	}
	if len(contacts[0].Emails) != 2 {
		t.Fatalf("contact emails = %v, want both stored addresses", contacts[0].Emails)
	}
}

// TestEnrichmentEmailHygieneReportCountsTheLiveWorkspaceJunk replays the exact
// values the acceptance workspace stored, so the operator-facing count of
// affected rows is measured rather than asserted.
func TestEnrichmentEmailHygieneReportCountsTheLiveWorkspaceJunk(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	observed := time.Now().UTC()
	seedAuditedBusiness(t, repository, "biz-hygiene", "https://hygiene.example",
		"hygiene.example", observed, enrichment.Result{AuditVersion: enrichment.AuditVersion})

	for _, stored := range []struct {
		value  string
		method string
	}{
		{value: "563-2030la@baronart.tattooopen", method: "visible_text"},
		{value: "626-554-7744inquiries@neptunetattoostudio.com", method: "visible_text"},
		{value: "filler@godaddy.combookingsordersmy", method: "visible_text"},
		{value: "filler@godaddy.comhomeshopthe", method: "visible_text"},
		{value: "shop!estatetattoo@gmail.com", method: "visible_text"},
		{value: "info@mantletattoo.com", method: "mailto"},
		{value: "la@baronart.tattoo", method: "structured_data"},
	} {
		if _, err := repository.db.ExecContext(ctx,
			`INSERT INTO emails(business_id, value, normalized_value, kind, status, extraction_method)
			VALUES ('biz-hygiene', ?, ?, 'unknown', 'syntax-valid', ?)`,
			stored.value, stored.value, stored.method,
		); err != nil {
			t.Fatalf("seed stored email %s: %v", stored.value, err)
		}
	}

	report, err := repository.EnrichmentEmailHygieneReport(ctx)
	if err != nil {
		t.Fatalf("EnrichmentEmailHygieneReport() error = %v", err)
	}

	if report.Total != 7 {
		t.Fatalf("classified %d addresses, want 7", report.Total)
	}
	if report.Unusable != 3 {
		t.Fatalf("unusable = %d, want the three welded top-level domains", report.Unusable)
	}
	if report.Repairable != 2 {
		t.Fatalf("repairable = %d, want the phone and punctuation prefixes", report.Repairable)
	}
	if report.Reasons[enrichment.RejectionUnknownTLD] != 3 {
		t.Fatalf("rejection reasons = %v", report.Reasons)
	}
	if report.Methods["visible_text"] != 5 {
		t.Fatalf("affected rows by method = %v, want all five from visible text", report.Methods)
	}
	if report.Affected() != 5 {
		t.Fatalf("affected = %d, want 5 of 7", report.Affected())
	}

	var untouched int64
	if err := repository.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM emails`).Scan(&untouched); err != nil {
		t.Fatalf("count stored emails: %v", err)
	}

	if untouched != 7 {
		t.Fatalf("the report changed stored rows: %d remain, want 7", untouched)
	}
}
