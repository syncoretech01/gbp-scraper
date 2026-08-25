package acceptance

import (
	"fmt"
	"strings"
)

const (
	// incidentRuntimeSeconds is the 60-minute runtime cap the production-style
	// incident ran under.
	incidentRuntimeSeconds = 60 * 60
	// defaultMarketConcurrency is the safe browser concurrency the market
	// experiments hold fixed while the area density varies. It defaults to the
	// value the lead proved stable (one).
	defaultMarketConcurrency = 1
)

// Workload is the shared scrape workload the escalation experiments hold
// constant while they vary only concurrency. It is a queries + grid + runtime
// bundle so the whole A..E series is comparable.
type Workload struct {
	// Queries are the search phrases. The incident used three.
	Queries []string
	// GridBBox is "minLat,minLon,maxLat,maxLon"; GridCellKM is the cell size.
	GridBBox   string
	GridCellKM float64
	// Lat and Lon seed the map centre; the grid overrides area coverage.
	Lat string
	Lon string
	// Zoom and Depth mirror the wizard controls.
	Zoom  int
	Depth int
	// RadiusMetres is the search radius around the centre. It is only used
	// when no grid is set; Fast mode requires it, because Fast mode refuses
	// grid coverage and has no other way to bound the area.
	RadiusMetres int
	// RuntimeSeconds is the per-job runtime cap.
	RuntimeSeconds int64
}

// DefaultWorkload reproduces the incident workload exactly: three queries over
// a 4x4 one-kilometre grid (16 cells, 48 seed tasks) around central Austin, TX,
// under a 60-minute cap. The coordinates are a placeholder target the lead can
// replace; the shape (3 queries x 16 cells) is what reproduces the incident.
func DefaultWorkload() Workload {
	return Workload{
		Queries: []string{
			"plumber in Austin TX 78701",
			"electrician in Austin TX 78701",
			"hvac contractor in Austin TX 78701",
		},
		GridBBox:       "30.250,-97.760,30.285,-97.720",
		GridCellKM:     1,
		Lat:            "30.2675",
		Lon:            "-97.7400",
		Zoom:           15,
		Depth:          10,
		RuntimeSeconds: incidentRuntimeSeconds,
	}
}

// CatalogOptions parameterises the named experiment catalog. The zero value is
// valid: DefaultCatalogOptions fills every field.
type CatalogOptions struct {
	// Workload is the shared escalation workload.
	Workload Workload
	// Escalation maps experiment ids A..E to their browser concurrency. The
	// default is the doubling ladder 1, 2, 4, 8, 16.
	Escalation map[string]int
	// MarketConcurrency is the fixed browser concurrency the market
	// experiments run at.
	MarketConcurrency int
	// Repeat is how many times each experiment is run for a repeatability
	// measurement; zero or one means a single run.
	Repeat int
}

// DefaultCatalogOptions returns the standard escalation ladder and the incident
// workload.
func DefaultCatalogOptions() CatalogOptions {
	return CatalogOptions{
		Workload: DefaultWorkload(),
		Escalation: map[string]int{
			"A": 1,
			"B": 2,
			"C": 4,
			"D": 8,
			"E": 16,
		},
		MarketConcurrency: defaultMarketConcurrency,
		Repeat:            1,
	}
}

func (o CatalogOptions) normalised() CatalogOptions {
	defaults := DefaultCatalogOptions()
	if len(o.Workload.Queries) == 0 {
		o.Workload = defaults.Workload
	}
	if len(o.Escalation) == 0 {
		o.Escalation = defaults.Escalation
	}
	if o.MarketConcurrency <= 0 {
		o.MarketConcurrency = defaults.MarketConcurrency
	}
	if o.Repeat <= 0 {
		o.Repeat = 1
	}

	return o
}

// browserJob builds a browser-mode, direct-connection, enrichment-off job for
// a workload at the given concurrency.
func browserJob(name string, workload Workload, concurrency int) JobRequest {
	return JobRequest{
		Name:           name,
		Keywords:       workload.Queries,
		Language:       defaultLanguage,
		Zoom:           workload.Zoom,
		Depth:          workload.Depth,
		FastMode:       false,
		Email:          false,
		Lat:            workload.Lat,
		Lon:            workload.Lon,
		Radius:         workload.RadiusMetres,
		RuntimeSeconds: workload.RuntimeSeconds,
		Concurrency:    concurrency,
		GridBBox:       workload.GridBBox,
		GridCellKM:     workload.GridCellKM,
	}
}

// Escalation returns experiments A..E: the same workload run at an increasing
// browser concurrency, so the run where browser mode breaks under load can be
// found by comparing their records. Experiments are returned in ladder order.
func Escalation(options CatalogOptions) []ExperimentConfig {
	options = options.normalised()

	ids := []string{"A", "B", "C", "D", "E"}
	configs := make([]ExperimentConfig, 0, len(ids))
	for _, id := range ids {
		concurrency, ok := options.Escalation[id]
		if !ok {
			continue
		}
		configs = append(configs, ExperimentConfig{
			ID:     id,
			Label:  fmt.Sprintf("browser mode, concurrency %d, 48-task workload, direct", concurrency),
			Job:    browserJob(fmt.Sprintf("acceptance-%s-browser-c%d", id, concurrency), options.Workload, concurrency),
			Repeat: options.Repeat,
		})
	}

	return configs
}

// marketWorkloads are the three area-density workloads. Coordinates are
// placeholders the lead replaces with a genuinely sparse (rural), medium
// (town), and dense (city) target; the grid extents differ so the task counts
// differ (roughly 4, 16, and 36 cells) even before real business density.
func marketWorkloads(base Workload) map[string]Workload {
	sparse := base
	sparse.GridBBox = "44.055,-121.330,44.073,-121.308"
	sparse.Lat = "44.0640"
	sparse.Lon = "-121.3190"
	sparse.Queries = []string{"plumber", "electrician", "hvac contractor"}

	medium := base
	medium.GridBBox = base.GridBBox
	medium.Lat = base.Lat
	medium.Lon = base.Lon
	medium.Queries = []string{"plumber", "electrician", "hvac contractor"}

	dense := base
	dense.GridBBox = "40.740,-74.010,40.792,-73.948"
	dense.Lat = "40.7660"
	dense.Lon = "-73.9790"
	dense.Queries = []string{"plumber", "electrician", "hvac contractor"}

	return map[string]Workload{"sparse": sparse, "medium": medium, "dense": dense}
}

// Markets returns the sparse/medium/dense experiments: the same fixed browser
// concurrency over three area densities, so yield and quality can be compared
// across market types. Experiments are returned sparse, medium, dense.
func Markets(options CatalogOptions) []ExperimentConfig {
	options = options.normalised()

	workloads := marketWorkloads(options.Workload)
	order := []string{"sparse", "medium", "dense"}
	configs := make([]ExperimentConfig, 0, len(order))
	for _, id := range order {
		workload := workloads[id]
		configs = append(configs, ExperimentConfig{
			ID:     id,
			Label:  fmt.Sprintf("%s market, browser mode, concurrency %d, direct", id, options.MarketConcurrency),
			Job:    browserJob("acceptance-market-"+id, workload, options.MarketConcurrency),
			Repeat: options.Repeat,
		})
	}

	return configs
}

// FastReference returns the fast-mode baseline experiment: the pure-HTTP
// stealth fetcher over the incident workload. It is the control the lead's
// fast-mode observation corresponds to and is not part of the A..E ladder.
func FastReference(options CatalogOptions) ExperimentConfig {
	options = options.normalised()

	job := browserJob("acceptance-fast-reference", options.Workload, 1)
	job.FastMode = true

	return ExperimentConfig{
		ID:     "fast",
		Label:  "fast mode (pure HTTP, no browser), concurrency 1, direct",
		Job:    job,
		Repeat: options.Repeat,
	}
}

// Catalog returns every named experiment in a stable order: the A..E
// escalation, the fast-mode reference, then the three markets.
func Catalog(options CatalogOptions) []ExperimentConfig {
	options = options.normalised()

	configs := Escalation(options)
	configs = append(configs, FastReference(options))
	configs = append(configs, Markets(options)...)

	return configs
}

// Experiment returns the named experiment for id (case-insensitive), reporting
// ok=false for an unknown id.
func Experiment(id string, options CatalogOptions) (ExperimentConfig, bool) {
	target := strings.ToUpper(strings.TrimSpace(id))
	for _, config := range Escalation(options) {
		if strings.ToUpper(config.ID) == target {
			return config, true
		}
	}

	lower := strings.ToLower(strings.TrimSpace(id))
	if lower == "fast" {
		return FastReference(options), true
	}
	for _, config := range Markets(options) {
		if config.ID == lower {
			return config, true
		}
	}

	return ExperimentConfig{}, false
}
