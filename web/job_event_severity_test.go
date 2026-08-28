package web

import (
	"slices"
	"testing"
)

// TestHonestJobEventSeverityCensusOfTheAcceptanceRun is the regression for
// issue Q. Job 7100e95b completed all 180 of its searches, failed none, lost
// nothing and was never blocked, and still reported 118 warnings: 117
// "cell-empty" events (one per search whose area held no matching businesses)
// and one "capacity-capped" event (the host forced the worker budget down).
//
// After the policy, the same census reads 117 information and 1 warning. The
// capacity cap stays a warning because it is real degradation an operator can
// act on; the empty cells are facts about the areas, not faults.
func TestHonestJobEventSeverityCensusOfTheAcceptanceRun(t *testing.T) {
	census := []struct {
		eventType string
		recorded  string
		count     int
	}{
		{"cell-empty", "warning", 117},
		{"capacity-capped", "warning", 1},
		{"task-checkpoint", "information", 180},
		{"task-started", "information", 180},
		{"control", "information", 1},
		{"created", "information", 1},
		{"incremental-summary", "information", 1},
		{"outcome", "information", 1},
		{"result-import", "information", 1},
		{"state", "information", 1},
		{"task-pool", "information", 1},
	}

	before := map[string]int{}
	after := map[string]int{}
	for _, entry := range census {
		before[entry.recorded] += entry.count
		after[HonestJobEventSeverity(entry.eventType, entry.recorded)] += entry.count
	}

	if before[JobEventSeverityWarning] != 118 || before[JobEventSeverityError] != 0 {
		t.Fatalf("fixture does not reproduce the run: %d warnings, %d errors",
			before[JobEventSeverityWarning], before[JobEventSeverityError])
	}

	if after[JobEventSeverityWarning] != 1 {
		t.Fatalf("warnings after the policy = %d, want 1", after[JobEventSeverityWarning])
	}
	if after[JobEventSeverityError] != 0 {
		t.Fatalf("errors after the policy = %d, want 0", after[JobEventSeverityError])
	}
	if after[JobEventSeverityInformation] != 484 {
		t.Fatalf("information after the policy = %d, want 484", after[JobEventSeverityInformation])
	}
	if before[JobEventSeverityInformation]+before[JobEventSeverityWarning] !=
		after[JobEventSeverityInformation]+after[JobEventSeverityWarning] {
		t.Fatal("reclassification lost or invented events")
	}
}

func TestHonestJobEventSeverityKeepsRealProblems(t *testing.T) {
	cases := []struct {
		eventType string
		recorded  string
		want      string
	}{
		// Demoted: nothing to act on.
		{"cell-empty", "warning", JobEventSeverityInformation},
		{"coverage-saturated", "warning", JobEventSeverityInformation},
		// Kept: degradation an operator can act on.
		{"capacity-capped", "warning", JobEventSeverityWarning},
		{"task-truncated", "information", JobEventSeverityWarning},
		{"proxy-failure", "warning", JobEventSeverityWarning},
		// Promoted or kept: a failed task or data at risk.
		{"task-commit-failed", "warning", JobEventSeverityError},
		{"task-merge-failed", "warning", JobEventSeverityError},
		{"low-disk", "warning", JobEventSeverityError},
		{"task-failed", "error", JobEventSeverityError},
		// Unknown types keep whatever the emitter recorded.
		{"something-new", "warning", JobEventSeverityWarning},
		{"something-new", "", JobEventSeverityInformation},
	}

	for _, testCase := range cases {
		got := HonestJobEventSeverity(testCase.eventType, testCase.recorded)
		if got != testCase.want {
			t.Errorf("HonestJobEventSeverity(%q, %q) = %q, want %q",
				testCase.eventType, testCase.recorded, got, testCase.want)
		}
	}
}

func TestInformationalJobEventTypesIsStableAndComplete(t *testing.T) {
	types := InformationalJobEventTypes()
	if !slices.IsSorted(types) {
		t.Fatalf("InformationalJobEventTypes is unsorted: %v", types)
	}
	if !slices.Contains(types, "cell-empty") {
		t.Fatalf("cell-empty missing from %v; it is 117 of the 118 warnings this issue is about", types)
	}
	for _, eventType := range types {
		if HonestJobEventSeverity(eventType, "warning") != JobEventSeverityInformation {
			t.Fatalf("%q is listed as informational but does not classify as information", eventType)
		}
	}
}
