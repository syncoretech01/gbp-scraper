package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

func TestAdvancedResultFiltersSupportNestedLogicAndSpatialQueries(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete := repository.(*repo)
	t.Cleanup(func() { _ = concrete.db.Close() })

	job := resultImportJob("advanced-filter-job", time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC))
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "advanced.csv")
	writeLegacyResultRows(t, path,
		map[string]string{
			"title": "Golden Gate Dental", "category": "Dentist", "status": "Open",
			"address":          "1 Market St, San Francisco, CA 94105, United States",
			"complete_address": `{"city":"San Francisco","state":"CA","postal_code":"94105","country":"US"}`,
			"website":          "https://golden.example", "emails": "info@golden.example",
			"review_rating": "4.9", "review_count": "120", "latitude": "37.789", "longitude": "-122.394",
			"place_id": "advanced-sf",
		},
		map[string]string{
			"title": "East Bay Dental", "category": "Dental clinic", "status": "Open",
			"address":          "1 Broadway, Oakland, CA 94607, United States",
			"complete_address": `{"city":"Oakland","state":"CA","postal_code":"94607","country":"US"}`,
			"review_rating":    "4.6", "review_count": "70", "latitude": "37.8044", "longitude": "-122.2712",
			"place_id": "advanced-oak",
		},
		map[string]string{
			"title": "Closed Smile", "category": "Dentist", "status": "Permanently closed",
			"address":          "1 Main St, San Jose, CA 95113, United States",
			"complete_address": `{"city":"San Jose","state":"CA","postal_code":"95113","country":"US"}`,
			"review_rating":    "3.4", "review_count": "12", "latitude": "37.3382", "longitude": "-121.8863",
			"place_id": "advanced-sj",
		},
	)
	if _, err := concrete.ImportLegacyCSV(ctx, job, path); err != nil {
		t.Fatalf("ImportLegacyCSV() error = %v", err)
	}

	var sfID string
	if err := concrete.db.QueryRowContext(ctx, "SELECT id FROM businesses WHERE place_id = 'advanced-sf'").Scan(&sfID); err != nil {
		t.Fatalf("read SF business ID: %v", err)
	}
	if _, err := concrete.db.ExecContext(ctx,
		`INSERT INTO social_profiles(business_id, platform, url, source_url, confidence)
		 VALUES (?, 'linkedin', 'https://linkedin.com/company/golden', 'https://golden.example', 0.9)`, sfID,
	); err != nil {
		t.Fatalf("insert social profile: %v", err)
	}

	tests := []struct {
		name   string
		search web.ResultSearch
		want   int64
	}{
		{
			name: "nested or and numeric",
			search: web.ResultSearch{Limit: 25, FilterGroup: &web.ResultFilterGroup{
				Logic: "and",
				Groups: []web.ResultFilterGroup{{
					Logic: "or",
					Filters: []web.ResultFilter{
						{Field: "city", Operator: "eq", Value: "San Francisco"},
						{Field: "city", Operator: "eq", Value: "Oakland"},
					},
				}},
				Filters: []web.ResultFilter{{Field: "rating", Operator: "gt", Value: "4.7"}},
			}},
			want: 1,
		},
		{name: "numeric between", search: web.ResultSearch{Limit: 25, Filters: []web.ResultFilter{{Field: "reviews", Operator: "between", Value: "60,130"}}}, want: 2},
		{name: "text negative", search: web.ResultSearch{Limit: 25, Filters: []web.ResultFilter{{Field: "name", Operator: "not_contains", Value: "Closed"}}}, want: 2},
		{name: "date range", search: web.ResultSearch{Limit: 25, Filters: []web.ResultFilter{{Field: "updated_at", Operator: "between", Value: "2026-08-10,2026-08-10"}}}, want: 3},
		{name: "radius", search: web.ResultSearch{Limit: 25, Filters: []web.ResultFilter{{Field: "distance", Operator: "within", Value: "37.7749,-122.4194,5"}}}, want: 1},
		{name: "bbox", search: web.ResultSearch{Limit: 25, Filters: []web.ResultFilter{{Field: "bbox", Operator: "within", Value: "37.70,-122.52,37.84,-122.35"}}}, want: 1},
		{name: "polygon", search: web.ResultSearch{Limit: 25, Filters: []web.ResultFilter{{Field: "polygon", Operator: "within", Value: `{"type":"Polygon","coordinates":[[[-122.52,37.70],[-122.35,37.70],[-122.35,37.84],[-122.52,37.84],[-122.52,37.70]]]}`}}}, want: 1},
		{name: "social platform", search: web.ResultSearch{Limit: 25, Filters: []web.ResultFilter{{Field: "social", Operator: "eq", Value: "linkedin"}}}, want: 1},
		{name: "email availability", search: web.ResultSearch{Limit: 25, Filters: []web.ResultFilter{{Field: "email", Operator: "not_empty"}}}, want: 1},
		{name: "closed status", search: web.ResultSearch{Limit: 25, Filters: []web.ResultFilter{{Field: "change_status", Operator: "neq", Value: "new"}}}, want: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			page, err := concrete.SearchBusinesses(ctx, test.search)
			if err != nil {
				t.Fatalf("SearchBusinesses() error = %v", err)
			}
			if page.Total != test.want {
				t.Fatalf("SearchBusinesses() total = %d, want %d; results=%+v", page.Total, test.want, page.Results)
			}
		})
	}
}

func TestAdvancedResultFiltersRejectUnsafeOrUnboundedExpressions(t *testing.T) {
	t.Parallel()

	for _, test := range []web.ResultFilter{
		{Field: "name", Operator: "contains", Value: `%_' OR 1=1 --`},
		{Field: "distance", Operator: "within", Value: "91,0,5"},
		{Field: "bbox", Operator: "within", Value: "40,-120,30,-130"},
		{Field: "rating", Operator: "between", Value: "5,1"},
		{Field: "polygon", Operator: "within", Value: `{"type":"LineString","coordinates":[]}`},
	} {
		clause, args, err := resultFilterSQL(test)
		if test.Field == "name" {
			if err != nil || clause == "" || len(args) != 1 {
				t.Fatalf("injection-like text must remain a bound value: clause=%q args=%v err=%v", clause, args, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("resultFilterSQL(%+v) unexpectedly succeeded: %s %v", test, clause, args)
		}
	}

	tooDeep := web.ResultFilterGroup{Logic: "and", Filters: []web.ResultFilter{{Field: "city", Operator: "eq", Value: "x"}}}
	for range 5 {
		tooDeep = web.ResultFilterGroup{Logic: "and", Groups: []web.ResultFilterGroup{tooDeep}}
	}
	if _, _, err := resultFilterGroupSQL(tooDeep, 1, new(int)); err == nil {
		t.Fatal("resultFilterGroupSQL() accepted an expression deeper than four groups")
	}
}
