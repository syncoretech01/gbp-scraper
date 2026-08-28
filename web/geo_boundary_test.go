package web

import (
	"context"
	"math"
	"strings"
	"testing"
)

// losAngelesJobData reproduces the configuration of the acceptance run that
// exposed the spillover: a 15 km radius around downtown Los Angeles, gridded
// from the bounding box that encloses that circle in 5 km cells.
func losAngelesJobData() JobData {
	return JobData{
		Lat:        "34.0522",
		Lon:        "-118.2437",
		Radius:     15000,
		GridBBox:   "33.917302,-118.406517,34.187098,-118.080883",
		GridCellKM: 5,
	}
}

func TestHaversineMetersMatchesKnownDistance(t *testing.T) {
	// Downtown Los Angeles to Anaheim, the farthest business the acceptance
	// run kept. The reference great-circle distance is 36.2 km.
	got := haversineMeters(34.0522, -118.2437, 33.84450, -117.94134)
	if math.Abs(got-36211) > 60 {
		t.Fatalf("haversineMeters = %.0f m, want about 36211 m", got)
	}

	if zero := haversineMeters(10, 20, 10, 20); zero != 0 {
		t.Fatalf("identical points measured %v m apart", zero)
	}
}

func TestJobSearchAreaClassifiesTheThreeZones(t *testing.T) {
	area := NewJobSearchArea(losAngelesJobData())
	if !area.Available() {
		t.Fatal("area built from a centre, a radius and a grid reports unavailable")
	}

	cases := []struct {
		name      string
		latitude  float64
		longitude float64
		want      GeoZone
	}{
		// Downtown, comfortably inside the circle.
		{"inside radius", 34.0500, -118.2500, GeoZoneInsideRadius},
		// A bounding-box corner: inside the square the grid was cut from,
		// beyond the circle the operator asked for.
		{"planned grid corner", 34.1860, -118.0820, GeoZoneInsidePlanned},
		// Anaheim: no planned query pointed anywhere near it.
		{"maps spillover", 33.84450, -117.94134, GeoZoneOutsidePlanned},
		// Null Island is how a listing with no position is stored.
		{"missing position", 0, 0, GeoZoneUnknown},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, zone := area.Classify(testCase.latitude, testCase.longitude)
			if zone != testCase.want {
				t.Fatalf("zone = %q, want %q", zone, testCase.want)
			}
		})
	}
}

func TestJobSearchAreaPlannedReachExceedsRadius(t *testing.T) {
	area := NewJobSearchArea(losAngelesJobData())
	reach := area.PlannedReachMeters()

	// The planner grids the square that encloses the circle, so its far corner
	// is the radius times root two. Reporting the radius as the plan's reach
	// would misattribute legitimate corner-cell results to spillover.
	if reach <= area.RadiusMeters {
		t.Fatalf("planned reach %.0f m does not exceed the %.0f m radius", reach, area.RadiusMeters)
	}
	if math.Abs(reach-21213) > 400 {
		t.Fatalf("planned reach = %.0f m, want about 21213 m", reach)
	}
}

func TestJobSearchAreaWithoutCentreClassifiesNothing(t *testing.T) {
	area := NewJobSearchArea(JobData{Radius: 15000})
	if area.Available() {
		t.Fatal("an area with no centre reports itself available")
	}

	distance, zone := area.Classify(34.05, -118.25)
	if zone != GeoZoneUnknown || distance != 0 {
		t.Fatalf("classify without a centre = (%v, %q), want (0, unknown)", distance, zone)
	}
}

func TestRadiusFilterValueMatchesStoredResultsFilterGrammar(t *testing.T) {
	area := NewJobSearchArea(losAngelesJobData())
	got := area.RadiusFilterValue()
	if got != "34.0522,-118.2437,15" {
		t.Fatalf("RadiusFilterValue = %q, want %q", got, "34.0522,-118.2437,15")
	}

	// The stored-results "distance within" filter parses exactly three
	// numbers: latitude, longitude, radius in kilometres. A value that does
	// not is a link that lands on an error.
	if parts := strings.Split(got, ","); len(parts) != 3 {
		t.Fatalf("filter value %q does not carry three fields", got)
	}

	if none := NewJobSearchArea(JobData{Lat: "34", Lon: "-118"}).RadiusFilterValue(); none != "" {
		t.Fatalf("a job with no radius produced filter value %q", none)
	}
}

// TestResultGeographyReproducesAcceptanceRunDistribution is the regression for
// issue O. The fixture is the shape of job 7100e95b: businesses inside the
// radius, businesses in the grid's corner band, and Maps spillover well beyond
// anything the plan pointed at. Every one of them must survive.
func TestResultGeographyReproducesAcceptanceRunDistribution(t *testing.T) {
	csv := strings.Join([]string{
		"title,latitude,longitude,place_id,website,phone,emails",
		"Downtown Ink,34.0500,-118.2500,p1,,,",
		"Echo Park Tattoo,34.0780,-118.2600,p2,,,",
		"Corner Cell Studio,34.1860,-118.0820,p3,,,",
		"Anaheim Ink,33.84450,-117.94134,p4,,,",
		"Fullerton Ink House,33.85963,-117.94352,p5,,,",
		"No Position Tattoo,,,p6,,,",
		"Null Island Tattoo,0,0,p7,,,",
	}, "\n") + "\n"

	stats, err := summarizeResultsWithin(
		context.Background(),
		strings.NewReader(csv),
		NewJobSearchArea(losAngelesJobData()),
	)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}

	if stats.Rows != 7 {
		t.Fatalf("rows = %d, want 7; geographic reporting must never drop a row", stats.Rows)
	}
	if stats.UniqueBusinesses != 7 {
		t.Fatalf("unique businesses = %d, want 7", stats.UniqueBusinesses)
	}

	geography := stats.Geography
	if !geography.Available {
		t.Fatal("geography unavailable for a job with a centre, a radius and a grid")
	}
	if geography.Measured != 5 || geography.WithoutCoordinates != 2 {
		t.Fatalf("measured/without = %d/%d, want 5/2", geography.Measured, geography.WithoutCoordinates)
	}
	if geography.InsideRadius != 2 {
		t.Fatalf("inside radius = %d, want 2", geography.InsideRadius)
	}
	if geography.InsidePlanned != 1 {
		t.Fatalf("inside planned grid = %d, want 1", geography.InsidePlanned)
	}
	if geography.OutsidePlanned != 2 {
		t.Fatalf("outside planned = %d, want 2", geography.OutsidePlanned)
	}
	if geography.OutsideRadius() != 3 {
		t.Fatalf("outside radius = %d, want 3", geography.OutsideRadius())
	}
	if geography.FarthestName != "Anaheim Ink" {
		t.Fatalf("farthest = %q, want %q", geography.FarthestName, "Anaheim Ink")
	}
	if math.Abs(geography.MaxMeters-36211) > 200 {
		t.Fatalf("max distance = %.0f m, want about 36211 m", geography.MaxMeters)
	}
	if geography.RadiusFilterValue != "34.0522,-118.2437,15" {
		t.Fatalf("radius filter value = %q", geography.RadiusFilterValue)
	}
}

func TestResultGeographyUnavailableWithoutJobArea(t *testing.T) {
	csv := "title,latitude,longitude,place_id\nSomewhere,34.05,-118.25,p1\n"

	stats, err := summarizeResults(context.Background(), strings.NewReader(csv))
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}

	if stats.Geography.Available {
		t.Fatal("geography reported as available for a summary with no job area")
	}
	if stats.Rows != 1 {
		t.Fatalf("rows = %d, want 1", stats.Rows)
	}
}

func TestGeographyMetricsNameSpilloverAndOfferANonDestructiveFilter(t *testing.T) {
	input := jobPipelineInput{
		Job: Job{ID: "job-1", Data: losAngelesJobData()},
		Stats: ResultStats{
			UniqueBusinesses: 331,
			Geography: ResultGeography{
				Available: true, Measured: 331,
				InsideRadius: 189, InsidePlanned: 27, OutsidePlanned: 115,
				MedianMeters: 12232, MaxMeters: 36211, FarthestName: "Anaheim Ink",
				RadiusMeters: 15000, PlannedReachMeters: 21213,
				RadiusFilterValue: "34.0522,-118.2437,15",
			},
		},
	}

	metrics := generatingGridMetrics(input)
	byLabel := map[string]jobPipelineMetric{}
	geographic := 0
	action := jobPipelineMetric{}
	for _, metric := range metrics {
		byLabel[metric.Label] = metric
		if metric.Group == pipelineGroupGeography {
			geographic++
		}
		if metric.Group == pipelineGroupGeographyFilter {
			action = metric
		}
	}

	if geographic < 5 {
		t.Fatalf("only %d geography metrics were produced", geographic)
	}
	if got := byLabel["Inside the 15.0 km radius"].Value; got != "189 of 331" {
		t.Fatalf("inside-radius metric = %q", got)
	}
	if got := byLabel["Outside the area this run searched"].Value; !strings.HasPrefix(got, "115 (34.7%") {
		t.Fatalf("spillover metric = %q, want it to lead with 115 (34.7%%)", got)
	}
	// The spillover line must say the businesses are kept. Silently implying
	// they were discarded is the failure this issue is about.
	if note := byLabel["Outside the area this run searched"].Note; !strings.Contains(note, "kept") {
		t.Fatalf("spillover note %q does not say the businesses are kept", note)
	}

	if action.Value == "" {
		t.Fatal("no geographic filter action was offered")
	}
	for _, want := range []string{
		"/app/results?", "job_id=job-1",
		"filter_field=distance", "filter_operator=within",
		"filter_value=34.0522%2C-118.2437%2C15",
	} {
		if !strings.Contains(action.Value, want) {
			t.Fatalf("filter URL %q is missing %q", action.Value, want)
		}
	}
	if !strings.Contains(action.Note, "comes back when the filter is cleared") {
		t.Fatalf("filter note %q does not promise the data survives", action.Note)
	}
}

func TestGeographyMetricsAbsentWithoutMeasurableGeography(t *testing.T) {
	input := jobPipelineInput{Job: Job{ID: "job-1"}}
	for _, metric := range generatingGridMetrics(input) {
		if metric.Group == pipelineGroupGeography || metric.Group == pipelineGroupGeographyFilter {
			t.Fatalf("geography metric %q rendered for a job with no coordinates", metric.Label)
		}
	}
}

// A job gridded from a saved area carries no radius. It still has geography
// worth reporting, but it has no circle, and a tile promising one would invent
// a boundary the operator never set.
func TestGeographyMetricsOmitTheRadiusTileWithoutARadius(t *testing.T) {
	input := jobPipelineInput{
		Job: Job{ID: "job-area", Data: JobData{
			Lat: "34.0522", Lon: "-118.2437",
			GridBBox: "33.917302,-118.406517,34.187098,-118.080883", GridCellKM: 5,
		}},
		Stats: ResultStats{
			UniqueBusinesses: 40,
			Geography: ResultGeography{
				Available: true, Measured: 40,
				InsidePlanned: 30, OutsidePlanned: 10,
				MedianMeters: 8000, MaxMeters: 24000, PlannedReachMeters: 21213,
			},
		},
	}

	for _, metric := range generatingGridMetrics(input) {
		if strings.Contains(metric.Label, "radius") {
			t.Fatalf("metric %q claims a radius this job never configured", metric.Label)
		}
		if metric.Group == pipelineGroupGeographyFilter {
			t.Fatalf("a radius filter was offered for a job with no radius: %q", metric.Value)
		}
	}
}
