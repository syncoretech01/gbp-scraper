package acceptance

import (
	"context"
	"encoding/json"
	"testing"
)

func TestEventsParsesSSEStream(t *testing.T) {
	fake := newFakeServer(t, defaultScenario())
	client := newClientFor(t, fake)

	events, err := client.Events(context.Background(), defaultScenario().jobID)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != len(defaultScenario().events) {
		t.Fatalf("got %d events, want %d", len(events), len(defaultScenario().events))
	}
	if events[0].Type != "task-pool" {
		t.Errorf("first event type = %q", events[0].Type)
	}
}

func TestConcurrencyFromEventsStructured(t *testing.T) {
	events, err := freshClient(t).Events(context.Background(), defaultScenario().jobID)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	evidence := concurrencyFromEvents(events, "")
	if evidence.Source != "worker-events" {
		t.Fatalf("source = %q", evidence.Source)
	}
	if evidence.Desired != 8 || evidence.PlannedWorkers != 4 || evidence.PerTaskConcurrency != 2 {
		t.Errorf("planned = %+v", evidence)
	}
	if evidence.PlannedEffective != 8 || evidence.FinalEffective != 2 {
		t.Errorf("effective = planned %d final %d", evidence.PlannedEffective, evidence.FinalEffective)
	}
	if evidence.AdaptiveReductions != 2 {
		t.Errorf("reductions = %d", evidence.AdaptiveReductions)
	}
}

func TestConcurrencyFromLogFallback(t *testing.T) {
	log := "2026-01-01\tinformation\tRunning 4 task(s) in parallel with 2 worker concurrency each\n" +
		"2026-01-01\tinformation\tAdaptive performance changed the concurrency budget from 8 to 4 (x)\n" +
		"2026-01-01\tinformation\tAdaptive performance changed the concurrency budget from 4 to 2 (y)\n"

	evidence := concurrencyFromEvents(nil, log)
	if evidence.Source != "log-messages" {
		t.Fatalf("source = %q", evidence.Source)
	}
	if evidence.PlannedWorkers != 4 || evidence.PerTaskConcurrency != 2 || evidence.PlannedEffective != 8 {
		t.Errorf("planned = %+v", evidence)
	}
	if evidence.FinalEffective != 2 || evidence.AdaptiveReductions != 2 {
		t.Errorf("final/reductions = %d/%d", evidence.FinalEffective, evidence.AdaptiveReductions)
	}
}

func TestFailureKindsFromEventsCountsKnownAndUnknown(t *testing.T) {
	events := []workerEvent{
		{Type: "browser-failure", Severity: "warning"},
		{Type: "browser-failure", Severity: "warning"},
		{Type: "blocked", Severity: "warning"},
		{Type: "task-pool", Severity: "information"},            // excluded (info, non-failure)
		{Type: "adaptive-performance", Severity: "information"}, // excluded
		{Type: "some-new-kind", Severity: "error"},              // counted defensively
	}

	kinds := failureKindsFromEvents(events)
	if kinds["browser-failure"] != 2 || kinds["blocked"] != 1 {
		t.Errorf("known kinds = %v", kinds)
	}
	if kinds["some-new-kind"] != 1 {
		t.Errorf("unknown warning/error kind should be counted defensively: %v", kinds)
	}
	if _, ok := kinds["task-pool"]; ok {
		t.Errorf("informational events must not be counted as failures")
	}
}

func TestFailureKindsFromBenchmarkExtraBindsLandedField(t *testing.T) {
	// Simulate the classification specialist landing a structured field.
	raw := []byte(`{"job_id":"x","totals":{},"runtime":{},"failure_kinds":{"browser-failure":7,"timeout":3}}`)
	var report benchmarkReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	kinds := failureKindsFromBenchmarkExtra(report)
	if kinds["browser-failure"] != 7 || kinds["timeout"] != 3 {
		t.Errorf("landed failure_kinds = %v", kinds)
	}
}

func TestFailureKindsFromBenchmarkExtraToleratesAbsence(t *testing.T) {
	// A report without the field must yield an empty map, never an error.
	raw := []byte(`{"job_id":"x","totals":{},"runtime":{}}`)
	var report benchmarkReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if kinds := failureKindsFromBenchmarkExtra(report); len(kinds) != 0 {
		t.Errorf("absent field should yield empty map, got %v", kinds)
	}
}

// client is a small helper that builds a client bound to a fresh fake server.
func freshClient(t *testing.T) *Client {
	t.Helper()
	fake := newFakeServer(t, defaultScenario())

	return newClientFor(t, fake)
}

// TestConcurrencyEvidenceRecordsTheWidthARunSettledAt covers the width ladder's
// headline reading. A rung labelled "six parallel tasks" is only evidence about
// six if the run actually held six, and auto capacity may move the count during
// the run, so the plan alone cannot answer it.
func TestConcurrencyEvidenceRecordsTheWidthARunSettledAt(t *testing.T) {
	events := []workerEvent{
		{
			Type: "task-pool",
			Context: `{"task_workers":6,"per_task_concurrency":1,"desired_concurrency":6,` +
				`"effective_concurrency":6,"planned_browsers":6}`,
		},
		{
			Type:    "adaptive-workers",
			Context: `{"previous_task_workers":6,"task_workers":3}`,
		},
		{
			Type:    "adaptive-workers",
			Context: `{"previous_task_workers":3,"task_workers":4}`,
		},
	}

	evidence := concurrencyFromEvents(events, "")

	if evidence.PlannedWorkers != 6 {
		t.Errorf("planned workers = %d, want 6", evidence.PlannedWorkers)
	}
	if evidence.PlannedBrowsers != 6 {
		t.Errorf("planned browsers = %d, want 6", evidence.PlannedBrowsers)
	}
	if evidence.FinalWorkers != 4 {
		t.Errorf("final workers = %d, want the 4 the run settled at", evidence.FinalWorkers)
	}
	if evidence.WorkerReductions != 1 || evidence.WorkerIncreases != 1 {
		t.Errorf("worker moves = %d down / %d up, want 1 / 1",
			evidence.WorkerReductions, evidence.WorkerIncreases)
	}
	if evidence.Source != "worker-events" {
		t.Errorf("source = %q, want worker-events", evidence.Source)
	}
}

// TestConcurrencyEvidenceRecoversWidthFromPlainText proves the same reading
// survives a build that attached no structured context, which is the fallback
// path the harness already relies on for concurrency.
func TestConcurrencyEvidenceRecoversWidthFromPlainText(t *testing.T) {
	log := "Running 4 task(s) in parallel with 1 worker concurrency each (4 browser(s) planned)\n" +
		"Auto capacity reduced parallel tasks from 4 to 2 (the platform refused an attempt)\n" +
		"Auto capacity increased parallel tasks from 2 to 3 (a clean task window)\n"

	evidence := concurrencyFromEvents(nil, log)

	if evidence.PlannedWorkers != 4 {
		t.Errorf("planned workers = %d, want 4", evidence.PlannedWorkers)
	}
	if evidence.FinalWorkers != 3 {
		t.Errorf("final workers = %d, want 3", evidence.FinalWorkers)
	}
	if evidence.WorkerReductions != 1 || evidence.WorkerIncreases != 1 {
		t.Errorf("worker moves = %d down / %d up, want 1 / 1",
			evidence.WorkerReductions, evidence.WorkerIncreases)
	}
	if evidence.Source != "log-messages" {
		t.Errorf("source = %q, want log-messages", evidence.Source)
	}
}

// TestFastModePoolLineStillParses guards the throughput phase's message change:
// a Fast-mode task-pool line ends with "(Fast mode: no browser)" instead of a
// browser count, and the harness must still recover the width from it.
func TestFastModePoolLineStillParses(t *testing.T) {
	log := "Running 4 task(s) in parallel with 1 worker concurrency each (Fast mode: no browser)"

	evidence := concurrencyFromEvents(nil, log)

	if evidence.PlannedWorkers != 4 || evidence.PerTaskConcurrency != 1 {
		t.Fatalf("fast-mode plan = %d workers x %d, want 4 x 1",
			evidence.PlannedWorkers, evidence.PerTaskConcurrency)
	}
}

// TestAutoCapacityEventsAreNotCountedAsFailures keeps the capacity events out
// of the failure-kind breakdown. They are operational information; counting a
// pool that safely narrowed itself as a failure would make every healthy
// adaptive run look broken.
func TestAutoCapacityEventsAreNotCountedAsFailures(t *testing.T) {
	events := []workerEvent{
		{Type: "adaptive-workers", Severity: "information"},
		{Type: "task-worker-retired", Severity: "information"},
		{Type: "capacity-capped", Severity: "warning"},
		{Type: "browser-failure", Severity: "warning"},
	}

	kinds := failureKindsFromEvents(events)

	for _, excluded := range []string{"adaptive-workers", "task-worker-retired", "capacity-capped"} {
		if _, present := kinds[excluded]; present {
			t.Errorf("%q was counted as a failure kind", excluded)
		}
	}

	if kinds["browser-failure"] != 1 {
		t.Errorf("browser-failure count = %d, want 1", kinds["browser-failure"])
	}
}
