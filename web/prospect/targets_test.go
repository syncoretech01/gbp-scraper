package prospect_test

import (
	"testing"

	"github.com/gosom/google-maps-scraper/web/prospect"
)

// caZips is a small, ordered stand-in for what TopZIPs returns: most populous
// first, each with its own centroid.
func caZips() []prospect.ZIPArea {
	return []prospect.ZIPArea{
		{ZIP: "90011", City: "Los Angeles", State: "CA", Latitude: 34.0074, Longitude: -118.2587, Population: 103892},
		{ZIP: "90650", City: "Norwalk", State: "CA", Latitude: 33.9067, Longitude: -118.0823, Population: 100425},
		{ZIP: "91331", City: "Pacoima", State: "CA", Latitude: 34.2556, Longitude: -118.4206, Population: 96455},
	}
}

// TestBuildTargetsKeepsGeographyPerZIP is the issue-I regression.
//
// Reproduced against the shipped build: CA + top 25 ZIPs + 3 synonyms produced
// 75 correct query strings but one global lat/lon, so step 2 reported "1 area,
// 75 searches" and all 75 searches ran from the same point. Every generated
// unit of work must now carry the ZIP it covers.
func TestBuildTargetsKeepsGeographyPerZIP(t *testing.T) {
	t.Parallel()

	synonyms := []string{"plumber", "plumbing company", "emergency plumber"}

	targets, err := prospect.BuildTargets(synonyms, caZips(), true)
	if err != nil {
		t.Fatalf("build targets: %v", err)
	}

	if len(targets) != 9 {
		t.Fatalf("3 synonyms x 3 ZIPs = %d targets, want 9", len(targets))
	}

	if areas := prospect.DistinctTargetZIPs(targets); areas != 3 {
		t.Fatalf("distinct geographic targets = %d, want 3", areas)
	}

	for _, target := range targets {
		if target.ZIP == "" || target.Latitude == 0 || target.Longitude == 0 {
			t.Fatalf("target %+v carries no geography", target)
		}

		if target.City != "Los Angeles" && target.City != "Norwalk" && target.City != "Pacoima" {
			t.Fatalf("target %q lost its city", target.ZIP)
		}

		if target.State != "CA" {
			t.Fatalf("target %q lost its state", target.ZIP)
		}

		if target.Origin != prospect.OriginSelected {
			t.Fatalf("target %q origin = %q", target.ZIP, target.Origin)
		}

		if target.Rank < 1 || target.Rank > 3 {
			t.Fatalf("target %q rank = %d, want the 1-based selection rank", target.ZIP, target.Rank)
		}

		if target.Population == 0 {
			t.Fatalf("target %q lost its population", target.ZIP)
		}

		if target.Synonym == "" {
			t.Fatalf("target %q lost the synonym it came from", target.ZIP)
		}
	}

	// The most populous ZIP is rank 1 and the least is rank 3.
	for _, target := range targets {
		switch target.ZIP {
		case "90011":
			if target.Rank != 1 {
				t.Fatalf("90011 rank = %d, want 1", target.Rank)
			}
		case "91331":
			if target.Rank != 3 {
				t.Fatalf("91331 rank = %d, want 3", target.Rank)
			}
		}
	}
}

// TestBuildTargetsMatchesBuildQueries proves the target generator did not
// change a single query string, so the keyword box and every existing caller
// see exactly what they saw before.
func TestBuildTargetsMatchesBuildQueries(t *testing.T) {
	t.Parallel()

	synonyms := []string{"plumber", "plumbing company", "emergency plumber"}

	queries, err := prospect.BuildQueries(synonyms, caZips(), true)
	if err != nil {
		t.Fatalf("build queries: %v", err)
	}

	targets, err := prospect.BuildTargets(synonyms, caZips(), true)
	if err != nil {
		t.Fatalf("build targets: %v", err)
	}

	want := prospect.QueryLines(queries)
	got := prospect.TargetLines(targets)

	if len(want) != len(got) {
		t.Fatalf("target lines = %d, query lines = %d", len(got), len(want))
	}

	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("line %d: target %q, query %q", i, got[i], want[i])
		}
	}
}

// TestTargetIDIsStableAcrossRuns pins checkpoint/rerun identity: the same ZIP
// and synonym must resolve to the same target id every time, and a different
// ZIP or synonym must not collide with it.
func TestTargetIDIsStableAcrossRuns(t *testing.T) {
	t.Parallel()

	first, err := prospect.BuildTargets([]string{"plumber"}, caZips(), true)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	second, err := prospect.BuildTargets([]string{"plumber"}, caZips(), true)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	seen := map[string]string{}

	for i := range first {
		if first[i].ID != second[i].ID {
			t.Fatalf("target %q id changed between runs: %q then %q", first[i].ZIP, first[i].ID, second[i].ID)
		}

		if first[i].ID == "" {
			t.Fatalf("target %q has no id", first[i].ZIP)
		}

		if previous, clash := seen[first[i].ID]; clash {
			t.Fatalf("id %q is shared by %q and %q", first[i].ID, previous, first[i].ZIP)
		}

		seen[first[i].ID] = first[i].ZIP
	}

	// A different synonym over the same ZIPs is a different set of targets.
	other, err := prospect.BuildTargets([]string{"emergency plumber"}, caZips(), true)
	if err != nil {
		t.Fatalf("build other: %v", err)
	}

	for _, target := range other {
		if _, clash := seen[target.ID]; clash {
			t.Fatalf("a different synonym reused target id %q", target.ID)
		}
	}
}

// TestNeighbourTargetsAreRealGeography pins the adaptive expansion half of
// issue I: expanding into a neighbouring ZIP must create a target with that
// ZIP's own centroid and its own identity, not another query string aimed at
// the parent's centre.
func TestNeighbourTargetsAreRealGeography(t *testing.T) {
	t.Parallel()

	targets, err := prospect.BuildTargets([]string{"plumber"}, caZips()[:1], true)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	parent := targets[0]

	universe := append(caZips(), prospect.ZIPArea{
		ZIP: "90001", City: "Los Angeles", State: "CA", Latitude: 33.9731, Longitude: -118.2479, Population: 57110,
	})

	neighbours := prospect.NeighbourTargets(parent, universe, []string{"90011"}, 20000, 5)
	if len(neighbours) == 0 {
		t.Fatal("no neighbours produced for a 20 km expansion")
	}

	for _, neighbour := range neighbours {
		if neighbour.ZIP == parent.ZIP {
			t.Fatal("expansion revisited the parent ZIP")
		}

		if neighbour.Latitude == parent.Latitude && neighbour.Longitude == parent.Longitude {
			t.Fatalf("neighbour %q reuses the parent centroid", neighbour.ZIP)
		}

		if neighbour.Origin != prospect.OriginNeighbour {
			t.Fatalf("neighbour %q origin = %q", neighbour.ZIP, neighbour.Origin)
		}

		if neighbour.ParentID != parent.ID {
			t.Fatalf("neighbour %q parent = %q, want %q", neighbour.ZIP, neighbour.ParentID, parent.ID)
		}

		if neighbour.ID == parent.ID || neighbour.ID == "" {
			t.Fatalf("neighbour %q id = %q", neighbour.ZIP, neighbour.ID)
		}

		if neighbour.Synonym != parent.Synonym {
			t.Fatalf("neighbour %q lost the synonym", neighbour.ZIP)
		}
	}

	// Nearest first, and deterministic.
	repeat := prospect.NeighbourTargets(parent, universe, []string{"90011"}, 20000, 5)
	for i := range neighbours {
		if neighbours[i].ID != repeat[i].ID {
			t.Fatalf("expansion is not deterministic at %d", i)
		}
	}

	if neighbours[0].ZIP != "90001" {
		t.Fatalf("nearest neighbour = %q, want 90001", neighbours[0].ZIP)
	}
}

// TestNeighbourTargetsRespectBounds keeps one expansion from growing without
// limit and from wandering outside the radius it was given.
func TestNeighbourTargetsRespectBounds(t *testing.T) {
	t.Parallel()

	targets, err := prospect.BuildTargets([]string{"plumber"}, caZips()[:1], true)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	parent := targets[0]

	if got := prospect.NeighbourTargets(parent, caZips(), nil, 20000, 1); len(got) != 1 {
		t.Fatalf("limit 1 returned %d targets", len(got))
	}

	if got := prospect.NeighbourTargets(parent, caZips(), nil, 500, 5); len(got) != 0 {
		t.Fatalf("a 500 m radius reached %d ZIPs", len(got))
	}

	if got := prospect.NeighbourTargets(parent, caZips(), nil, 0, 5); got != nil {
		t.Fatal("a zero radius must expand into nothing")
	}

	if got := prospect.NeighbourTargets(parent, caZips(), nil, 20000, 0); got != nil {
		t.Fatal("a zero limit must expand into nothing")
	}
}

// TestTargetCentreIsPopulationWeighted keeps the job's opening map centre the
// same population-weighted centroid the query generator already produced.
func TestTargetCentreIsPopulationWeighted(t *testing.T) {
	t.Parallel()

	zips := caZips()

	targets, err := prospect.BuildTargets([]string{"plumber", "plumbing company"}, zips, true)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	wantLat, wantLon := prospect.Centre(zips)
	gotLat, gotLon := prospect.TargetCentre(targets)

	if wantLat != gotLat || wantLon != gotLon {
		t.Fatalf("target centre = %v,%v want %v,%v", gotLat, gotLon, wantLat, wantLon)
	}
}
