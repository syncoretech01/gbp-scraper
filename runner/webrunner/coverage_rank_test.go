package webrunner

import (
	"encoding/json"
	"testing"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/prospect"
)

// refinementPayload builds the durable payload of a refinement task recorded
// at the given zoom, which is what the chain-depth rule reads.
func refinementPayload(t *testing.T, zoom int) json.RawMessage {
	t.Helper()

	encoded, err := json.Marshal(expansionTaskPayload{
		Query: "dentist in Alpha IL 60001", ZIP: "60001", ParentZIP: "60001",
		Expansion: true, Refinement: true, Zoom: zoom,
	})
	if err != nil {
		t.Fatalf("encode refinement payload: %v", err)
	}

	return encoded
}

// rankTestZIPs are three same-state ZIPs far enough apart to be genuinely
// different ground under the default separation floor.
func rankTestZIPs() []prospect.ZIPArea {
	return []prospect.ZIPArea{
		{ZIP: "60001", City: "Alpha", State: "IL", Latitude: 40.0, Longitude: -89.0},
		{ZIP: "60002", City: "Beta", State: "IL", Latitude: 40.3, Longitude: -89.0},
		{ZIP: "60003", City: "Gamma", State: "IL", Latitude: 40.7, Longitude: -89.0},
	}
}

func rankTestEngine(t *testing.T, options web.CoverageOptions, zoom int) *coverageEngine {
	t.Helper()

	engine := newCoverageEngine("job-rank", options, web.CoverageSeedState{
		Queries: []string{"dentist in Alpha IL 60001"}, MaxSequence: 0,
	})
	engine.zipAreas = rankTestZIPs

	return engine.withPlanZoom(zoom)
}

func TestExpectedRefinementYieldPricesTheHiddenFeedPage(t *testing.T) {
	t.Parallel()

	// Every observed listing was new, so the hidden page is expected to pay
	// a full feed page of new businesses.
	if score := expectedRefinementYield(web.JobTaskCheckpoint{
		RowsAdded: 20, Truncated: true, TruncationCap: 20,
	}); score != gmapsFeedPageSize {
		t.Fatalf("clean-yield refinement score = %f, want %d", score, gmapsFeedPageSize)
	}

	// Half the listings were businesses the workspace already held.
	if score := expectedRefinementYield(web.JobTaskCheckpoint{
		RowsAdded: 20, RowsReplaced: 10, Truncated: true, TruncationCap: 20,
	}); score != gmapsFeedPageSize/2 {
		t.Fatalf("half-yield refinement score = %f, want %d", score, gmapsFeedPageSize/2)
	}

	// A task that observed nothing prices at nothing.
	if score := expectedRefinementYield(web.JobTaskCheckpoint{}); score != 0 {
		t.Fatalf("empty refinement score = %f, want 0", score)
	}
}

func TestWorkRankingFundsTheTruncatedCellBeforeAnUnprovenNeighbour(t *testing.T) {
	t.Parallel()

	// One unit of shared budget and both kinds of work on offer: the cell
	// that is KNOWN to be hiding listings must win it.
	engine := rankTestEngine(t, web.CoverageOptions{MaxExpansions: 1, ExpansionMinNew: 1}, 15)

	decision := engine.recordCompletion(
		web.JobTask{Key: "t-parent", Query: "dentist in Alpha IL 60001"},
		web.JobTaskCheckpoint{RowsAdded: 20, Truncated: true, TruncationCap: 20},
	)

	if len(decision.refinements) != 1 || len(decision.expansions) != 0 {
		t.Fatalf("refinements = %d expansions = %d, want 1 and 0",
			len(decision.refinements), len(decision.expansions))
	}
}

func TestWorkRankingPrefersNeighboursOverAWornOutRefinement(t *testing.T) {
	t.Parallel()

	engine := rankTestEngine(t, web.CoverageOptions{MaxExpansions: 1, ExpansionMinNew: 1}, 15)

	// Neighbour probes have measurably paid ten new businesses each this
	// run, so an unexplored neighbour is worth more than a tighter look at
	// a capped cell that is now returning mostly re-found rows.
	engine.expansionProbes = 2
	engine.expansionProbeNetNew = 20

	decision := engine.recordCompletion(
		web.JobTask{Key: "t-parent", Query: "dentist in Alpha IL 60001"},
		web.JobTaskCheckpoint{RowsAdded: 100, RowsReplaced: 70, Truncated: true, TruncationCap: 100},
	)

	if len(decision.expansions) != 1 || len(decision.refinements) != 0 {
		t.Fatalf("refinements = %d expansions = %d, want 0 and 1",
			len(decision.refinements), len(decision.expansions))
	}
}

func TestWorkRankingPricesNeighboursFromCompletedProbes(t *testing.T) {
	t.Parallel()

	engine := rankTestEngine(t, web.CoverageOptions{MaxExpansions: 6, ExpansionMinNew: 1}, 15)

	// Two neighbour probes have already come back empty, so the measured
	// mean is zero and a truncated cell that is still paying outranks any
	// further neighbour.
	for _, key := range []string{"exp-a", "exp-b"} {
		engine.recordCompletion(web.JobTask{
			Key: key, Query: "dentist in Beta IL 60002",
			Origin: web.CoverageExpansionOriginPrefix + "60001",
		}, web.JobTaskCheckpoint{})
	}

	if engine.expansionProbes != 2 || engine.expansionProbeNetNew != 0 {
		t.Fatalf("probe evidence = %d probes / %d net-new, want 2 and 0",
			engine.expansionProbes, engine.expansionProbeNetNew)
	}

	if score := engine.expectedExpansionYieldLocked(100); score != 0 {
		t.Fatalf("expansion score = %f, want the measured 0 rather than the prior", score)
	}

	decision := engine.recordCompletion(
		web.JobTask{Key: "t-parent", Query: "dentist in Alpha IL 60001"},
		web.JobTaskCheckpoint{RowsAdded: 20, Truncated: true, TruncationCap: 20},
	)

	if len(decision.refinements) != 1 {
		t.Fatalf("refinements = %d, want the refinement funded first", len(decision.refinements))
	}

	if decision.refinements[0].Sequence != 1 {
		t.Fatalf("refinement sequence = %d, want the next plan sequence", decision.refinements[0].Sequence)
	}
}

func TestMarginalYieldGateFollowsTheCellsCumulativeNetNew(t *testing.T) {
	t.Parallel()

	engine := rankTestEngine(t, web.CoverageOptions{MaxExpansions: 4, ExpansionMinNew: 10}, 15)

	// Six new businesses is below the floor on its own.
	first := engine.recordCompletion(
		web.JobTask{Key: "t-syn-1", Query: "dentist in Alpha IL 60001"},
		web.JobTaskCheckpoint{RowsAdded: 6},
	)
	if len(first.expansions) != 0 {
		t.Fatalf("expansions = %d, want none below the floor", len(first.expansions))
	}

	// A second synonym over the same cell carries it past the floor: the
	// plan is still finding new businesses there, so it expands.
	second := engine.recordCompletion(
		web.JobTask{Key: "t-syn-2", Query: "dental clinic in Alpha IL 60001"},
		web.JobTaskCheckpoint{RowsAdded: 6},
	)
	if len(second.expansions) == 0 {
		t.Fatal("a cell whose cumulative net-new cleared the floor earned no expansion")
	}
}

func TestMarginalYieldGateStopsACellThatIsMostlyRefinding(t *testing.T) {
	t.Parallel()

	engine := rankTestEngine(t, web.CoverageOptions{MaxExpansions: 4, ExpansionMinNew: 10}, 15)

	// 100 rows, 88 of them already in the workspace: twelve new businesses
	// clear the floor, but the cell is overwhelmingly re-finding, so its
	// neighbourhood is not worth budget.
	decision := engine.recordCompletion(
		web.JobTask{Key: "t-refound", Query: "dentist in Alpha IL 60001"},
		web.JobTaskCheckpoint{RowsAdded: 100, RowsReplaced: 88},
	)

	if len(decision.expansions) != 0 {
		t.Fatalf("expansions = %d, want 0 for a cell that is mostly re-finding", len(decision.expansions))
	}

	if share := engine.cellYield["60001"].share(); share >= coverageExpansionMinYieldShare {
		t.Fatalf("cell share = %f, want it below the marginal-yield floor", share)
	}
}

func TestRefinementChainIsBoundedAndEvidenceLed(t *testing.T) {
	t.Parallel()

	engine := rankTestEngine(t, web.CoverageOptions{MaxExpansions: 8, ExpansionMinNew: 5}, 15)

	first := engine.recordCompletion(
		web.JobTask{Key: "t-parent", Query: "dentist in Alpha IL 60001"},
		web.JobTaskCheckpoint{RowsAdded: 20, Truncated: true, TruncationCap: 20},
	)
	if len(first.refinements) != 1 {
		t.Fatalf("first refinements = %d, want 1", len(first.refinements))
	}

	refinement := first.refinements[0]

	// The second pass was cut off again AND kept paying, so it earns one
	// further, tighter pass.
	second := engine.recordCompletion(web.JobTask{
		Key: refinement.Key, Query: refinement.Query,
		Origin: refinement.Origin, Payload: refinement.Payload,
	}, web.JobTaskCheckpoint{RowsAdded: 20, Truncated: true, TruncationCap: 20})

	if len(second.refinements) != 1 {
		t.Fatalf("chained refinements = %d, want 1", len(second.refinements))
	}

	chained := second.refinements[0]
	if chained.Key == refinement.Key {
		t.Fatalf("chained refinement reused task key %q", chained.Key)
	}

	// The chain stops at the bounded depth however strong the evidence.
	third := engine.recordCompletion(web.JobTask{
		Key: chained.Key, Query: chained.Query,
		Origin: chained.Origin, Payload: chained.Payload,
	}, web.JobTaskCheckpoint{RowsAdded: 20, Truncated: true, TruncationCap: 20})

	if len(third.refinements) != 0 {
		t.Fatalf("refinements past the depth bound = %d, want 0", len(third.refinements))
	}
}

func TestRefinementChainStopsWhenTheTighterPassStopsPaying(t *testing.T) {
	t.Parallel()

	engine := rankTestEngine(t, web.CoverageOptions{MaxExpansions: 8, ExpansionMinNew: 15}, 15)

	first := engine.recordCompletion(
		web.JobTask{Key: "t-parent", Query: "dentist in Alpha IL 60001"},
		web.JobTaskCheckpoint{RowsAdded: 20, Truncated: true, TruncationCap: 20},
	)
	if len(first.refinements) != 1 {
		t.Fatalf("first refinements = %d, want 1", len(first.refinements))
	}

	refinement := first.refinements[0]

	// Capped again, but only two of its twenty rows were new: below the
	// floor, so the chain ends here.
	second := engine.recordCompletion(web.JobTask{
		Key: refinement.Key, Query: refinement.Query,
		Origin: refinement.Origin, Payload: refinement.Payload,
	}, web.JobTaskCheckpoint{RowsAdded: 20, RowsReplaced: 18, Truncated: true, TruncationCap: 20})

	if len(second.refinements) != 0 {
		t.Fatalf("chained refinements = %d, want 0 once the pass stopped paying", len(second.refinements))
	}
}

func TestRefinementDepthComesFromTheDurablePayload(t *testing.T) {
	t.Parallel()

	plan := web.JobTask{Key: "t-plan", Query: "dentist in Alpha IL 60001"}
	if depth := refinementDepthOf(plan, 15); depth != 0 {
		t.Fatalf("plan task depth = %d, want 0", depth)
	}

	cases := []struct {
		zoom int
		want int
	}{
		{zoom: 17, want: 1},
		{zoom: 19, want: 2},
		{zoom: 21, want: 3},
	}

	for _, testCase := range cases {
		task := web.JobTask{
			Key: "ref", Query: "dentist in Alpha IL 60001",
			Origin:  web.CoverageRefinementOriginPrefix + "60001",
			Payload: refinementPayload(t, testCase.zoom),
		}

		if depth := refinementDepthOf(task, 15); depth != testCase.want {
			t.Fatalf("depth at zoom %d = %d, want %d", testCase.zoom, depth, testCase.want)
		}
	}

	// A payload written before zooms were recorded is treated as one pass
	// in already, so an old refinement can never restart the chain.
	legacy := web.JobTask{
		Key: "ref-legacy", Query: "dentist in Alpha IL 60001",
		Origin: web.CoverageRefinementOriginPrefix + "60001",
	}
	if depth := refinementDepthOf(legacy, 15); depth != 1 {
		t.Fatalf("legacy refinement depth = %d, want 1", depth)
	}
}

func TestRankedWorkNeverReEnqueuesAnIdenticalTaskKey(t *testing.T) {
	t.Parallel()

	options := web.CoverageOptions{MaxExpansions: 8, ExpansionMinNew: 1}
	task := web.JobTask{Key: "t-parent", Query: "dentist in Alpha IL 60001"}
	checkpoint := web.JobTaskCheckpoint{RowsAdded: 20, Truncated: true, TruncationCap: 20}

	engine := rankTestEngine(t, options, 15)
	first := engine.recordCompletion(task, checkpoint)

	// A restarted process rebuilds the engine from the durable plan and
	// re-decides. Every key it produces must be one the plan already holds,
	// so the durable append is a no-op rather than duplicate work.
	resumed := newCoverageEngine("job-rank", options, web.CoverageSeedState{
		Queries: []string{"dentist in Alpha IL 60001"}, MaxSequence: 0,
	})
	resumed.zipAreas = rankTestZIPs
	resumed = resumed.withPlanZoom(15)

	second := resumed.recordCompletion(task, checkpoint)

	keysOf := func(decision coverageDecision) []string {
		keys := make([]string, 0, len(decision.refinements)+len(decision.expansions))
		for _, definition := range decision.refinements {
			keys = append(keys, definition.Key)
		}

		for _, definition := range decision.expansions {
			keys = append(keys, definition.Key)
		}

		return keys
	}

	before, after := keysOf(first), keysOf(second)
	if len(before) == 0 || len(before) != len(after) {
		t.Fatalf("decision keys = %v then %v", before, after)
	}

	for index := range before {
		if before[index] != after[index] {
			t.Fatalf("resumed key %d = %q, want the deterministic %q", index, after[index], before[index])
		}
	}
}
