package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/enrichment"
)

func TestDurableWebsiteEnrichmentQueueAuditAndEvidence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete := repository.(*repo)
	t.Cleanup(func() { _ = concrete.db.Close() })

	observedAt := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	job := resultImportJob("enrichment-job", observedAt)
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "enrichment.csv")
	writeLegacyResultRows(t, path, map[string]string{
		"title": "Enriched Dental", "category": "Dentist", "status": "Open",
		"address": "1 Market St, San Francisco", "website": "https://enriched.example",
		"place_id": "enrichment-place", "latitude": "37.78", "longitude": "-122.4",
	})
	if _, err := concrete.ImportLegacyCSV(ctx, job, path); err != nil {
		t.Fatalf("ImportLegacyCSV() error = %v", err)
	}
	var businessID string
	if err := concrete.db.QueryRowContext(ctx,
		"SELECT id FROM businesses WHERE place_id = 'enrichment-place'",
	).Scan(&businessID); err != nil {
		t.Fatalf("read business ID: %v", err)
	}

	options := web.EnrichmentOptions{
		Scope: "homepage_contact_about", MaxPages: 3, TimeoutSeconds: 10,
		MaxBodyBytes: 2 << 20, MaxRedirects: 10, MaxInternalLinkChecks: 10,
		CheckMX: true, StaleAfterHours: 24,
	}
	batch, err := concrete.QueueBusinessEnrichment(ctx, []string{businessID}, options, "test", "")
	if err != nil || batch.Queued != 1 || len(batch.Tasks) != 1 {
		t.Fatalf("QueueBusinessEnrichment() = %+v, %v", batch, err)
	}
	duplicate, err := concrete.QueueBusinessEnrichment(ctx, []string{businessID}, options, "test", "")
	if err != nil || duplicate.Queued != 0 || duplicate.Skipped != 1 || len(duplicate.Tasks) != 1 {
		t.Fatalf("duplicate queue = %+v, %v", duplicate, err)
	}

	task, found, err := concrete.ClaimEnrichmentTask(ctx)
	if err != nil || !found || task.State != "running" || task.Attempts != 1 {
		t.Fatalf("ClaimEnrichmentTask() = %+v, %v, %v", task, found, err)
	}
	source := enrichment.Source{
		PageURL: "https://enriched.example/contact", PageKind: enrichment.PageContact,
		Method: enrichment.MethodVisibleText,
	}
	analysis := enrichment.Result{
		RequestedURL: "https://enriched.example", FinalURL: "https://www.enriched.example",
		Reachable: true, StatusCode: 200, HTTPS: true, TLSValid: true,
		ResponseTime: 125 * time.Millisecond,
		RedirectChain: []enrichment.Redirect{{
			From: "https://enriched.example", To: "https://www.enriched.example", StatusCode: 301,
		}},
		Pages: []enrichment.PageResult{
			{
				RequestedURL: "https://enriched.example", FinalURL: "https://www.enriched.example",
				Kind: enrichment.PageHomepage, StatusCode: 200, ResponseTime: 125 * time.Millisecond,
				SizeBytes: 4096, ContentType: "text/html", Title: "Enriched Dental",
				MetaDescription: "San Francisco dentist", Language: "en", MobileViewport: true,
			},
			{
				RequestedURL: source.PageURL, FinalURL: source.PageURL, Kind: enrichment.PageContact,
				StatusCode: 200, ResponseTime: 80 * time.Millisecond, SizeBytes: 2048,
				ContentType: "text/html", Title: "Contact us", Language: "en", MobileViewport: true,
			},
		},
		Emails: []enrichment.Email{{
			Address: "info@enriched.example", Domain: "enriched.example", ValidSyntax: true,
			Role: "info", RoleAddress: true, MXStatus: enrichment.MXPresent,
			MXRecords: []string{"mx.enriched.example"}, Relevance: 95, Rank: 1,
			Sources: []enrichment.Source{source},
		}},
		Phones: []enrichment.Phone{{Value: "+1 415 555 0100", Sources: []enrichment.Source{source}}},
		SocialProfiles: []enrichment.SocialProfile{{
			Platform: "linkedin", URL: "https://linkedin.com/company/enriched",
			Sources: []enrichment.Source{source},
		}},
		Technologies:         []enrichment.Detection{{Name: "WordPress", Confidence: 0.9, Evidence: []string{"wp-content"}}},
		Trackers:             []enrichment.Detection{{Name: "Google Analytics", Confidence: 0.8, Evidence: []string{"gtag"}}},
		InternalLinksChecked: 2, BrokenInternalLinkCount: 1,
		BrokenInternalLinks: []enrichment.LinkCheck{{URL: "https://www.enriched.example/missing", StatusCode: 404, Broken: true}},
	}
	auditID, err := concrete.StoreWebsiteAudit(ctx, task, analysis, observedAt, observedAt.Add(time.Second))
	if err != nil {
		t.Fatalf("StoreWebsiteAudit() error = %v", err)
	}
	if err := concrete.FinishEnrichmentTask(ctx, task.ID, &auditID, nil); err != nil {
		t.Fatalf("FinishEnrichmentTask() error = %v", err)
	}

	for table, expected := range map[string]int{
		"website_audits": 1, "website_audit_pages": 2, "contact_evidence": 3,
		"website_detections": 2,
	} {
		var count int
		if err := concrete.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != expected {
			t.Errorf("%s count = %d, want %d", table, count, expected)
		}
	}

	history, err := concrete.WebsiteAuditHistory(ctx, businessID, 10)
	if err != nil || len(history) != 1 {
		t.Fatalf("WebsiteAuditHistory() = %+v, %v", history, err)
	}
	if !history[0].Reachable || history[0].StatusCode != 200 || len(history[0].Pages) != 2 ||
		len(history[0].Emails) != 1 || len(history[0].Technologies) != 1 || len(history[0].Trackers) != 1 {
		t.Fatalf("stored audit = %+v", history[0])
	}

	detail, err := concrete.GetBusiness(ctx, businessID)
	if err != nil {
		t.Fatalf("GetBusiness() error = %v", err)
	}
	if detail.Business.WebsiteStatus != "active" || detail.Business.PrimaryEmail != "info@enriched.example" ||
		len(detail.Emails) != 1 || len(detail.SocialProfiles) != 1 || len(detail.Provenance) < 3 {
		t.Fatalf("enriched business detail = %+v", detail)
	}
	tasks, err := concrete.ListEnrichmentTasks(ctx, 10)
	if err != nil || len(tasks) != 1 || tasks[0].State != "completed" || tasks[0].AuditID == nil {
		t.Fatalf("ListEnrichmentTasks() = %+v, %v", tasks, err)
	}
}
