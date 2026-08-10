package jobruntime

import (
	"errors"
	"testing"
)

func TestStopReasonValidity(t *testing.T) {
	t.Parallel()

	valid := []StopReason{
		StopReasonNone,
		StopReasonCompleted,
		StopReasonPauseRequested,
		StopReasonUserCancelled,
		StopReasonRuntimeLimit,
		StopReasonMaximumRecords,
		StopReasonLowDisk,
		StopReasonShutdown,
		StopReasonReconfigure,
		StopReasonProxiesUnavailable,
		StopReasonTaskFailures,
		StopReasonTasksIncomplete,
		StopReasonFatalError,
	}
	for _, reason := range valid {
		if !reason.Valid() {
			t.Errorf("StopReason(%q).Valid() = false", reason)
		}
	}

	for _, reason := range []StopReason{"unknown", "COMPLETED", " "} {
		if reason.Valid() {
			t.Errorf("StopReason(%q).Valid() = true", reason)
		}
	}
}

func TestTaskSummaryCalculationsAndValidation(t *testing.T) {
	t.Parallel()

	summary := TaskSummary{Total: 12, Completed: 5, Failed: 2, Skipped: 1}
	if got := summary.Terminal(); got != 8 {
		t.Errorf("Terminal() = %d, want 8", got)
	}
	if got := summary.Remaining(); got != 4 {
		t.Errorf("Remaining() = %d, want 4", got)
	}
	if err := summary.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	overfull := TaskSummary{Total: 1, Completed: 2}
	if got := overfull.Remaining(); got != 0 {
		t.Errorf("invalid Remaining() = %d, want 0", got)
	}
	if err := overfull.Validate(); !errors.Is(err, ErrInvalidTaskSummary) {
		t.Errorf("overfull Validate() error = %v, want ErrInvalidTaskSummary", err)
	}

	negativeCases := []TaskSummary{
		{Total: -1},
		{Completed: -1},
		{Failed: -1},
		{Skipped: -1},
	}
	for index, invalid := range negativeCases {
		if err := invalid.Validate(); !errors.Is(err, ErrInvalidTaskSummary) {
			t.Errorf("negative case %d Validate() error = %v, want ErrInvalidTaskSummary", index, err)
		}
	}
}

func TestClassifyOutcomeExplicitStopReasons(t *testing.T) {
	t.Parallel()

	tests := []struct {
		reason      StopReason
		state       State
		recoverable bool
	}{
		{reason: StopReasonPauseRequested, state: StatePaused, recoverable: true},
		{reason: StopReasonLowDisk, state: StatePaused, recoverable: true},
		{reason: StopReasonShutdown, state: StatePaused, recoverable: true},
		{reason: StopReasonProxiesUnavailable, state: StatePaused, recoverable: true},
		{reason: StopReasonUserCancelled, state: StateCancelled, recoverable: true},
		{reason: StopReasonRuntimeLimit, state: StatePartial, recoverable: true},
		{reason: StopReasonMaximumRecords, state: StatePartial, recoverable: true},
		{reason: StopReasonReconfigure, state: StateQueued, recoverable: true},
		{reason: StopReasonFatalError, state: StateFailed, recoverable: true},
		{reason: StopReasonTaskFailures, state: StatePartial, recoverable: true},
		{reason: StopReasonTasksIncomplete, state: StatePartial, recoverable: true},
	}

	for _, test := range tests {
		t.Run(string(test.reason), func(t *testing.T) {
			t.Parallel()

			outcome, err := ClassifyOutcome(RunResult{
				Reason:           test.reason,
				Tasks:            TaskSummary{Total: 3, Completed: 1},
				CommittedRecords: 4,
			})
			if err != nil {
				t.Fatalf("ClassifyOutcome() error = %v", err)
			}
			if outcome.State != test.state {
				t.Errorf("State = %q, want %q", outcome.State, test.state)
			}
			if outcome.Reason != test.reason {
				t.Errorf("Reason = %q, want %q", outcome.Reason, test.reason)
			}
			if outcome.Recoverable != test.recoverable {
				t.Errorf("Recoverable = %t, want %t", outcome.Recoverable, test.recoverable)
			}
			if !outcome.HasPartialResults {
				t.Error("HasPartialResults = false with committed records")
			}
		})
	}
}

func TestClassifyOutcomeNaturalCompletionEvidence(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("executor stopped")
	tests := []struct {
		name        string
		result      RunResult
		state       State
		reason      StopReason
		recoverable bool
	}{
		{
			name:   "all tasks completed",
			result: RunResult{Tasks: TaskSummary{Total: 5, Completed: 5}},
			state:  StateCompleted,
			reason: StopReasonCompleted,
		},
		{
			name:   "explicit completed reason uses evidence",
			result: RunResult{Reason: StopReasonCompleted, Tasks: TaskSummary{Total: 2, Completed: 2}},
			state:  StateCompleted,
			reason: StopReasonCompleted,
		},
		{
			name:   "zero task run completes",
			result: RunResult{},
			state:  StateCompleted,
			reason: StopReasonCompleted,
		},
		{
			name:        "failed task produces partial",
			result:      RunResult{Tasks: TaskSummary{Total: 3, Completed: 2, Failed: 1}},
			state:       StatePartial,
			reason:      StopReasonTaskFailures,
			recoverable: true,
		},
		{
			name:        "skipped task produces partial",
			result:      RunResult{Tasks: TaskSummary{Total: 3, Completed: 2, Skipped: 1}},
			state:       StatePartial,
			reason:      StopReasonTaskFailures,
			recoverable: true,
		},
		{
			name:        "unfinished task produces partial",
			result:      RunResult{Tasks: TaskSummary{Total: 3, Completed: 2}},
			state:       StatePartial,
			reason:      StopReasonTasksIncomplete,
			recoverable: true,
		},
		{
			name:        "executor error produces failed",
			result:      RunResult{Tasks: TaskSummary{Total: 3, Completed: 3}, Err: sentinel},
			state:       StateFailed,
			reason:      StopReasonFatalError,
			recoverable: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			outcome, err := ClassifyOutcome(test.result)
			if err != nil {
				t.Fatalf("ClassifyOutcome() error = %v", err)
			}
			if outcome.State != test.state || outcome.Reason != test.reason || outcome.Recoverable != test.recoverable {
				t.Errorf(
					"ClassifyOutcome() = {State:%q Reason:%q Recoverable:%t}, want {%q %q %t}",
					outcome.State,
					outcome.Reason,
					outcome.Recoverable,
					test.state,
					test.reason,
					test.recoverable,
				)
			}
		})
	}
}

func TestClassifyOutcomePartialResultFlag(t *testing.T) {
	t.Parallel()

	withoutRecords, err := ClassifyOutcome(RunResult{
		Reason: StopReasonRuntimeLimit,
		Tasks:  TaskSummary{Total: 1},
	})
	if err != nil {
		t.Fatalf("ClassifyOutcome(no records) error = %v", err)
	}
	if withoutRecords.HasPartialResults {
		t.Error("HasPartialResults = true with no committed records")
	}

	withRecords, err := ClassifyOutcome(RunResult{
		Reason:           StopReasonFatalError,
		Tasks:            TaskSummary{Total: 1},
		CommittedRecords: 1,
	})
	if err != nil {
		t.Fatalf("ClassifyOutcome(records) error = %v", err)
	}
	if !withRecords.HasPartialResults {
		t.Error("HasPartialResults = false with committed records")
	}
}

func TestClassifyOutcomeRejectsInvalidEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		result    RunResult
		wantError error
	}{
		{
			name:      "unknown reason",
			result:    RunResult{Reason: StopReason("unknown")},
			wantError: ErrInvalidStopReason,
		},
		{
			name:      "negative task counter",
			result:    RunResult{Tasks: TaskSummary{Total: -1}},
			wantError: ErrInvalidTaskSummary,
		},
		{
			name:      "overfull task counter",
			result:    RunResult{Tasks: TaskSummary{Total: 1, Completed: 2}},
			wantError: ErrInvalidTaskSummary,
		},
		{
			name:      "negative committed records",
			result:    RunResult{CommittedRecords: -1},
			wantError: ErrInvalidTaskSummary,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := ClassifyOutcome(test.result)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("ClassifyOutcome() error = %v, want %v", err, test.wantError)
			}
		})
	}
}
