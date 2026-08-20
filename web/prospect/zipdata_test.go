package prospect

import (
	"math"
	"testing"
)

// minimumEmbeddedZIPAreas is the row count the embedded dataset must
// never fall under (it currently ships ~41k ZIPs).
const minimumEmbeddedZIPAreas = 40000

func TestEmbeddedZIPDataset(t *testing.T) {
	areas, err := EmbeddedZIPDataset()
	if err != nil {
		t.Fatalf("EmbeddedZIPDataset() error = %v", err)
	}

	if len(areas) < minimumEmbeddedZIPAreas {
		t.Fatalf("EmbeddedZIPDataset() has %d areas, want at least %d", len(areas), minimumEmbeddedZIPAreas)
	}

	if convenience := EmbeddedZIPAreas(); len(convenience) != len(areas) {
		t.Fatalf("EmbeddedZIPAreas() has %d areas, want %d", len(convenience), len(areas))
	}

	seen := make(map[string]struct{}, len(areas))

	for _, area := range areas {
		if _, ok := seen[area.ZIP]; ok {
			t.Fatalf("duplicate ZIP %q in embedded dataset", area.ZIP)
		}

		seen[area.ZIP] = struct{}{}
	}
}

func TestEmbeddedZIPDatasetSpotCheck94110(t *testing.T) {
	var found *ZIPArea

	for _, area := range EmbeddedZIPAreas() {
		if area.ZIP == "94110" {
			found = &area
			break
		}
	}

	if found == nil {
		t.Fatal("ZIP 94110 missing from embedded dataset")
	}

	if found.City != "San Francisco" || found.State != "CA" {
		t.Fatalf("ZIP 94110 = %s, %s, want San Francisco, CA", found.City, found.State)
	}

	if found.Population <= 0 {
		t.Fatalf("ZIP 94110 population = %d, want > 0", found.Population)
	}

	if math.Abs(found.Latitude-37.75) > 0.1 || math.Abs(found.Longitude+122.42) > 0.1 {
		t.Fatalf("ZIP 94110 coordinates = (%v, %v), want near (37.75, -122.42)", found.Latitude, found.Longitude)
	}
}

func TestEmbeddedZIPStates(t *testing.T) {
	states := EmbeddedZIPStates()

	const minimumStates = 50
	if len(states) < minimumStates {
		t.Fatalf("EmbeddedZIPStates() has %d entries, want at least %d", len(states), minimumStates)
	}

	index := make(map[string]struct{}, len(states))
	for _, state := range states {
		index[state] = struct{}{}
	}

	for _, want := range []string{"CA", "NY", "TX"} {
		if _, ok := index[want]; !ok {
			t.Fatalf("EmbeddedZIPStates() = %v, missing %q", states, want)
		}
	}

	for i := 1; i < len(states); i++ {
		if states[i-1] >= states[i] {
			t.Fatalf("EmbeddedZIPStates() not sorted unique at %d: %q >= %q", i, states[i-1], states[i])
		}
	}
}

func TestTopZIPsOverEmbeddedDataset(t *testing.T) {
	const topN = 10

	top := TopZIPs(EmbeddedZIPAreas(), "WY", "", topN)
	if len(top) != topN {
		t.Fatalf("TopZIPs(embedded, WY) returned %d areas, want %d", len(top), topN)
	}

	for i, area := range top {
		if area.State != "WY" {
			t.Fatalf("TopZIPs(embedded, WY)[%d].State = %q, want WY", i, area.State)
		}

		if i > 0 && top[i-1].Population < area.Population {
			t.Fatalf("TopZIPs(embedded, WY) not sorted by population at %d: %d < %d", i, top[i-1].Population, area.Population)
		}
	}

	if top[0].Population <= 0 {
		t.Fatalf("TopZIPs(embedded, WY)[0].Population = %d, want > 0", top[0].Population)
	}
}
