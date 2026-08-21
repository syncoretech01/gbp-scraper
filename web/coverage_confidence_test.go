package web

import (
	"encoding/json"
	"testing"
	"time"
)

func confidenceCell(t *testing.T, confidence CoverageConfidence, cell string) CoverageCellConfidence {
	t.Helper()

	for _, candidate := range confidence.Cells {
		if candidate.Cell == cell {
			return candidate
		}
	}

	t.Fatalf("cell %q is missing from %#v", cell, confidence.Cells)

	return CoverageCellConfidence{}
}

func TestCoverageConfidenceRatesEveryCellFromStoredEvidence(t *testing.T) {
	t.Parallel()

	options := &CoverageOptions{AutoStop: true, MaxExpansions: 6, ExpansionMinNew: 10}
	finished := time.Unix(1_700_000_000, 0).UTC()

	rows := []CoverageTaskRow{
		// A clean, uncapped sweep below the expansion floor.
		{
			TaskKey: "plan-1", Query: "dentist in Springfield IL 62701", State: "completed",
			Attempts: 1, RowsAdded: 6, FinishedAt: &finished,
		},
		// Capped and never refined.
		{
			TaskKey: "plan-2", Query: "dentist in Chicago IL 60601", State: "completed",
			Attempts: 1, RowsAdded: 120, Truncated: true, FinishedAt: &finished,
		},
		// Capped, refined, and the refinement came back below the cap.
		{
			TaskKey: "plan-3", Query: "dentist in Aurora IL 60505", State: "completed",
			Attempts: 1, RowsAdded: 5, Truncated: true, FinishedAt: &finished,
		},
		{
			TaskKey: "ref-3", Query: "dentist in Aurora IL 60505", Origin: CoverageRefinementOriginPrefix + "60505",
			State: "completed", Attempts: 1, RowsAdded: 2, FinishedAt: &finished,
		},
		// Every attempt failed.
		{
			TaskKey: "plan-4", Query: "dentist in Peoria IL 61602", State: "failed",
			Attempts: 3, LastError: "browser crashed",
		},
		// Productive but never expanded and never refined.
		{
			TaskKey: "plan-5", Query: "dentist in Rockford IL 61101", State: "completed",
			Attempts: 1, RowsAdded: 40, FinishedAt: &finished,
		},
	}

	report := buildCoverageReport(options, rows)
	confidence := report.Confidence

	if confidence.Rollup.Cells != 5 {
		t.Fatalf("rollup cells = %d, want 5 (%#v)", confidence.Rollup.Cells, confidence.Cells)
	}

	clean := confidenceCell(t, confidence, "62701")
	if clean.Rating != CoverageConfidenceComplete || clean.Reason != CoverageReasonSweptClean {
		t.Fatalf("clean cell = %q/%q, want complete/swept-clean", clean.Rating, clean.Reason)
	}

	if clean.NetNew != 6 || clean.NetNewRatio != 1 {
		t.Fatalf("clean cell net-new = %d ratio = %f, want 6 and 1", clean.NetNew, clean.NetNewRatio)
	}

	capped := confidenceCell(t, confidence, "60601")
	if capped.Rating != CoverageConfidenceLikelyTruncated ||
		capped.Reason != CoverageReasonTruncatedUnrefined {
		t.Fatalf("capped cell = %q/%q, want likely-truncated/truncated-unrefined", capped.Rating, capped.Reason)
	}

	refined := confidenceCell(t, confidence, "60505")
	if refined.Rating != CoverageConfidenceComplete || refined.Reason != CoverageReasonRefinedAndCleared {
		t.Fatalf("refined cell = %q/%q, want complete/refined-and-cleared", refined.Rating, refined.Reason)
	}

	if !refined.Refined || refined.RefinementTasks != 1 {
		t.Fatalf("refined cell refinement evidence = %#v", refined)
	}

	failed := confidenceCell(t, confidence, "61602")
	if failed.Rating != CoverageConfidenceLowConfidence ||
		failed.Reason != CoverageReasonNoSuccessfulAttempt {
		t.Fatalf("failed cell = %q/%q, want low-confidence/no-successful-attempt", failed.Rating, failed.Reason)
	}

	if failed.Attempts != 3 {
		t.Fatalf("failed cell attempts = %d, want 3", failed.Attempts)
	}

	unexplored := confidenceCell(t, confidence, "61101")
	if unexplored.Rating != CoverageConfidenceUnexploredAdjacent ||
		unexplored.Reason != CoverageReasonNeighboursRedundant {
		t.Fatalf("unexplored cell = %q/%q, want unexplored-adjacent/neighbours-redundant",
			unexplored.Rating, unexplored.Reason)
	}

	rollup := confidence.Rollup
	if rollup.Complete != 2 || rollup.LikelyTruncated != 1 ||
		rollup.LowConfidence != 1 || rollup.UnexploredAdjacent != 1 {
		t.Fatalf("rollup = %#v", rollup)
	}

	if rollup.ExpansionBudget != 6 || rollup.ExpansionsUsed != 1 || rollup.ExpansionBudgetLeft != 5 {
		t.Fatalf("rollup budget = %#v", rollup)
	}

	if rollup.TruncatedCells != 2 || rollup.RefinedCells != 1 || rollup.ExpandedCells != 0 {
		t.Fatalf("rollup counters = %#v", rollup)
	}
}

func TestCoverageConfidenceReportsARefinementThatWasCappedAgain(t *testing.T) {
	t.Parallel()

	finished := time.Unix(1_700_000_100, 0).UTC()
	options := &CoverageOptions{AutoStop: true, MaxExpansions: 4}

	report := buildCoverageReport(options, []CoverageTaskRow{
		{
			TaskKey: "plan-1", Query: "roofer in Houston TX 77002", State: "completed",
			Attempts: 1, RowsAdded: 120, Truncated: true, FinishedAt: &finished,
		},
		{
			TaskKey: "ref-1", Query: "roofer in Houston TX 77002",
			Origin: CoverageRefinementOriginPrefix + "77002", State: "completed",
			Attempts: 1, RowsAdded: 120, Truncated: true, FinishedAt: &finished,
		},
	})

	cell := confidenceCell(t, report.Confidence, "77002")
	if cell.Rating != CoverageConfidenceLikelyTruncated ||
		cell.Reason != CoverageReasonTruncatedAfterRefinement {
		t.Fatalf("cell = %q/%q, want likely-truncated/truncated-after-refinement", cell.Rating, cell.Reason)
	}

	if !cell.TruncatedAfterRefinement {
		t.Fatalf("cell = %#v, want the after-refinement truncation flag", cell)
	}
}

func TestCoverageConfidenceExplainsWhyNeighboursWereNeverExplored(t *testing.T) {
	t.Parallel()

	finished := time.Unix(1_700_000_200, 0).UTC()
	productive := []CoverageTaskRow{{
		TaskKey: "plan-1", Query: "plumber in Austin TX 78701", State: "completed",
		Attempts: 1, RowsAdded: 60, FinishedAt: &finished,
	}}

	disabled := buildCoverageReport(&CoverageOptions{AutoStop: true}, productive)
	if cell := confidenceCell(t, disabled.Confidence, "78701"); cell.Reason != CoverageReasonExpansionDisabled {
		t.Fatalf("disabled reason = %q, want expansion-disabled", cell.Reason)
	}

	// One expansion already consumed the whole budget elsewhere.
	exhausted := buildCoverageReport(&CoverageOptions{AutoStop: true, MaxExpansions: 1}, append(
		append([]CoverageTaskRow{}, productive...),
		CoverageTaskRow{
			TaskKey: "exp-1", Query: "plumber in Round Rock TX 78664",
			Origin: CoverageExpansionOriginPrefix + "78681", State: "completed",
			Attempts: 1, RowsAdded: 4, FinishedAt: &finished,
		},
	))

	if cell := confidenceCell(t, exhausted.Confidence, "78701"); cell.Reason != CoverageReasonExpansionBudgetExhausted {
		t.Fatalf("exhausted reason = %q, want expansion-budget-exhausted", cell.Reason)
	}

	// A cell that DID expand is never reported as unexplored.
	expanded := buildCoverageReport(&CoverageOptions{AutoStop: true, MaxExpansions: 5}, append(
		append([]CoverageTaskRow{}, productive...),
		CoverageTaskRow{
			TaskKey: "exp-2", Query: "plumber in Round Rock TX 78664",
			Origin: CoverageExpansionOriginPrefix + "78701", State: "completed",
			Attempts: 1, RowsAdded: 9, FinishedAt: &finished,
		},
	))

	cell := confidenceCell(t, expanded.Confidence, "78701")
	if cell.Rating != CoverageConfidenceComplete || !cell.Expanded || cell.ExpansionTasks != 1 {
		t.Fatalf("expanded parent = %#v, want a complete, expanded cell", cell)
	}

	// The neighbour it created is itself never rated unexplored-adjacent,
	// because a probe's own neighbourhood is not the operator's plan.
	neighbour := confidenceCell(t, expanded.Confidence, "78664")
	if !neighbour.FromExpansion || neighbour.Rating == CoverageConfidenceUnexploredAdjacent {
		t.Fatalf("expansion probe = %#v", neighbour)
	}
}

func TestCoverageConfidenceMarksSkippedCellsWhenThePlanWasStopped(t *testing.T) {
	t.Parallel()

	finished := time.Unix(1_700_000_300, 0).UTC()
	report := buildCoverageReport(&CoverageOptions{AutoStop: true, MaxExpansions: 3}, []CoverageTaskRow{
		{
			TaskKey: "plan-1", Query: "vet in Reno NV 89501", State: "completed",
			Attempts: 1, RowsAdded: 2, FinishedAt: &finished,
		},
		{
			TaskKey: "plan-2", Query: "vet in Sparks NV 89431", State: "skipped",
			LastError: CoverageSkipReason,
		},
	})

	if !report.Saturation.Stopped {
		t.Fatalf("saturation = %#v, want a stopped plan", report.Saturation)
	}

	cell := confidenceCell(t, report.Confidence, "89431")
	if cell.Rating != CoverageConfidenceLowConfidence || cell.Reason != CoverageReasonPlanStopped {
		t.Fatalf("skipped cell = %q/%q, want low-confidence/plan-stopped-before-cell", cell.Rating, cell.Reason)
	}
}

func TestCoverageConfidenceReportsMostlyRefoundCells(t *testing.T) {
	t.Parallel()

	finished := time.Unix(1_700_000_400, 0).UTC()
	report := buildCoverageReport(&CoverageOptions{AutoStop: true, MaxExpansions: 3}, []CoverageTaskRow{{
		TaskKey: "plan-1", Query: "florist in Tampa FL 33602", State: "completed",
		Attempts: 1, RowsAdded: 20, RowsReplaced: 19, FinishedAt: &finished,
	}})

	cell := confidenceCell(t, report.Confidence, "33602")
	if cell.Rating != CoverageConfidenceComplete || cell.Reason != CoverageReasonMostlyRefound {
		t.Fatalf("re-found cell = %q/%q, want complete/mostly-refound", cell.Rating, cell.Reason)
	}

	if cell.NetNew != 1 {
		t.Fatalf("re-found cell net-new = %d, want 1", cell.NetNew)
	}
}

func TestCoverageConfidenceGroupsNonGBPQueriesByQuery(t *testing.T) {
	t.Parallel()

	finished := time.Unix(1_700_000_500, 0).UTC()
	report := buildCoverageReport(nil, []CoverageTaskRow{{
		TaskKey: "plan-1", Query: "coffee shop", State: "completed",
		Attempts: 1, RowsAdded: 5, FinishedAt: &finished,
	}})

	cell := confidenceCell(t, report.Confidence, "coffee shop")
	if cell.ZIP != "" || cell.Rating != CoverageConfidenceComplete {
		t.Fatalf("non-GBP cell = %#v", cell)
	}
}

func TestCoverageReportKeepsItsHistoricalKeys(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(buildCoverageReport(nil, nil))
	if err != nil {
		t.Fatalf("marshal coverage report: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode coverage report: %v", err)
	}

	for _, key := range []string{"totals", "saturation", "by_query", "trend", "confidence"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("coverage report is missing the %q key: %s", key, encoded)
		}
	}
}
