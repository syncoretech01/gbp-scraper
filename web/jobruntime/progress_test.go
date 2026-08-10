package jobruntime

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"
)

func TestStageValidity(t *testing.T) {
	t.Parallel()

	valid := []Stage{
		StageNone,
		StagePreparingQueries,
		StageGeneratingGrid,
		StageSearchingMaps,
		StageExtractingDetails,
		StageCrawlingWebsites,
		StageExtractingContacts,
		StageDeduplicating,
		StageSavingExporting,
	}
	for _, stage := range valid {
		if !stage.Valid() {
			t.Errorf("Stage(%q).Valid() = false", stage)
		}
	}

	for _, stage := range []Stage{"unknown", "RUNNING", " "} {
		if stage.Valid() {
			t.Errorf("Stage(%q).Valid() = true", stage)
		}
	}
}

func TestProgressCountersCalculationsAndValidation(t *testing.T) {
	t.Parallel()

	counters := ProgressCounters{
		TotalTasks:     15,
		CompletedTasks: 5,
		FailedTasks:    2,
		SkippedTasks:   1,
		ActiveTasks:    3,
		RawRecords:     20,
		UniqueRecords:  17,
		Websites:       8,
		Emails:         4,
		Duplicates:     3,
		Retries:        2,
		Warnings:       1,
		Errors:         2,
	}
	if got := counters.TerminalTasks(); got != 8 {
		t.Errorf("TerminalTasks() = %d, want 8", got)
	}
	if got := counters.RemainingTasks(); got != 4 {
		t.Errorf("RemainingTasks() = %d, want 4", got)
	}
	if err := counters.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	overfull := ProgressCounters{TotalTasks: 1, CompletedTasks: 1, ActiveTasks: 1}
	if got := overfull.RemainingTasks(); got != 0 {
		t.Errorf("invalid RemainingTasks() = %d, want 0", got)
	}
	if err := overfull.Validate(); !errors.Is(err, ErrInvalidProgress) {
		t.Errorf("overfull Validate() error = %v, want ErrInvalidProgress", err)
	}

	tooManyUnique := ProgressCounters{RawRecords: 1, UniqueRecords: 2}
	if err := tooManyUnique.Validate(); !errors.Is(err, ErrInvalidProgress) {
		t.Errorf("unique > raw Validate() error = %v, want ErrInvalidProgress", err)
	}
}

func TestProgressCountersRejectEveryNegativeField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		set  func(*ProgressCounters)
	}{
		{name: "total tasks", set: func(c *ProgressCounters) { c.TotalTasks = -1 }},
		{name: "completed tasks", set: func(c *ProgressCounters) { c.CompletedTasks = -1 }},
		{name: "failed tasks", set: func(c *ProgressCounters) { c.FailedTasks = -1 }},
		{name: "skipped tasks", set: func(c *ProgressCounters) { c.SkippedTasks = -1 }},
		{name: "active tasks", set: func(c *ProgressCounters) { c.ActiveTasks = -1 }},
		{name: "raw records", set: func(c *ProgressCounters) { c.RawRecords = -1 }},
		{name: "unique records", set: func(c *ProgressCounters) { c.UniqueRecords = -1 }},
		{name: "websites", set: func(c *ProgressCounters) { c.Websites = -1 }},
		{name: "emails", set: func(c *ProgressCounters) { c.Emails = -1 }},
		{name: "duplicates", set: func(c *ProgressCounters) { c.Duplicates = -1 }},
		{name: "retries", set: func(c *ProgressCounters) { c.Retries = -1 }},
		{name: "warnings", set: func(c *ProgressCounters) { c.Warnings = -1 }},
		{name: "errors", set: func(c *ProgressCounters) { c.Errors = -1 }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var counters ProgressCounters
			test.set(&counters)
			if err := counters.Validate(); !errors.Is(err, ErrInvalidProgress) {
				t.Fatalf("Validate() error = %v, want ErrInvalidProgress", err)
			}
		})
	}
}

func TestBuildProgressSnapshot(t *testing.T) {
	t.Parallel()

	input := ProgressInput{
		State: StateRunning,
		Stage: StageExtractingDetails,
		Counters: ProgressCounters{
			TotalTasks:     10,
			CompletedTasks: 3,
			FailedTasks:    1,
			ActiveTasks:    2,
			RawRecords:     30,
			UniqueRecords:  25,
			Websites:       12,
			Emails:         7,
			Duplicates:     5,
			Retries:        3,
			Warnings:       2,
			Errors:         1,
		},
		Elapsed:        90*time.Second + 500*time.Millisecond,
		CurrentQuery:   "dentists in San Francisco",
		CurrentCell:    "37.7749,-122.4194",
		CurrentDomain:  "example.test",
		CPUPercent:     42.5,
		MemoryBytes:    512 * 1024 * 1024,
		DiskFreeBytes:  10 * 1024 * 1024 * 1024,
		BrowserCount:   2,
		ActivePages:    5,
		DatabaseWrites: 77,
		WebsiteQueue:   9,
	}

	snapshot, err := BuildProgressSnapshot(input)
	if err != nil {
		t.Fatalf("BuildProgressSnapshot() error = %v", err)
	}
	if snapshot.State != input.State || snapshot.Stage != input.Stage {
		t.Errorf("state/stage = %q/%q, want %q/%q", snapshot.State, snapshot.Stage, input.State, input.Stage)
	}
	if snapshot.Counters != input.Counters {
		t.Errorf("Counters = %+v, want %+v", snapshot.Counters, input.Counters)
	}
	if snapshot.RemainingTasks != 4 {
		t.Errorf("RemainingTasks = %d, want 4", snapshot.RemainingTasks)
	}
	if snapshot.Percent != 40 {
		t.Errorf("Percent = %v, want 40", snapshot.Percent)
	}
	if snapshot.RuntimeMillis != 90500 {
		t.Errorf("RuntimeMillis = %d, want 90500", snapshot.RuntimeMillis)
	}
	if snapshot.ETASeconds == nil || *snapshot.ETASeconds != 136 {
		t.Errorf("ETASeconds = %v, want pointer to 136", snapshot.ETASeconds)
	}
	if math.Abs(snapshot.PlacesPerMinute-19.88950276243094) > 1e-12 {
		t.Errorf("PlacesPerMinute = %.15f, want 19.88950276243094", snapshot.PlacesPerMinute)
	}
	if snapshot.CurrentQuery != input.CurrentQuery || snapshot.CurrentCell != input.CurrentCell || snapshot.CurrentDomain != input.CurrentDomain {
		t.Errorf("current work fields were not preserved: %+v", snapshot)
	}
	if snapshot.CPUPercent != input.CPUPercent || snapshot.MemoryBytes != input.MemoryBytes ||
		snapshot.DiskFreeBytes != input.DiskFreeBytes || snapshot.BrowserCount != input.BrowserCount ||
		snapshot.ActivePages != input.ActivePages || snapshot.DatabaseWrites != input.DatabaseWrites ||
		snapshot.WebsiteQueue != input.WebsiteQueue {
		t.Errorf("resource fields were not preserved: %+v", snapshot)
	}
}

func TestBuildProgressSnapshotPercentageAndETAAvailability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		state       State
		counters    ProgressCounters
		elapsed     time.Duration
		percent     float64
		etaExpected bool
		etaSeconds  int64
	}{
		{
			name:       "not started",
			state:      StateDraft,
			counters:   ProgressCounters{},
			percent:    0,
			etaSeconds: 0,
		},
		{
			name:       "empty completed run",
			state:      StateCompleted,
			counters:   ProgressCounters{},
			percent:    100,
			etaSeconds: 0,
		},
		{
			name:       "no terminal samples",
			state:      StateRunning,
			counters:   ProgressCounters{TotalTasks: 5, ActiveTasks: 1},
			elapsed:    time.Minute,
			percent:    0,
			etaSeconds: 0,
		},
		{
			name:        "all tasks terminal",
			state:       StateCompleted,
			counters:    ProgressCounters{TotalTasks: 5, CompletedTasks: 5},
			elapsed:     time.Minute,
			percent:     100,
			etaExpected: true,
			etaSeconds:  0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			snapshot, err := BuildProgressSnapshot(ProgressInput{
				State:    test.state,
				Stage:    StageNone,
				Counters: test.counters,
				Elapsed:  test.elapsed,
			})
			if err != nil {
				t.Fatalf("BuildProgressSnapshot() error = %v", err)
			}
			if snapshot.Percent != test.percent {
				t.Errorf("Percent = %v, want %v", snapshot.Percent, test.percent)
			}
			if got := snapshot.ETASeconds != nil; got != test.etaExpected {
				t.Errorf("ETA availability = %t, want %t (value %v)", got, test.etaExpected, snapshot.ETASeconds)
			}
			if snapshot.ETASeconds != nil && *snapshot.ETASeconds != test.etaSeconds {
				t.Errorf("ETASeconds = %d, want %d", *snapshot.ETASeconds, test.etaSeconds)
			}
		})
	}
}

func TestBuildProgressSnapshotJSONContract(t *testing.T) {
	t.Parallel()

	snapshot, err := BuildProgressSnapshot(ProgressInput{
		State:    StateRunning,
		Stage:    StageSearchingMaps,
		Counters: ProgressCounters{TotalTasks: 2, CompletedTasks: 1, RawRecords: 1, UniqueRecords: 1},
		Elapsed:  1500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("BuildProgressSnapshot() error = %v", err)
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got := fields["runtime_ms"]; got != float64(1500) {
		t.Errorf("runtime_ms = %#v, want 1500", got)
	}
	if got := fields["eta_seconds"]; got != float64(2) {
		t.Errorf("eta_seconds = %#v, want 2", got)
	}
	if _, exists := fields["elapsed"]; exists {
		t.Error("JSON leaked a time.Duration elapsed field")
	}
	if _, exists := fields["current_query"]; exists {
		t.Error("empty current_query was not omitted")
	}
}

func TestBuildProgressSnapshotRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	valid := ProgressInput{State: StateRunning, Stage: StageSearchingMaps}
	tests := []struct {
		name      string
		mutate    func(*ProgressInput)
		wantError error
	}{
		{name: "invalid state", mutate: func(input *ProgressInput) { input.State = State("unknown") }, wantError: ErrInvalidState},
		{name: "invalid stage", mutate: func(input *ProgressInput) { input.Stage = Stage("unknown") }, wantError: ErrInvalidStage},
		{name: "invalid counters", mutate: func(input *ProgressInput) { input.Counters.TotalTasks = -1 }, wantError: ErrInvalidProgress},
		{name: "negative elapsed", mutate: func(input *ProgressInput) { input.Elapsed = -time.Second }, wantError: ErrInvalidProgress},
		{name: "negative CPU", mutate: func(input *ProgressInput) { input.CPUPercent = -1 }, wantError: ErrInvalidProgress},
		{name: "NaN CPU", mutate: func(input *ProgressInput) { input.CPUPercent = math.NaN() }, wantError: ErrInvalidProgress},
		{name: "positive infinite CPU", mutate: func(input *ProgressInput) { input.CPUPercent = math.Inf(1) }, wantError: ErrInvalidProgress},
		{name: "negative infinite CPU", mutate: func(input *ProgressInput) { input.CPUPercent = math.Inf(-1) }, wantError: ErrInvalidProgress},
		{name: "negative browsers", mutate: func(input *ProgressInput) { input.BrowserCount = -1 }, wantError: ErrInvalidProgress},
		{name: "negative pages", mutate: func(input *ProgressInput) { input.ActivePages = -1 }, wantError: ErrInvalidProgress},
		{name: "negative writes", mutate: func(input *ProgressInput) { input.DatabaseWrites = -1 }, wantError: ErrInvalidProgress},
		{name: "negative website queue", mutate: func(input *ProgressInput) { input.WebsiteQueue = -1 }, wantError: ErrInvalidProgress},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			input := valid
			test.mutate(&input)
			_, err := BuildProgressSnapshot(input)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("BuildProgressSnapshot() error = %v, want %v", err, test.wantError)
			}
		})
	}
}

func TestEstimateETA(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		total     int64
		terminal  int64
		elapsed   time.Duration
		want      time.Duration
		available bool
	}{
		{name: "average rate", total: 10, terminal: 4, elapsed: 90 * time.Second, want: 135 * time.Second, available: true},
		{name: "all complete", total: 10, terminal: 10, elapsed: 90 * time.Second, want: 0, available: true},
		{name: "all complete without elapsed", total: 10, terminal: 10, elapsed: 0, want: 0, available: true},
		{name: "unknown total", total: 0, terminal: 0, elapsed: time.Minute},
		{name: "no terminal sample", total: 10, terminal: 0, elapsed: time.Minute},
		{name: "no elapsed sample", total: 10, terminal: 1, elapsed: 0},
		{name: "negative total", total: -1, terminal: 0, elapsed: time.Minute},
		{name: "negative terminal", total: 1, terminal: -1, elapsed: time.Minute},
		{name: "terminal exceeds total", total: 1, terminal: 2, elapsed: time.Minute},
		{name: "negative elapsed", total: 1, terminal: 1, elapsed: -time.Second},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, available := EstimateETA(test.total, test.terminal, test.elapsed)
			if available != test.available || got != test.want {
				t.Errorf("EstimateETA() = (%v, %t), want (%v, %t)", got, available, test.want, test.available)
			}
		})
	}
}

func TestEstimateETAHandlesDurationBounds(t *testing.T) {
	t.Parallel()

	maxDuration := time.Duration(int64(^uint64(0) >> 1))
	got, available := EstimateETA(2, 1, maxDuration)
	if !available || got != maxDuration {
		t.Errorf("EstimateETA(valid maximum) = (%v, %t), want (%v, true)", got, available, maxDuration)
	}

	_, available = EstimateETA(int64(^uint64(0)>>1), 1, maxDuration)
	if available {
		t.Error("EstimateETA(overflowing estimate) reported an available duration")
	}
}

func TestRatePerMinute(t *testing.T) {
	t.Parallel()

	if got := RatePerMinute(30, 90*time.Second); got != 20 {
		t.Errorf("RatePerMinute() = %v, want 20", got)
	}
	for _, test := range []struct {
		count   int64
		elapsed time.Duration
	}{
		{count: 0, elapsed: time.Minute},
		{count: -1, elapsed: time.Minute},
		{count: 1, elapsed: 0},
		{count: 1, elapsed: -time.Second},
	} {
		if got := RatePerMinute(test.count, test.elapsed); got != 0 {
			t.Errorf("RatePerMinute(%d, %v) = %v, want 0", test.count, test.elapsed, got)
		}
	}
}
