package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// The prospecting UI reads the migration-11 business columns through
// SearchBusinesses/GetBusiness and filters them through the bounded query
// language, so this exercises the SELECT, the scan, and every registered
// prospect filter column against a real schema.
func TestSearchBusinessesReturnsAndFiltersProspectSignals(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	job := resultImportJob("job-prospects", time.Now().UTC())
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "prospects.csv")
	writeLegacyResultRows(t, path,
		map[string]string{
			"title": "Harbor Dental", "category": "Dentist", "place_id": "prospect-place-1",
			"address":       "1 Market St, San Francisco, CA 94105, United States",
			"review_rating": "4.6", "review_count": "120", "status": "Open",
		},
		map[string]string{
			"title": "Bay Plumbing", "category": "Plumber", "place_id": "prospect-place-2",
			"address": "2 Mission St, San Francisco, CA 94105, United States",
			"website": "https://bayplumbing.example", "review_rating": "4.1",
			"review_count": "40", "status": "Open",
		},
	)
	if _, err := repository.ImportLegacyCSV(ctx, job, path); err != nil {
		t.Fatalf("ImportLegacyCSV() error = %v", err)
	}

	page, err := repository.SearchBusinesses(ctx, web.ResultSearch{Limit: 10})
	if err != nil {
		t.Fatalf("SearchBusinesses() error = %v", err)
	}
	if len(page.Results) != 2 {
		t.Fatalf("seeded businesses = %d, want 2", len(page.Results))
	}
	ids := make(map[string]string, len(page.Results))
	for _, result := range page.Results {
		ids[result.Name] = result.ID
		if result.ProspectStatus != "" || result.ProspectScore != nil || result.ProspectTier != "" {
			t.Fatalf("unscored business %q already reports prospect fields: %+v", result.Name, result)
		}
	}

	reasons := `[{"signal":"NO_WEBSITE","contribution":40,"detail":"the listing links no website"}]`
	if _, err := repository.db.ExecContext(ctx,
		`UPDATE businesses SET prospect_status = 'NO_WEBSITE', prospect_score = 87.5,
			prospect_tier = 'A', prospect_reasons = ?, prospect_updated_at = ?
		WHERE id = ?`, reasons, time.Now().Unix(), ids["Harbor Dental"]); err != nil {
		t.Fatalf("seed prospect columns: %v", err)
	}
	if _, err := repository.db.ExecContext(ctx,
		`UPDATE businesses SET prospect_status = 'LIVE', prospect_score = 12,
			prospect_tier = 'E', prospect_updated_at = ?
		WHERE id = ?`, time.Now().Unix(), ids["Bay Plumbing"]); err != nil {
		t.Fatalf("seed prospect columns: %v", err)
	}

	page, err = repository.SearchBusinesses(ctx, web.ResultSearch{Limit: 10, Sort: "name_asc"})
	if err != nil {
		t.Fatalf("SearchBusinesses() after seeding error = %v", err)
	}
	byName := make(map[string]web.BusinessResult, len(page.Results))
	for _, result := range page.Results {
		byName[result.Name] = result
	}
	dental := byName["Harbor Dental"]
	if dental.ProspectStatus != "NO_WEBSITE" || dental.ProspectTier != "A" ||
		dental.ProspectScore == nil || *dental.ProspectScore != 87.5 {
		t.Fatalf("scored business = %+v", dental)
	}
	plumbing := byName["Bay Plumbing"]
	if plumbing.ProspectStatus != "LIVE" || plumbing.ProspectTier != "E" ||
		plumbing.ProspectScore == nil || *plumbing.ProspectScore != 12 {
		t.Fatalf("scored business = %+v", plumbing)
	}

	filters := []struct {
		name   string
		filter web.ResultFilter
		want   string
	}{
		{"status", web.ResultFilter{Field: "prospect_status", Operator: "eq", Value: "NO_WEBSITE"}, "Harbor Dental"},
		{"tier", web.ResultFilter{Field: "prospect_tier", Operator: "eq", Value: "E"}, "Bay Plumbing"},
		{"score-gte", web.ResultFilter{Field: "prospect_score", Operator: "gte", Value: "50"}, "Harbor Dental"},
		{"score-lte", web.ResultFilter{Field: "prospect_score", Operator: "lte", Value: "20"}, "Bay Plumbing"},
	}
	for _, test := range filters {
		filtered, filterErr := repository.SearchBusinesses(ctx, web.ResultSearch{
			Limit:   10,
			Filters: []web.ResultFilter{test.filter},
		})
		if filterErr != nil {
			t.Fatalf("SearchBusinesses(%s) error = %v", test.name, filterErr)
		}
		if len(filtered.Results) != 1 || filtered.Results[0].Name != test.want {
			t.Fatalf("filter %s = %+v, want only %q", test.name, filtered.Results, test.want)
		}
	}

	detail, err := repository.GetBusiness(ctx, ids["Harbor Dental"])
	if err != nil {
		t.Fatalf("GetBusiness() error = %v", err)
	}
	if detail.Business.ProspectStatus != "NO_WEBSITE" || detail.Business.ProspectTier != "A" {
		t.Fatalf("detail prospect fields = %+v", detail.Business)
	}
	if detail.ProspectReasons != reasons {
		t.Fatalf("detail prospect reasons = %q, want %q", detail.ProspectReasons, reasons)
	}
}
