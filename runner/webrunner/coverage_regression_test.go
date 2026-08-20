package webrunner

import (
	"encoding/json"
	"testing"

	"github.com/gosom/google-maps-scraper/gmaps"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/prospect"
)

// denseMetroZIPs mirrors the real San Francisco geometry that exposed the
// overlap defect: every neighbour sits about 2 km from the parent, well
// inside a 10 km search radius.
func denseMetroZIPs() []prospect.ZIPArea {
	return []prospect.ZIPArea{
		{ZIP: "94110", City: "San Francisco", State: "CA", Latitude: 37.7485, Longitude: -122.4156},
		{ZIP: "94114", City: "San Francisco", State: "CA", Latitude: 37.7583, Longitude: -122.4350},
		{ZIP: "94107", City: "San Francisco", State: "CA", Latitude: 37.7621, Longitude: -122.3971},
		{ZIP: "94131", City: "San Francisco", State: "CA", Latitude: 37.7449, Longitude: -122.4380},
		{ZIP: "95003", City: "Aptos", State: "CA", Latitude: 36.9772, Longitude: -121.8994},
	}
}

// TestExpansionSkipsNeighboursInsideTheSearchRadius pins the measured dense
// metro defect: with a 10 km radius, ZIP centroids about 2 km apart re-search
// ground the parent already swept, so they must not be expanded into.
func TestExpansionSkipsNeighboursInsideTheSearchRadius(t *testing.T) {
	t.Parallel()

	engine := newCoverageEngine("job-radius", web.CoverageOptions{
		MaxExpansions:   3,
		ExpansionMinNew: 1,
	}, web.CoverageSeedState{
		Queries:     []string{"coffee shop in San Francisco CA 94110"},
		MaxSequence: 0,
	}).withSearchRadius(10000)
	engine.zipAreas = func() []prospect.ZIPArea { return denseMetroZIPs() }

	decision := engine.recordCompletion(
		web.JobTask{Key: "t-parent", Query: "coffee shop in San Francisco CA 94110"},
		web.JobTaskCheckpoint{RowsAdded: 18, DuplicatesSkipped: 2},
	)

	for _, expansion := range decision.expansions {
		if zip, ok := web.ParseGBPQueryZIP(expansion.Query); ok {
			if zip == "94114" || zip == "94107" || zip == "94131" {
				t.Fatalf("expanded into %s, which lies inside the 10 km search radius", zip)
			}
		}
	}

	// Aptos is far enough away to be genuinely new ground, so the budget is
	// still spendable where it can pay.
	if len(decision.expansions) != 1 {
		t.Fatalf("expansions = %d, want only the ZIP outside the radius", len(decision.expansions))
	}

	if zip, _ := web.ParseGBPQueryZIP(decision.expansions[0].Query); zip != "95003" {
		t.Fatalf("expanded into %q, want the distant ZIP 95003", zip)
	}
}

// TestSparseNeighboursStayEligible guards the other direction: a rural plan
// whose neighbours sit well beyond the radius must still expand, because that
// is genuinely unexplored ground.
func TestSparseNeighboursStayEligible(t *testing.T) {
	t.Parallel()

	areas := []prospect.ZIPArea{
		{ZIP: "82225", City: "Lusk", State: "WY", Latitude: 42.7625, Longitude: -104.4524},
		{ZIP: "82227", City: "Manville", State: "WY", Latitude: 42.7772, Longitude: -104.6135},
		{ZIP: "82240", City: "Torrington", State: "WY", Latitude: 42.0655, Longitude: -104.1836},
	}

	engine := newCoverageEngine("job-sparse", web.CoverageOptions{
		MaxExpansions:   2,
		ExpansionMinNew: 1,
	}, web.CoverageSeedState{
		Queries:     []string{"auto repair in Lusk WY 82225"},
		MaxSequence: 0,
	}).withSearchRadius(10000)
	engine.zipAreas = func() []prospect.ZIPArea { return areas }

	decision := engine.recordCompletion(
		web.JobTask{Key: "t-parent", Query: "auto repair in Lusk WY 82225"},
		web.JobTaskCheckpoint{RowsAdded: 8},
	)

	if len(decision.expansions) == 0 {
		t.Fatal("a rural neighbourhood beyond the search radius must still be expandable")
	}
}

// TestEngineExpansionsAreNotEvidenceAboutThePlan pins the measured saturation
// defect: three empty engine probes must not fill the window and skip the
// operator's remaining queries.
func TestEngineExpansionsAreNotEvidenceAboutThePlan(t *testing.T) {
	t.Parallel()

	engine := newCoverageEngine("job-evidence", web.CoverageOptions{
		AutoStop:         true,
		SaturationWindow: 3,
		MinNewRatio:      0.1,
	}, web.CoverageSeedState{MaxSequence: 0})

	for i := 0; i < 5; i++ {
		decision := engine.recordCompletion(
			web.JobTask{
				Key:    "expansion",
				Query:  "nail salon in San Francisco CA 94114",
				Origin: web.CoverageExpansionOriginPrefix + "94110",
			},
			web.JobTaskCheckpoint{},
		)

		if decision.saturatedNow {
			t.Fatal("empty engine expansions saturated the plan; the operator's queries would be skipped")
		}

		if decision.windowSize != 0 {
			t.Fatalf("expansion entered the evidence window (size %d)", decision.windowSize)
		}
	}

	// The operator's own queries remain the evidence that can stop the plan.
	var saturated bool

	for i := 0; i < 3; i++ {
		decision := engine.recordCompletion(
			web.JobTask{Key: "plan", Query: "nail salon in San Francisco CA 94103"},
			web.JobTaskCheckpoint{},
		)
		saturated = saturated || decision.saturatedNow
	}

	if !saturated {
		t.Fatal("a full window of empty plan queries must still saturate")
	}
}

// TestExpansionSeedMatchesTheJobSearchMode pins the defect that made every
// expansion worthless: a fast-mode job searches through structured
// lat/lon/radius parameters, so building a browser viewport seed for it
// returned nothing. Measured before the fix: the identical query yielded 17
// rows as a plan query and 0 as an expansion.
func TestExpansionSeedMatchesTheJobSearchMode(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(expansionTaskPayload{
		SeedID:      "exp-seed",
		Query:       "coffee shop in South San Francisco CA 94080",
		Coordinates: "37.657400,-122.423500",
		ZIP:         "94080",
		ParentZIP:   "94110",
		Expansion:   true,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	task := web.JobTask{
		Key:     "exp-seed",
		Query:   "coffee shop in South San Francisco CA 94080",
		Origin:  web.CoverageExpansionOriginPrefix + "94110",
		Payload: payload,
	}

	for _, testCase := range []struct {
		name     string
		fastMode bool
	}{
		{"fast mode searches through parameters", true},
		{"browser mode drives a viewport", false},
	} {
		job := &web.Job{ID: "job-seed", Data: web.JobData{
			Lang: "en", Depth: 3, Zoom: 15, Radius: 10000, FastMode: testCase.fastMode,
		}}

		seed, buildErr := buildExpansionSeed(job, task, nil, nil, false)
		if buildErr != nil {
			t.Fatalf("%s: buildExpansionSeed() error = %v", testCase.name, buildErr)
		}

		if seed == nil {
			t.Fatalf("%s: no seed built", testCase.name)
		}

		switch typed := seed.(type) {
		case *gmaps.SearchJob:
			if !testCase.fastMode {
				t.Fatalf("%s: browser job got a fast-mode search seed", testCase.name)
			}

			if typed.ID != "exp-seed" {
				t.Fatalf("%s: seed id = %q, want the durable task key", testCase.name, typed.ID)
			}
		case *gmaps.GmapJob:
			if testCase.fastMode {
				t.Fatalf("%s: fast-mode job got a browser viewport seed", testCase.name)
			}

			if typed.ID != "exp-seed" {
				t.Fatalf("%s: seed id = %q, want the durable task key", testCase.name, typed.ID)
			}
		default:
			t.Fatalf("%s: unexpected seed type %T", testCase.name, seed)
		}
	}
}
