package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestParseResultSearchSupportsNestedAndLegacyORFilters(t *testing.T) {
	t.Parallel()

	values := url.Values{}
	values.Set("filter_json", `{"logic":"and","groups":[{"logic":"or","filters":[{"field":"city","operator":"eq","value":"San Francisco"},{"field":"city","operator":"eq","value":"Oakland"}]}],"filters":[{"field":"rating","operator":"gte","value":"4.5"}]}`)
	request := httptest.NewRequest("GET", "/api/v1/results?"+values.Encode(), nil)
	search, err := parseResultSearch(request)
	if err != nil {
		t.Fatalf("parseResultSearch() error = %v", err)
	}
	if search.FilterGroup == nil || search.FilterGroup.Logic != "and" || len(search.FilterGroup.Groups) != 1 ||
		len(search.FilterGroup.Filters) != 1 {
		t.Fatalf("nested filter = %+v", search.FilterGroup)
	}

	values = url.Values{
		"filter_field":    {"city", "city"},
		"filter_operator": {"eq", "eq"},
		"filter_value":    {"San Francisco", "Oakland"},
		"filter_logic":    {"or"},
	}
	request = httptest.NewRequest("GET", "/api/v1/results?"+values.Encode(), nil)
	search, err = parseResultSearch(request)
	if err != nil {
		t.Fatalf("parseResultSearch(legacy OR) error = %v", err)
	}
	if len(search.Filters) != 0 || search.FilterGroup == nil || search.FilterGroup.Logic != "or" ||
		len(search.FilterGroup.Filters) != 2 {
		t.Fatalf("legacy OR filter = %+v", search)
	}
}

func TestSavedResultViewRoundTripsNestedAndORFilters(t *testing.T) {
	t.Parallel()

	values := url.Values{
		"q":               {"dentist"},
		"filter_field":    {"city", "city"},
		"filter_operator": {"eq", "eq"},
		"filter_value":    {"San Francisco", "Oakland"},
		"filter_logic":    {"or"},
		"filter_json":     {`{"logic":"and","filters":[{"field":"rating","operator":"gte","value":"4.5"}]}`},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/saved-views", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := request.ParseForm(); err != nil {
		t.Fatalf("ParseForm() error = %v", err)
	}
	search, err := resultSearchFromForm(request)
	if err != nil {
		t.Fatalf("resultSearchFromForm() error = %v", err)
	}
	if search.Query != "dentist" || len(search.Filters) != 0 || search.FilterGroup == nil ||
		search.FilterGroup.Logic != "and" || len(search.FilterGroup.Groups) != 2 {
		t.Fatalf("saved search = %+v", search)
	}

	roundTrip := httptest.NewRequest(http.MethodGet, savedViewURL(search), nil)
	parsed, err := parseResultSearch(roundTrip)
	if err != nil {
		t.Fatalf("parseResultSearch(savedViewURL) error = %v", err)
	}
	if parsed.Query != search.Query || resultFilterGroupJSON(parsed.FilterGroup) != resultFilterGroupJSON(search.FilterGroup) {
		t.Fatalf("round trip = %+v, want %+v", parsed, search)
	}
}

func TestParseResultSearchBoundsNestedFilterLanguage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "unknown field", value: `{"logic":"and","surprise":true,"filters":[{"field":"city","operator":"eq","value":"x"}]}`, want: "unknown field"},
		{name: "trailing JSON", value: `{"logic":"and","filters":[{"field":"city","operator":"eq","value":"x"}]} {}`, want: "one JSON object"},
		{name: "empty group", value: `{"logic":"and"}`, want: "group is empty"},
		{name: "invalid logic", value: `{"logic":"xor","filters":[{"field":"city","operator":"eq","value":"x"}]}`, want: "logic"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			values := url.Values{"filter_json": {test.value}}
			_, err := parseResultSearch(httptest.NewRequest("GET", "/api/v1/results?"+values.Encode(), nil))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseResultSearch() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
