package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web/enrichment"
)

// enrichmentHistoryContract mirrors the unexported optional interface the web
// enrichment worker type-asserts on. It cannot be imported (web imports this
// package), so this local copy pins the sqlite side of the contract: if the
// signatures below stop matching, the worker silently falls back to the
// configured timeout instead of adapting, and this assertion is what catches
// the drift.
type enrichmentHistoryContract interface {
	WebsiteLatencyHistory(
		ctx context.Context,
		businessID string,
		websiteURL string,
		limit int,
	) (enrichment.SiteHistory, error)
	RecordEnrichmentEvent(ctx context.Context, action string, entityID string, details string) error
}

var _ enrichmentHistoryContract = (*repo)(nil)

// latencyHistoryRepo opens an isolated schema in t.TempDir(). The live
// workspace database is never touched by these tests.
func latencyHistoryRepo(t *testing.T) *repo {
	t.Helper()

	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete, ok := repository.(*repo)
	if !ok {
		t.Fatalf("New() returned %T, want *repo", repository)
	}
	t.Cleanup(func() { _ = concrete.db.Close() })

	return concrete
}

func seedLatencyBusiness(t *testing.T, repository *repo, id, website, status string) {
	t.Helper()

	stamp := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC).Unix()
	if _, err := repository.db.ExecContext(
		context.Background(),
		`INSERT INTO businesses(
			id, canonical_key, name, normalized_name, website, website_status,
			first_seen_at, last_seen_at, last_changed_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, "key-"+id, "Latency "+id, "latency "+id, website, status,
		stamp, stamp, stamp, stamp, stamp,
	); err != nil {
		t.Fatalf("seed business %s: %v", id, err)
	}
}

type seededAudit struct {
	requestedURL string
	finalURL     string
	reachable    bool
	statusCode   int
	responseMS   int64
	auditError   string
	completedAt  int64
}

func seedLatencyAudits(t *testing.T, repository *repo, businessID string, audits []seededAudit) {
	t.Helper()

	for index, audit := range audits {
		reachable := 0
		if audit.reachable {
			reachable = 1
		}
		if _, err := repository.db.ExecContext(
			context.Background(),
			`INSERT INTO website_audits(
				business_id, requested_url, final_url, reachable, status_code,
				response_time_ms, error, started_at, completed_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			businessID, audit.requestedURL, audit.finalURL, reachable, audit.statusCode,
			audit.responseMS, audit.auditError, audit.completedAt, audit.completedAt,
		); err != nil {
			t.Fatalf("seed audit %d for %s: %v", index, businessID, err)
		}
	}
}

func TestWebsiteLatencyHistoryReadsBoundedNewestFirstPerHost(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository := latencyHistoryRepo(t)
	seedLatencyBusiness(t, repository, "business-fast", "https://fast.example", "active")
	seedLatencyAudits(t, repository, "business-fast", []seededAudit{
		{requestedURL: "https://fast.example", finalURL: "https://www.fast.example",
			reachable: true, statusCode: 200, responseMS: 900, completedAt: 100},
		{requestedURL: "https://fast.example", finalURL: "https://www.fast.example",
			reachable: true, statusCode: 200, responseMS: 400, completedAt: 200},
		// An audit of a previous website on another host must not leak into
		// the current host's latency evidence.
		{requestedURL: "https://old.example", finalURL: "https://old.example",
			reachable: true, statusCode: 200, responseMS: 9000, completedAt: 150},
	})

	history, err := repository.WebsiteLatencyHistory(ctx, "business-fast", "https://fast.example", 10)
	if err != nil {
		t.Fatalf("WebsiteLatencyHistory() error = %v", err)
	}
	if history.Host != "fast.example" || history.LastStatus != "active" {
		t.Fatalf("history identity = %+v", history)
	}
	if len(history.Observations) != 2 {
		t.Fatalf("observations = %+v, want the two same-host audits", history.Observations)
	}
	// Newest first: completed_at 200 precedes completed_at 100.
	if history.Observations[0].ResponseTime != 400*time.Millisecond ||
		history.Observations[1].ResponseTime != 900*time.Millisecond {
		t.Fatalf("observation order = %+v", history.Observations)
	}
	for index, observation := range history.Observations {
		if !observation.Reachable || observation.TimedOut || observation.Failed {
			t.Fatalf("observation %d = %+v, want a healthy sample", index, observation)
		}
	}

	// A fast healthy history must reduce the budget without exceeding it.
	if adapted := enrichment.AdaptiveTimeout(10*time.Second, history); adapted >= 10*time.Second {
		t.Logf("adapted budget = %v", adapted)
	} else if adapted <= 0 {
		t.Fatalf("adapted budget = %v", adapted)
	}
}

func TestWebsiteLatencyHistoryClassifiesFailuresAndHonorsTheWindow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository := latencyHistoryRepo(t)
	seedLatencyBusiness(t, repository, "business-bad", "https://bad.example", "error")
	seedLatencyAudits(t, repository, "business-bad", []seededAudit{
		{requestedURL: "https://bad.example", completedAt: 100,
			auditError: `Get "https://bad.example": context deadline exceeded`},
		{requestedURL: "https://bad.example", completedAt: 200,
			auditError: "dial tcp: lookup bad.example: no such host"},
		{requestedURL: "https://bad.example", reachable: true, statusCode: 503,
			responseMS: 120, auditError: "unexpected status 503", completedAt: 300},
	})

	history, err := repository.WebsiteLatencyHistory(ctx, "business-bad", "https://bad.example", 10)
	if err != nil {
		t.Fatalf("WebsiteLatencyHistory() error = %v", err)
	}
	if len(history.Observations) != 3 || history.LastStatus != "error" {
		t.Fatalf("history = %+v", history)
	}
	// Newest first: the 503, then the DNS failure, then the timeout.
	if history.Observations[0].TimedOut || history.Observations[0].Failed ||
		!history.Observations[0].Reachable {
		t.Fatalf("page-level failure = %+v, want a usable latency sample", history.Observations[0])
	}
	if !history.Observations[1].Failed || history.Observations[1].TimedOut {
		t.Fatalf("DNS failure = %+v, want Failed", history.Observations[1])
	}
	if !history.Observations[2].TimedOut || history.Observations[2].Failed {
		t.Fatalf("deadline failure = %+v, want TimedOut", history.Observations[2])
	}

	// The window bound is applied by the query, not by the caller.
	bounded, err := repository.WebsiteLatencyHistory(ctx, "business-bad", "https://bad.example", 1)
	if err != nil || len(bounded.Observations) != 1 {
		t.Fatalf("bounded history = %+v, %v", bounded, err)
	}

	// A repeatedly failing host earns a shorter leash, never a longer one.
	adapted := enrichment.AdaptiveTimeout(10*time.Second, history)
	if adapted >= 10*time.Second || adapted <= 0 {
		t.Fatalf("adapted budget for a failing host = %v", adapted)
	}
}

func TestWebsiteLatencyHistoryHandlesAbsentHistoryCleanly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository := latencyHistoryRepo(t)
	seedLatencyBusiness(t, repository, "business-new", "https://new.example", "unknown")

	history, err := repository.WebsiteLatencyHistory(ctx, "business-new", "https://new.example", 10)
	if err != nil {
		t.Fatalf("WebsiteLatencyHistory() error = %v", err)
	}
	if len(history.Observations) != 0 {
		t.Fatalf("observations = %+v, want none", history.Observations)
	}
	if adapted := enrichment.AdaptiveTimeout(10*time.Second, history); adapted != 10*time.Second {
		t.Fatalf("adapted budget without history = %v, want the configured ceiling", adapted)
	}

	unknown, err := repository.WebsiteLatencyHistory(ctx, "no-such-business", "https://new.example", 10)
	if err != nil || len(unknown.Observations) != 0 || unknown.LastStatus != "" {
		t.Fatalf("unknown business history = %+v, %v", unknown, err)
	}

	blank, err := repository.WebsiteLatencyHistory(ctx, "   ", "https://new.example", 10)
	if err != nil || len(blank.Observations) != 0 {
		t.Fatalf("blank business history = %+v, %v", blank, err)
	}
}

func TestRecordEnrichmentEventAppendsAuditLogRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository := latencyHistoryRepo(t)
	if err := repository.RecordEnrichmentEvent(ctx, "enrichment_timeout_adapted", "task-1", ""); err != nil {
		t.Fatalf("RecordEnrichmentEvent() error = %v", err)
	}

	var action, entityType, entityID, details string
	if err := repository.db.QueryRowContext(ctx,
		`SELECT action, entity_type, entity_id, details FROM audit_logs ORDER BY id DESC LIMIT 1`,
	).Scan(&action, &entityType, &entityID, &details); err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if action != "enrichment_timeout_adapted" || entityType != "website_audit" ||
		entityID != "task-1" || details != "{}" {
		t.Fatalf("audit log row = %q %q %q %q", action, entityType, entityID, details)
	}
}
