package jobruntime

import (
	"errors"
	"fmt"
)

// StopReason records why an executor stopped. It must be persisted separately
// from State so operators can distinguish, for example, a runtime limit from
// failed tasks.
type StopReason string

const (
	StopReasonNone               StopReason = ""
	StopReasonCompleted          StopReason = "completed"
	StopReasonPauseRequested     StopReason = "pause_requested"
	StopReasonUserCancelled      StopReason = "user_cancelled"
	StopReasonRuntimeLimit       StopReason = "runtime_limit"
	StopReasonMaximumRecords     StopReason = "maximum_records"
	StopReasonLowDisk            StopReason = "low_disk"
	StopReasonShutdown           StopReason = "shutdown"
	StopReasonReconfigure        StopReason = "reconfigure"
	StopReasonProxiesUnavailable StopReason = "proxies_unavailable"
	StopReasonTaskFailures       StopReason = "task_failures"
	StopReasonTasksIncomplete    StopReason = "tasks_incomplete"
	StopReasonFatalError         StopReason = "fatal_error"
)

var (
	// ErrInvalidStopReason indicates that an outcome contains an unknown stop
	// reason.
	ErrInvalidStopReason = errors.New("invalid stop reason")
	// ErrInvalidTaskSummary indicates inconsistent or negative task counters.
	ErrInvalidTaskSummary = errors.New("invalid task summary")
)

// Valid reports whether r is a recognized stop reason. The empty reason is
// valid and represents natural executor completion.
func (r StopReason) Valid() bool {
	switch r {
	case StopReasonNone,
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
		StopReasonFatalError:
		return true
	default:
		return false
	}
}

// TaskSummary contains the terminal task counters used to classify a run.
type TaskSummary struct {
	Total     int64
	Completed int64
	Failed    int64
	Skipped   int64
}

// Terminal returns the number of tasks which reached any terminal state.
func (s TaskSummary) Terminal() int64 {
	return s.Completed + s.Failed + s.Skipped
}

// Remaining returns the number of tasks which did not reach a terminal state.
// Invalid summaries return zero; call Validate before relying on the value.
func (s TaskSummary) Remaining() int64 {
	remaining := s.Total - s.Terminal()
	if remaining < 0 {
		return 0
	}

	return remaining
}

// Validate checks task counters for values which cannot represent a real run.
func (s TaskSummary) Validate() error {
	if s.Total < 0 || s.Completed < 0 || s.Failed < 0 || s.Skipped < 0 {
		return fmt.Errorf("%w: counters cannot be negative", ErrInvalidTaskSummary)
	}

	if s.Terminal() > s.Total {
		return fmt.Errorf(
			"%w: terminal tasks (%d) exceed total tasks (%d)",
			ErrInvalidTaskSummary,
			s.Terminal(),
			s.Total,
		)
	}

	return nil
}

// RunResult is the durable evidence used to classify a stopped executor.
type RunResult struct {
	Reason           StopReason
	Tasks            TaskSummary
	CommittedRecords int64
	Err              error
}

// Outcome is the canonical state and metadata derived from a RunResult.
type Outcome struct {
	State             State
	Reason            StopReason
	Recoverable       bool
	HasPartialResults bool
}

// ClassifyOutcome applies the lifecycle's deterministic stop semantics.
// Runtime/record limits always produce Partial, while operator or safety pauses
// preserve a resumable Paused job. Fatal executor errors remain Failed even if
// some results were committed; those results are still exposed as partial data.
func ClassifyOutcome(result RunResult) (Outcome, error) {
	if !result.Reason.Valid() {
		return Outcome{}, fmt.Errorf("%w: %q", ErrInvalidStopReason, result.Reason)
	}

	if err := result.Tasks.Validate(); err != nil {
		return Outcome{}, err
	}

	if result.CommittedRecords < 0 {
		return Outcome{}, fmt.Errorf("%w: committed records cannot be negative", ErrInvalidTaskSummary)
	}

	outcome := Outcome{
		Reason:            result.Reason,
		HasPartialResults: result.CommittedRecords > 0,
	}

	switch result.Reason {
	case StopReasonPauseRequested, StopReasonLowDisk, StopReasonShutdown, StopReasonProxiesUnavailable:
		outcome.State = StatePaused
		outcome.Recoverable = true

		return outcome, nil
	case StopReasonUserCancelled:
		outcome.State = StateCancelled
		outcome.Recoverable = true

		return outcome, nil
	case StopReasonRuntimeLimit, StopReasonMaximumRecords:
		outcome.State = StatePartial
		outcome.Recoverable = true

		return outcome, nil
	case StopReasonReconfigure:
		outcome.State = StateQueued
		outcome.Recoverable = true

		return outcome, nil
	case StopReasonFatalError:
		outcome.State = StateFailed
		outcome.Recoverable = true

		return outcome, nil
	case StopReasonTaskFailures, StopReasonTasksIncomplete:
		outcome.State = StatePartial
		outcome.Recoverable = true

		return outcome, nil
	case StopReasonNone, StopReasonCompleted:
		// Natural completion is classified from the evidence below.
	}

	if result.Err != nil {
		outcome.State = StateFailed
		outcome.Reason = StopReasonFatalError
		outcome.Recoverable = true

		return outcome, nil
	}

	if result.Tasks.Failed > 0 || result.Tasks.Skipped > 0 {
		outcome.State = StatePartial
		outcome.Reason = StopReasonTaskFailures
		outcome.Recoverable = true

		return outcome, nil
	}

	if result.Tasks.Remaining() > 0 {
		outcome.State = StatePartial
		outcome.Reason = StopReasonTasksIncomplete
		outcome.Recoverable = true

		return outcome, nil
	}

	outcome.State = StateCompleted
	outcome.Reason = StopReasonCompleted

	return outcome, nil
}
