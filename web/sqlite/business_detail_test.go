package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestGetBusinessIncludesAuditEnrichmentChangesAndDuplicateEvidence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete := repository.(*repo)
	t.Cleanup(func() { _ = concrete.db.Close() })

	observedAt := time.Date(2026, time.August, 10, 14, 30, 0, 0, time.UTC)
	job := resultImportJob("detail-audit-job", observedAt)
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "detail.csv")
	writeLegacyResultRows(t, path,
		map[string]string{
			"title": "Audited Dental", "category": "Dentist", "status": "Open",
			"address": "1 Market St, San Francisco, CA 94105", "website": "https://audit.example",
			"emails": "hello@audit.example", "phone": "+1 415 555 0100", "place_id": "detail-audit-a",
		},
		map[string]string{
			"title": "Audited Dentistry", "category": "Dentist", "status": "Open",
			"address": "2 Market Street, San Francisco, CA 94105", "website": "https://other-audit.example",
			"place_id": "detail-audit-b",
		},
	)
	if _, err := concrete.ImportLegacyCSV(ctx, job, path); err != nil {
		t.Fatalf("ImportLegacyCSV() error = %v", err)
	}

	var businessID, duplicateID string
	if err := concrete.db.QueryRowContext(ctx, "SELECT id FROM businesses WHERE place_id = 'detail-audit-a'").Scan(&businessID); err != nil {
		t.Fatalf("read business ID: %v", err)
	}
	if err := concrete.db.QueryRowContext(ctx, "SELECT id FROM businesses WHERE place_id = 'detail-audit-b'").Scan(&duplicateID); err != nil {
		t.Fatalf("read duplicate ID: %v", err)
	}

	if _, err := concrete.db.ExecContext(ctx,
		`INSERT INTO websites(
			business_id, url, final_url, domain, status, http_status, https, response_time_ms,
			redirect_chain, page_title, meta_description, language, technologies, social_links, last_checked_at
		) VALUES (?, 'https://audit.example', 'https://www.audit.example', 'audit.example', 'active', 200, 1, 125,
			'["https://audit.example","https://www.audit.example"]', 'Audited Dental', 'Local dentist', 'en',
			'["wordpress"]', '{"linkedin":"https://linkedin.com/company/audit"}', ?)`,
		businessID,
		observedAt.Unix(),
	); err != nil {
		t.Fatalf("insert website audit: %v", err)
	}
	if _, err := concrete.db.ExecContext(ctx,
		`UPDATE emails SET kind = 'role', status = 'mx_valid', domain_has_mx = 1,
			source_url = 'https://audit.example/contact', extraction_method = 'visible_text',
			confidence = 0.95, last_checked_at = ? WHERE business_id = ?`,
		observedAt.Unix(),
		businessID,
	); err != nil {
		t.Fatalf("update email evidence: %v", err)
	}
	if _, err := concrete.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO social_profiles(business_id, platform, url, source_url, confidence)
		 VALUES (?, 'linkedin', 'https://linkedin.com/company/audit', 'https://audit.example', 0.9)`,
		businessID,
	); err != nil {
		t.Fatalf("insert social evidence: %v", err)
	}

	var versionID int64
	if err := concrete.db.QueryRowContext(ctx,
		"SELECT id FROM business_versions WHERE business_id = ? ORDER BY version_no DESC LIMIT 1",
		businessID,
	).Scan(&versionID); err != nil {
		t.Fatalf("read version ID: %v", err)
	}
	if _, err := concrete.db.ExecContext(ctx,
		`INSERT INTO business_changes(
			business_id, from_version_id, to_version_id, field_name, before_value, after_value, change_kind, detected_at
		) VALUES (?, ?, ?, 'website', '"http://audit.example"', '"https://audit.example"', 'updated', ?)`,
		businessID,
		versionID,
		versionID,
		observedAt.Unix(),
	); err != nil {
		t.Fatalf("insert change: %v", err)
	}

	leftID, rightID := businessID, duplicateID
	if leftID > rightID {
		leftID, rightID = rightID, leftID
	}
	if _, err := concrete.db.ExecContext(ctx,
		`INSERT INTO duplicate_candidates(
			left_business_id, right_business_id, score, signals, state, created_at
		) VALUES (?, ?, 0.91, '{"domain":1,"address":0.8}', 'pending', ?)
		ON CONFLICT(left_business_id, right_business_id) DO UPDATE SET
			score = excluded.score, signals = excluded.signals, state = 'pending'`,
		leftID,
		rightID,
		observedAt.Unix(),
	); err != nil {
		t.Fatalf("insert duplicate evidence: %v", err)
	}

	detail, err := concrete.GetBusiness(ctx, businessID)
	if err != nil {
		t.Fatalf("GetBusiness() error = %v", err)
	}
	if len(detail.Sources) == 0 || detail.Sources[0].RawJSON == "" || len(detail.Provenance) == 0 {
		t.Fatalf("source/provenance detail missing: %+v", detail)
	}
	if len(detail.Websites) != 1 || detail.Websites[0].HTTPStatus == nil || *detail.Websites[0].HTTPStatus != 200 {
		t.Fatalf("website detail = %+v", detail.Websites)
	}
	if len(detail.Emails) == 0 || detail.Emails[0].DomainHasMX == nil || !*detail.Emails[0].DomainHasMX {
		t.Fatalf("email detail = %+v", detail.Emails)
	}
	if len(detail.Phones) == 0 || len(detail.SocialProfiles) != 1 {
		t.Fatalf("contact detail: phones=%+v social=%+v", detail.Phones, detail.SocialProfiles)
	}
	if len(detail.Versions) == 0 || detail.Versions[0].Snapshot == "" || len(detail.Changes) != 1 {
		t.Fatalf("history detail: versions=%+v changes=%+v", detail.Versions, detail.Changes)
	}
	if len(detail.DuplicateMatches) != 1 || detail.DuplicateMatches[0].BusinessID != duplicateID ||
		len(detail.Duplicates) != 1 || detail.Duplicates[0] != duplicateID {
		t.Fatalf("duplicate detail: ids=%v matches=%+v", detail.Duplicates, detail.DuplicateMatches)
	}
}
