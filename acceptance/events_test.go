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
