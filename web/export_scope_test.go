package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The export builder must be able to address all four record scopes the
// specification names. Each one resolves to a different stored filter, and the
// stored filter is what a repeat or a preset replays later, so the resolution
// itself is the contract worth pinning.

func TestExportCreationResolvesEveryRecordScope(t *testing.T) {
	t.Parallel()

	server := newMaintenanceActionServer(t, t.TempDir())

	tests := []struct {
		name       string
		form       url.Values
		sourceType string
		assert     func(*testing.T, exportCreationRequest)
	}{
		{
			name:       "all",
			form:       url.Values{"format": {"csv"}, "source_scope": {"all"}, "name": {"Everything"}},
			sourceType: "results_all",
			assert: func(t *testing.T, creation exportCreationRequest) {
				if len(creation.Search.Filters) != 0 || creation.Search.Query != "" {
					t.Fatalf("the all scope carried a filter: %+v", creation.Search)
				}
			},
		},
		{
			name: "filtered",
			form: url.Values{
				"format": {"csv"}, "source_scope": {"filtered"}, "name": {"Filtered"},
				"q": {"dentist"}, "filter_field": {"city"}, "filter_operator": {"eq"}, "filter_value": {"San Francisco"},
			},
			sourceType: "results_filtered",
			assert: func(t *testing.T, creation exportCreationRequest) {
				if creation.Search.Query != "dentist" {
					t.Fatalf("the filtered scope lost the search text: %+v", creation.Search)
				}
				if len(creation.Search.Filters) != 1 || creation.Search.Filters[0].Field != "city" {
					t.Fatalf("the filtered scope lost its filters: %+v", creation.Search.Filters)
				}
			},
		},
		{
			name: "selected",
			form: url.Values{
				"format": {"csv"}, "source_scope": {"selected"}, "name": {"Selected"},
				"selected_ids": {"business-one, business-two"},
			},
			sourceType: "results_selected",
			assert: func(t *testing.T, creation exportCreationRequest) {
				if len(creation.Search.Filters) != 1 || creation.Search.Filters[0].Operator != "in" {
					t.Fatalf("the selected scope did not become an id filter: %+v", creation.Search.Filters)
				}
				if !strings.Contains(creation.Search.Filters[0].Value, "business-two") {
					t.Fatalf("the selected scope dropped an identifier: %+v", creation.Search.Filters[0])
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequest(http.MethodPost, "/api/v1/exports", strings.NewReader(test.form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if err := request.ParseForm(); err != nil {
				t.Fatal(err)
			}

			creation, err := server.resolveExportCreation(request)
			if err != nil {
				t.Fatal(err)
			}
			if creation.SourceType != test.sourceType {
				t.Fatalf("source type = %q, want %q", creation.SourceType, test.sourceType)
			}
			test.assert(t, creation)
		})
	}

	// A saved view must exist before it can be exported; an unknown identifier
	// is refused rather than silently exporting everything.
	request := httptest.NewRequest(http.MethodPost, "/api/v1/exports",
		strings.NewReader("format=csv&source_scope=saved_view&saved_view_id=missing-view"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := request.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if _, err := server.resolveExportCreation(request); err == nil {
		t.Fatal("an unknown saved view was accepted as an export scope")
	}
}

// Delivery options must survive the round trip into stored configuration,
// because a repeated export replays exactly what was stored.
func TestExportDeliveryOptionsRoundTripThroughStoredConfiguration(t *testing.T) {
	t.Parallel()

	server := newMaintenanceActionServer(t, t.TempDir())

	form := url.Values{
		"format": {"csv"}, "source_scope": {"all"}, "name": {"Split delivery"},
		"split_by": {"city"}, "zip": {"true"}, "include_raw": {"true"},
		"include_sources": {"true"}, "include_provenance": {"true"}, "include_changes": {"true"},
		"columns_spec": {"name=Business\ncity=Market"},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/exports", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := request.ParseForm(); err != nil {
		t.Fatal(err)
	}

	creation, err := server.resolveExportCreation(request)
	if err != nil {
		t.Fatal(err)
	}
	options := creation.Options
	if options.SplitBy != "city" || !options.ZIP || !options.IncludeRaw ||
		!options.IncludeSources || !options.IncludeProvenance || !options.IncludeChanges {
		t.Fatalf("delivery options = %+v", options)
	}

	columnJSON, optionJSON, err := encodeExportConfiguration(creation.Columns, options)
	if err != nil {
		t.Fatal(err)
	}
	filters, err := json.Marshal(creation.Search)
	if err != nil {
		t.Fatal(err)
	}

	restored, err := storedExportCreation(creation.Name, creation.Format, string(filters), columnJSON, optionJSON)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Options != options {
		t.Fatalf("stored options = %+v, want %+v", restored.Options, options)
	}
	// The chosen columns come first, and the include-* options appended the
	// raw, source, provenance, and change columns that carry that evidence.
	if len(restored.Columns) != 6 || restored.Columns[0].Label != "Business" || restored.Columns[1].Label != "Market" {
		t.Fatalf("stored columns = %+v", restored.Columns)
	}
	for _, key := range []string{"raw_json", "sources_json", "provenance_json", "changes_json"} {
		found := false
		for _, column := range restored.Columns {
			if column.Key == key {
				found = true

				break
			}
		}
		if !found {
			t.Fatalf("stored columns miss the %s evidence column: %+v", key, restored.Columns)
		}
	}
}
