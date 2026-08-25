package acceptance

import (
	"testing"

	"github.com/gosom/google-maps-scraper/grid"
)

func TestDefaultWorkloadReproducesIncidentShape(t *testing.T) {
	workload := DefaultWorkload()
	if len(workload.Queries) != 3 {
		t.Fatalf("queries = %d, want 3", len(workload.Queries))
	}

	bbox, err := grid.ParseBoundingBox(workload.GridBBox)
	if err != nil {
		t.Fatalf("ParseBoundingBox: %v", err)
	}
	cells := grid.EstimateCellCount(bbox, workload.GridCellKM)
	if cells != 16 {
		t.Fatalf("grid cells = %d, want 16", cells)
	}
	if seed := len(workload.Queries) * cells; seed != 48 {
		t.Fatalf("seed tasks = %d, want 48", seed)
	}
	if workload.RuntimeSeconds != 60*60 {
		t.Errorf("runtime = %d, want 3600", workload.RuntimeSeconds)
	}
}

func TestEscalationLadder(t *testing.T) {
	configs := Escalation(DefaultCatalogOptions())
	if len(configs) != 5 {
		t.Fatalf("escalation configs = %d, want 5", len(configs))
	}

	wantConcurrency := map[string]int{"A": 1, "B": 2, "C": 4, "D": 8, "E": 16}
	for _, config := range configs {
		if config.Job.FastMode {
			t.Errorf("experiment %s should be browser mode", config.ID)
		}
		if config.Job.connection() != ConnectionDirect {
			t.Errorf("experiment %s should be direct", config.ID)
		}
		if config.Job.Email {
			t.Errorf("experiment %s should have enrichment off", config.ID)
		}
		if got := config.Job.Concurrency; got != wantConcurrency[config.ID] {
			t.Errorf("experiment %s concurrency = %d, want %d", config.ID, got, wantConcurrency[config.ID])
		}
		if err := config.Job.Validate(); err != nil {
			t.Errorf("experiment %s invalid: %v", config.ID, err)
		}
	}
}

func TestMarketsAreThreeDensities(t *testing.T) {
	configs := Markets(DefaultCatalogOptions())
	if len(configs) != 3 {
		t.Fatalf("markets = %d, want 3", len(configs))
	}
	order := []string{"sparse", "medium", "dense"}
	for index, config := range configs {
		if config.ID != order[index] {
			t.Errorf("market %d id = %q, want %q", index, config.ID, order[index])
		}
		if config.Job.Concurrency != defaultMarketConcurrency {
			t.Errorf("market %s concurrency = %d, want %d", config.ID, config.Job.Concurrency, defaultMarketConcurrency)
		}
		if err := config.Job.Validate(); err != nil {
			t.Errorf("market %s invalid: %v", config.ID, err)
		}
		bbox, err := grid.ParseBoundingBox(config.Job.GridBBox)
		if err != nil {
			t.Errorf("market %s bbox invalid: %v", config.ID, err)
			continue
		}
		if grid.EstimateCellCount(bbox, config.Job.GridCellKM) < 1 {
			t.Errorf("market %s produced no cells", config.ID)
		}
	}
}

func TestFastReferenceIsFastMode(t *testing.T) {
	config := FastReference(DefaultCatalogOptions())
	if !config.Job.FastMode {
		t.Errorf("fast reference should be fast mode")
	}
	if config.ID != "fast" {
		t.Errorf("fast reference id = %q", config.ID)
	}
}

func TestExperimentLookup(t *testing.T) {
	options := DefaultCatalogOptions()
	cases := map[string]int{"A": 1, "c": 4, "E": 16}
	for id, wantConcurrency := range cases {
		config, ok := Experiment(id, options)
		if !ok {
			t.Fatalf("Experiment(%q) not found", id)
		}
		if config.Job.Concurrency != wantConcurrency {
			t.Errorf("Experiment(%q) concurrency = %d, want %d", id, config.Job.Concurrency, wantConcurrency)
		}
	}
	for _, id := range []string{"sparse", "medium", "dense", "fast"} {
		if _, ok := Experiment(id, options); !ok {
			t.Errorf("Experiment(%q) not found", id)
		}
	}
	if _, ok := Experiment("nope", options); ok {
		t.Errorf("Experiment(nope) should be unknown")
	}
}

func TestCatalogContainsEverything(t *testing.T) {
	configs := Catalog(DefaultCatalogOptions())
	if len(configs) != 9 { // A-E (5) + fast (1) + markets (3)
		t.Fatalf("catalog size = %d, want 9", len(configs))
	}
	seen := map[string]bool{}
	for _, config := range configs {
		seen[config.ID] = true
	}
	for _, id := range []string{"A", "B", "C", "D", "E", "fast", "sparse", "medium", "dense"} {
		if !seen[id] {
			t.Errorf("catalog missing %q", id)
		}
	}
}
