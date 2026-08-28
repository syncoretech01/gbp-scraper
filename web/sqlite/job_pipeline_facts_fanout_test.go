package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// TestJobPipelineFactsCountWebsitesOncePerBusiness reproduces the metric
// fan-out that made the acceptance job cfe2d653 report 60 websites checked and
// 110 pages crawled when the workspace held 25 websites and 35 pages.
//
// The shape is exactly the live one: one business observed several times by
// the same job (several queries, several grid cells) has several
// business_sources rows, so joining websites through business_sources
// multiplies every website by its business's observation count.
func TestJobPipelineFactsCountWebsitesOncePerBusiness(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	job := lifecycleTestJob("job-facts-fanout", time.Now().UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("CreateWithState() error = %v", err)
	}

	seedFanoutBusiness(t, repository, job.ID, "biz-fanout-1", "https://one.example", 3, 2, "active")
	seedFanoutBusiness(t, repository, job.ID, "biz-fanout-2", "https://two.example", 1, 5, "inactive")

	facts, err := repository.JobPipelineFacts(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobPipelineFacts() error = %v", err)
	}

	if facts.UniqueBusinesses != 2 {
		t.Fatalf("unique businesses = %d, want 2", facts.UniqueBusinesses)
	}
	// Four business_sources rows, two websites. The joined query reported four.
	if facts.WebsitesChecked != 2 {
		t.Fatalf("websites checked = %d, want 2 (one per website row, not one per observation)",
			facts.WebsitesChecked)
	}
	if facts.WebsitesActive != 1 || facts.WebsitesInactive != 1 {
		t.Fatalf("website status split = %d active / %d inactive, want 1/1",
			facts.WebsitesActive, facts.WebsitesInactive)
	}
	// 2 + 5 pages. The joined query reported 3*2 + 1*5 = 11.
	if facts.PagesChecked != 7 {
		t.Fatalf("pages checked = %d, want 7", facts.PagesChecked)
	}
	if facts.DomainsChecked != 2 {
		t.Fatalf("domains checked = %d, want 2", facts.DomainsChecked)
	}
	// 120ms and 240ms average to 180ms; weighting by observation count would
	// give 150ms.
	if facts.AverageResponseMS < 179 || facts.AverageResponseMS > 181 {
		t.Fatalf("average response = %.2f ms, want about 180", facts.AverageResponseMS)
	}
	if facts.EmailAddresses != 2 {
		t.Fatalf("distinct email addresses = %d, want 2", facts.EmailAddresses)
	}
	if facts.WithEmail != 2 {
		t.Fatalf("businesses with an email = %d, want 2", facts.WithEmail)
	}
}

// seedFanoutBusiness writes one business that the job observed
// observationCount times, with one crawled website carrying pages pages and
// one stored email address.
func seedFanoutBusiness(
	t *testing.T,
	repository *repo,
	jobID string,
	businessID string,
	website string,
	observationCount int,
	pages int,
	status string,
) {
	t.Helper()

	ctx := context.Background()
	now := time.Now().UTC().Unix()

	if _, err := repository.db.ExecContext(ctx,
		`INSERT INTO businesses(
			id, canonical_key, name, normalized_name, website,
			first_seen_at, last_seen_at, last_changed_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		businessID, "key-"+businessID, "Fanout "+businessID, "fanout "+businessID,
		website, now, now, now, now, now,
	); err != nil {
		t.Fatalf("seed business %s: %v", businessID, err)
	}

	for observation := range observationCount {
		if _, err := repository.db.ExecContext(ctx,
			`INSERT INTO business_sources(business_id, job_id, source_type, extracted_at)
			VALUES (?, ?, 'google_maps', ?)`,
			businessID, jobID, now+int64(observation),
		); err != nil {
			t.Fatalf("seed business source %d for %s: %v", observation, businessID, err)
		}
	}

	responseMS := 120
	if pages > 2 {
		responseMS = 240
	}

	if _, err := repository.db.ExecContext(ctx,
		`INSERT INTO websites(
			business_id, url, final_url, domain, status, http_status, https,
			response_time_ms, last_checked_at, pages_checked
		) VALUES (?, ?, ?, ?, ?, 200, 1, ?, ?, ?)`,
		businessID, website, website, businessID+".example", status, responseMS, now, pages,
	); err != nil {
		t.Fatalf("seed website for %s: %v", businessID, err)
	}

	if _, err := repository.db.ExecContext(ctx,
		`INSERT INTO emails(business_id, value, normalized_value, kind, status)
		VALUES (?, ?, ?, 'role', 'syntax-valid')`,
		businessID, "info@"+businessID+".example", "info@"+businessID+".example",
	); err != nil {
		t.Fatalf("seed email for %s: %v", businessID, err)
	}
}

// TestJobPipelineFactsSeparateDiscoveryAndEnrichmentTiming proves the monitor
// can tell a six-second listing walk apart from the website audits that ran for
// minutes after it, and refuses to call the job complete while audits are
// still queued.
func TestJobPipelineFactsSeparateDiscoveryAndEnrichmentTiming(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	job := lifecycleTestJob("job-facts-timing", time.Now().UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("CreateWithState() error = %v", err)
	}

	// The live acceptance run: discovery started at t and finished at t+6, the
	// audits ran from t+12 to t+169.
	const discoveryStart = 1787927323

	if _, err := repository.db.ExecContext(ctx,
		`UPDATE job_runtime SET started_at = ?, finished_at = ? WHERE job_id = ?`,
		discoveryStart, discoveryStart+6, job.ID,
	); err != nil {
		t.Fatalf("seed job runtime timing: %v", err)
	}

	if _, err := repository.db.ExecContext(ctx,
		`INSERT INTO businesses(
			id, canonical_key, name, normalized_name, website,
			first_seen_at, last_seen_at, last_changed_at, created_at, updated_at
		) VALUES ('biz-timing', 'key-biz-timing', 'Timing', 'timing',
			'https://timing.example', ?, ?, ?, ?, ?)`,
		discoveryStart, discoveryStart, discoveryStart, discoveryStart, discoveryStart,
	); err != nil {
		t.Fatalf("seed business: %v", err)
	}

	for _, task := range []struct {
		id       string
		state    string
		started  int64
		finished any
	}{
		{id: "task-done", state: "completed", started: discoveryStart + 12, finished: discoveryStart + 169},
		{id: "task-waiting", state: "queued", started: discoveryStart + 12, finished: nil},
	} {
		if _, err := repository.db.ExecContext(ctx,
			`INSERT INTO enrichment_tasks(
				id, business_id, job_id, website_url, state, requested_by,
				options, created_at, started_at, finished_at, updated_at
			) VALUES (?, 'biz-timing', ?, 'https://timing.example', ?, 'job_completion', '{}', ?, ?, ?, ?)`,
			task.id, job.ID, task.state, task.started, task.started, task.finished, task.started,
		); err != nil {
			t.Fatalf("seed enrichment task %s: %v", task.id, err)
		}
	}

	facts, err := repository.JobPipelineFacts(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobPipelineFacts() error = %v", err)
	}

	if facts.DiscoveryDurationMS != 6000 {
		t.Fatalf("discovery duration = %d ms, want 6000", facts.DiscoveryDurationMS)
	}
	if facts.EnrichmentDurationMS != 157000 {
		t.Fatalf("enrichment duration = %d ms, want 157000", facts.EnrichmentDurationMS)
	}
	if facts.TotalDurationMS != 169000 {
		t.Fatalf("total duration = %d ms, want 169000", facts.TotalDurationMS)
	}
	if facts.EnrichmentTasksTotal != 2 || facts.EnrichmentCompleted != 1 || facts.EnrichmentQueued != 1 {
		t.Fatalf("enrichment task counters = %#v", facts)
	}
	if facts.EnrichmentPending() != 1 {
		t.Fatalf("enrichment pending = %d, want 1", facts.EnrichmentPending())
	}
	if facts.EnrichmentComplete {
		t.Fatal("a job with a queued website audit reported enrichment complete")
	}
}
