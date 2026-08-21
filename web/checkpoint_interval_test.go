package web

import (
	"testing"
	"time"
)

func TestCheckpointIntervalIsBoundedAndDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		seconds int
		want    time.Duration
	}{
		{name: "unset uses the default", want: DefaultCheckpointSeconds * time.Second},
		{name: "configured value is honoured", seconds: 120, want: 2 * time.Minute},
		{name: "minimum is honoured", seconds: MinimumCheckpointSeconds, want: MinimumCheckpointSeconds * time.Second},
		{name: "maximum is honoured", seconds: MaximumCheckpointSeconds, want: MaximumCheckpointSeconds * time.Second},
		{name: "below the minimum falls back", seconds: 1, want: DefaultCheckpointSeconds * time.Second},
		{name: "above the maximum falls back", seconds: 100_000, want: DefaultCheckpointSeconds * time.Second},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			data := JobData{CheckpointSeconds: test.seconds}
			if got := data.CheckpointInterval(); got != test.want {
				t.Fatalf("CheckpointInterval() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestCheckpointSecondsValidationRejectsOutOfRangeValues(t *testing.T) {
	t.Parallel()

	base := func() JobData {
		return JobData{
			Keywords: []string{"dentist"},
			Lang:     "en",
			Zoom:     15,
			Depth:    10,
			MaxTime:  10 * time.Minute,
		}
	}

	valid := base()
	valid.CheckpointSeconds = 60

	if err := valid.Validate(); err != nil {
		t.Fatalf("a valid checkpoint interval was rejected: %v", err)
	}

	unset := base()
	if err := unset.Validate(); err != nil {
		t.Fatalf("an unset checkpoint interval was rejected: %v", err)
	}

	for _, seconds := range []int{-1, 1, MinimumCheckpointSeconds - 1, MaximumCheckpointSeconds + 1} {
		invalid := base()
		invalid.CheckpointSeconds = seconds

		if err := invalid.Validate(); err == nil {
			t.Errorf("checkpoint interval %d was accepted, want a validation error", seconds)
		}
	}
}
