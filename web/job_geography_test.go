package web

import (
	"math"
	"strings"
	"testing"
)

// TestSummarizeCellSpilloverAttributesTheAcceptanceRunToGoogle is the
// measurement that answers issue O's question. The fixture is the shape of job
// 7100e95b: 5 km cells, and businesses returned by them at distances a 5 km
// cell could never legitimately cover.
//
// A mis-cut grid would show results clustered inside their own cells and the
// grid itself reaching too far. This shows the opposite: the cells are 3.5 km
// across at their furthest corner and Maps answered them with businesses 20 km
// away, which is the platform widening the query and nothing else.
func TestSummarizeCellSpilloverAttributesTheAcceptanceRunToGoogle(t *testing.T) {
	observations := []JobCellObservation{
		// Inside the cell that asked.
		{Cell: "34.074506,-118.216777", Latitude: 34.0745, Longitude: -118.2168},
		{Cell: "34.074506,-118.216777", Latitude: 34.0800, Longitude: -118.2200},
		// Answered from far outside it.
		{Cell: "33.939760,-118.108355", Latitude: 33.8445, Longitude: -117.9413},
		{Cell: "34.164338,-118.270989", Latitude: 34.2643, Longitude: -118.4685},
		// Unusable evidence is skipped rather than counted as a zero distance.
		{Cell: "not-a-cell", Latitude: 34.05, Longitude: -118.25},
		{Cell: "34.074506,-118.216777", Latitude: 0, Longitude: 0},
	}

	spillover := summarizeCellSpillover(observations, 5)

	if !spillover.Available {
		t.Fatal("spillover unavailable for four measurable observations")
	}
	if spillover.Measured != 4 {
		t.Fatalf("measured = %d, want 4 (unusable rows must be skipped, not counted)", spillover.Measured)
	}
	if spillover.Cells != 3 {
		t.Fatalf("cells = %d, want 3", spillover.Cells)
	}
	if math.Abs(spillover.CellReachMeters-3535.5) > 1 {
		t.Fatalf("cell reach = %.1f m, want the 3535.5 m half-diagonal of a 5 km cell",
			spillover.CellReachMeters)
	}
	if spillover.WithinOwnCell != 2 || spillover.Beyond != 2 {
		t.Fatalf("within/beyond = %d/%d, want 2/2", spillover.WithinOwnCell, spillover.Beyond)
	}
	if got := spillover.BeyondPercent(); got != 50 {
		t.Fatalf("beyond share = %.1f%%, want 50.0%%", got)
	}
	if spillover.MaxMeters < 15000 {
		t.Fatalf("max distance from the searching cell = %.0f m, want well past the cell", spillover.MaxMeters)
	}
}

func TestSummarizeCellSpilloverWithoutACellSizeMakesNoClaim(t *testing.T) {
	observations := []JobCellObservation{
		{Cell: "34.074506,-118.216777", Latitude: 34.0745, Longitude: -118.2168},
		{Cell: "34.074506,-118.216777", Latitude: 33.8445, Longitude: -117.9413},
	}

	spillover := summarizeCellSpillover(observations, 0)

	if !spillover.Available || spillover.Measured != 2 {
		t.Fatalf("measured = %d (available %v), want 2 measurable observations",
			spillover.Measured, spillover.Available)
	}
	if spillover.WithinOwnCell != 0 || spillover.Beyond != 0 {
		t.Fatalf("within/beyond = %d/%d, want 0/0 with no cell size to measure against",
			spillover.WithinOwnCell, spillover.Beyond)
	}
}

func TestSummarizeCellSpilloverEmptyEvidenceIsUnavailable(t *testing.T) {
	if spillover := summarizeCellSpillover(nil, 5); spillover.Available {
		t.Fatal("spillover claimed to be available with no observations")
	}
}

func TestParseCellCentreRejectsMalformedCells(t *testing.T) {
	for _, cell := range []string{"", "34.05", "abc,def", "91,0", "0,181", "34.05,"} {
		if _, _, ok := parseCellCentre(cell); ok {
			t.Errorf("parseCellCentre(%q) accepted a malformed cell", cell)
		}
	}

	latitude, longitude, ok := parseCellCentre(" 34.074506 , -118.216777 ")
	if !ok || latitude != 34.074506 || longitude != -118.216777 {
		t.Fatalf("parseCellCentre = (%v, %v, %v)", latitude, longitude, ok)
	}
}

// The spillover line is what turns "115 businesses outside the area" from an
// accusation against the planner into an attributed measurement, so it must
// render whenever the evidence exists.
func TestGeographyMetricsReportCellSpillover(t *testing.T) {
	input := jobPipelineInput{
		Job: Job{ID: "job-1", Data: losAngelesJobData()},
		Stats: ResultStats{
			UniqueBusinesses: 331,
			Geography: ResultGeography{
				Available: true, Measured: 331,
				InsideRadius: 189, InsidePlanned: 27, OutsidePlanned: 115,
				MedianMeters: 12232, MaxMeters: 36211,
				RadiusMeters: 15000, PlannedReachMeters: 21213,
				Spillover: CellSpillover{
					Available: true, Measured: 331, Cells: 28,
					WithinOwnCell: 25, Beyond: 306,
					CellReachMeters: 3535.5, MedianMeters: 11978, MaxMeters: 20086,
				},
			},
		},
	}

	var spillover jobPipelineMetric
	for _, metric := range generatingGridMetrics(input) {
		if strings.HasPrefix(metric.Label, "Returned from outside the cell") {
			spillover = metric
		}
	}

	if spillover.Label == "" {
		t.Fatal("the cell-spillover metric was not rendered")
	}
	if spillover.Value != "306 of 331 (92.4%)" {
		t.Fatalf("spillover value = %q, want %q", spillover.Value, "306 of 331 (92.4%)")
	}
	if !strings.Contains(spillover.Note, "3.5 km") {
		t.Fatalf("spillover note %q does not quote the cell's own reach", spillover.Note)
	}
}

// A job with no grid must not be told about a corner band that never existed.
func TestGeographyMetricsOmitTheGridBandForAnUngriddedJob(t *testing.T) {
	input := jobPipelineInput{
		Job: Job{ID: "job-1", Data: JobData{Lat: "37.7749", Lon: "-122.4194", Radius: 10000}},
		Stats: ResultStats{
			UniqueBusinesses: 36,
			Geography: ResultGeography{
				Available: true, Measured: 36, InsideRadius: 36,
				MedianMeters: 2100, MaxMeters: 6700,
				RadiusMeters: 10000, PlannedReachMeters: 10000,
			},
		},
	}

	for _, metric := range generatingGridMetrics(input) {
		if strings.Contains(metric.Label, "inside the searched grid") {
			t.Fatalf("metric %q describes a grid this job never planned", metric.Label)
		}
	}
}
