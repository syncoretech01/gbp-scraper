package prospect

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestParseZIPCSVGood(t *testing.T) {
	csvData := "zip,city,state,latitude,longitude,population\n" +
		"78704,Austin,TX,30.245,-97.766,47000\n" +
		"02116,Boston,MA,42.35,-71.076,22000\n"

	areas, err := ParseZIPCSV(strings.NewReader(csvData))
	if err != nil {
		t.Fatalf("ParseZIPCSV() error = %v", err)
	}

	want := []ZIPArea{
		{ZIP: "78704", City: "Austin", State: "TX", Latitude: 30.245, Longitude: -97.766, Population: 47000},
		{ZIP: "02116", City: "Boston", State: "MA", Latitude: 42.35, Longitude: -71.076, Population: 22000},
	}

	if !reflect.DeepEqual(areas, want) {
		t.Fatalf("ParseZIPCSV() = %+v, want %+v", areas, want)
	}
}

func TestParseZIPCSVShuffledColumns(t *testing.T) {
	csvData := "City,POPULATION,zip,State,longitude,Latitude,extra\n" +
		"Austin,47000,78704,TX,-97.766,30.245,ignored\n"

	areas, err := ParseZIPCSV(strings.NewReader(csvData))
	if err != nil {
		t.Fatalf("ParseZIPCSV() error = %v", err)
	}

	want := ZIPArea{ZIP: "78704", City: "Austin", State: "TX", Latitude: 30.245, Longitude: -97.766, Population: 47000}
	if len(areas) != 1 || areas[0] != want {
		t.Fatalf("ParseZIPCSV() = %+v, want [%+v]", areas, want)
	}
}

func TestParseZIPCSVErrors(t *testing.T) {
	header := "zip,city,state,latitude,longitude,population\n"

	cases := []struct {
		name    string
		csvData string
		wantSub string
	}{
		{
			name:    "empty input",
			csvData: "",
			wantSub: "empty",
		},
		{
			name:    "missing column",
			csvData: "zip,city,state,latitude,longitude\n78704,Austin,TX,30.2,-97.7\n",
			wantSub: "population",
		},
		{
			name:    "zip too short",
			csvData: header + "1234,Austin,TX,30.2,-97.7,100\n",
			wantSub: "5 digits",
		},
		{
			name:    "zip not numeric",
			csvData: header + "abcde,Austin,TX,30.2,-97.7,100\n",
			wantSub: "5 digits",
		},
		{
			name:    "latitude out of range",
			csvData: header + "78704,Austin,TX,95.0,-97.7,100\n",
			wantSub: "latitude",
		},
		{
			name:    "latitude not a number",
			csvData: header + "78704,Austin,TX,north,-97.7,100\n",
			wantSub: "latitude",
		},
		{
			name:    "longitude out of range",
			csvData: header + "78704,Austin,TX,30.2,181.0,100\n",
			wantSub: "longitude",
		},
		{
			name:    "negative population",
			csvData: header + "78704,Austin,TX,30.2,-97.7,-5\n",
			wantSub: "population",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseZIPCSV(strings.NewReader(tc.csvData))
			if err == nil {
				t.Fatal("ParseZIPCSV() = nil error, want error")
			}

			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestParseZIPCSVRowBound(t *testing.T) {
	var b strings.Builder

	b.WriteString("zip,city,state,latitude,longitude,population\n")

	for i := 0; i <= 100000; i++ {
		b.WriteString("94102,San Francisco,CA,37.78,-122.42,100\n")
	}

	_, err := ParseZIPCSV(strings.NewReader(b.String()))
	if err == nil || !strings.Contains(err.Error(), "100000") {
		t.Fatalf("ParseZIPCSV() with 100001 rows error = %v, want the row bound error", err)
	}
}

func TestSampleZIPAreas(t *testing.T) {
	areas := SampleZIPAreas()

	if len(areas) != 60 {
		t.Fatalf("len(SampleZIPAreas()) = %d, want 60", len(areas))
	}

	cities := map[string]int{}
	seen := map[string]bool{}

	for _, area := range areas {
		if !isFiveDigitZIP(area.ZIP) {
			t.Errorf("sample zip %q is not 5 digits", area.ZIP)
		}

		if seen[area.ZIP] {
			t.Errorf("duplicate sample zip %q", area.ZIP)
		}

		seen[area.ZIP] = true

		if area.Latitude < -90 || area.Latitude > 90 || area.Longitude < -180 || area.Longitude > 180 {
			t.Errorf("sample zip %q has out-of-range coordinates (%v, %v)", area.ZIP, area.Latitude, area.Longitude)
		}

		if area.Population <= 0 {
			t.Errorf("sample zip %q has non-positive population", area.ZIP)
		}

		if area.City == "" || area.State == "" {
			t.Errorf("sample zip %q is missing city or state", area.ZIP)
		}

		cities[area.City]++
	}

	if len(cities) != 10 {
		t.Fatalf("sample covers %d cities, want 10: %v", len(cities), cities)
	}
}

func TestTopZIPs(t *testing.T) {
	areas := SampleZIPAreas()

	// Case-insensitive state filter, sorted by population desc.
	ca := TopZIPs(areas, "ca", "", 500)
	if len(ca) != 12 {
		t.Fatalf("CA filter returned %d areas, want 12", len(ca))
	}

	for i := 1; i < len(ca); i++ {
		if ca[i].Population > ca[i-1].Population {
			t.Fatalf("CA areas not sorted by population desc at %d: %+v", i, ca)
		}
	}

	// City filter plus cap.
	austin := TopZIPs(areas, "", "AUSTIN", 3)
	if len(austin) != 3 {
		t.Fatalf("Austin top 3 returned %d areas", len(austin))
	}

	if austin[0].ZIP != "78745" || austin[1].ZIP != "78704" || austin[2].ZIP != "78759" {
		t.Fatalf("Austin top 3 = %s, %s, %s; want 78745, 78704, 78759", austin[0].ZIP, austin[1].ZIP, austin[2].ZIP)
	}

	// Combined filters.
	both := TopZIPs(areas, "tx", "houston", 500)
	if len(both) != 6 {
		t.Fatalf("TX+Houston returned %d areas, want 6", len(both))
	}

	// Empty filters return everything (bounded).
	all := TopZIPs(areas, "", "", 500)
	if len(all) != 60 {
		t.Fatalf("unfiltered returned %d areas, want 60", len(all))
	}

	// topN below 1 clamps to 1.
	one := TopZIPs(areas, "", "", 0)
	if len(one) != 1 {
		t.Fatalf("topN=0 returned %d areas, want 1", len(one))
	}

	// topN above 500 clamps to 500.
	big := make([]ZIPArea, 0, 600)
	for i := 0; i < 600; i++ {
		big = append(big, ZIPArea{ZIP: fmt.Sprintf("%05d", i), City: "X", State: "XX", Population: i})
	}

	capped := TopZIPs(big, "", "", 9999)
	if len(capped) != 500 {
		t.Fatalf("topN=9999 returned %d areas, want 500", len(capped))
	}

	// Population ties break by ZIP ascending for determinism.
	tied := []ZIPArea{
		{ZIP: "22222", Population: 10},
		{ZIP: "11111", Population: 10},
	}

	ordered := TopZIPs(tied, "", "", 500)
	if ordered[0].ZIP != "11111" || ordered[1].ZIP != "22222" {
		t.Fatalf("tie-break order = %s, %s; want 11111, 22222", ordered[0].ZIP, ordered[1].ZIP)
	}
}

func TestBuildQueriesWithCity(t *testing.T) {
	zips := []ZIPArea{
		{ZIP: "78704", City: "Austin", State: "tx"},
		{ZIP: "78745", City: "Austin", State: "tx"},
	}

	queries, err := BuildQueries([]string{"Plumber", " plumber ", "", "Drain Cleaning"}, zips, true)
	if err != nil {
		t.Fatalf("BuildQueries() error = %v", err)
	}

	want := []string{
		"Plumber in Austin TX 78704",
		"Plumber in Austin TX 78745",
		"Drain Cleaning in Austin TX 78704",
		"Drain Cleaning in Austin TX 78745",
	}

	if got := QueryLines(queries); !reflect.DeepEqual(got, want) {
		t.Fatalf("QueryLines() = %v, want %v", got, want)
	}

	// Each generated query carries its ZIP area.
	if queries[1].ZIP.ZIP != "78745" {
		t.Fatalf("queries[1].ZIP = %+v, want 78745", queries[1].ZIP)
	}
}

func TestBuildQueriesWithoutCity(t *testing.T) {
	zips := []ZIPArea{{ZIP: "78704", City: "Austin", State: "TX"}}

	queries, err := BuildQueries([]string{"plumber"}, zips, false)
	if err != nil {
		t.Fatalf("BuildQueries() error = %v", err)
	}

	if len(queries) != 1 || queries[0].Query != "plumber 78704" {
		t.Fatalf("queries = %+v, want [plumber 78704]", queries)
	}
}

func TestBuildQueriesRemovesExactDuplicates(t *testing.T) {
	zips := []ZIPArea{
		{ZIP: "78704", City: "Austin", State: "TX"},
		{ZIP: "78704", City: "Austin", State: "TX"}, // duplicate row
	}

	queries, err := BuildQueries([]string{"plumber"}, zips, true)
	if err != nil {
		t.Fatalf("BuildQueries() error = %v", err)
	}

	if len(queries) != 1 {
		t.Fatalf("got %d queries, want duplicate removed: %+v", len(queries), queries)
	}
}

func TestBuildQueriesBound(t *testing.T) {
	synonyms := make([]string, 51)
	for i := range synonyms {
		synonyms[i] = fmt.Sprintf("synonym-%d", i)
	}

	zips := make([]ZIPArea, 100)
	for i := range zips {
		zips[i] = ZIPArea{ZIP: fmt.Sprintf("%05d", i), City: "X", State: "XX"}
	}

	_, err := BuildQueries(synonyms, zips, false)
	if err == nil {
		t.Fatal("BuildQueries() = nil error, want bound error for 5100 queries")
	}

	if !strings.Contains(err.Error(), "5000") || !strings.Contains(err.Error(), "5100") {
		t.Fatalf("bound error %q should mention the limit and the requested total", err)
	}

	// Exactly at the bound is fine.
	queries, err := BuildQueries(synonyms[:50], zips, false)
	if err != nil {
		t.Fatalf("BuildQueries() at the bound error = %v", err)
	}

	if len(queries) != 5000 {
		t.Fatalf("got %d queries, want 5000", len(queries))
	}
}

func TestBuildQueriesEmptySynonyms(t *testing.T) {
	queries, err := BuildQueries([]string{"", "   "}, SampleZIPAreas(), true)
	if err != nil {
		t.Fatalf("BuildQueries() error = %v", err)
	}

	if len(queries) != 0 {
		t.Fatalf("got %d queries from empty synonyms, want 0", len(queries))
	}
}

func TestCentre(t *testing.T) {
	// Population-weighted centroid.
	zips := []ZIPArea{
		{ZIP: "11111", Latitude: 10, Longitude: 20, Population: 100},
		{ZIP: "22222", Latitude: 30, Longitude: 40, Population: 300},
	}

	lat, lon := Centre(zips)
	if math.Abs(lat-25) > 1e-9 || math.Abs(lon-35) > 1e-9 {
		t.Fatalf("Centre() = (%v, %v), want (25, 35)", lat, lon)
	}

	// Fallback to plain average when populations are unknown.
	zips[0].Population = 0
	zips[1].Population = 0

	lat, lon = Centre(zips)
	if math.Abs(lat-20) > 1e-9 || math.Abs(lon-30) > 1e-9 {
		t.Fatalf("Centre() fallback = (%v, %v), want (20, 30)", lat, lon)
	}

	// Empty input.
	lat, lon = Centre(nil)
	if lat != 0 || lon != 0 {
		t.Fatalf("Centre(nil) = (%v, %v), want (0, 0)", lat, lon)
	}
}
