package web

import (
	"sort"
	"strings"
)

// Coverage confidence ratings. Every covered cell is rated exactly one of
// these, and the job-level rollup counts them. The ratings are ordered by
// how much attention they deserve: an unexplored neighbourhood is a missed
// opportunity, a truncated cell is a known blind spot, and a cell with no
// usable evidence is neither.
const (
	// CoverageConfidenceComplete means the cell was swept without hitting
	// the result cap and its own evidence gives no reason to look again.
	CoverageConfidenceComplete = "complete"
	// CoverageConfidenceLikelyTruncated means at least one query in the
	// cell returned as many listings as its depth can ever render, so real
	// businesses were very probably never shown.
	CoverageConfidenceLikelyTruncated = "likely-truncated"
	// CoverageConfidenceLowConfidence means the cell has no clean evidence
	// to judge: every attempt failed, the plan was stopped before it ran,
	// or it never ran at all.
	CoverageConfidenceLowConfidence = "low-confidence"
	// CoverageConfidenceUnexploredAdjacent means the cell was productive
	// enough to justify looking at its neighbours, but no neighbour task
	// was ever appended from it.
	CoverageConfidenceUnexploredAdjacent = "unexplored-adjacent"
)

// Machine-readable reason codes. Each one names the single piece of stored
// evidence that decided the rating, so a UI can explain the verdict without
// re-deriving it and an operator can act on it.
const (
	// CoverageReasonSweptClean is a completed cell below its result cap.
	CoverageReasonSweptClean = "swept-clean"
	// CoverageReasonMostlyRefound is a completed cell whose observations
	// were overwhelmingly businesses the workspace already held.
	CoverageReasonMostlyRefound = "mostly-refound"
	// CoverageReasonTruncatedUnrefined is a capped cell that never earned a
	// second, tighter look.
	CoverageReasonTruncatedUnrefined = "truncated-unrefined"
	// CoverageReasonTruncatedAfterRefinement is a capped cell that was
	// refined and hit the cap again, so it is still hiding listings.
	CoverageReasonTruncatedAfterRefinement = "truncated-after-refinement"
	// CoverageReasonRefinedAndCleared is a capped cell whose refinement
	// came back below the cap: the blind spot was closed.
	CoverageReasonRefinedAndCleared = "refined-and-cleared"
	// CoverageReasonNoSuccessfulAttempt is a cell whose every attempt
	// failed, which says nothing about the businesses in it.
	CoverageReasonNoSuccessfulAttempt = "no-successful-attempt"
	// CoverageReasonPlanStopped is a cell that was skipped when the
	// adaptive engine stopped the remaining plan.
	CoverageReasonPlanStopped = "plan-stopped-before-cell"
	// CoverageReasonNotAttempted is a cell still waiting to run.
	CoverageReasonNotAttempted = "not-attempted"
	// CoverageReasonRetriedAttempts is a cell that eventually completed but
	// only after failed attempts, so part of its ground may be thin.
	CoverageReasonRetriedAttempts = "retried-attempts"
	// CoverageReasonNeighboursRedundant is a productive cell with budget
	// still available whose neighbours were all judged to overlap ground
	// the plan already covers (or whose ZIP is outside the dataset).
	CoverageReasonNeighboursRedundant = "neighbours-redundant"
	// CoverageReasonExpansionBudgetExhausted is a productive cell that came
	// too late: the job's whole expansion budget was already spent.
	CoverageReasonExpansionBudgetExhausted = "expansion-budget-exhausted"
	// CoverageReasonExpansionDisabled is a productive cell in a job that
	// never allowed mid-run expansion at all.
	CoverageReasonExpansionDisabled = "expansion-disabled"
)

// coverageRefoundConfidenceCeiling is the net-new share below which a
// completed, uncapped cell is reported as having mostly re-found businesses
// the workspace already held. It is a reporting threshold only: it never
// stops a plan and never spends budget.
const coverageRefoundConfidenceCeiling = 0.10

// CoverageCellConfidence is the confidence assessment of one covered cell —
// a ZIP when the plan is GBP-shaped, otherwise the query itself. Every field
// is derived from durable task rows; nothing here triggers new scraping.
type CoverageCellConfidence struct {
	// Cell is the stable identity of the cell: its ZIP when the queries are
	// GBP-shaped ("<synonym> in <city> <ST> <zip5>"), otherwise the query.
	Cell string `json:"cell"`
	// ZIP is the cell's ZIP, empty for a non-GBP query.
	ZIP string `json:"zip"`
	// Rating is one of the four CoverageConfidence* values.
	Rating string `json:"rating"`
	// Reason is the machine-readable CoverageReason* code behind Rating.
	Reason string `json:"reason"`
	// Queries lists the distinct plan queries that cover this cell.
	Queries []string `json:"queries"`
	Tasks   int      `json:"tasks"`
	// Completed, Failed, Skipped and Pending partition Tasks by state.
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Skipped   int `json:"skipped"`
	Pending   int `json:"pending"`
	// Attempts is the total number of attempts spent on the cell, so an
	// operator can see the cost behind a low-confidence verdict.
	Attempts          int   `json:"attempts"`
	RowsAdded         int64 `json:"rows_added"`
	RowsReplaced      int64 `json:"rows_replaced"`
	DuplicatesSkipped int64 `json:"duplicates_skipped"`
	// NetNew is rows added minus the stored rows they superseded: the
	// businesses this cell genuinely contributed.
	NetNew int64 `json:"net_new"`
	// NetNewRatio is NetNew / (NetNew + re-found + duplicates), the same
	// measure the saturation window judges. It is 1 when the cell observed
	// nothing at all, so "no evidence" never reads as saturation.
	NetNewRatio float64 `json:"net_new_ratio"`
	// Truncated reports that at least one task in the cell reached its
	// depth's result cap.
	Truncated bool `json:"truncated"`
	// TruncatedAfterRefinement reports that a refinement of this cell hit
	// the cap as well.
	TruncatedAfterRefinement bool `json:"truncated_after_refinement"`
	// Refined reports that the engine appended at least one tighter-zoom
	// re-cover of this cell.
	Refined bool `json:"refined"`
	// RefinementTasks and ExpansionTasks count the appended tasks this cell
	// is the parent of.
	RefinementTasks int `json:"refinement_tasks"`
	ExpansionTasks  int `json:"expansion_tasks"`
	// Expanded reports that at least one neighbour task was appended from
	// this cell.
	Expanded bool `json:"expanded"`
	// FromExpansion reports that the cell entered the plan as a neighbour
	// expansion rather than as an operator query.
	FromExpansion bool `json:"from_expansion"`
}

// CoverageConfidenceRollup is the job-level summary of the per-cell ratings
// plus the expansion budget the ratings were judged against.
type CoverageConfidenceRollup struct {
	Cells              int `json:"cells"`
	Complete           int `json:"complete"`
	LikelyTruncated    int `json:"likely_truncated"`
	LowConfidence      int `json:"low_confidence"`
	UnexploredAdjacent int `json:"unexplored_adjacent"`
	TruncatedCells     int `json:"truncated_cells"`
	RefinedCells       int `json:"refined_cells"`
	ExpandedCells      int `json:"expanded_cells"`
	// ExpansionBudget is the job's configured MaxExpansions, ExpansionsUsed
	// how many appended tasks the plan already holds, and
	// ExpansionBudgetLeft the difference (never negative).
	ExpansionBudget     int `json:"expansion_budget"`
	ExpansionsUsed      int `json:"expansions_used"`
	ExpansionBudgetLeft int `json:"expansion_budget_left"`
}

// CoverageConfidence is the additive per-job coverage-confidence assessment
// carried by CoverageReport. It is a pure derivation from the durable task
// plan: no field here causes any scraping.
type CoverageConfidence struct {
	Rollup CoverageConfidenceRollup `json:"rollup"`
	Cells  []CoverageCellConfidence `json:"cells"`
}

// coverageCellAccumulator gathers the durable evidence of one cell before it
// is rated.
type coverageCellAccumulator struct {
	cell       string
	zip        string
	order      int
	queries    []string
	querySeen  map[string]struct{}
	confidence CoverageCellConfidence
}

// buildCoverageConfidence rates every covered cell of a plan from the task
// rows alone. Cells are keyed by ZIP for GBP-shaped queries so all synonyms
// of one neighbourhood are judged together, and by query otherwise.
//
// The rating rules run in a fixed order, most actionable first, so the same
// evidence always produces the same verdict:
//
//  1. no clean evidence (every attempt failed, the plan was stopped before
//     the cell ran, or it never ran) -> low-confidence;
//  2. a query in the cell reached its result cap -> likely-truncated, with
//     the reason distinguishing "never refined", "refined and capped again",
//     and "refined and cleared" (which downgrades to complete);
//  3. the cell was productive enough to justify neighbours but none were
//     appended -> unexplored-adjacent, with the reason naming why;
//  4. otherwise -> complete.
func buildCoverageConfidence(
	options *CoverageOptions,
	rows []CoverageTaskRow,
	saturation CoverageSaturation,
) CoverageConfidence {
	cells := collectCoverageCells(rows)

	confidence := CoverageConfidence{Cells: make([]CoverageCellConfidence, 0, len(cells))}
	confidence.Rollup.ExpansionBudget = options.maxExpansions()

	ordered := make([]*coverageCellAccumulator, 0, len(cells))
	for _, accumulator := range cells {
		ordered = append(ordered, accumulator)
	}

	sort.Slice(ordered, func(a, b int) bool {
		if ordered[a].order != ordered[b].order {
			return ordered[a].order < ordered[b].order
		}

		return ordered[a].cell < ordered[b].cell
	})

	for _, accumulator := range ordered {
		confidence.Rollup.ExpansionsUsed += accumulator.confidence.ExpansionTasks +
			accumulator.confidence.RefinementTasks
	}

	confidence.Rollup.ExpansionBudgetLeft = max(
		0, confidence.Rollup.ExpansionBudget-confidence.Rollup.ExpansionsUsed,
	)

	budgetLeft := confidence.Rollup.ExpansionBudgetLeft

	for _, accumulator := range ordered {
		// A parent ZIP referenced only by an appended task's origin has no
		// plan row of its own and is not a covered cell.
		if accumulator.confidence.Tasks == 0 {
			continue
		}

		cell := accumulator.confidence
		cell.Queries = accumulator.queries
		cell.NetNew = coverageNetNew(cell.RowsAdded, cell.RowsReplaced)
		cell.NetNewRatio = coverageNetNewRatio(cell.NetNew, cell.RowsReplaced, cell.DuplicatesSkipped)
		cell.Refined = cell.RefinementTasks > 0
		cell.Expanded = cell.ExpansionTasks > 0
		cell.Rating, cell.Reason = rateCoverageCell(options, cell, saturation, budgetLeft)

		switch cell.Rating {
		case CoverageConfidenceLikelyTruncated:
			confidence.Rollup.LikelyTruncated++
		case CoverageConfidenceLowConfidence:
			confidence.Rollup.LowConfidence++
		case CoverageConfidenceUnexploredAdjacent:
			confidence.Rollup.UnexploredAdjacent++
		default:
			confidence.Rollup.Complete++
		}

		if cell.Truncated {
			confidence.Rollup.TruncatedCells++
		}

		if cell.Refined {
			confidence.Rollup.RefinedCells++
		}

		if cell.Expanded {
			confidence.Rollup.ExpandedCells++
		}

		confidence.Rollup.Cells++
		confidence.Cells = append(confidence.Cells, cell)
	}

	return confidence
}

// collectCoverageCells folds the plan's task rows into one accumulator per
// cell, attributing appended tasks both to the cell they run in and to the
// parent cell that earned them.
func collectCoverageCells(rows []CoverageTaskRow) map[string]*coverageCellAccumulator {
	cells := make(map[string]*coverageCellAccumulator, len(rows))

	ensure := func(key, zip string, order int) *coverageCellAccumulator {
		accumulator, found := cells[key]
		if found {
			return accumulator
		}

		accumulator = &coverageCellAccumulator{
			cell:       key,
			zip:        zip,
			order:      order,
			queries:    make([]string, 0, 2),
			querySeen:  make(map[string]struct{}, 2),
			confidence: CoverageCellConfidence{Cell: key, ZIP: zip},
		}
		cells[key] = accumulator

		return accumulator
	}

	for index, row := range rows {
		zip, gbp := ParseGBPQueryZIP(row.Query)

		key := strings.TrimSpace(row.Query)
		if gbp {
			key = zip
		}

		if key == "" {
			key = row.TaskKey
		}

		accumulator := ensure(key, zip, index)

		if query := strings.TrimSpace(row.Query); query != "" {
			if _, seen := accumulator.querySeen[query]; !seen {
				accumulator.querySeen[query] = struct{}{}
				accumulator.queries = append(accumulator.queries, query)
			}
		}

		accumulator.confidence.Tasks++
		accumulator.confidence.Attempts += row.Attempts
		accumulator.confidence.RowsAdded += row.RowsAdded
		accumulator.confidence.RowsReplaced += row.RowsReplaced
		accumulator.confidence.DuplicatesSkipped += row.DuplicatesSkipped

		switch row.State {
		case "completed":
			accumulator.confidence.Completed++
		case "failed":
			accumulator.confidence.Failed++
		case "skipped":
			accumulator.confidence.Skipped++
		default:
			accumulator.confidence.Pending++
		}

		isRefinement := strings.HasPrefix(row.Origin, CoverageRefinementOriginPrefix)
		isExpansion := strings.HasPrefix(row.Origin, CoverageExpansionOriginPrefix)

		if row.Truncated {
			accumulator.confidence.Truncated = true

			if isRefinement {
				accumulator.confidence.TruncatedAfterRefinement = true
			}
		}

		if isExpansion {
			accumulator.confidence.FromExpansion = true
		}

		// An appended task also counts against the cell that earned it: the
		// origin suffix is that parent's ZIP.
		switch {
		case isRefinement:
			parent := ensure(strings.TrimPrefix(row.Origin, CoverageRefinementOriginPrefix), zip, index)
			parent.confidence.RefinementTasks++

			if row.Truncated {
				parent.confidence.TruncatedAfterRefinement = true
			}
		case isExpansion:
			parentZIP := strings.TrimPrefix(row.Origin, CoverageExpansionOriginPrefix)
			if parentZIP != "" && parentZIP != key {
				ensure(parentZIP, parentZIP, index).confidence.ExpansionTasks++
			}
		}
	}

	return cells
}

// rateCoverageCell applies the documented rule order to one cell's evidence.
func rateCoverageCell(
	options *CoverageOptions,
	cell CoverageCellConfidence,
	saturation CoverageSaturation,
	budgetLeft int,
) (rating, reason string) {
	if cell.Completed == 0 {
		switch {
		case cell.Skipped > 0 && saturation.Stopped:
			return CoverageConfidenceLowConfidence, CoverageReasonPlanStopped
		case cell.Failed > 0:
			return CoverageConfidenceLowConfidence, CoverageReasonNoSuccessfulAttempt
		default:
			return CoverageConfidenceLowConfidence, CoverageReasonNotAttempted
		}
	}

	if cell.Truncated {
		switch {
		case cell.TruncatedAfterRefinement:
			return CoverageConfidenceLikelyTruncated, CoverageReasonTruncatedAfterRefinement
		case !cell.Refined:
			return CoverageConfidenceLikelyTruncated, CoverageReasonTruncatedUnrefined
		}
	}

	if rating, reason, ok := rateCoverageNeighbourhood(options, cell, budgetLeft); ok {
		return rating, reason
	}

	switch {
	case cell.Truncated && cell.Refined:
		return CoverageConfidenceComplete, CoverageReasonRefinedAndCleared
	case cell.Failed > 0:
		return CoverageConfidenceComplete, CoverageReasonRetriedAttempts
	case cell.NetNewRatio < coverageRefoundConfidenceCeiling:
		return CoverageConfidenceComplete, CoverageReasonMostlyRefound
	default:
		return CoverageConfidenceComplete, CoverageReasonSweptClean
	}
}

// rateCoverageNeighbourhood reports whether a cell productive enough to earn
// neighbours never got any, and why. ok is false when the cell either was
// expanded or was never productive enough to deserve it, in which case the
// caller falls through to the completed ratings.
func rateCoverageNeighbourhood(
	options *CoverageOptions,
	cell CoverageCellConfidence,
	budgetLeft int,
) (rating, reason string, ok bool) {
	if cell.Expanded || cell.FromExpansion {
		return "", "", false
	}

	if cell.NetNew < int64(options.ExpansionMinNewOrDefault()) {
		return "", "", false
	}

	switch {
	case options.maxExpansions() <= 0:
		return CoverageConfidenceUnexploredAdjacent, CoverageReasonExpansionDisabled, true
	case budgetLeft <= 0:
		return CoverageConfidenceUnexploredAdjacent, CoverageReasonExpansionBudgetExhausted, true
	default:
		return CoverageConfidenceUnexploredAdjacent, CoverageReasonNeighboursRedundant, true
	}
}

// maxExpansions reads the configured expansion budget; a nil options block
// means the engine never ran, which is a zero budget.
func (c *CoverageOptions) maxExpansions() int {
	if c == nil {
		return 0
	}

	return c.MaxExpansions
}

// coverageNetNew is added rows minus the stored rows they superseded,
// floored at zero because one run row can supersede several stored rows
// through multi-key identity matching.
func coverageNetNew(rowsAdded, rowsReplaced int64) int64 {
	if rowsReplaced >= rowsAdded {
		return 0
	}

	return rowsAdded - rowsReplaced
}

// coverageNetNewRatio is the share of everything a cell observed that was
// genuinely new. With nothing observed it reports 1, so "no evidence" never
// reads as saturation.
func coverageNetNewRatio(netNew, refound, duplicates int64) float64 {
	total := netNew + refound + duplicates
	if total <= 0 {
		return 1
	}

	return float64(netNew) / float64(total)
}
