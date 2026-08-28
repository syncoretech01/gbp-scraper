package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/enrichment"
	"github.com/gosom/google-maps-scraper/web/prospect"
)

// websiteStateObservedAt is the fixed import timestamp every fixture in this
// file uses, so nothing here depends on the wall clock.
var websiteStateObservedAt = time.Date(2026, time.August, 20, 8, 0, 0, 0, time.UTC)

// websiteStateFixture imports the six shapes the canonical state machine has
// to keep apart:
//
//	state-none     no website at all
//	state-social   an Instagram profile listed as the website
//	state-pinterest a network the first social host list did not recognise
//	state-shared-a  a real site
//	state-shared-b  a second business on the same domain
//	state-solo      a second, distinct domain
func websiteStateFixture(t *testing.T, ctx context.Context, concrete *repo, jobID string) map[string]string {
	t.Helper()

	job := resultImportJob(jobID, websiteStateObservedAt)
	if err := concrete.Create(ctx, &job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), jobID+".csv")
	writeLegacyResultRows(t, path,
		map[string]string{
			"input_id": "none", "title": "No Website Ink", "category": "Tattoo shop",
			"address": "1 First St, Los Angeles, CA 90012", "phone": "+1 213-555-0101",
			"latitude": "34.05", "longitude": "-118.24", "place_id": "state-none",
		},
		map[string]string{
			"input_id": "social", "title": "Brown Pride Tattoos", "category": "Tattoo shop",
			"address": "2 Second St, Los Angeles, CA 90012", "phone": "+1 213-555-0102",
			"website":  "https://www.instagram.com/brownpridetattooshop?hl=en",
			"latitude": "34.06", "longitude": "-118.25", "place_id": "state-social",
		},
		map[string]string{
			"input_id": "pinterest", "title": "Pin Board Tattoo", "category": "Tattoo shop",
			"address": "3 Third St, Los Angeles, CA 90012", "phone": "+1 213-555-0103",
			"website":  "https://www.pinterest.com/pinboardtattoo",
			"latitude": "34.07", "longitude": "-118.26", "place_id": "state-pinterest",
		},
		map[string]string{
			"input_id": "shared-a", "title": "Shared Domain Studio", "category": "Tattoo shop",
			"address": "4 Fourth St, Los Angeles, CA 90012", "phone": "+1 213-555-0104",
			"website":  "https://shared-domain.example",
			"latitude": "34.08", "longitude": "-118.27", "place_id": "state-shared-a",
		},
		map[string]string{
			"input_id": "shared-b", "title": "Shared Domain Studio Annex", "category": "Tattoo shop",
			"address": "5 Fifth St, Los Angeles, CA 90012", "phone": "+1 213-555-0105",
			"website":  "http://www.shared-domain.example/annex",
			"latitude": "34.09", "longitude": "-118.28", "place_id": "state-shared-b",
		},
		map[string]string{
			"input_id": "solo", "title": "Solo Domain Tattoo", "category": "Tattoo shop",
			"address": "6 Sixth St, Los Angeles, CA 90012", "phone": "+1 213-555-0106",
			"website":  "https://solo-domain.example",
			"latitude": "34.10", "longitude": "-118.29", "place_id": "state-solo",
		},
	)
	if _, err := concrete.ImportLegacyCSV(ctx, job, path); err != nil {
		t.Fatalf("ImportLegacyCSV() error = %v", err)
	}

	ids := make(map[string]string, 6)
	for _, placeID := range []string{
		"state-none", "state-social", "state-pinterest",
		"state-shared-a", "state-shared-b", "state-solo",
	} {
		var id string
		if err := concrete.db.QueryRowContext(
			ctx, `SELECT id FROM businesses WHERE place_id = ?`, placeID,
		).Scan(&id); err != nil {
			t.Fatalf("read business for %s: %v", placeID, err)
		}
		ids[placeID] = id
	}

	return ids
}

func websiteStateSweepOptions() web.EnrichmentOptions {
	return web.EnrichmentOptions{
		Scope: "homepage_contact_about", MaxPages: 1, TimeoutSeconds: 10,
		MaxBodyBytes: 2 << 20, MaxRedirects: 10, MaxInternalLinkChecks: 0,
		DisableInternalChecks: true, StaleAfterHours: 24,
	}
}

func websiteStateCount(t *testing.T, summary web.WebsiteStateSummary, state string) int64 {
	t.Helper()

	for _, count := range summary.Counts {
		if count.State == state {
			return count.Count
		}
	}
	t.Fatalf("summary has no bucket for %s: %+v", state, summary.Counts)

	return 0
}

// TestWebsiteAuditSweepDeduplicatesDomainsAndResumes is the regression guard
// for the missing bulk operation: 268 website-bearing businesses on the live
// workspace sat at "never checked" with no way to audit them, and a naive
// bulk audit would have probed the same domain once per duplicate listing.
func TestWebsiteAuditSweepDeduplicatesDomainsAndResumes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	concrete := newProspectRepository(t)
	ids := websiteStateFixture(t, ctx, concrete, "website-state-sweep")

	summary, err := concrete.WebsiteStateSummary(ctx, "website-state-sweep")
	if err != nil {
		t.Fatalf("WebsiteStateSummary() error = %v", err)
	}
	if got := websiteStateCount(t, summary, web.WebsiteStateNeverChecked); got != 3 {
		t.Fatalf("never-checked businesses = %d, want 3", got)
	}
	if got := websiteStateCount(t, summary, web.WebsiteStateSocialOnly); got != 2 {
		t.Fatalf("social-only businesses = %d, want 2", got)
	}
	if got := websiteStateCount(t, summary, web.WebsiteStateNoWebsite); got != 1 {
		t.Fatalf("no-website businesses = %d, want 1", got)
	}

	sweep, err := concrete.StartWebsiteAuditSweep(ctx, web.WebsiteAuditSweepRequest{
		JobID:       "website-state-sweep",
		States:      []string{web.WebsiteStateNeverChecked},
		Limit:       100,
		Options:     websiteStateSweepOptions(),
		RequestedBy: "test",
	})
	if err != nil {
		t.Fatalf("StartWebsiteAuditSweep() error = %v", err)
	}
	// Three never-checked businesses live on two domains, so exactly two
	// probes are queued and the duplicate listing is recorded as reusing the
	// other business's evidence.
	if sweep.Queued != 2 || sweep.UniqueDomains != 2 || sweep.SkippedDuplicateDomain != 1 {
		t.Fatalf("sweep = %+v, want 2 queued over 2 domains with 1 duplicate skipped", sweep)
	}
	if sweep.SkippedIneligible != 3 {
		t.Fatalf("skipped ineligible = %d, want 3 (one no-website, two social)", sweep.SkippedIneligible)
	}
	if sweep.Progress.Queued != 2 || sweep.Progress.Done {
		t.Fatalf("initial progress = %+v, want two queued and not done", sweep.Progress)
	}

	// The chosen representative for the shared domain is the HTTPS root URL,
	// not the deeper plain-HTTP one.
	var queuedURL string
	if err := concrete.db.QueryRowContext(
		ctx,
		`SELECT website_url FROM enrichment_tasks WHERE business_id = ?`,
		ids["state-shared-a"],
	).Scan(&queuedURL); err != nil {
		t.Fatalf("read queued URL for the shared domain: %v", err)
	}
	if queuedURL != "https://shared-domain.example" {
		t.Fatalf("queued URL = %q, want the https root", queuedURL)
	}
	var annexTasks int64
	if err := concrete.db.QueryRowContext(
		ctx, `SELECT COUNT(*) FROM enrichment_tasks WHERE business_id = ?`, ids["state-shared-b"],
	).Scan(&annexTasks); err != nil {
		t.Fatalf("count annex tasks: %v", err)
	}
	if annexTasks != 0 {
		t.Fatalf("the duplicate listing on the same domain queued %d tasks, want 0", annexTasks)
	}

	// A queued business reports QUEUED, never "never checked".
	resolution, err := concrete.BusinessWebsiteState(ctx, ids["state-solo"])
	if err != nil {
		t.Fatalf("BusinessWebsiteState() error = %v", err)
	}
	if resolution.State != web.WebsiteStateQueued {
		t.Fatalf("queued business state = %q, want %q", resolution.State, web.WebsiteStateQueued)
	}

	// Re-running the sweep while work is outstanding must not duplicate it.
	again, err := concrete.StartWebsiteAuditSweep(ctx, web.WebsiteAuditSweepRequest{
		JobID:       "website-state-sweep",
		States:      []string{web.WebsiteStateNeverChecked},
		Limit:       100,
		Options:     websiteStateSweepOptions(),
		RequestedBy: "test",
	})
	if err != nil {
		t.Fatalf("second StartWebsiteAuditSweep() error = %v", err)
	}
	if again.Queued != 0 {
		t.Fatalf("re-running the sweep queued %d extra tasks, want 0", again.Queued)
	}

	// Drain the queue the way the local worker does, recording one reachable
	// site and one transport failure.
	drainWebsiteSweep(t, ctx, concrete, map[string]enrichment.Result{
		"https://shared-domain.example": {
			RequestedURL: "https://shared-domain.example", FinalURL: "https://shared-domain.example",
			Reachable: true, StatusCode: 200, HTTPS: true, TLSValid: true,
			ResponseTime: 300 * time.Millisecond,
			Pages: []enrichment.PageResult{{
				RequestedURL: "https://shared-domain.example", FinalURL: "https://shared-domain.example",
				Kind: enrichment.PageHomepage, StatusCode: 200, SizeBytes: 4096,
				ContentType: "text/html", Title: "Shared Domain Studio",
				MetaDescription: "Tattoo studio", MobileViewport: true, CopyrightYear: 2026,
			}},
		},
		"https://solo-domain.example": {
			RequestedURL: "https://solo-domain.example",
			Error:        `resolve host "solo-domain.example": lookup solo-domain.example: no such host`,
		},
	})

	drained, err := concrete.WebsiteAuditSweepByID(ctx, sweep.ID)
	if err != nil {
		t.Fatalf("WebsiteAuditSweepByID() error = %v", err)
	}
	if !drained.Progress.Done || drained.Progress.Completed != 2 || drained.Progress.Percent != 100 {
		t.Fatalf("drained progress = %+v, want two completed and done", drained.Progress)
	}

	final, err := concrete.WebsiteStateSummary(ctx, "website-state-sweep")
	if err != nil {
		t.Fatalf("final WebsiteStateSummary() error = %v", err)
	}
	if got := websiteStateCount(t, final, web.WebsiteStateNeverChecked); got != 0 {
		t.Fatalf("never-checked after the sweep = %d, want 0", got)
	}
	// The failed probe is an ERROR observation, not a business nobody looked
	// at. This is the exact distinction the state machine was added for.
	if got := websiteStateCount(t, final, web.WebsiteStateError); got != 1 {
		t.Fatalf("error state after the sweep = %d, want 1", got)
	}
	// Both businesses on the audited domain are LIVE, and the one that never
	// ran its own probe says whose evidence it is using.
	if got := websiteStateCount(t, final, web.WebsiteStateLive); got != 2 {
		t.Fatalf("live state after the sweep = %d, want 2", got)
	}
	if final.ReusedDomainEvidence != 1 {
		t.Fatalf("reused domain evidence = %d, want 1", final.ReusedDomainEvidence)
	}
	annex, err := concrete.BusinessWebsiteState(ctx, ids["state-shared-b"])
	if err != nil {
		t.Fatalf("BusinessWebsiteState(annex) error = %v", err)
	}
	if annex.State != web.WebsiteStateLive || annex.ReusedFromDomain != "shared-domain.example" {
		t.Fatalf("annex resolution = %+v, want LIVE reusing shared-domain.example", annex)
	}

	// The freshness window now suppresses a repeat probe of the same domain.
	repeat, err := concrete.StartWebsiteAuditSweep(ctx, web.WebsiteAuditSweepRequest{
		JobID:       "website-state-sweep",
		States:      []string{web.WebsiteStateLive, web.WebsiteStateError},
		Limit:       100,
		Options:     websiteStateSweepOptions(),
		RequestedBy: "test",
	})
	if err != nil {
		t.Fatalf("freshness sweep error = %v", err)
	}
	if repeat.Queued != 0 || repeat.SkippedFresh != 2 {
		t.Fatalf("freshness sweep = %+v, want nothing queued and two fresh skips", repeat)
	}
}

// drainWebsiteSweep processes every queued enrichment task the way the local
// worker does, using the supplied canned analysis per requested URL.
func drainWebsiteSweep(
	t *testing.T,
	ctx context.Context,
	concrete *repo,
	responses map[string]enrichment.Result,
) {
	t.Helper()

	for range len(responses) + 2 {
		task, found, err := concrete.ClaimEnrichmentTask(ctx)
		if err != nil {
			t.Fatalf("ClaimEnrichmentTask() error = %v", err)
		}
		if !found {
			return
		}
		analysis, ok := responses[task.WebsiteURL]
		if !ok {
			t.Fatalf("no canned response for %q", task.WebsiteURL)
		}
		// Audits complete "now" so the freshness window, which is relative to
		// the wall clock, means what it means in production.
		startedAt := time.Now().UTC()
		auditID, err := concrete.StoreWebsiteAudit(
			ctx, task, analysis, startedAt, startedAt.Add(time.Second),
		)
		if err != nil {
			t.Fatalf("StoreWebsiteAudit() error = %v", err)
		}
		if err := concrete.FinishEnrichmentTask(ctx, task.ID, &auditID, nil); err != nil {
			t.Fatalf("FinishEnrichmentTask() error = %v", err)
		}
		// The local worker rescores the audited business right after the task
		// completes; the domain siblings are refreshed by the storage hook.
		if _, err := concrete.RecalculateQuality(ctx, []string{task.BusinessID}); err != nil {
			t.Fatalf("RecalculateQuality() error = %v", err)
		}
	}
}

// TestSocialListingBackfillCorrectsStoredClassification proves the fix for
// social URLs stored as websites: they are recorded in social_profiles, are
// reclassified SOCIAL_ONLY, and stop earning website credit.
func TestSocialListingBackfillCorrectsStoredClassification(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	concrete := newProspectRepository(t)
	ids := websiteStateFixture(t, ctx, concrete, "website-state-social")

	dryRun, err := concrete.BackfillSocialListings(ctx, false, 0)
	if err != nil {
		t.Fatalf("BackfillSocialListings(dry run) error = %v", err)
	}
	if dryRun.Applied {
		t.Fatal("a dry run must not report itself as applied")
	}
	if dryRun.Social != 2 || dryRun.ProfilesInserted != 0 {
		t.Fatalf("dry run = %+v, want two social listings and no writes", dryRun)
	}
	if dryRun.ByPlatform["instagram"] != 1 || dryRun.ByPlatform["pinterest"] != 1 {
		t.Fatalf("dry run platforms = %+v, want one instagram and one pinterest", dryRun.ByPlatform)
	}
	var profiles int64
	if err := concrete.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM social_profiles`).Scan(&profiles); err != nil {
		t.Fatalf("count social profiles: %v", err)
	}
	if profiles != 0 {
		t.Fatalf("dry run wrote %d social_profiles rows", profiles)
	}

	applied, err := concrete.BackfillSocialListings(ctx, true, 0)
	if err != nil {
		t.Fatalf("BackfillSocialListings(apply) error = %v", err)
	}
	if applied.ProfilesInserted != 2 {
		t.Fatalf("applied = %+v, want two social_profiles rows created", applied)
	}
	if applied.QualityRescored != 2 {
		t.Fatalf("applied.QualityRescored = %d, want 2", applied.QualityRescored)
	}

	for placeID, platform := range map[string]string{
		"state-social": "instagram", "state-pinterest": "pinterest",
	} {
		var storedPlatform string
		if err := concrete.db.QueryRowContext(
			ctx, `SELECT platform FROM social_profiles WHERE business_id = ?`, ids[placeID],
		).Scan(&storedPlatform); err != nil {
			t.Fatalf("read social profile for %s: %v", placeID, err)
		}
		if storedPlatform != platform {
			t.Fatalf("stored platform for %s = %q, want %q", placeID, storedPlatform, platform)
		}
		state := readProspectState(t, concrete, ids[placeID])
		if state.status != prospect.StatusSocialOnly {
			t.Fatalf("prospect status for %s = %q, want %s", placeID, state.status, prospect.StatusSocialOnly)
		}
	}

	// Running it again is a no-op: the unique key makes the write idempotent.
	repeat, err := concrete.BackfillSocialListings(ctx, true, 0)
	if err != nil {
		t.Fatalf("second BackfillSocialListings() error = %v", err)
	}
	if repeat.ProfilesInserted != 0 {
		t.Fatalf("re-running the backfill inserted %d rows, want 0", repeat.ProfilesInserted)
	}

	// The social listing now earns social credit and no website credit.
	report, err := concrete.BusinessQuality(ctx, ids["state-social"])
	if err != nil {
		t.Fatalf("BusinessQuality() error = %v", err)
	}
	components := make(map[string]web.QualityContribution, len(report.Contributions))
	for _, contribution := range report.Contributions {
		components[contribution.Component] = contribution
	}
	if active := components["active_website"]; active.Contribution != 0 || active.Passed {
		t.Fatalf("active_website for a social listing = %+v, want zero and not passed", active)
	}
	if secure := components["https"]; secure.Contribution != 0 || secure.Passed {
		t.Fatalf("https for a social listing = %+v, want zero and not passed", secure)
	}
	if social := components["social_profiles"]; social.Contribution <= 0 || !social.Passed {
		t.Fatalf("social_profiles for a social listing = %+v, want credited", social)
	}

	// Website health refuses to grade a rented profile page.
	health, err := concrete.BusinessWebsiteHealth(ctx, ids["state-social"])
	if err != nil {
		t.Fatalf("BusinessWebsiteHealth() error = %v", err)
	}
	if health.Available || health.Score != 0 {
		t.Fatalf("website health for a social listing = %+v, want unavailable", health)
	}
	if health.State != web.WebsiteStateSocialOnly {
		t.Fatalf("website health state = %q, want %q", health.State, web.WebsiteStateSocialOnly)
	}
}

// TestQualityNeverCreditsAnUncheckedWebsite is the regression guard for the
// scoring defect: an unaudited https:// URL used to collect half the
// active-website weight plus full HTTPS credit, both marked "passed", so a
// business nobody had checked outscored one whose site was known good.
func TestQualityNeverCreditsAnUncheckedWebsite(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	concrete := newProspectRepository(t)
	ids := websiteStateFixture(t, ctx, concrete, "website-state-quality")

	unchecked, err := concrete.BusinessQuality(ctx, ids["state-solo"])
	if err != nil {
		t.Fatalf("BusinessQuality() error = %v", err)
	}
	components := make(map[string]web.QualityContribution, len(unchecked.Contributions))
	for _, contribution := range unchecked.Contributions {
		components[contribution.Component] = contribution
	}
	if active := components["active_website"]; active.Contribution != 0 || active.Passed {
		t.Fatalf("active_website for an unaudited site = %+v, want zero and not passed", active)
	}
	if secure := components["https"]; secure.Contribution != 0 || secure.Passed {
		t.Fatalf("https for an unaudited https:// URL = %+v, want zero and not passed", secure)
	}
	if unchecked.Confidence >= 1 {
		t.Fatalf("confidence = %v; an unaudited website must lower record confidence", unchecked.Confidence)
	}

	// The stored score must always equal the sum of its published components.
	sum := 0.0
	for _, contribution := range unchecked.Contributions {
		sum += contribution.Contribution
	}
	var storedScore float64
	if err := concrete.db.QueryRowContext(
		ctx, `SELECT quality_score FROM businesses WHERE id = ?`, ids["state-solo"],
	).Scan(&storedScore); err != nil {
		t.Fatalf("read stored quality score: %v", err)
	}
	if diff := storedScore - sum; diff > 0.05 || diff < -0.05 {
		t.Fatalf("stored score %v disagrees with its own breakdown %v", storedScore, sum)
	}

	// Auditing the site is what earns the website credit.
	if _, err := concrete.StartWebsiteAuditSweep(ctx, web.WebsiteAuditSweepRequest{
		JobID:       "website-state-quality",
		States:      []string{web.WebsiteStateNeverChecked},
		Limit:       100,
		Options:     websiteStateSweepOptions(),
		RequestedBy: "test",
	}); err != nil {
		t.Fatalf("StartWebsiteAuditSweep() error = %v", err)
	}
	drainWebsiteSweep(t, ctx, concrete, map[string]enrichment.Result{
		"https://shared-domain.example": {
			RequestedURL: "https://shared-domain.example", FinalURL: "https://shared-domain.example",
			Reachable: true, StatusCode: 200, HTTPS: true, TLSValid: true,
			ResponseTime: 300 * time.Millisecond,
			Pages: []enrichment.PageResult{{
				RequestedURL: "https://shared-domain.example", FinalURL: "https://shared-domain.example",
				Kind: enrichment.PageHomepage, StatusCode: 200, SizeBytes: 4096,
				ContentType: "text/html", Title: "Shared Domain Studio",
				MetaDescription: "Tattoo studio", MobileViewport: true, CopyrightYear: 2026,
			}},
		},
		"https://solo-domain.example": {
			RequestedURL: "https://solo-domain.example", FinalURL: "https://solo-domain.example",
			Reachable: true, StatusCode: 200, HTTPS: true, TLSValid: true,
			ResponseTime: 200 * time.Millisecond,
			Pages: []enrichment.PageResult{{
				RequestedURL: "https://solo-domain.example", FinalURL: "https://solo-domain.example",
				Kind: enrichment.PageHomepage, StatusCode: 200, SizeBytes: 2048,
				ContentType: "text/html", Title: "Solo Domain Tattoo",
				MetaDescription: "Tattoo studio", MobileViewport: true, CopyrightYear: 2026,
			}},
		},
	})

	audited, err := concrete.BusinessQuality(ctx, ids["state-solo"])
	if err != nil {
		t.Fatalf("BusinessQuality() after the audit error = %v", err)
	}
	auditedComponents := make(map[string]web.QualityContribution, len(audited.Contributions))
	for _, contribution := range audited.Contributions {
		auditedComponents[contribution.Component] = contribution
	}
	if active := auditedComponents["active_website"]; active.Contribution <= 0 || !active.Passed {
		t.Fatalf("active_website after a successful audit = %+v, want credited", active)
	}
	if audited.Confidence <= unchecked.Confidence {
		t.Fatalf("confidence did not rise after auditing: %v -> %v", unchecked.Confidence, audited.Confidence)
	}

	// The duplicate listing on the audited domain inherits the evidence rather
	// than staying unknown.
	annex, err := concrete.BusinessQuality(ctx, ids["state-shared-b"])
	if err != nil {
		t.Fatalf("BusinessQuality(annex) error = %v", err)
	}
	for _, contribution := range annex.Contributions {
		if contribution.Component == "active_website" {
			if contribution.Contribution <= 0 || !contribution.Passed {
				t.Fatalf("annex active_website = %+v, want reused domain evidence", contribution)
			}
		}
	}

	// Website health is now available for the audited site and is graded from
	// the audit alone.
	health, err := concrete.BusinessWebsiteHealth(ctx, ids["state-solo"])
	if err != nil {
		t.Fatalf("BusinessWebsiteHealth() error = %v", err)
	}
	if !health.Available || health.Score <= 0 || health.Grade == "" {
		t.Fatalf("website health after the audit = %+v, want a graded score", health)
	}
	if health.RuleVersion != web.WebsiteHealthRuleVersion {
		t.Fatalf("health rule version = %q, want %q", health.RuleVersion, web.WebsiteHealthRuleVersion)
	}
}

// TestQualityScoreDriftDetectsAndRepairsAForeignScore reproduces the live
// defect where a record's stored quality score no longer matched its own
// stored explanation, and proves the audit finds and repairs it.
func TestQualityScoreDriftDetectsAndRepairsAForeignScore(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	concrete := newProspectRepository(t)
	ids := websiteStateFixture(t, ctx, concrete, "website-state-drift")

	clean, err := concrete.QualityScoreDrift(ctx, false)
	if err != nil {
		t.Fatalf("QualityScoreDrift() error = %v", err)
	}
	if clean.Checked != 6 || clean.Drifted != 0 {
		t.Fatalf("freshly imported workspace drift = %+v, want 6 checked and none drifted", clean)
	}

	// Simulate a foreign writer raising the column without touching the
	// breakdown, which is exactly what a monotonic MAX() merge of an
	// import-time completeness number does.
	if _, err := concrete.db.ExecContext(
		ctx, `UPDATE businesses SET quality_score = 80 WHERE id = ?`, ids["state-social"],
	); err != nil {
		t.Fatalf("simulate foreign score: %v", err)
	}

	detected, err := concrete.QualityScoreDrift(ctx, false)
	if err != nil {
		t.Fatalf("QualityScoreDrift() error = %v", err)
	}
	if detected.Drifted != 1 || detected.Repaired != 0 || len(detected.Samples) != 1 {
		t.Fatalf("drift detection = %+v, want exactly one drifted row and no repair", detected)
	}
	if detected.Samples[0].BusinessID != ids["state-social"] || detected.Samples[0].StoredScore != 80 {
		t.Fatalf("drift sample = %+v, want the tampered business at 80", detected.Samples[0])
	}

	repaired, err := concrete.QualityScoreDrift(ctx, true)
	if err != nil {
		t.Fatalf("QualityScoreDrift(repair) error = %v", err)
	}
	if repaired.Drifted != 1 || repaired.Repaired != 1 {
		t.Fatalf("drift repair = %+v, want one row repaired", repaired)
	}

	after, err := concrete.QualityScoreDrift(ctx, false)
	if err != nil {
		t.Fatalf("QualityScoreDrift() after repair error = %v", err)
	}
	if after.Drifted != 0 {
		t.Fatalf("drift after repair = %+v, want none", after)
	}
}

// TestUnauditedBusinessIDsDrivesTheScoringPrerequisite proves the list that
// makes "queue the audit before scoring" possible only names businesses whose
// owned website has genuinely never been checked.
func TestUnauditedBusinessIDsDrivesTheScoringPrerequisite(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	concrete := newProspectRepository(t)
	ids := websiteStateFixture(t, ctx, concrete, "website-state-prerequisite")

	pending, err := concrete.UnauditedBusinessIDs(ctx, nil)
	if err != nil {
		t.Fatalf("UnauditedBusinessIDs() error = %v", err)
	}
	if len(pending) != 3 {
		t.Fatalf("unaudited businesses = %v, want the three with an owned website", pending)
	}
	unwanted := map[string]string{
		ids["state-none"]:      "no website",
		ids["state-social"]:    "instagram listing",
		ids["state-pinterest"]: "pinterest listing",
	}
	for _, id := range pending {
		if reason, bad := unwanted[id]; bad {
			t.Fatalf("business with %s was queued for a website audit", reason)
		}
	}

	scoped, err := concrete.UnauditedBusinessIDs(ctx, []string{ids["state-social"], ids["state-solo"]})
	if err != nil {
		t.Fatalf("scoped UnauditedBusinessIDs() error = %v", err)
	}
	if len(scoped) != 1 || scoped[0] != ids["state-solo"] {
		t.Fatalf("scoped unaudited = %v, want only the solo domain", scoped)
	}

	// An ID list far past one SQLite statement's bound-variable budget has to
	// be read in batches rather than blowing up the query.
	large := make([]string, 0, websiteStateIDBatch*3)
	for range websiteStateIDBatch * 3 {
		large = append(large, ids["state-solo"])
	}
	batched, err := concrete.UnauditedBusinessIDs(ctx, large)
	if err != nil {
		t.Fatalf("batched UnauditedBusinessIDs() error = %v", err)
	}
	if len(batched) == 0 {
		t.Fatal("batched read returned nothing; the solo domain is still unaudited")
	}
	for _, id := range batched {
		if id != ids["state-solo"] {
			t.Fatalf("batched read returned %s, want only the solo domain", id)
		}
	}
}
