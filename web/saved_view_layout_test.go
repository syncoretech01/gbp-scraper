package web

import (
	"net/url"
	"strings"
	"testing"
)

// A saved view has to carry the whole workspace. These tests pin the layout
// half of that promise: bounding what an operator may store, and putting a
// stored layout back on the Results URL that reopens it.

func TestNormalizeSavedViewLayoutBoundsOperatorInput(t *testing.T) {
	t.Parallel()

	columns, group := NormalizeSavedViewLayout(
		[]string{" Name ", "name", "", "contacts", strings.Repeat("x", 40)},
		"CITY",
	)

	if len(columns) != 2 || columns[0] != "name" || columns[1] != "contacts" {
		t.Fatalf("columns = %v, want the deduplicated lower-case pair", columns)
	}

	if group != "city" {
		t.Fatalf("group = %q, want city", group)
	}

	if _, unknown := NormalizeSavedViewLayout(nil, "by-mood"); unknown != "none" {
		t.Fatalf("unknown grouping = %q, want none", unknown)
	}

	overflow := make([]string, 0, MaximumSavedViewColumns+10)
	for index := range MaximumSavedViewColumns + 10 {
		overflow = append(overflow, "column"+string(rune('a'+index%26))+string(rune('a'+index/26)))
	}

	bounded, _ := NormalizeSavedViewLayout(overflow, "none")
	if len(bounded) != MaximumSavedViewColumns {
		t.Fatalf("bounded columns = %d, want %d", len(bounded), MaximumSavedViewColumns)
	}
}

func TestSavedViewLayoutURLCarriesColumnsAndGrouping(t *testing.T) {
	t.Parallel()

	target := savedViewLayoutURL(SavedResultView{
		Name:    "Outreach review",
		Search:  ResultSearch{Query: "dentist", Sort: "rating_desc"},
		Columns: []string{"select", "name", "contacts"},
		Group:   "city",
	})

	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", target, err)
	}

	if parsed.Path != "/app/results" {
		t.Fatalf("path = %q, want /app/results", parsed.Path)
	}

	values := parsed.Query()
	if values.Get("columns") != "select,name,contacts" {
		t.Fatalf("columns = %q, want the stored order", values.Get("columns"))
	}

	if values.Get("group") != "city" {
		t.Fatalf("group = %q, want city", values.Get("group"))
	}

	if values.Get("q") != "dentist" || values.Get("sort") != "rating_desc" {
		t.Fatalf("query = %v, want the saved search preserved", values)
	}

	// A view without a layout must not add empty parameters.
	plain := savedViewLayoutURL(SavedResultView{Search: ResultSearch{Query: "dentist"}})
	if strings.Contains(plain, "columns=") || strings.Contains(plain, "group=") {
		t.Fatalf("layout-free view URL = %q, want no layout parameters", plain)
	}
}

func TestNormalizeSavedViewLayoutQueryReadsTheResultsURL(t *testing.T) {
	t.Parallel()

	columns, group := NormalizeSavedViewLayoutQuery(url.Values{
		"columns": {"select,name,,emails"},
		"group":   {"reviewed"},
	})

	if columns != "select,name,emails" {
		t.Fatalf("columns = %q, want the cleaned list", columns)
	}

	if group != "reviewed" {
		t.Fatalf("group = %q, want reviewed", group)
	}

	empty, none := NormalizeSavedViewLayoutQuery(url.Values{})
	if empty != "" || none != "" {
		t.Fatalf("empty URL = %q/%q, want no layout", empty, none)
	}
}
