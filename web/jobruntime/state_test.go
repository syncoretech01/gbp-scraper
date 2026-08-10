package jobruntime

import (
	"errors"
	"testing"
)

func TestStateProperties(t *testing.T) {
	t.Parallel()

	tests := []struct {
		state       State
		terminal    bool
		active      bool
		restartable bool
		legacy      LegacyStatus
	}{
		{state: StateDraft, legacy: LegacyStatusPending},
		{state: StateQueued, legacy: LegacyStatusPending},
		{state: StateStarting, active: true, legacy: LegacyStatusWorking},
		{state: StateRunning, active: true, legacy: LegacyStatusWorking},
		{state: StatePaused, restartable: true, legacy: LegacyStatusPending},
		{state: StateCancelling, active: true, legacy: LegacyStatusWorking},
		{state: StateCompleted, terminal: true, restartable: true, legacy: LegacyStatusOK},
		{state: StatePartial, terminal: true, restartable: true, legacy: LegacyStatusOK},
		{state: StateFailed, terminal: true, restartable: true, legacy: LegacyStatusFailed},
		{state: StateCancelled, terminal: true, restartable: true, legacy: LegacyStatusOK},
	}

	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			t.Parallel()

			if !test.state.Valid() {
				t.Fatalf("Valid() = false for canonical state %q", test.state)
			}
			if got := test.state.Terminal(); got != test.terminal {
				t.Errorf("Terminal() = %t, want %t", got, test.terminal)
			}
			if got := test.state.Active(); got != test.active {
				t.Errorf("Active() = %t, want %t", got, test.active)
			}
			if got := test.state.Restartable(); got != test.restartable {
				t.Errorf("Restartable() = %t, want %t", got, test.restartable)
			}

			parsed, err := ParseState(string(test.state))
			if err != nil {
				t.Fatalf("ParseState() error = %v", err)
			}
			if parsed != test.state {
				t.Errorf("ParseState() = %q, want %q", parsed, test.state)
			}

			legacy, err := LegacyStatusForState(test.state)
			if err != nil {
				t.Fatalf("LegacyStatusForState() error = %v", err)
			}
			if legacy != test.legacy {
				t.Errorf("LegacyStatusForState() = %q, want %q", legacy, test.legacy)
			}
		})
	}
}

func TestInvalidStatesAreRejected(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "unknown", "RUNNING", " running "} {
		state := State(value)
		if state.Valid() {
			t.Errorf("State(%q).Valid() = true", value)
		}
		if state.Terminal() {
			t.Errorf("State(%q).Terminal() = true", value)
		}
		if state.Active() {
			t.Errorf("State(%q).Active() = true", value)
		}
		if state.Restartable() {
			t.Errorf("State(%q).Restartable() = true", value)
		}

		if _, err := ParseState(value); !errors.Is(err, ErrInvalidState) {
			t.Errorf("ParseState(%q) error = %v, want ErrInvalidState", value, err)
		}
		if _, err := LegacyStatusForState(state); !errors.Is(err, ErrInvalidState) {
			t.Errorf("LegacyStatusForState(%q) error = %v, want ErrInvalidState", value, err)
		}
	}
}

func TestCanTransitionCanonicalMatrix(t *testing.T) {
	t.Parallel()

	states := []State{
		StateDraft,
		StateQueued,
		StateStarting,
		StateRunning,
		StatePaused,
		StateCancelling,
		StateCompleted,
		StatePartial,
		StateFailed,
		StateCancelled,
	}
	allowed := map[State]map[State]bool{
		StateDraft: {
			StateQueued:    true,
			StateCancelled: true,
		},
		StateQueued: {
			StateStarting:  true,
			StatePaused:    true,
			StateCancelled: true,
		},
		StateStarting: {
			StateRunning:    true,
			StatePaused:     true,
			StateCancelling: true,
			StatePartial:    true,
			StateFailed:     true,
		},
		StateRunning: {
			StatePaused:     true,
			StateCancelling: true,
			StateCompleted:  true,
			StatePartial:    true,
			StateFailed:     true,
		},
		StatePaused: {
			StateQueued:    true,
			StateCancelled: true,
		},
		StateCancelling: {
			StateCancelled: true,
			StateFailed:    true,
		},
		StateCompleted: {StateQueued: true},
		StatePartial:   {StateQueued: true},
		StateFailed:    {StateQueued: true},
		StateCancelled: {StateQueued: true},
	}

	for _, from := range states {
		for _, to := range states {
			want := allowed[from][to]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%q, %q) = %t, want %t", from, to, got, want)
			}

			err := ValidateTransition(from, to)
			if want && err != nil {
				t.Errorf("ValidateTransition(%q, %q) error = %v", from, to, err)
			}
			if !want && !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("ValidateTransition(%q, %q) error = %v, want ErrInvalidTransition", from, to, err)
			}
		}
	}
}

func TestTransitionsRejectInvalidEndpoints(t *testing.T) {
	t.Parallel()

	invalid := State("unknown")
	if CanTransition(invalid, StateQueued) {
		t.Error("CanTransition() accepted an invalid source")
	}
	if CanTransition(StateDraft, invalid) {
		t.Error("CanTransition() accepted an invalid destination")
	}
	if err := ValidateTransition(invalid, StateQueued); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("invalid source error = %v, want ErrInvalidState", err)
	}
	if err := ValidateTransition(StateDraft, invalid); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("invalid destination error = %v, want ErrInvalidState", err)
	}
}
