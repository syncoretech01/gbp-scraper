package webrunner

import (
	"sort"
	"strings"

	"github.com/gosom/google-maps-scraper/web"
)

// The two kinds of work the adaptive engine may append mid-run. Both draw on
// the same MaxExpansions budget, which is why they have to be ranked against
// each other rather than funded in a fixed order.
const (
	coverageWorkRefinement = "refinement"
	coverageWorkExpansion  = "expansion"
)

const (
	// coverageExpansionYieldDiscount is how much of a productive parent
	// cell's own net-new an unproven neighbour is expected to return before
	// any expansion probe of this run has actually completed. A neighbour
	// search overlaps the ground the parent already swept, so half is a
	// deliberately optimistic-but-bounded prior; once real probes finish,
	// their measured mean replaces it entirely.
	coverageExpansionYieldDiscount = 0.5
	// coverageExpansionMinYieldShare is the share of everything a cell
	// observed that must still be genuinely new before its neighbourhood is
	// worth budget. A cell mostly re-finding businesses the workspace
	// already holds has not found new ground, however many rows it wrote,
	// so its neighbours are unlikely to either.
	coverageExpansionMinYieldShare = 0.2
	// coverageMaxRefinementDepth bounds how many times one cell may be
	// re-covered at a progressively tighter zoom. Depth 1 is the historical
	// single second look; depth 2 is granted only when that second look was
	// itself cut off at the cap AND still returned net-new at or above the
	// expansion floor, so a stubbornly capped cell cannot start a chain.
	coverageMaxRefinementDepth = 2
)

// coverageCellYield is the running net-new record of one ZIP cell across
// every task of the plan that covered it.
type coverageCellYield struct {
	netNew     int64
	refound    int64
	duplicates int64
	tasks      int
}

// share is the fraction of everything the cell observed that was genuinely
// new. A cell that observed nothing reports 1, so "no evidence" never reads
// as exhaustion.
func (yield coverageCellYield) share() float64 {
	total := yield.netNew + yield.refound + yield.duplicates
	if total <= 0 {
		return 1
	}

	return float64(yield.netNew) / float64(total)
}

// coverageWorkCandidate is one appendable unit of adaptive work together with
// the expected marginal unique yield it is ranked by. build assigns the plan
// sequence at funding time so an unfunded candidate consumes nothing, and
// onFund performs the engine-side bookkeeping of a funded one.
type coverageWorkCandidate struct {
	kind   string
	score  float64
	build  func(sequence int) web.JobTaskDefinition
	onFund func()
}

// recordYieldLocked folds one finished task into the per-cell net-new record
// and, for engine-generated probes, into the measured expansion yield. It is
// the only writer of both, and callers hold engine.mu.
func (engine *coverageEngine) recordYieldLocked(task web.JobTask, checkpoint web.JobTaskCheckpoint) {
	netNew := coverageNetNewRows(checkpoint)

	if zip, ok := web.ParseGBPQueryZIP(task.Query); ok {
		if engine.cellYield == nil {
			engine.cellYield = make(map[string]coverageCellYield)
		}

		entry := engine.cellYield[zip]
		entry.netNew += netNew
		entry.refound += checkpoint.RowsReplaced
		entry.duplicates += checkpoint.DuplicatesSkipped
		entry.tasks++
		engine.cellYield[zip] = entry
	}

	// Only neighbour probes measure what unexplored ground pays; a
	// refinement re-covers a cell the plan already had.
	if strings.HasPrefix(task.Origin, web.CoverageExpansionOriginPrefix) {
		engine.expansionProbes++
		engine.expansionProbeNetNew += netNew
	}
}

// expectedRefinementYield is the marginal unique yield a tighter second look
// at a truncated cell is expected to return.
//
// The feed stopped at the cap, so at least one further page of listings
// exists behind it. The cell's own observed net-new RATE is the best
// available estimate of how much of that page is new to the workspace, which
// makes the estimate (net-new / observed listings) x one feed page.
func expectedRefinementYield(checkpoint web.JobTaskCheckpoint) float64 {
	observed := coverageTaskYield(checkpoint)
	if observed <= 0 {
		return 0
	}

	rate := float64(coverageNetNewRows(checkpoint)) / float64(observed)

	return rate * gmapsFeedPageSize
}

// expectedExpansionYieldLocked is the marginal unique yield one neighbour
// probe is expected to return. Measured evidence wins as soon as it exists:
// once any expansion probe of this run has completed, the mean net-new those
// probes actually returned replaces the prior. Callers hold engine.mu.
func (engine *coverageEngine) expectedExpansionYieldLocked(parentNetNew int64) float64 {
	if engine.expansionProbes > 0 {
		return float64(engine.expansionProbeNetNew) / float64(engine.expansionProbes)
	}

	return float64(parentNetNew) * coverageExpansionYieldDiscount
}

// expansionWarrantedLocked is the marginal-unique-yield gate on spending
// budget in a cell's neighbourhood. It replaces the old single-task
// "RowsAdded >= ExpansionMinNew" test with two rules over the same floor:
//
//   - the cell's CUMULATIVE net-new across every task that covered it must
//     reach ExpansionMinNew, so a plan that is still finding new businesses
//     one synonym at a time expands even when no single task cleared the
//     floor on its own;
//   - the cell's net-new SHARE must still be at or above
//     coverageExpansionMinYieldShare, so a cell that is mostly re-finding
//     what the workspace already holds does not.
//
// A nil Coverage block never constructs an engine, so a job without one is
// unaffected. Callers hold engine.mu.
func (engine *coverageEngine) expansionWarrantedLocked(zip string) bool {
	yield := engine.cellYield[zip]

	if yield.netNew < int64(engine.options.ExpansionMinNewOrDefault()) {
		return false
	}

	return yield.share() >= coverageExpansionMinYieldShare
}

// planAppendedWorkLocked ranks the refinement and the neighbour expansions
// one finished task earned, then funds them from the shared MaxExpansions
// budget in descending expected marginal unique yield.
//
// # Ranking rule
//
// Every candidate is scored by the number of genuinely new businesses it is
// expected to return, estimated from net-new the run has actually recorded:
//
//   - A REFINEMENT re-covers a cell whose feed was cut off at the depth cap.
//     Its score is the cell's observed net-new rate applied to the one
//     further feed page a tighter zoom pulls into view — see
//     expectedRefinementYield.
//   - An EXPANSION probes a neighbour cell nobody has queried. Its score is
//     the mean net-new the run's completed expansion probes returned, or,
//     before any has completed, the parent's own net-new discounted by
//     coverageExpansionYieldDiscount — see expectedExpansionYieldLocked.
//
// Candidates are funded highest score first. A tie goes to the refinement: a
// truncated cell is KNOWN to be hiding listings, while a neighbour is only a
// guess. Within the expansion batch the nearest-first order from neighbour
// selection is preserved, so the same evidence always produces the same
// plan. Nothing is built until it is funded, every funded task carries a
// deterministic task key, and appending an existing key is a durable no-op,
// so a restart re-decides without ever re-enqueuing identical work.
//
// Callers hold engine.mu.
func (engine *coverageEngine) planAppendedWorkLocked(
	task web.JobTask,
	checkpoint web.JobTaskCheckpoint,
	decision *coverageDecision,
) {
	if engine.options.MaxExpansions <= 0 || engine.expansionsAdded >= engine.options.MaxExpansions {
		return
	}

	candidates := make([]coverageWorkCandidate, 0, 1+coverageExpansionBatch)

	if refinement, ok := engine.refinementCandidateLocked(task, checkpoint); ok {
		candidates = append(candidates, refinement)
	}

	candidates = append(candidates, engine.expansionCandidatesLocked(task, checkpoint, decision)...)

	sort.SliceStable(candidates, func(a, b int) bool {
		if candidates[a].score != candidates[b].score {
			return candidates[a].score > candidates[b].score
		}

		return candidates[a].kind == coverageWorkRefinement && candidates[b].kind != coverageWorkRefinement
	})

	for _, candidate := range candidates {
		if engine.expansionsAdded >= engine.options.MaxExpansions {
			break
		}

		definition := candidate.build(engine.nextSequence)

		engine.nextSequence++
		engine.expansionsAdded++

		if candidate.onFund != nil {
			candidate.onFund()
		}

		if candidate.kind == coverageWorkRefinement {
			decision.refinements = append(decision.refinements, definition)

			continue
		}

		decision.expansions = append(decision.expansions, definition)
	}
}
