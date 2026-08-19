package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// newManualEditFixture opens a fresh repository and imports one business so an
// edit has something durable to act on.
func newManualEditFixture(t *testing.T) (*repo, string, func()) {
	t.Helper()

	ctx := context.Background()
	dataDirectory := t.TempDir()
	repository, err := New(filepath.Join(dataDirectory, "manual-edits.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete := repository.(*repo)

	job := resultImportJob("job-manual-edit", time.Now().UTC())
	if err := concrete.Create(ctx, &job); err != nil {
		t.Fatalf("create job: %v", err)
	}

	path := filepath.Join(dataDirectory, "manual-edit.csv")
	writeLegacyResultRows(t, path, map[string]string{
		"title": "Harbor Dental", "category": "Dentist", "place_id": "harbor-manual-1",
		"address": "1 Market St, San Francisco, CA 94105, United States",
		"phone":   "+1 415 555 0100", "website": "https://harbordental.example",
	})
	if _, err := concrete.ImportLegacyCSV(ctx, job, path); err != nil {
		t.Fatalf("import business: %v", err)
	}

	page, err := concrete.SearchBusinesses(ctx, web.ResultSearch{Limit: 5})
	if err != nil || len(page.Results) != 1 {
		t.Fatalf("seeded business lookup: results=%d err=%v", len(page.Results), err)
	}

	return concrete, page.Results[0].ID, func() {
		if err := concrete.db.Close(); err != nil {
			t.Errorf("close repository: %v", err)
		}
	}
}

func TestManualPhoneEditUpdatesColumnProvenanceChangeAndAudit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, businessID, closeRepository := newManualEditFixture(t)

	defer closeRepository()

	result, err := repository.ApplyManualFieldEdit(ctx, web.ManualFieldEdit{
		BusinessID: businessID, Field: "phone", Value: "+1 415 555 0199",
		Reason: "verified with the front desk", Operator: "reviewer-7",
	})
	if err != nil {
		t.Fatalf("ApplyManualFieldEdit() error = %v", err)
	}

	if result.Value != "+1 415 555 0199" || result.PreviousValue != "+1 415 555 0100" {
		t.Fatalf("edit result = %#v", result)
	}

	// The column and its import-style normalized companion both change.
	var phone, normalizedPhone string
	if err := repository.db.QueryRowContext(ctx,
		`SELECT phone, normalized_phone FROM businesses WHERE id = ?`, businessID,
	).Scan(&phone, &normalizedPhone); err != nil {
		t.Fatalf("read business columns: %v", err)
	}

	if phone != "+1 415 555 0199" || normalizedPhone != "+14155550199" {
		t.Fatalf("stored phone = %q normalized = %q", phone, normalizedPhone)
	}

	// Provenance keeps the previous value, the operator, and the reason, and
	// becomes the single preferred observation for the field.
	var original, normalized, operator, reason string
	var preferred int
	if err := repository.db.QueryRowContext(ctx,
		`SELECT original_value, normalized_value, operator, edit_reason, preferred
		FROM field_provenance
		WHERE business_id = ? AND field_name = 'phone' AND source_type = 'manual_edit'`,
		businessID,
	).Scan(&original, &normalized, &operator, &reason, &preferred); err != nil {
		t.Fatalf("read manual edit provenance: %v", err)
	}

	if original != "+1 415 555 0100" || normalized != "+1 415 555 0199" ||
		operator != "reviewer-7" || reason != "verified with the front desk" || preferred != 1 {
		t.Fatalf("provenance original=%q normalized=%q operator=%q reason=%q preferred=%d",
			original, normalized, operator, reason, preferred)
	}

	var supersededPreferred int
	if err := repository.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM field_provenance
		WHERE business_id = ? AND field_name = 'phone' AND preferred = 1 AND superseded_at IS NULL`,
		businessID,
	).Scan(&supersededPreferred); err != nil {
		t.Fatalf("count preferred provenance: %v", err)
	}

	if supersededPreferred != 1 {
		t.Fatalf("preferred provenance rows = %d, want exactly 1", supersededPreferred)
	}

	// The edit shows up in the change history and in the audit log.
	var changes, audits int
	if err := repository.db.QueryRowContext(ctx,
		`SELECT
			(SELECT COUNT(*) FROM business_changes
				WHERE business_id = ? AND field_name = 'phone' AND change_kind = 'manual_edit'),
			(SELECT COUNT(*) FROM audit_logs
				WHERE action = 'business_field_edited' AND entity_id = ?)`,
		businessID, businessID,
	).Scan(&changes, &audits); err != nil {
		t.Fatalf("read change and audit rows: %v", err)
	}

	if changes != 1 || audits != 1 {
		t.Fatalf("change rows = %d, audit rows = %d, want 1 and 1", changes, audits)
	}

	// A second, independent read returns the new value.
	detail, err := repository.GetBusiness(ctx, businessID)
	if err != nil {
		t.Fatalf("GetBusiness() error = %v", err)
	}

	if detail.Business.Phone != "+1 415 555 0199" || detail.Business.NormalizedPhone != "+14155550199" {
		t.Fatalf("re-read phone = %q normalized = %q",
			detail.Business.Phone, detail.Business.NormalizedPhone)
	}
}

func TestManualWebsiteEditRefreshesDomainAndUnknownBusinessFails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, businessID, closeRepository := newManualEditFixture(t)

	defer closeRepository()

	result, err := repository.ApplyManualFieldEdit(ctx, web.ManualFieldEdit{
		BusinessID: businessID, Field: "website", Value: "https://www.harbordentalsf.com/home",
		Reason: "site moved to a new domain", Operator: "reviewer-7",
	})
	if err != nil {
		t.Fatalf("ApplyManualFieldEdit() error = %v", err)
	}

	if result.Value != "https://www.harbordentalsf.com/home" {
		t.Fatalf("stored website = %q", result.Value)
	}

	var website, domain string
	if err := repository.db.QueryRowContext(ctx,
		`SELECT website, domain FROM businesses WHERE id = ?`, businessID,
	).Scan(&website, &domain); err != nil {
		t.Fatalf("read website columns: %v", err)
	}

	if website != "https://www.harbordentalsf.com/home" || domain != "harbordentalsf.com" {
		t.Fatalf("website = %q domain = %q", website, domain)
	}

	_, err = repository.ApplyManualFieldEdit(ctx, web.ManualFieldEdit{
		BusinessID: "biz_does_not_exist", Field: "name", Value: "Ghost",
		Reason: "should never be stored",
	})
	if !errors.Is(err, web.ErrBusinessNotFound) {
		t.Fatalf("unknown business error = %v, want ErrBusinessNotFound", err)
	}
}
