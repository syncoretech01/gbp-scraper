package jobruntime

import (
	"errors"
	"strings"
	"testing"
)

type controlExpectation struct {
	disposition ControlDisposition
	next        State
	eventual    State
	stop        StopReason
}

func TestDecideControlCanonicalMatrix(t *testing.T) {
	t.Parallel()

	type stateCases map[Control]controlExpectation
	tests := map[State]stateCases{
		StateDraft: {
			ControlStart:   applied(StateQueued, StopReasonNone),
			ControlPause:   rejected(StateDraft),
			ControlResume:  rejected(StateDraft),
			ControlCancel:  applied(StateCancelled, StopReasonUserCancelled),
			ControlRestart: rejected(StateDraft),
		},
		StateQueued: {
			ControlStart:   noop(StateQueued),
			ControlPause:   applied(StatePaused, StopReasonNone),
			ControlResume:  noop(StateQueued),
			ControlCancel:  applied(StateCancelled, StopReasonUserCancelled),
			ControlRestart: noop(StateQueued),
		},
		StateStarting: {
			ControlStart:   noop(StateStarting),
			ControlPause:   requested(StateStarting, StatePaused, StopReasonPauseRequested),
			ControlResume:  noop(StateStarting),
			ControlCancel:  requested(StateCancelling, StateCancelled, StopReasonUserCancelled),
			ControlRestart: noop(StateStarting),
		},
		StateRunning: {
			ControlStart:   noop(StateRunning),
			ControlPause:   requested(StateRunning, StatePaused, StopReasonPauseRequested),
			ControlResume:  noop(StateRunning),
			ControlCancel:  requested(StateCancelling, StateCancelled, StopReasonUserCancelled),
			ControlRestart: noop(StateRunning),
		},
		StatePaused: {
			ControlStart:   applied(StateQueued, StopReasonNone),
			ControlPause:   noop(StatePaused),
			ControlResume:  applied(StateQueued, StopReasonNone),
			ControlCancel:  applied(StateCancelled, StopReasonUserCancelled),
			ControlRestart: applied(StateQueued, StopReasonNone),
		},
		StateCancelling: {
			ControlStart:   rejected(StateCancelling),
			ControlPause:   rejected(StateCancelling),
			ControlResume:  rejected(StateCancelling),
			ControlCancel:  noop(StateCancelling),
			ControlRestart: rejected(StateCancelling),
		},
		StateCompleted: terminalControlCases(StateCompleted),
		StatePartial:   terminalControlCases(StatePartial),
		StateFailed:    terminalControlCases(StateFailed),
		StateCancelled: {
			ControlStart:   rejected(StateCancelled),
			ControlPause:   rejected(StateCancelled),
			ControlResume:  rejected(StateCancelled),
			ControlCancel:  noop(StateCancelled),
			ControlRestart: applied(StateQueued, StopReasonNone),
		},
	}

	controls := []Control{ControlStart, ControlPause, ControlResume, ControlCancel, ControlRestart}
	for state, stateTests := range tests {
		for _, control := range controls {
			expectation, exists := stateTests[control]
			if !exists {
				t.Fatalf("test bug: no expectation for %s/%s", state, control)
			}

			name := string(state) + "/" + string(control)
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				decision, err := DecideControl(state, StopReasonNone, control)
				if err != nil {
					t.Fatalf("DecideControl() error = %v", err)
				}
				assertControlDecision(t, decision, state, control, expectation)
			})
		}
	}
}

func TestDecideControlIdempotentPendingStops(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		state   State
		pending StopReason
		control Control
		want    controlExpectation
	}{
		{
			name:    "repeated pause while starting",
			state:   StateStarting,
			pending: StopReasonPauseRequested,
			control: ControlPause,
			want:    withStop(noop(StateStarting), StopReasonPauseRequested),
		},
		{
			name:    "repeated pause while running",
			state:   StateRunning,
			pending: StopReasonPauseRequested,
			control: ControlPause,
			want:    withStop(noop(StateRunning), StopReasonPauseRequested),
		},
		{
			name:    "resume withdraws pending pause while starting",
			state:   StateStarting,
			pending: StopReasonPauseRequested,
			control: ControlResume,
			want:    applied(StateStarting, StopReasonNone),
		},
		{
			name:    "resume withdraws pending pause while running",
			state:   StateRunning,
			pending: StopReasonPauseRequested,
			control: ControlResume,
			want:    applied(StateRunning, StopReasonNone),
		},
		{
			name:    "cancel supersedes pending pause",
			state:   StateRunning,
			pending: StopReasonPauseRequested,
			control: ControlCancel,
			want:    requested(StateCancelling, StateCancelled, StopReasonUserCancelled),
		},
		{
			name:    "repeated cancel while worker is running",
			state:   StateRunning,
			pending: StopReasonUserCancelled,
			control: ControlCancel,
			want:    withStop(noop(StateRunning), StopReasonUserCancelled),
		},
		{
			name:    "pause cannot supersede pending cancel",
			state:   StateRunning,
			pending: StopReasonUserCancelled,
			control: ControlPause,
			want:    withStop(rejected(StateRunning), StopReasonUserCancelled),
		},
		{
			name:    "resume cannot withdraw pending cancel",
			state:   StateRunning,
			pending: StopReasonUserCancelled,
			control: ControlResume,
			want:    withStop(noop(StateRunning), StopReasonUserCancelled),
		},
		{
			name:    "cancel remains idempotent in cancelling state",
			state:   StateCancelling,
			pending: StopReasonUserCancelled,
			control: ControlCancel,
			want:    withStop(noop(StateCancelling), StopReasonUserCancelled),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			decision, err := DecideControl(test.state, test.pending, test.control)
			if err != nil {
				t.Fatalf("DecideControl() error = %v", err)
			}
			assertControlDecision(t, decision, test.state, test.control, test.want)
		})
	}
}

func TestDecideControlRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	if _, err := DecideControl(State("unknown"), StopReasonNone, ControlStart); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("invalid state error = %v, want ErrInvalidState", err)
	}
	if _, err := DecideControl(StateDraft, StopReasonNone, Control("launch")); !errors.Is(err, ErrInvalidControl) {
		t.Fatalf("invalid control error = %v, want ErrInvalidControl", err)
	}
	if _, err := DecideControl(StateDraft, StopReason("mystery"), ControlStart); !errors.Is(err, ErrInvalidStopReason) {
		t.Fatalf("invalid stop reason error = %v, want ErrInvalidStopReason", err)
	}
}

func TestControlDecisionHelpers(t *testing.T) {
	t.Parallel()

	appliedDecision, err := DecideControl(StateDraft, StopReasonNone, ControlStart)
	if err != nil {
		t.Fatalf("DecideControl(start) error = %v", err)
	}
	if !appliedDecision.Changed() || appliedDecision.Async() || appliedDecision.Error() != nil {
		t.Errorf("unexpected applied helpers: changed=%t async=%t error=%v", appliedDecision.Changed(), appliedDecision.Async(), appliedDecision.Error())
	}

	requestedDecision, err := DecideControl(StateRunning, StopReasonNone, ControlPause)
	if err != nil {
		t.Fatalf("DecideControl(pause) error = %v", err)
	}
	if !requestedDecision.Changed() || !requestedDecision.Async() || requestedDecision.Error() != nil {
		t.Errorf("unexpected requested helpers: changed=%t async=%t error=%v", requestedDecision.Changed(), requestedDecision.Async(), requestedDecision.Error())
	}

	noopDecision, err := DecideControl(StateRunning, StopReasonNone, ControlStart)
	if err != nil {
		t.Fatalf("DecideControl(noop) error = %v", err)
	}
	if noopDecision.Changed() || noopDecision.Async() || noopDecision.Error() != nil {
		t.Errorf("unexpected noop helpers: changed=%t async=%t error=%v", noopDecision.Changed(), noopDecision.Async(), noopDecision.Error())
	}

	rejectedDecision, err := DecideControl(StateDraft, StopReasonNone, ControlPause)
	if err != nil {
		t.Fatalf("DecideControl(rejected) error = %v", err)
	}
	if rejectedDecision.Changed() || rejectedDecision.Async() {
		t.Errorf("unexpected rejected helpers: changed=%t async=%t", rejectedDecision.Changed(), rejectedDecision.Async())
	}
	decisionErr := rejectedDecision.Error()
	if !errors.Is(decisionErr, ErrControlRejected) {
		t.Fatalf("Error() = %v, want ErrControlRejected", decisionErr)
	}
	if !strings.Contains(decisionErr.Error(), string(ControlPause)) || !strings.Contains(decisionErr.Error(), string(StateDraft)) {
		t.Errorf("Error() lacks control/state context: %v", decisionErr)
	}
}

func terminalControlCases(state State) map[Control]controlExpectation {
	return map[Control]controlExpectation{
		ControlStart:   rejected(state),
		ControlPause:   rejected(state),
		ControlResume:  rejected(state),
		ControlCancel:  rejected(state),
		ControlRestart: applied(StateQueued, StopReasonNone),
	}
}

func applied(next State, stop StopReason) controlExpectation {
	return controlExpectation{
		disposition: ControlApplied,
		next:        next,
		eventual:    next,
		stop:        stop,
	}
}

func requested(next, eventual State, stop StopReason) controlExpectation {
	return controlExpectation{
		disposition: ControlRequested,
		next:        next,
		eventual:    eventual,
		stop:        stop,
	}
}

func noop(state State) controlExpectation {
	return controlExpectation{
		disposition: ControlNoop,
		next:        state,
		eventual:    state,
		stop:        StopReasonNone,
	}
}

func rejected(state State) controlExpectation {
	return controlExpectation{
		disposition: ControlRejected,
		next:        state,
		eventual:    state,
		stop:        StopReasonNone,
	}
}

func withStop(expectation controlExpectation, stop StopReason) controlExpectation {
	expectation.stop = stop

	return expectation
}

func assertControlDecision(
	t *testing.T,
	decision ControlDecision,
	state State,
	control Control,
	want controlExpectation,
) {
	t.Helper()

	if decision.Control != control {
		t.Errorf("Control = %q, want %q", decision.Control, control)
	}
	if decision.CurrentState != state {
		t.Errorf("CurrentState = %q, want %q", decision.CurrentState, state)
	}
	if decision.Disposition != want.disposition {
		t.Errorf("Disposition = %q, want %q", decision.Disposition, want.disposition)
	}
	if decision.NextState != want.next {
		t.Errorf("NextState = %q, want %q", decision.NextState, want.next)
	}
	if decision.EventualState != want.eventual {
		t.Errorf("EventualState = %q, want %q", decision.EventualState, want.eventual)
	}
	if decision.RequestedStop != want.stop {
		t.Errorf("RequestedStop = %q, want %q", decision.RequestedStop, want.stop)
	}
	if decision.Message == "" {
		t.Error("Message is empty")
	}
	if got := decision.Changed(); got != (want.disposition == ControlApplied || want.disposition == ControlRequested) {
		t.Errorf("Changed() = %t for disposition %q", got, want.disposition)
	}
	if got := decision.Async(); got != (want.disposition == ControlRequested) {
		t.Errorf("Async() = %t for disposition %q", got, want.disposition)
	}
	if want.disposition == ControlRejected {
		if !errors.Is(decision.Error(), ErrControlRejected) {
			t.Errorf("Error() = %v, want ErrControlRejected", decision.Error())
		}
	} else if decision.Error() != nil {
		t.Errorf("Error() = %v, want nil", decision.Error())
	}
}
