package jobruntime

import (
	"errors"
	"fmt"
)

// Control is an operator lifecycle command.
type Control string

const (
	ControlStart   Control = "start"
	ControlPause   Control = "pause"
	ControlResume  Control = "resume"
	ControlCancel  Control = "cancel"
	ControlRestart Control = "restart"
)

// ControlDisposition describes whether a command changed durable state,
// requested an asynchronous stop, was already satisfied, or is invalid for the
// current state.
type ControlDisposition string

const (
	ControlApplied   ControlDisposition = "applied"
	ControlRequested ControlDisposition = "requested"
	ControlNoop      ControlDisposition = "noop"
	ControlRejected  ControlDisposition = "rejected"
)

var (
	// ErrInvalidControl indicates an unknown operator command.
	ErrInvalidControl = errors.New("invalid job control")
	// ErrControlRejected indicates a recognized command which is not allowed
	// from the current lifecycle state.
	ErrControlRejected = errors.New("job control rejected")
)

// ControlDecision is a side-effect-free instruction for a control handler.
// NextState is the immediate persisted state. EventualState is the state the
// worker must persist after honoring RequestedStop. For synchronous commands
// the two fields are equal.
type ControlDecision struct {
	Control       Control
	Disposition   ControlDisposition
	CurrentState  State
	NextState     State
	EventualState State
	RequestedStop StopReason
	Message       string
}

// Changed reports whether the decision requires a durable state or requested-
// stop update.
func (d ControlDecision) Changed() bool {
	return d.Disposition == ControlApplied || d.Disposition == ControlRequested
}

// Async reports whether a worker must reach a safe checkpoint before applying
// EventualState.
func (d ControlDecision) Async() bool {
	return d.Disposition == ControlRequested
}

// Error converts a rejected decision into an error suitable for an HTTP 409
// response. Non-rejected decisions return nil.
func (d ControlDecision) Error() error {
	if d.Disposition != ControlRejected {
		return nil
	}

	return fmt.Errorf("%w: %s while job is %s", ErrControlRejected, d.Control, d.CurrentState)
}

// DecideControl evaluates a command without performing I/O. pendingStop is the
// currently persisted asynchronous stop request. Passing it makes repeated
// pause/cancel requests true no-ops and lets resume withdraw a pause which has
// not yet reached its checkpoint.
func DecideControl(state State, pendingStop StopReason, control Control) (ControlDecision, error) {
	if !state.Valid() {
		return ControlDecision{}, fmt.Errorf("%w: %q", ErrInvalidState, state)
	}

	if !validControl(control) {
		return ControlDecision{}, fmt.Errorf("%w: %q", ErrInvalidControl, control)
	}

	if !pendingStop.Valid() {
		return ControlDecision{}, fmt.Errorf("%w: %q", ErrInvalidStopReason, pendingStop)
	}

	decision := ControlDecision{
		Control:       control,
		Disposition:   ControlRejected,
		CurrentState:  state,
		NextState:     state,
		EventualState: state,
		RequestedStop: pendingStop,
	}

	switch control {
	case ControlStart:
		return decideStart(decision), nil
	case ControlPause:
		return decidePause(decision), nil
	case ControlResume:
		return decideResume(decision), nil
	case ControlCancel:
		return decideCancel(decision), nil
	case ControlRestart:
		return decideRestart(decision), nil
	default:
		return ControlDecision{}, fmt.Errorf("%w: %q", ErrInvalidControl, control)
	}
}

func validControl(control Control) bool {
	switch control {
	case ControlStart, ControlPause, ControlResume, ControlCancel, ControlRestart:
		return true
	default:
		return false
	}
}

func decideStart(decision ControlDecision) ControlDecision {
	switch decision.CurrentState {
	case StateDraft, StatePaused:
		return applyControl(decision, StateQueued, StopReasonNone, "job queued")
	case StateQueued, StateStarting, StateRunning:
		return noopControl(decision, "job is already started")
	default:
		return rejectControl(decision, "start requires a draft or paused job")
	}
}

func decidePause(decision ControlDecision) ControlDecision {
	if decision.RequestedStop == StopReasonUserCancelled || decision.CurrentState == StateCancelling {
		return rejectControl(decision, "cancellation already takes precedence")
	}

	switch decision.CurrentState {
	case StateQueued:
		return applyControl(decision, StatePaused, StopReasonNone, "queued job paused")
	case StateStarting, StateRunning:
		if decision.RequestedStop == StopReasonPauseRequested {
			return noopControl(decision, "pause already requested")
		}

		return requestControl(
			decision,
			decision.CurrentState,
			StatePaused,
			StopReasonPauseRequested,
			"pause requested; waiting for a safe checkpoint",
		)
	case StatePaused:
		return noopControl(decision, "job is already paused")
	default:
		return rejectControl(decision, "pause requires a queued, starting, or running job")
	}
}

func decideResume(decision ControlDecision) ControlDecision {
	switch decision.CurrentState {
	case StatePaused:
		return applyControl(decision, StateQueued, StopReasonNone, "job queued for resume")
	case StateStarting, StateRunning:
		if decision.RequestedStop == StopReasonPauseRequested {
			return applyControl(decision, decision.CurrentState, StopReasonNone, "pending pause withdrawn")
		}

		return noopControl(decision, "job is already running")
	case StateQueued:
		return noopControl(decision, "job is already queued")
	default:
		return rejectControl(decision, "resume requires a paused or active job")
	}
}

func decideCancel(decision ControlDecision) ControlDecision {
	if decision.RequestedStop == StopReasonUserCancelled ||
		decision.CurrentState == StateCancelling ||
		decision.CurrentState == StateCancelled {
		return noopControl(decision, "cancellation already requested or completed")
	}

	switch decision.CurrentState {
	case StateDraft, StateQueued, StatePaused:
		return applyControl(decision, StateCancelled, StopReasonUserCancelled, "job cancelled")
	case StateStarting, StateRunning:
		return requestControl(
			decision,
			StateCancelling,
			StateCancelled,
			StopReasonUserCancelled,
			"cancellation requested; waiting for current writes",
		)
	default:
		return rejectControl(decision, "completed jobs cannot be cancelled")
	}
}

func decideRestart(decision ControlDecision) ControlDecision {
	switch decision.CurrentState {
	case StatePaused, StateCompleted, StatePartial, StateFailed, StateCancelled:
		return applyControl(decision, StateQueued, StopReasonNone, "job queued from its checkpoint")
	case StateQueued, StateStarting, StateRunning:
		return noopControl(decision, "job is already queued or active")
	default:
		return rejectControl(decision, "restart requires a stopped job")
	}
}

func applyControl(
	decision ControlDecision,
	nextState State,
	requestedStop StopReason,
	message string,
) ControlDecision {
	decision.Disposition = ControlApplied
	decision.NextState = nextState
	decision.EventualState = nextState
	decision.RequestedStop = requestedStop
	decision.Message = message

	return decision
}

func requestControl(
	decision ControlDecision,
	nextState State,
	eventualState State,
	requestedStop StopReason,
	message string,
) ControlDecision {
	decision.Disposition = ControlRequested
	decision.NextState = nextState
	decision.EventualState = eventualState
	decision.RequestedStop = requestedStop
	decision.Message = message

	return decision
}

func noopControl(decision ControlDecision, message string) ControlDecision {
	decision.Disposition = ControlNoop
	decision.Message = message

	return decision
}

func rejectControl(decision ControlDecision, message string) ControlDecision {
	decision.Disposition = ControlRejected
	decision.Message = message

	return decision
}
