// Package jobruntime defines the durable job lifecycle shared by the local
// worker, persistence layer, API, and user interface.
package jobruntime

import (
	"errors"
	"fmt"
)

// State is the canonical lifecycle state of a local scrape job.
type State string

const (
	StateDraft      State = "draft"
	StateQueued     State = "queued"
	StateStarting   State = "starting"
	StateRunning    State = "running"
	StatePaused     State = "paused"
	StateCancelling State = "cancelling"
	StateCompleted  State = "completed"
	StatePartial    State = "partial"
	StateFailed     State = "failed"
	StateCancelled  State = "cancelled"
)

// LegacyStatus is the four-value status understood by the original web UI and
// API. New code should use State and store this value only as a compatibility
// projection.
type LegacyStatus string

const (
	LegacyStatusPending LegacyStatus = "pending"
	LegacyStatusWorking LegacyStatus = "working"
	LegacyStatusOK      LegacyStatus = "ok"
	LegacyStatusFailed  LegacyStatus = "failed"
)

var (
	// ErrInvalidState indicates that a lifecycle state is unknown.
	ErrInvalidState = errors.New("invalid job state")
	// ErrInvalidTransition indicates that a requested lifecycle transition is
	// not part of the canonical state machine.
	ErrInvalidTransition = errors.New("invalid job state transition")
)

// Valid reports whether s is a canonical lifecycle state.
func (s State) Valid() bool {
	switch s {
	case StateDraft,
		StateQueued,
		StateStarting,
		StateRunning,
		StatePaused,
		StateCancelling,
		StateCompleted,
		StatePartial,
		StateFailed,
		StateCancelled:
		return true
	default:
		return false
	}
}

// Terminal reports whether no more work is expected without an explicit
// restart.
func (s State) Terminal() bool {
	switch s {
	case StateCompleted, StatePartial, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

// Active reports whether a worker may currently own resources for the job.
func (s State) Active() bool {
	switch s {
	case StateStarting, StateRunning, StateCancelling:
		return true
	default:
		return false
	}
}

// Restartable reports whether the job can be queued again while retaining its
// durable task and result history.
func (s State) Restartable() bool {
	switch s {
	case StatePaused, StateCompleted, StatePartial, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

// ParseState validates and returns a State.
func ParseState(value string) (State, error) {
	state := State(value)
	if !state.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidState, value)
	}

	return state, nil
}

// LegacyStatusForState projects a canonical state onto the original web
// status vocabulary. Partial and cancelled jobs project to ok because they
// have safely stopped and may expose useful partial downloads; callers must
// use State when they need to distinguish those outcomes.
func LegacyStatusForState(state State) (LegacyStatus, error) {
	switch state {
	case StateDraft, StateQueued, StatePaused:
		return LegacyStatusPending, nil
	case StateStarting, StateRunning, StateCancelling:
		return LegacyStatusWorking, nil
	case StateCompleted, StatePartial, StateCancelled:
		return LegacyStatusOK, nil
	case StateFailed:
		return LegacyStatusFailed, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidState, state)
	}
}

// CanTransition reports whether a direct state transition is valid. A
// transition to the same state is intentionally false; idempotent HTTP/control
// handling is modelled by DecideControl instead of writing duplicate state
// changes and events.
func CanTransition(from, to State) bool {
	if !from.Valid() || !to.Valid() || from == to {
		return false
	}

	switch from {
	case StateDraft:
		return to == StateQueued || to == StateCancelled
	case StateQueued:
		return to == StateStarting || to == StatePaused || to == StateCancelled
	case StateStarting:
		return to == StateRunning || to == StatePaused || to == StateCancelling ||
			to == StatePartial || to == StateFailed
	case StateRunning:
		return to == StatePaused || to == StateCancelling || to == StateCompleted ||
			to == StatePartial || to == StateFailed
	case StatePaused:
		return to == StateQueued || to == StateCancelled
	case StateCancelling:
		return to == StateCancelled || to == StateFailed
	case StateCompleted, StatePartial, StateFailed, StateCancelled:
		return to == StateQueued
	default:
		return false
	}
}

// ValidateTransition returns a descriptive error for invalid lifecycle
// transitions.
func ValidateTransition(from, to State) error {
	if !from.Valid() {
		return fmt.Errorf("%w: source %q", ErrInvalidState, from)
	}

	if !to.Valid() {
		return fmt.Errorf("%w: destination %q", ErrInvalidState, to)
	}

	if !CanTransition(from, to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
	}

	return nil
}
