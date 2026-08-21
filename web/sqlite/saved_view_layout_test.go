package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// A saved view pins a whole workspace, not just a query: reopening it has to
// restore the same visible columns, their order, and the grouping.

func TestSavedResultViewRoundTripsColumnsAndGrouping(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	now := time.Now().UTC()
	view := web.SavedResultView{
		ID:   "view-layout-1",
		Name: "Outreach review",
		Search: web.ResultSearch{
			Query: "dentist", Sort: "rating_desc", Limit: 25,
			Filters: []web.ResultFilter{{Field: "website", Operator: "empty"}},
		},
		Columns:   []string{"select", "name", "contacts", "emails", "social"},
		Group:     "city",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := repository.SaveResultView(ctx, view); err != nil {
		t.Fatalf("SaveResultView() error = %v", err)
	}

	stored, err := repository.GetSavedResultView(ctx, view.ID)
	if err != nil {
		t.Fatalf("GetSavedResultView() error = %v", err)
	}

	if stored.Group != "city" {
		t.Fatalf("grouping = %q, want city", stored.Group)
	}

	if len(stored.Columns) != len(view.Columns) {
		t.Fatalf("columns = %v, want %v", stored.Columns, view.Columns)
	}

	for index, column := range view.Columns {
		if stored.Columns[index] != column {
			t.Fatalf("column %d = %q, want %q", index, stored.Columns[index], column)
		}
	}

	if stored.Search.Sort != "rating_desc" || stored.Search.Query != "dentist" {
		t.Fatalf("search = %+v, want the saved query and sort", stored.Search)
	}

	// Saving again must replace the layout rather than accumulate one.
	stored.Columns = []string{"select", "name", "quality"}
	stored.Group = "none"

	if err := repository.SaveResultView(ctx, stored); err != nil {
		t.Fatalf("SaveResultView() update error = %v", err)
	}

	updated, err := repository.GetSavedResultView(ctx, view.ID)
	if err != nil {
		t.Fatalf("GetSavedResultView() after update error = %v", err)
	}

	if len(updated.Columns) != 3 || updated.Group != "none" {
		t.Fatalf("updated layout = %v/%q, want three columns and no grouping", updated.Columns, updated.Group)
	}
}

func TestSavedResultViewWithoutALayoutStaysEmpty(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	view := web.SavedResultView{
		ID: "view-layout-2", Name: "No layout",
		Search: web.ResultSearch{Limit: 25},
	}

	if err := repository.SaveResultView(ctx, view); err != nil {
		t.Fatalf("SaveResultView() error = %v", err)
	}

	stored, err := repository.GetSavedResultView(ctx, view.ID)
	if err != nil {
		t.Fatalf("GetSavedResultView() error = %v", err)
	}

	if len(stored.Columns) != 0 {
		t.Fatalf("columns = %v, want none", stored.Columns)
	}

	if stored.Group != "none" {
		t.Fatalf("grouping = %q, want none", stored.Group)
	}
}
