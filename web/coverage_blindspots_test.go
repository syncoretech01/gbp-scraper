package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCoverageReportSurfacesTruncationAndRefinementsAdditively(t *testing.T) {
	t.Parallel()

	started := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	finished := started.Add(time.Minute)

	rows := []CoverageTaskRow{
		{
			TaskKey: "t-1", Query: "dentist in Springfield IL 62701", State: "completed",
			Attempts: 1, Sequence: 0, RowsAdded: 120, Truncated: true,
			StartedAt: &started, FinishedAt: &finished,
		},
		{
			TaskKey: "t-2", Query: "dentist in Springfield IL 62701", State: "completed",
			Attempts: 1, Sequence: 1, RowsAdded: 40,
			Origin:    CoverageRefinementOriginPrefix + "62701",
			StartedAt: &started, FinishedAt: &finished,
		},
		{
			TaskKey: "t-3", Query: "dentist in Chatham IL 62629", State: "completed",
			Attempts: 1, Sequence: 2, RowsAdded: 5, RowsReplaced: 5,
			Origin:    CoverageExpansionOriginPrefix + "62701",
			StartedAt: &started, FinishedAt: &finished,
		},
	}

	report := buildCoverageReport(&CoverageOptions{MaxExpansions: 4}, rows)

	if report.Totals.TasksTruncated != 1 {
		t.Fatalf("truncated tasks = %d, want 1", report.Totals.TasksTruncated)
	}

	// A refinement must never be counted as a neighbour expansion: the two
	// are measured separately.
	if report.Totals.RefinementsAdded != 1 || report.Totals.ExpansionsAdded != 1 {
		t.Fatalf("totals = %#v, want one refinement and one expansion", report.Totals)
	}

	if !report.ByQuery[0].Truncated || report.ByQuery[1].Truncated || report.ByQuery[2].Truncated {
		t.Fatalf("by_query truncation flags = %v, %v, %v; want only the first",
			report.ByQuery[0].Truncated, report.ByQuery[1].Truncated, report.ByQuery[2].Truncated)
	}

	// The refinement re-covers its parent's own cell, so it carries the
	// same ZIP rather than a neighbour's.
	if report.ByQuery[1].ZIP != "62701" || report.ByQuery[2].ZIP != "62629" {
		t.Fatalf("by_query ZIPs = %q and %q", report.ByQuery[1].ZIP, report.ByQuery[2].ZIP)
	}

	// Overlap is only visible per query through rows_replaced: the
	// neighbour expansion re-found every business it returned.
	if report.ByQuery[2].RowsReplaced != 5 || report.ByQuery[2].DuplicatesSkipped != 0 {
		t.Fatalf("neighbour row = %#v, want the overlap under rows_replaced", report.ByQuery[2])
	}

	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}

	// The UI contract is additive: every historical key survives and the
	// new ones sit beside them.
	for _, key := range []string{
		`"totals"`, `"tasks_total"`, `"tasks_done"`, `"tasks_failed"`, `"tasks_skipped"`,
		`"rows_added"`, `"rows_replaced"`, `"duplicates_skipped"`, `"expansions_added"`,
		`"refinements_added"`, `"tasks_truncated"`,
		`"saturation"`, `"by_query"`, `"task_key"`, `"query"`, `"zip"`, `"origin"`, `"state"`,
		`"attempts"`, `"seconds"`, `"truncated"`, `"trend"`, `"seq"`, `"finished_at"`,
		`"rows_replaced"`,
	} {
		if !strings.Contains(string(payload), key) {
			t.Errorf("serialized report lacks %s: %s", key, payload)
		}
	}
}

func TestCoverageReportWithoutTruncationEvidenceReportsZero(t *testing.T) {
	t.Parallel()

	// Checkpoints written by a job without a coverage block carry no
	// truncation signal, and the report must not invent one.
	rows := []CoverageTaskRow{
		{TaskKey: "t-1", Query: "coffee shop", State: "completed", RowsAdded: 9000},
		{TaskKey: "t-2", Query: "coffee shop", State: "completed", RowsAdded: 9000},
	}

	report := buildCoverageReport(nil, rows)

	if report.Totals.TasksTruncated != 0 || report.Totals.RefinementsAdded != 0 {
		t.Fatalf("totals = %#v, want no truncation or refinement evidence", report.Totals)
	}

	for _, row := range report.ByQuery {
		if row.Truncated {
			t.Fatalf("row %q reported truncation without evidence", row.TaskKey)
		}
	}
}

func TestJobTaskCheckpointTruncationFieldsAreAdditive(t *testing.T) {
	t.Parallel()

	// A checkpoint written before the signal existed decodes to the zero
	// values, so an older durable payload keeps its exact meaning.
	var legacy JobTaskCheckpoint
	if err := json.Unmarshal(
		[]byte(`{"state":"completed","rows_added":7,"duplicates_skipped":2}`), &legacy,
	); err != nil {
		t.Fatalf("decode legacy checkpoint: %v", err)
	}

	if legacy.Truncated || legacy.TruncationCap != 0 || legacy.RowsAdded != 7 {
		t.Fatalf("legacy checkpoint = %#v", legacy)
	}

	// An unset signal stays out of the payload entirely.
	payload, err := json.Marshal(JobTaskCheckpoint{State: "completed", RowsAdded: 7})
	if err != nil {
		t.Fatalf("encode checkpoint: %v", err)
	}

	if strings.Contains(string(payload), "truncated") || strings.Contains(string(payload), "truncation_cap") {
		t.Fatalf("checkpoint payload = %s, want the unset signal omitted", payload)
	}

	payload, err = json.Marshal(JobTaskCheckpoint{State: "completed", RowsAdded: 20, Truncated: true, TruncationCap: 20})
	if err != nil {
		t.Fatalf("encode truncated checkpoint: %v", err)
	}

	if !strings.Contains(string(payload), `"truncated":true`) ||
		!strings.Contains(string(payload), `"truncation_cap":20`) {
		t.Fatalf("checkpoint payload = %s, want the signal recorded", payload)
	}
}
