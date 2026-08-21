package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// lineageFilterFixture imports two runs of one market: a first run that
// discovers two businesses, and a rescan that re-observes one of them with a
// changed phone number and discovers a third. Both runs are then linked into
// one campaign, which is the evidence the new filters read.
func lineageFilterFixture(t *testing.T) (*repo, context.Context) {
	t.Helper()

	ctx := context.Background()

	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	concrete, ok := repository.(*repo)
	if !ok {
		t.Fatalf("repository type = %T, want *repo", repository)
	}

	t.Cleanup(func() { _ = concrete.db.Close() })

	first := resultImportJob("lineage-run-1", time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC))
	if err := repository.Create(ctx, &first); err != nil {
		t.Fatalf("create first job: %v", err)
	}

	firstPath := filepath.Join(t.TempDir(), "run-1.csv")
	writeLegacyResultRows(t, firstPath,
		map[string]string{
			"title": "Alpha Dental", "category": "Dentist", "status": "Open",
			"address": "1 Market St, San Francisco, CA 94105, United States",
			"phone":   "+1 415-555-0101", "place_id": "lineage-alpha",
		},
		map[string]string{
			"title": "Beta Dental", "category": "Dentist", "status": "Open",
			"address": "2 Market St, San Francisco, CA 94105, United States",
			"phone":   "+1 415-555-0102", "place_id": "lineage-beta",
		},
	)

	if _, err := concrete.ImportLegacyCSV(ctx, first, firstPath); err != nil {
		t.Fatalf("import first run: %v", err)
	}

	second := resultImportJob("lineage-run-2", time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC))
	if err := repository.Create(ctx, &second); err != nil {
		t.Fatalf("create rescan job: %v", err)
	}

	secondPath := filepath.Join(t.TempDir(), "run-2.csv")
	writeLegacyResultRows(t, secondPath,
		map[string]string{
			"title": "Alpha Dental", "category": "Dentist", "status": "Open",
			"address": "1 Market St, San Francisco, CA 94105, United States",
			"phone":   "+1 415-555-0999", "place_id": "lineage-alpha",
		},
		map[string]string{
			"title": "Gamma Dental", "category": "Dentist", "status": "Open",
			"address": "3 Market St, San Francisco, CA 94105, United States",
			"phone":   "+1 415-555-0103", "place_id": "lineage-gamma",
		},
	)

	if _, err := concrete.ImportLegacyCSV(ctx, second, secondPath); err != nil {
		t.Fatalf("import rescan: %v", err)
	}

	links := []web.JobCampaignLink{
		{JobID: "lineage-run-1", CampaignID: "lineage-run-1", RootJobID: "lineage-run-1"},
		{
			JobID: "lineage-run-2", CampaignID: "lineage-run-1", RootJobID: "lineage-run-1",
			SourceJobID: "lineage-run-1", Mode: web.RerunModeChangedOnly, Generation: 1,
		},
	}

	for _, link := range links {
		if err := concrete.SaveJobCampaignLink(ctx, link); err != nil {
			t.Fatalf("save campaign link %s: %v", link.JobID, err)
		}
	}

	return concrete, ctx
}

func TestLineageResultFiltersAnswerNewAndChangedQuestions(t *testing.T) {
	t.Parallel()

	repository, ctx := lineageFilterFixture(t)

	tests := []struct {
		name   string
		filter web.ResultFilter
		want   int64
	}{
		{
			name:   "first seen by the original run",
			filter: web.ResultFilter{Field: "first_seen_job", Operator: "eq", Value: "lineage-run-1"},
			want:   2,
		},
		{
			name:   "first seen by the rescan",
			filter: web.ResultFilter{Field: "first_seen_job", Operator: "eq", Value: "lineage-run-2"},
			want:   1,
		},
		{
			name:   "not first seen by the rescan",
			filter: web.ResultFilter{Field: "first_seen_job", Operator: "neq", Value: "lineage-run-2"},
			want:   2,
		},
		{
			name:   "changed by the rescan",
			filter: web.ResultFilter{Field: "changed_by_job", Operator: "eq", Value: "lineage-run-2"},
			want:   1,
		},
		{
			name:   "observed at all by the rescan",
			filter: web.ResultFilter{Field: "seen_by_job", Operator: "eq", Value: "lineage-run-2"},
			want:   2,
		},
		{
			name:   "first seen anywhere in the campaign",
			filter: web.ResultFilter{Field: "first_seen_campaign", Operator: "eq", Value: "lineage-run-1"},
			want:   3,
		},
		{
			name:   "changed anywhere in the campaign",
			filter: web.ResultFilter{Field: "changed_by_campaign", Operator: "eq", Value: "lineage-run-1"},
			want:   1,
		},
		{
			name:   "observed anywhere in the campaign",
			filter: web.ResultFilter{Field: "seen_by_campaign", Operator: "eq", Value: "lineage-run-1"},
			want:   3,
		},
		{
			name:   "changed since the first run",
			filter: web.ResultFilter{Field: "changed_at", Operator: "after", Value: "2026-08-15"},
			want:   1,
		},
		{
			name:   "changed before the rescan",
			filter: web.ResultFilter{Field: "changed_at", Operator: "before", Value: "2026-08-15"},
			want:   0,
		},
		{
			name:   "has any recorded change",
			filter: web.ResultFilter{Field: "changed_field", Operator: "not_empty"},
			want:   1,
		},
		{
			name:   "has no recorded change",
			filter: web.ResultFilter{Field: "changed_field", Operator: "empty"},
			want:   2,
		},
		{
			name:   "has more than one stored version",
			filter: web.ResultFilter{Field: "version_count", Operator: "gt", Value: "1"},
			want:   1,
		},
		{
			name:   "changed a phone field",
			filter: web.ResultFilter{Field: "changed_field", Operator: "contains", Value: "phone"},
			want:   1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			page, err := repository.SearchBusinesses(ctx, web.ResultSearch{
				Limit: 25, Filters: []web.ResultFilter{test.filter},
			})
			if err != nil {
				t.Fatalf("SearchBusinesses(%#v) error = %v", test.filter, err)
			}

			if page.Total != test.want {
				t.Fatalf("total = %d, want %d; results = %+v", page.Total, test.want, page.Results)
			}
		})
	}
}

func TestLineageResultFiltersComposeWithTheExistingLanguage(t *testing.T) {
	t.Parallel()

	repository, ctx := lineageFilterFixture(t)

	// "New in this rescan AND in San Francisco" is exactly the operator
	// question the filters exist for, and it must work through the same
	// nested group expression saved views persist.
	page, err := repository.SearchBusinesses(ctx, web.ResultSearch{
		Limit: 25,
		FilterGroup: &web.ResultFilterGroup{
			Logic: "and",
			Filters: []web.ResultFilter{
				{Field: "first_seen_job", Operator: "eq", Value: "lineage-run-2"},
				{Field: "city", Operator: "eq", Value: "San Francisco"},
			},
		},
	})
	if err != nil {
		t.Fatalf("SearchBusinesses() error = %v", err)
	}

	if page.Total != 1 || len(page.Results) != 1 || page.Results[0].Name != "Gamma Dental" {
		t.Fatalf("composed filter total = %d, results = %+v", page.Total, page.Results)
	}
}

func TestLineageResultFiltersRejectUnsafeInput(t *testing.T) {
	t.Parallel()

	repository, ctx := lineageFilterFixture(t)

	for _, filter := range []web.ResultFilter{
		{Field: "first_seen_job", Operator: "eq", Value: "'; DROP TABLE businesses; --"},
		{Field: "first_seen_job", Operator: "eq", Value: ""},
		{Field: "seen_by_campaign", Operator: "contains_all", Value: "lineage-run-1"},
		{Field: "changed_field", Operator: "eq", Value: ""},
		{Field: "changed_field", Operator: "within", Value: "phone"},
		{Field: "changed_at", Operator: "eq", Value: "not-a-date"},
		{Field: "version_count", Operator: "gt", Value: "not-a-number"},
	} {
		if _, err := repository.SearchBusinesses(ctx, web.ResultSearch{
			Limit: 25, Filters: []web.ResultFilter{filter},
		}); err == nil {
			t.Fatalf("filter %#v was accepted", filter)
		}
	}

	// The rejected statements changed nothing.
	page, err := repository.SearchBusinesses(ctx, web.ResultSearch{Limit: 25})
	if err != nil || page.Total != 3 {
		t.Fatalf("total after rejected filters = %d (%v), want 3", page.Total, err)
	}
}
