package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// A business that two jobs both discovered has two truthful answers to "which
// query found this". Reporting the newest one regardless of what the operator
// is looking at is how a job-scoped view ends up naming a different job's
// discovery, and an export of that view inherits the wrong provenance. These
// tests pin the scoped answer.

// scopedProvenanceFixture imports the same business under two jobs with two
// different discovery queries, plus a business only the first job saw.
func scopedProvenanceFixture(t *testing.T) (context.Context, *repo, string, string) {
	t.Helper()

	ctx := context.Background()
	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete, ok := repository.(*repo)
	if !ok {
		t.Fatal("New() did not return the bundled repository")
	}
	t.Cleanup(func() { _ = concrete.db.Close() })

	shared := map[string]string{
		"title": "Shared Ink Studio", "category": "Tattoo shop", "status": "Open",
		"address":          "1 Market St, San Francisco, CA 94105, United States",
		"complete_address": `{"city":"San Francisco","state":"CA","postal_code":"94105","country":"US"}`,
		"phone":            "+1 415 555 0100",
		"review_rating":    "4.8", "review_count": "210",
		"latitude": "37.789", "longitude": "-122.394", "place_id": "scoped-shared",
	}
	tattooOnly := map[string]string{
		"title": "Tattoo Only", "category": "Tattoo shop", "status": "Open",
		"address":          "2 Market St, San Francisco, CA 94105, United States",
		"complete_address": `{"city":"San Francisco","state":"CA","postal_code":"94105","country":"US"}`,
		"website":          "https://tattoo-only.example",
		"review_rating":    "4.1", "review_count": "12", "place_id": "scoped-tattoo",
	}

	tattoo := resultImportJob("scoped-tattoo-job", time.Date(2026, time.August, 1, 9, 0, 0, 0, time.UTC))
	tattoo.Data.Keywords = []string{"tattoo artist", "tattoo shop"}
	if err := repository.Create(ctx, &tattoo); err != nil {
		t.Fatalf("Create(tattoo) error = %v", err)
	}
	tattooCSV := filepath.Join(t.TempDir(), "tattoo.csv")
	writeLegacyResultRows(t, tattooCSV, shared, tattooOnly)
	if _, err := concrete.ImportLegacyCSV(ctx, tattoo, tattooCSV); err != nil {
		t.Fatalf("ImportLegacyCSV(tattoo) error = %v", err)
	}

	dental := resultImportJob("scoped-dental-job", time.Date(2026, time.August, 2, 9, 0, 0, 0, time.UTC))
	dental.Data.Keywords = []string{"dentists in San Francisco"}
	if err := repository.Create(ctx, &dental); err != nil {
		t.Fatalf("Create(dental) error = %v", err)
	}
	dentalCSV := filepath.Join(t.TempDir(), "dental.csv")
	writeLegacyResultRows(t, dentalCSV, shared)
	if _, err := concrete.ImportLegacyCSV(ctx, dental, dentalCSV); err != nil {
		t.Fatalf("ImportLegacyCSV(dental) error = %v", err)
	}

	return ctx, concrete, tattoo.ID, dental.ID
}

func TestSearchReportsTheObservationInsideTheScopeBeingViewed(t *testing.T) {
	t.Parallel()

	ctx, repository, tattooID, dentalID := scopedProvenanceFixture(t)

	// The dental job imported last, so an unscoped read reports its
	// observation. That is the correct workspace-wide answer.
	workspace, err := repository.SearchBusinesses(ctx, web.ResultSearch{Limit: 25})
	if err != nil {
		t.Fatalf("SearchBusinesses() error = %v", err)
	}
	if workspace.Total != 2 {
		t.Fatalf("workspace total = %d, want 2", workspace.Total)
	}
	shared := findBusinessByName(t, workspace.Results, "Shared Ink Studio")
	if shared.SourceJobID != dentalID {
		t.Fatalf("workspace source job = %q, want the newest observation %q", shared.SourceJobID, dentalID)
	}
	if shared.ObservationCount != 2 {
		t.Fatalf("workspace observation count = %d, want 2", shared.ObservationCount)
	}
	if len(shared.SourceJobIDs) != 2 || len(shared.SourceQueries) != 2 {
		t.Fatalf("workspace provenance jobs=%v queries=%v", shared.SourceJobIDs, shared.SourceQueries)
	}

	// Scoped to the tattoo job, the same row must report the tattoo job's own
	// observation and query, never the dental job's.
	scoped, err := repository.SearchBusinesses(ctx, web.ResultSearch{Limit: 25, JobID: tattooID})
	if err != nil {
		t.Fatalf("SearchBusinesses(tattoo) error = %v", err)
	}
	if scoped.Total != 2 {
		t.Fatalf("tattoo total = %d, want 2", scoped.Total)
	}
	scopedShared := findBusinessByName(t, scoped.Results, "Shared Ink Studio")
	if scopedShared.SourceJobID != tattooID {
		t.Fatalf("scoped source job = %q, want %q", scopedShared.SourceJobID, tattooID)
	}
	if scopedShared.SourceQuery != "tattoo artist | tattoo shop" {
		t.Fatalf("scoped source query = %q", scopedShared.SourceQuery)
	}
	if scopedShared.ObservationCount != 1 {
		t.Fatalf("scoped observation count = %d, want 1", scopedShared.ObservationCount)
	}
	// The workspace total stays visible beside the scoped count, so "seen
	// once by this job" is distinguishable from "seen once, ever".
	if scopedShared.TotalObservationCount != 2 {
		t.Fatalf("total observation count = %d, want 2", scopedShared.TotalObservationCount)
	}
	if len(scopedShared.SourceJobIDs) != 1 || scopedShared.SourceJobIDs[0] != tattooID {
		t.Fatalf("scoped source jobs = %v", scopedShared.SourceJobIDs)
	}
	if scopedShared.FirstObservedAt == nil || scopedShared.LastObservedAt == nil {
		t.Fatal("scoped provenance carried no observation window")
	}

	// Scoped to the dental job, the same business reports the other query.
	dentalPage, err := repository.SearchBusinesses(ctx, web.ResultSearch{Limit: 25, JobID: dentalID})
	if err != nil {
		t.Fatalf("SearchBusinesses(dental) error = %v", err)
	}
	if dentalPage.Total != 1 {
		t.Fatalf("dental total = %d, want 1", dentalPage.Total)
	}
	if dentalPage.Results[0].SourceQuery != "dentists in San Francisco" {
		t.Fatalf("dental source query = %q", dentalPage.Results[0].SourceQuery)
	}
}

// A source row written by enrichment carries no discovery query. Preferring
// the newest row regardless would report an empty query for a business whose
// discovery query is perfectly well known, so the one observation reported is
// the newest one in scope that actually names a query.
func TestScopedObservationPrefersARowThatNamesItsDiscoveryQuery(t *testing.T) {
	t.Parallel()

	ctx, repository, tattooID, _ := scopedProvenanceFixture(t)

	var businessID string
	if err := repository.db.QueryRowContext(ctx,
		"SELECT id FROM businesses WHERE place_id = 'scoped-shared'").Scan(&businessID); err != nil {
		t.Fatalf("read business id: %v", err)
	}
	if _, err := repository.db.ExecContext(ctx,
		`INSERT INTO business_sources(business_id, job_id, source_type, source_query,
			extraction_method, confidence, extracted_at)
		VALUES (?, ?, 'website', '', 'bounded_html_analysis', 1, ?)`,
		businessID, tattooID, time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC).Unix(),
	); err != nil {
		t.Fatalf("insert enrichment observation: %v", err)
	}

	page, err := repository.SearchBusinesses(ctx, web.ResultSearch{Limit: 25, JobID: tattooID})
	if err != nil {
		t.Fatalf("SearchBusinesses() error = %v", err)
	}
	shared := findBusinessByName(t, page.Results, "Shared Ink Studio")
	if shared.SourceQuery != "tattoo artist | tattoo shop" {
		t.Fatalf("source query = %q, want the discovery query", shared.SourceQuery)
	}
	if shared.SourceJobID != tattooID {
		t.Fatalf("source job = %q, want %q", shared.SourceJobID, tattooID)
	}
	if shared.ObservationCount != 2 {
		t.Fatalf("observation count = %d, want 2 in this job", shared.ObservationCount)
	}
}

// Every prospecting control on the Results table is a filter in the bounded
// query language, so an export can reproduce the table's own selection. A
// filter that only exists in the interface cannot be exported.
func TestProspectingFiltersAreAvailableToEveryQuery(t *testing.T) {
	t.Parallel()

	ctx, repository, tattooID, _ := scopedProvenanceFixture(t)

	tests := []struct {
		name   string
		filter web.ResultFilter
		want   int64
	}{
		{"no website", web.ResultFilter{Field: "no_website", Operator: "eq", Value: "true"}, 1},
		{"has website", web.ResultFilter{Field: "has_website", Operator: "eq", Value: "true"}, 1},
		{"contactable", web.ResultFilter{Field: "contactable", Operator: "eq", Value: "true"}, 1},
		{"never checked", web.ResultFilter{Field: "never_checked", Operator: "eq", Value: "true"}, 2},
		{"state never checked", web.ResultFilter{Field: "website_state", Operator: "eq", Value: web.WebsiteStateNeverChecked}, 2},
		{"state no website", web.ResultFilter{Field: "website_state", Operator: "eq", Value: web.WebsiteStateNoWebsite}, 1},
		{"state set", web.ResultFilter{Field: "website_state", Operator: "in", Value: "NO_WEBSITE,LIVE"}, 1},
		{"weak website", web.ResultFilter{Field: "weak_website", Operator: "eq", Value: "true"}, 0},
		{"observation count", web.ResultFilter{Field: "observation_count", Operator: "gte", Value: "2"}, 1},
		{"source query", web.ResultFilter{Field: "source_query", Operator: "contains", Value: "dentists"}, 1},
		{"source job", web.ResultFilter{Field: "source_job", Operator: "eq", Value: tattooID}, 2},
		{"email count", web.ResultFilter{Field: "email_count", Operator: "gte", Value: "1"}, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			page, err := repository.SearchBusinesses(ctx, web.ResultSearch{
				Limit: 25, Filters: []web.ResultFilter{test.filter},
			})
			if err != nil {
				t.Fatalf("SearchBusinesses(%+v) error = %v", test.filter, err)
			}
			if page.Total != test.want {
				t.Fatalf("total = %d, want %d", page.Total, test.want)
			}
		})
	}
}

// The scoped provenance expressions bind their job identifier rather than
// interpolating it, so a hostile job identifier can only ever be a value.
func TestScopedProvenanceBindsTheJobIdentifier(t *testing.T) {
	t.Parallel()

	ctx, repository, _, _ := scopedProvenanceFixture(t)

	page, err := repository.SearchBusinesses(ctx, web.ResultSearch{
		Limit: 25, JobID: "') OR 1=1 --",
	})
	if err != nil {
		t.Fatalf("SearchBusinesses() error = %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("an unknown job matched %d businesses, want 0", page.Total)
	}
}

// One page of a search and every page an export spools must describe the same
// set. The export paginates with a 250-row page, so a workspace larger than
// one page is where a second filter engine would start to drift.
func TestPaginatedReadsCoverExactlyTheMatchedSet(t *testing.T) {
	t.Parallel()

	ctx, repository, tattooID, _ := scopedProvenanceFixture(t)

	search := web.ResultSearch{JobID: tattooID, Limit: 1, Sort: "updated_desc"}
	collected := make(map[string]struct{})
	var total int64
	for offset := 0; ; offset++ {
		search.Offset = offset
		page, err := repository.SearchBusinesses(ctx, search)
		if err != nil {
			t.Fatalf("SearchBusinesses(offset %d) error = %v", offset, err)
		}
		total = page.Total
		if len(page.Results) == 0 {
			break
		}
		for _, business := range page.Results {
			collected[business.ID] = struct{}{}
		}
		if int64(len(collected)) >= page.Total {
			break
		}
	}
	if int64(len(collected)) != total {
		t.Fatalf("pagination covered %d rows, the query matched %d", len(collected), total)
	}

	single, err := repository.SearchBusinesses(ctx, web.ResultSearch{JobID: tattooID, Limit: 500})
	if err != nil {
		t.Fatalf("SearchBusinesses(single page) error = %v", err)
	}
	if int64(len(single.Results)) != total {
		t.Fatalf("one page returned %d rows, pagination covered %d", len(single.Results), total)
	}
	for _, business := range single.Results {
		if _, ok := collected[business.ID]; !ok {
			t.Fatalf("business %s is missing from the paginated pass", business.ID)
		}
	}
}

func findBusinessByName(t *testing.T, results []web.BusinessResult, name string) web.BusinessResult {
	t.Helper()

	for _, business := range results {
		if business.Name == name {
			return business
		}
	}
	t.Fatalf("business %q is not in the page", name)

	return web.BusinessResult{}
}

// The Results chips are nested OR expressions over stored classifications; the
// single-field prospecting filters are the same question asked once. They must
// select the same businesses, because a chip and an exported column that
// disagree is how a "weak website" file ends up holding something else.
func TestProspectingFlagsMatchTheChipExpressionsTheyReplace(t *testing.T) {
	t.Parallel()

	ctx, repository, _, _ := scopedProvenanceFixture(t)

	// Give the fixture one row in every weak state so the comparison is not
	// trivially satisfied by two empty sets.
	states := web.WeakWebsiteStates()
	rows, err := repository.db.QueryContext(ctx, "SELECT id FROM businesses ORDER BY id")
	if err != nil {
		t.Fatalf("read business ids: %v", err)
	}
	ids := make([]string, 0, 2)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	for index, id := range ids {
		if _, err := repository.db.ExecContext(ctx,
			"UPDATE businesses SET prospect_status = ? WHERE id = ?", states[index%len(states)], id,
		); err != nil {
			t.Fatalf("set prospect status: %v", err)
		}
	}

	chip := make([]web.ResultFilter, 0, len(states))
	for _, state := range states {
		chip = append(chip, web.ResultFilter{Field: "prospect_status", Operator: "eq", Value: state})
	}
	expression, err := repository.SearchBusinesses(ctx, web.ResultSearch{
		Limit: 500, FilterGroup: &web.ResultFilterGroup{Logic: "or", Filters: chip},
	})
	if err != nil {
		t.Fatalf("SearchBusinesses(chip) error = %v", err)
	}
	flag, err := repository.SearchBusinesses(ctx, web.ResultSearch{
		Limit: 500, Filters: []web.ResultFilter{{Field: "weak_website", Operator: "eq", Value: "true"}},
	})
	if err != nil {
		t.Fatalf("SearchBusinesses(flag) error = %v", err)
	}
	if expression.Total == 0 {
		t.Fatal("the chip expression matched nothing, so the comparison proves nothing")
	}
	if expression.Total != flag.Total {
		t.Fatalf("chip matched %d, weak_website matched %d", expression.Total, flag.Total)
	}
	seen := make(map[string]struct{}, len(expression.Results))
	for _, business := range expression.Results {
		seen[business.ID] = struct{}{}
	}
	for _, business := range flag.Results {
		if _, ok := seen[business.ID]; !ok {
			t.Fatalf("weak_website selected %s, the chip expression did not", business.ID)
		}
	}
}
