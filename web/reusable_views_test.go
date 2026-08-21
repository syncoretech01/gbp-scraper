package web

import (
	"strings"
	"testing"
	"time"
)

// Specification section 09 names eight example reusable views. They are shipped
// as starter content, so this test is the checklist: every example exists, and
// each one is expressed in the bounded filter language rather than in prose.

func TestStarterViewsCoverEverySpecificationExample(t *testing.T) {
	t.Parallel()

	views := starterResultViews(time.Now().UTC())
	byName := make(map[string]SavedResultView, len(views))

	for _, view := range views {
		byName[view.Name] = view
	}

	examples := map[string][]string{
		"Businesses without websites":          {"website"},
		"Active website, no email":             {"website_status", "email"},
		"Highly rated, low-quality website":    {"rating", "website", "quality_score"},
		"Has phone, no website":                {"phone", "website"},
		"Email and LinkedIn":                   {"email", "social"},
		"50+ reviews, open":                    {"review_count"},
		"New or changed since the last scrape": {},
		"Permanently closed listings":          {"business_status"},
	}

	for name, fields := range examples {
		view, ok := byName[name]
		if !ok {
			t.Errorf("example reusable view %q is missing", name)

			continue
		}

		if len(view.Search.Filters) == 0 && view.Search.FilterGroup == nil {
			t.Errorf("example view %q carries no filter", name)
		}

		for _, field := range fields {
			if !starterViewUsesField(view.Search, field) {
				t.Errorf("example view %q does not filter on %q", name, field)
			}
		}
	}
}

func TestNewOrChangedStarterViewMatchesTheImportVocabulary(t *testing.T) {
	t.Parallel()

	var view SavedResultView

	for _, candidate := range starterResultViews(time.Now().UTC()) {
		if candidate.Name == "New or changed since the last scrape" {
			view = candidate
		}
	}

	if view.Search.FilterGroup == nil {
		t.Fatal("the new-or-changed view has no nested expression")
	}

	if !strings.EqualFold(view.Search.FilterGroup.Logic, "or") {
		t.Fatalf("logic = %q, want or", view.Search.FilterGroup.Logic)
	}

	// The import writes exactly these change statuses, so the view must use
	// them rather than a friendlier invented word.
	wanted := map[string]bool{"new": true, "updated": true}
	for _, filter := range view.Search.FilterGroup.Filters {
		if filter.Field != "change_status" {
			t.Fatalf("filter field = %q, want change_status", filter.Field)
		}

		delete(wanted, filter.Value)
	}

	if len(wanted) != 0 {
		t.Fatalf("missing change statuses: %v", wanted)
	}
}

// starterViewUsesField reports whether a saved search filters on a field,
// looking through the nested expression as well as the flat rows.
func starterViewUsesField(search ResultSearch, field string) bool {
	for _, filter := range search.Filters {
		if filter.Field == field {
			return true
		}
	}

	var walk func(group *ResultFilterGroup) bool
	walk = func(group *ResultFilterGroup) bool {
		if group == nil {
			return false
		}

		for _, filter := range group.Filters {
			if filter.Field == field {
				return true
			}
		}

		for index := range group.Groups {
			if walk(&group.Groups[index]) {
				return true
			}
		}

		return false
	}

	return walk(search.FilterGroup)
}
