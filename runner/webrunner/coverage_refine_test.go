package webrunner

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/gosom/google-maps-scraper/gmaps"
	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
	"github.com/gosom/google-maps-scraper/web/prospect"
	"github.com/gosom/google-maps-scraper/web/resultimport"
	"github.com/gosom/scrapemate"
)

// truncatingMate writes exactly rows distinct businesses per task, so a task
// can be made to land precisely on, just under, or above the cap its depth
// allows.
type truncatingMate struct {
	output  io.Writer
	tracker *poolTracker
	rows    int
	onStart func(ctx context.Context, seedID string) error
}

func (mate *truncatingMate) Start(ctx context.Context, jobs ...scrapemate.IJob) error {
	seed := "unknown"
	if len(jobs) > 0 {
		seed = jobs[0].GetID()
	}

	mate.tracker.enter(seed)
	defer mate.tracker.exit()

	if mate.onStart != nil {
		if err := mate.onStart(ctx, seed); err != nil {
			return err
		}
	}

	header := resultimport.LegacyHeaders()

	writer := csv.NewWriter(mate.output)
	if err := writer.Write(header); err != nil {
		return err
	}

	for index := range mate.rows {
		row := make([]string, len(header))

		for column, name := range header {
			switch name {
			case "place_id":
				row[column] = fmt.Sprintf("place-%s-%d", seed, index)
			case "title":
				row[column] = fmt.Sprintf("Business %s %d", seed, index)
			case "address":
				row[column] = fmt.Sprintf("%s %d Market Street, Springfield", seed, index)
			case "latitude":
				row[column] = "39.7817"
			case "longitude":
				row[column] = "-89.6501"
			}
		}

		if err := writer.Write(row); err != nil {
			return err
		}
	}

	writer.Flush()

	return writer.Error()
}

func (*truncatingMate) Close() error { return nil }

func TestEffectiveQueryResultCapModel(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name  string
		depth int
		want  int
	}{
		{name: "depth below one is treated as one", depth: 0, want: gmapsFeedPageSize},
		{name: "negative depth is treated as one", depth: -3, want: gmapsFeedPageSize},
		{name: "one page per scroll", depth: 1, want: 20},
		{name: "three pages", depth: 3, want: 60},
		{name: "exactly at the feed ceiling", depth: 6, want: gmapsFeedResultCeiling},
		{name: "depth beyond the ceiling is clamped", depth: 40, want: gmapsFeedResultCeiling},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := effectiveQueryResultCap(testCase.depth); got != testCase.want {
				t.Fatalf("effectiveQueryResultCap(%d) = %d, want %d", testCase.depth, got, testCase.want)
			}
		})
	}
}

func TestMarkCoverageTruncationBoundaries(t *testing.T) {
	t.Parallel()

	engine := newCoverageEngine("job-truncation", web.CoverageOptions{}, web.CoverageSeedState{MaxSequence: -1})

	for _, testCase := range []struct {
		name       string
		depth      int
		checkpoint web.JobTaskCheckpoint
		want       bool
		wantCap    int
	}{
		{
			name: "one below the cap is not truncated", depth: 1,
			checkpoint: web.JobTaskCheckpoint{RowsAdded: 19},
			want:       false, wantCap: 20,
		},
		{
			name: "exactly at the cap is truncated", depth: 1,
			checkpoint: web.JobTaskCheckpoint{RowsAdded: 20},
			want:       true, wantCap: 20,
		},
		{
			name: "in-run duplicates count toward the yield", depth: 1,
			checkpoint: web.JobTaskCheckpoint{RowsAdded: 18, DuplicatesSkipped: 2},
			want:       true, wantCap: 20,
		},
		{
			name: "re-found rows count toward the yield, not against it", depth: 1,
			checkpoint: web.JobTaskCheckpoint{RowsAdded: 20, RowsReplaced: 20},
			want:       true, wantCap: 20,
		},
		{
			name: "an empty task is never truncated", depth: 10,
			checkpoint: web.JobTaskCheckpoint{},
			want:       false, wantCap: gmapsFeedResultCeiling,
		},
		{
			name: "the feed ceiling caps deep plans", depth: 30,
			checkpoint: web.JobTaskCheckpoint{RowsAdded: gmapsFeedResultCeiling},
			want:       true, wantCap: gmapsFeedResultCeiling,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			checkpoint := testCase.checkpoint
			markCoverageTruncation(engine, &checkpoint, testCase.depth)

			if checkpoint.Truncated != testCase.want {
				t.Fatalf("truncated = %v, want %v", checkpoint.Truncated, testCase.want)
			}

			if checkpoint.TruncationCap != testCase.wantCap {
				t.Fatalf("truncation cap = %d, want %d", checkpoint.TruncationCap, testCase.wantCap)
			}
		})
	}
}

func TestMarkCoverageTruncationWithoutAnEngineChangesNothing(t *testing.T) {
	t.Parallel()

	// A job without a coverage block must keep exactly the historical
	// durable checkpoint payload.
	checkpoint := web.JobTaskCheckpoint{RowsAdded: 500, DuplicatesSkipped: 500}
	before := checkpoint

	markCoverageTruncation(nil, &checkpoint, 1)

	if checkpoint != before {
		t.Fatalf("checkpoint = %#v, want it untouched (%#v)", checkpoint, before)
	}
}

// refineTestEngine builds an engine over a small, fully controlled ZIP set.
func refineTestEngine(t *testing.T, options web.CoverageOptions, seed web.CoverageSeedState, zoom int) *coverageEngine {
	t.Helper()

	areas := []prospect.ZIPArea{
		{ZIP: "60001", City: "Alpha", State: "IL", Latitude: 40.0, Longitude: -89.0},
		{ZIP: "60002", City: "Beta", State: "IL", Latitude: 40.05, Longitude: -89.0},
		{ZIP: "60003", City: "Gamma", State: "IL", Latitude: 40.5, Longitude: -89.0},
	}

	engine := newCoverageEngine("job-refine", options, seed)
	engine.zipAreas = func() []prospect.ZIPArea { return areas }

	return engine.withPlanZoom(zoom)
}

func TestCoverageEngineRefinesATruncatedCellOnce(t *testing.T) {
	t.Parallel()

	engine := refineTestEngine(t,
		web.CoverageOptions{MaxExpansions: 4, ExpansionMinNew: 1000},
		web.CoverageSeedState{Queries: []string{"dentist in Alpha IL 60001"}, MaxSequence: 0},
		15,
	)

	parent := web.JobTask{Key: "t-parent", Query: "dentist in Alpha IL 60001"}
	truncated := web.JobTaskCheckpoint{RowsAdded: 20, Truncated: true, TruncationCap: 20}

	decision := engine.recordCompletion(parent, truncated)

	if len(decision.refinements) != 1 {
		t.Fatalf("refinements = %d, want 1", len(decision.refinements))
	}

	refinement := decision.refinements[0]

	// The refinement re-covers the parent's own cell, tagged apart from a
	// neighbour expansion so its value can be measured separately.
	if refinement.Origin != web.CoverageRefinementOriginPrefix+"60001" {
		t.Fatalf("refinement origin = %q", refinement.Origin)
	}

	if refinement.Query != "dentist in Alpha IL 60001" {
		t.Fatalf("refinement query = %q, want the parent cell re-queried", refinement.Query)
	}

	if !strings.HasPrefix(refinement.Key, "ref-") || refinement.Key == parent.Key {
		t.Fatalf("refinement key = %q, want a distinct ref- key", refinement.Key)
	}

	if refinement.Sequence != 1 {
		t.Fatalf("refinement sequence = %d, want 1", refinement.Sequence)
	}

	// The parent may not earn a second refinement, however often it is
	// reported (a retried attempt, a duplicate completion).
	if repeat := engine.recordCompletion(parent, truncated); len(repeat.refinements) != 0 {
		t.Fatalf("repeat refinements = %d, want 0", len(repeat.refinements))
	}

	// A different truncated cell still earns its own single refinement.
	other := engine.recordCompletion(
		web.JobTask{Key: "t-other", Query: "dentist in Gamma IL 60003"}, truncated,
	)
	if len(other.refinements) != 1 {
		t.Fatalf("second cell refinements = %d, want 1", len(other.refinements))
	}

	if other.refinements[0].Key == refinement.Key {
		t.Fatalf("two cells produced the same task key %q", refinement.Key)
	}
}

func TestCoverageEngineDeclinesRefinementsItCannotJustify(t *testing.T) {
	t.Parallel()

	truncated := web.JobTaskCheckpoint{RowsAdded: 20, Truncated: true, TruncationCap: 20}
	seed := web.CoverageSeedState{Queries: []string{"dentist in Alpha IL 60001"}, MaxSequence: 0}

	for _, testCase := range []struct {
		name    string
		options web.CoverageOptions
		zoom    int
		task    web.JobTask
		point   web.JobTaskCheckpoint
	}{
		{
			name:    "a task that was not truncated",
			options: web.CoverageOptions{MaxExpansions: 4},
			zoom:    15,
			task:    web.JobTask{Key: "t-1", Query: "dentist in Alpha IL 60001"},
			point:   web.JobTaskCheckpoint{RowsAdded: 19, TruncationCap: 20},
		},
		{
			name:    "expansion disabled leaves no budget",
			options: web.CoverageOptions{MaxExpansions: 0},
			zoom:    15,
			task:    web.JobTask{Key: "t-2", Query: "dentist in Alpha IL 60001"},
			point:   truncated,
		},
		{
			name:    "a refinement already at the maximum chain depth",
			options: web.CoverageOptions{MaxExpansions: 4},
			zoom:    15,
			task: web.JobTask{
				Key: "ref-deep", Query: "dentist in Alpha IL 60001",
				Origin:  web.CoverageRefinementOriginPrefix + "60001",
				Payload: refinementPayload(t, 15+2*coverageRefinementZoomStep),
			},
			point: truncated,
		},
		{
			name:    "a refinement that stopped paying",
			options: web.CoverageOptions{MaxExpansions: 4, ExpansionMinNew: 50},
			zoom:    15,
			task: web.JobTask{
				Key: "ref-poor", Query: "dentist in Alpha IL 60001",
				Origin:  web.CoverageRefinementOriginPrefix + "60001",
				Payload: refinementPayload(t, 15+coverageRefinementZoomStep),
			},
			point: truncated,
		},
		{
			name:    "no tighter zoom is available",
			options: web.CoverageOptions{MaxExpansions: 4},
			zoom:    maximumMapsZoom,
			task:    web.JobTask{Key: "t-3", Query: "dentist in Alpha IL 60001"},
			point:   truncated,
		},
		{
			name:    "the plan has no zoom to tighten",
			options: web.CoverageOptions{MaxExpansions: 4},
			zoom:    0,
			task:    web.JobTask{Key: "t-4", Query: "dentist in Alpha IL 60001"},
			point:   truncated,
		},
		{
			name:    "a query that is not ZIP shaped",
			options: web.CoverageOptions{MaxExpansions: 4},
			zoom:    15,
			task:    web.JobTask{Key: "t-5", Query: "coffee shop"},
			point:   truncated,
		},
		{
			name:    "a ZIP outside the dataset",
			options: web.CoverageOptions{MaxExpansions: 4},
			zoom:    15,
			task:    web.JobTask{Key: "t-6", Query: "dentist in Nowhere IL 99999"},
			point:   truncated,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			engine := refineTestEngine(t, testCase.options, seed, testCase.zoom)

			if decision := engine.recordCompletion(testCase.task, testCase.point); len(decision.refinements) != 0 {
				t.Fatalf("refinements = %d, want 0", len(decision.refinements))
			}
		})
	}
}

func TestCoverageRefinementSharesTheExpansionBudget(t *testing.T) {
	t.Parallel()

	// One unit of budget: the truncated cell takes it, so no neighbour is
	// added even though the task is productive enough to earn one.
	engine := refineTestEngine(t,
		web.CoverageOptions{MaxExpansions: 1, ExpansionMinNew: 1},
		web.CoverageSeedState{Queries: []string{"dentist in Alpha IL 60001"}, MaxSequence: 0},
		15,
	)

	decision := engine.recordCompletion(
		web.JobTask{Key: "t-parent", Query: "dentist in Alpha IL 60001"},
		web.JobTaskCheckpoint{RowsAdded: 20, Truncated: true, TruncationCap: 20},
	)

	if len(decision.refinements) != 1 || len(decision.expansions) != 0 {
		t.Fatalf("refinements = %d and expansions = %d, want 1 and 0",
			len(decision.refinements), len(decision.expansions))
	}

	// The budget is spent: a later productive cell adds nothing at all.
	next := engine.recordCompletion(
		web.JobTask{Key: "t-next", Query: "dentist in Gamma IL 60003"},
		web.JobTaskCheckpoint{RowsAdded: 20, Truncated: true, TruncationCap: 20},
	)
	if len(next.refinements) != 0 || len(next.expansions) != 0 {
		t.Fatalf("after the budget is spent: %d refinement(s), %d expansion(s)",
			len(next.refinements), len(next.expansions))
	}
}

func TestCoverageRefinementSeedRebuildsFromPayloadOnly(t *testing.T) {
	t.Parallel()

	job := coverageScrapeJob("55555555-5555-4555-8555-555555555560", []string{"dentist in Alpha IL 60001"})
	job.Data.Zoom = 15

	definition := refinementTaskDefinition(job.ID, "dentist", prospect.ZIPArea{
		ZIP: "60001", City: "Alpha", State: "IL", Latitude: 40.0, Longitude: -89.0,
	}, refinementZoom(job.Data.Zoom), 7)

	task := web.JobTask{
		Key:     definition.Key,
		Query:   definition.Query,
		Origin:  definition.Origin,
		Payload: definition.Payload,
	}

	seed, err := buildExpansionSeed(&job, task, nil, nil, false)
	if err != nil {
		t.Fatalf("build refinement seed: %v", err)
	}

	if seed == nil || seed.GetID() != definition.Key {
		t.Fatalf("seed = %#v, want ID %q", seed, definition.Key)
	}

	mapJob, ok := seed.(*gmaps.GmapJob)
	if !ok {
		t.Fatalf("seed type = %T, want *gmaps.GmapJob", seed)
	}

	// The refinement looks at the ZIP's own centroid, two zoom levels
	// tighter than the plan, and takes both facts from its payload alone.
	if !strings.Contains(mapJob.URL, "@40.000000,-89.000000,17z") {
		t.Fatalf("refinement URL = %q, want the ZIP centroid at zoom 17", mapJob.URL)
	}

	// A payload written before refinements existed carries no zoom and must
	// keep using the job's own.
	legacy := expansionTaskDefinition(job.ID, "dentist", "60001", prospect.ZIPArea{
		ZIP: "60002", City: "Beta", State: "IL", Latitude: 40.05, Longitude: -89.0,
	}, 8)

	legacySeed, err := buildExpansionSeed(&job, web.JobTask{
		Key: legacy.Key, Query: legacy.Query, Origin: legacy.Origin, Payload: legacy.Payload,
	}, nil, nil, false)
	if err != nil {
		t.Fatalf("build expansion seed: %v", err)
	}

	legacyJob, ok := legacySeed.(*gmaps.GmapJob)
	if !ok {
		t.Fatalf("legacy seed type = %T, want *gmaps.GmapJob", legacySeed)
	}

	if !strings.Contains(legacyJob.URL, ",15z") {
		t.Fatalf("expansion URL = %q, want the plan zoom 15", legacyJob.URL)
	}
}

func TestCoverageOverlapGuardSkipsNearCoincidentZIPs(t *testing.T) {
	t.Parallel()

	// 60002 shares the parent's centroid exactly and 60003 sits 200 m away:
	// both would re-search the ground 60001 already covers. Only 60004,
	// comfortably beyond the separation floor, is worth a task.
	areas := []prospect.ZIPArea{
		{ZIP: "60001", City: "Alpha", State: "IL", Latitude: 40.0, Longitude: -89.0},
		{ZIP: "60002", City: "Beta", State: "IL", Latitude: 40.0, Longitude: -89.0},
		{ZIP: "60003", City: "Gamma", State: "IL", Latitude: 40.0018, Longitude: -89.0},
		{ZIP: "60004", City: "Delta", State: "IL", Latitude: 40.2, Longitude: -89.0},
	}

	engine := newCoverageEngine("job-overlap", web.CoverageOptions{
		MaxExpansions:   3,
		ExpansionMinNew: 1,
	}, web.CoverageSeedState{Queries: []string{"dentist in Alpha IL 60001"}, MaxSequence: 0})
	engine.zipAreas = func() []prospect.ZIPArea { return areas }

	decision := engine.recordCompletion(
		web.JobTask{Key: "t-parent", Query: "dentist in Alpha IL 60001"},
		web.JobTaskCheckpoint{RowsAdded: 5},
	)

	if len(decision.expansions) != 1 {
		t.Fatalf("expansions = %d, want 1; overlapping neighbours must be skipped", len(decision.expansions))
	}

	if decision.expansions[0].Query != "dentist in Delta IL 60004" {
		t.Fatalf("expansion = %q, want the only non-overlapping neighbour", decision.expansions[0].Query)
	}
}

func TestCoverageOverlapGuardSeparatesChosenNeighboursFromEachOther(t *testing.T) {
	t.Parallel()

	// 60003 clears the parent but coincides with 60002, which is chosen
	// first, so the batch must skip past it to 60004.
	areas := []prospect.ZIPArea{
		{ZIP: "60001", City: "Alpha", State: "IL", Latitude: 40.0, Longitude: -89.0},
		{ZIP: "60002", City: "Beta", State: "IL", Latitude: 40.1, Longitude: -89.0},
		{ZIP: "60003", City: "Gamma", State: "IL", Latitude: 40.1005, Longitude: -89.0},
		{ZIP: "60004", City: "Delta", State: "IL", Latitude: 40.3, Longitude: -89.0},
	}

	engine := newCoverageEngine("job-overlap-batch", web.CoverageOptions{
		MaxExpansions:   2,
		ExpansionMinNew: 1,
	}, web.CoverageSeedState{Queries: []string{"dentist in Alpha IL 60001"}, MaxSequence: 0})
	engine.zipAreas = func() []prospect.ZIPArea { return areas }

	decision := engine.recordCompletion(
		web.JobTask{Key: "t-parent", Query: "dentist in Alpha IL 60001"},
		web.JobTaskCheckpoint{RowsAdded: 5},
	)

	if len(decision.expansions) != 2 {
		t.Fatalf("expansions = %d, want 2", len(decision.expansions))
	}

	if decision.expansions[0].Query != "dentist in Beta IL 60002" ||
		decision.expansions[1].Query != "dentist in Delta IL 60004" {
		t.Fatalf("expansions = %q and %q, want 60002 then 60004",
			decision.expansions[0].Query, decision.expansions[1].Query)
	}
}

func TestCoverageExpandsOneNeighbourhoodPerParentZIP(t *testing.T) {
	t.Parallel()

	areas := []prospect.ZIPArea{
		{ZIP: "60001", City: "Alpha", State: "IL", Latitude: 40.0, Longitude: -89.0},
		{ZIP: "60002", City: "Beta", State: "IL", Latitude: 40.05, Longitude: -89.0},
		{ZIP: "60003", City: "Gamma", State: "IL", Latitude: 40.5, Longitude: -89.0},
		{ZIP: "60004", City: "Delta", State: "IL", Latitude: 41.5, Longitude: -89.0},
	}

	engine := newCoverageEngine("job-parent-once", web.CoverageOptions{
		MaxExpansions:   10,
		ExpansionMinNew: 1,
	}, web.CoverageSeedState{
		Queries:     []string{"dentist in Alpha IL 60001", "dental clinic in Alpha IL 60001"},
		MaxSequence: 1,
	})
	engine.zipAreas = func() []prospect.ZIPArea { return areas }

	first := engine.recordCompletion(
		web.JobTask{Key: "t-synonym-1", Query: "dentist in Alpha IL 60001"},
		web.JobTaskCheckpoint{RowsAdded: 5},
	)
	if len(first.expansions) == 0 {
		t.Fatal("the first synonym at a productive ZIP earned no expansion")
	}

	// The plan is a synonym x ZIP cross product, so the same ZIP completes
	// once per synonym. Its neighbourhood must be paid for only once.
	second := engine.recordCompletion(
		web.JobTask{Key: "t-synonym-2", Query: "dental clinic in Alpha IL 60001"},
		web.JobTaskCheckpoint{RowsAdded: 5},
	)
	if len(second.expansions) != 0 {
		t.Fatalf("second synonym at the same ZIP added %d expansion(s), want 0", len(second.expansions))
	}
}

func TestRefinementTaskKeyIsDeterministicAndDistinct(t *testing.T) {
	t.Parallel()

	area := prospect.ZIPArea{ZIP: "60001", City: "Alpha", State: "IL", Latitude: 40.0, Longitude: -89.0}

	// The key is derived from the job, the query and the zoom alone, so a
	// restart that re-decides the same refinement re-enqueues nothing: the
	// durable append is a no-op on a key the plan already holds.
	first := refinementTaskDefinition("job-key", "dentist", area, 17, 3)
	second := refinementTaskDefinition("job-key", "dentist", area, 17, 99)

	if first.Key != second.Key {
		t.Fatalf("keys = %q and %q, want the same deterministic key", first.Key, second.Key)
	}

	for _, other := range []web.JobTaskDefinition{
		refinementTaskDefinition("job-other", "dentist", area, 17, 3),
		refinementTaskDefinition("job-key", "dental clinic", area, 17, 3),
		refinementTaskDefinition("job-key", "dentist", area, 19, 3),
		expansionTaskDefinition("job-key", "dentist", "60001", area, 3),
	} {
		if other.Key == first.Key {
			t.Fatalf("distinct task %q collided with the refinement key %q", other.Query, first.Key)
		}
	}
}

func TestCoverageNetNewRows(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name       string
		checkpoint web.JobTaskCheckpoint
		want       int64
	}{
		{
			name:       "a wholly fresh task is all new",
			checkpoint: web.JobTaskCheckpoint{RowsAdded: 12},
			want:       12,
		},
		{
			name:       "a task that re-found everything is all overlap",
			checkpoint: web.JobTaskCheckpoint{RowsAdded: 12, RowsReplaced: 12},
			want:       0,
		},
		{
			name:       "partial overlap leaves the remainder",
			checkpoint: web.JobTaskCheckpoint{RowsAdded: 12, RowsReplaced: 5},
			want:       7,
		},
		{
			name:       "one row may supersede several stored rows",
			checkpoint: web.JobTaskCheckpoint{RowsAdded: 2, RowsReplaced: 5},
			want:       0,
		},
		{
			name:       "in-run duplicates are not overlap with other queries",
			checkpoint: web.JobTaskCheckpoint{RowsAdded: 12, DuplicatesSkipped: 30},
			want:       12,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := coverageNetNewRows(testCase.checkpoint); got != testCase.want {
				t.Fatalf("coverageNetNewRows(%#v) = %d, want %d", testCase.checkpoint, got, testCase.want)
			}
		})
	}
}

func TestCoverageExpansionIgnoresRowsAnotherQueryAlreadyFound(t *testing.T) {
	t.Parallel()

	engine := refineTestEngine(t,
		web.CoverageOptions{MaxExpansions: 4, ExpansionMinNew: 3},
		web.CoverageSeedState{Queries: []string{"dentist in Alpha IL 60001"}, MaxSequence: 0},
		15,
	)

	// Every one of this task's rows was already collected by an earlier
	// query: it broke no new ground and must not buy neighbour tasks out of
	// the shared budget. The saturation window cannot see this at all -
	// duplicates_skipped only counts collisions inside one result file, so
	// the ratio reads a perfect 1.0.
	overlapping := web.JobTaskCheckpoint{RowsAdded: 20, RowsReplaced: 20}

	if ratio := web.CoverageWindowRatio([]web.CoverageSample{{
		RowsAdded: overlapping.RowsAdded, DuplicatesSkipped: overlapping.DuplicatesSkipped,
	}}); ratio != 1 {
		t.Fatalf("window ratio = %f, want the 1.0 the engine actually sees", ratio)
	}

	decision := engine.recordCompletion(
		web.JobTask{Key: "t-overlap", Query: "dentist in Alpha IL 60001"}, overlapping,
	)
	if len(decision.expansions) != 0 {
		t.Fatalf("expansions = %d, want 0 for a query that found nothing new", len(decision.expansions))
	}

	// Genuinely new ground still expands: three of this query's four rows
	// were businesses the workspace had never seen.
	productive := engine.recordCompletion(
		web.JobTask{Key: "t-productive", Query: "dentist in Gamma IL 60003"},
		web.JobTaskCheckpoint{RowsAdded: 4, RowsReplaced: 1},
	)
	if len(productive.expansions) == 0 {
		t.Fatal("a query with three genuinely new rows earned no expansion")
	}
}

func TestCoverageRefinementAppendsOnceAndResumesAfterRestart(t *testing.T) {
	t.Parallel()

	service, dataFolder := newPoolTestService(t)

	parent := coverageParentArea(t)
	parentQuery := strings.Join(strings.Fields(fmt.Sprintf(
		"dentist in %s %s %s", parent.City, strings.ToUpper(parent.State), parent.ZIP,
	)), " ")

	job := coverageScrapeJob("55555555-5555-4555-8555-555555555561", []string{parentQuery})
	// Depth one caps a query at a single feed page, so a task that returns
	// a full page is truncated by the model.
	job.Data.Depth = 1
	job.Data.Coverage = &web.CoverageOptions{
		// A single unit of shared budget: the truncated cell must take it
		// before any neighbour ZIP does.
		MaxExpansions:   1,
		ExpansionMinNew: 1,
	}

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()

	firstTracker := &poolTracker{}
	firstWorker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 2},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			return &truncatingMate{
				output: output, tracker: firstTracker, rows: gmapsFeedPageSize,
				onStart: func(taskCtx context.Context, seed string) error {
					// Die the moment the refinement starts, so the resumed
					// run has to rebuild its seed from the durable payload.
					if strings.HasPrefix(seed, "ref-") {
						cancelFirst()
						<-taskCtx.Done()

						return taskCtx.Err()
					}

					return nil
				},
			}, nil
		},
		sampleResources: healthyResources,
	}

	if err := firstWorker.scrapeJob(firstCtx, &job); err != nil {
		t.Fatalf("first (interrupted) scrape: %v", err)
	}

	execution, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution after interrupt: %v", err)
	}

	if execution.Tasks.Total != 2 || execution.Tasks.Completed != 1 || execution.Tasks.Pending != 1 {
		t.Fatalf("task summary after interrupt = %#v, want 1 completed and 1 resumable", execution.Tasks)
	}

	// The refinement counts against the shared budget across restarts.
	seedState, err := service.JobCoverageSeedState(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("read seed state: %v", err)
	}

	if seedState.ExpansionTasks != 1 {
		t.Fatalf("appended tasks after first run = %d, want 1", seedState.ExpansionTasks)
	}

	if _, _, err := service.ApplyControl(context.Background(), job.ID, jobruntime.ControlResume); err != nil {
		t.Fatalf("resume job: %v", err)
	}

	secondTracker := &poolTracker{}
	secondWorker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 2},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			return &truncatingMate{output: output, tracker: secondTracker, rows: gmapsFeedPageSize}, nil
		},
		sampleResources: healthyResources,
	}

	if err := secondWorker.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("resumed scrape: %v", err)
	}

	execution, err = service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution after resume: %v", err)
	}

	// The refinement is itself truncated, but a refinement is never refined
	// and the budget is spent, so the plan stops at two tasks.
	if execution.Tasks.Total != 2 || execution.Tasks.Completed != 2 || execution.Tasks.Pending != 0 {
		t.Fatalf("task summary after resume = %#v, want exactly 2 completed", execution.Tasks)
	}

	if job.Status != web.StatusOK {
		t.Fatalf("job status = %q, want %q", job.Status, web.StatusOK)
	}

	refinementSeeds := 0

	for _, seed := range secondTracker.startedSeeds() {
		if strings.HasPrefix(seed, "ref-") {
			refinementSeeds++
		}
	}

	if refinementSeeds != 1 {
		t.Fatalf("resumed run started %d refinement seed(s), want 1", refinementSeeds)
	}

	report, err := service.JobCoverage(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("read coverage: %v", err)
	}

	if report.Totals.RefinementsAdded != 1 || report.Totals.ExpansionsAdded != 0 {
		t.Fatalf("totals = %#v, want one refinement and no neighbour expansion", report.Totals)
	}

	if report.Totals.TasksTruncated != 2 {
		t.Fatalf("truncated tasks = %d, want 2", report.Totals.TasksTruncated)
	}

	refinementRows := 0

	for _, row := range report.ByQuery {
		if !strings.HasPrefix(row.Origin, web.CoverageRefinementOriginPrefix) {
			continue
		}

		refinementRows++

		if row.Origin != web.CoverageRefinementOriginPrefix+parent.ZIP {
			t.Fatalf("refinement origin = %q, want the parent's own ZIP %s", row.Origin, parent.ZIP)
		}

		if row.ZIP != parent.ZIP {
			t.Fatalf("refinement ZIP = %q, want the same cell %s", row.ZIP, parent.ZIP)
		}

		if !row.Truncated {
			t.Fatalf("refinement row = %#v, want the truncation signal recorded", row)
		}
	}

	if refinementRows != 1 {
		t.Fatalf("coverage rows show %d refinement task(s), want 1", refinementRows)
	}
}
