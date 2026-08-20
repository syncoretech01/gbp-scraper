package enrichment

import (
	"testing"
	"time"
)

func healthySample(response time.Duration) SiteObservation {
	return SiteObservation{ResponseTime: response, Reachable: true}
}

func timeoutSample() SiteObservation {
	return SiteObservation{TimedOut: true}
}

func failureSample() SiteObservation {
	return SiteObservation{Failed: true}
}

func TestAdaptiveTimeoutPolicy(t *testing.T) {
	t.Parallel()

	const ceiling = 10 * time.Second

	testCases := []struct {
		name    string
		ceiling time.Duration
		history SiteHistory
		want    time.Duration
	}{
		{
			name:    "no history keeps the configured ceiling exactly",
			ceiling: ceiling,
			history: SiteHistory{},
			want:    ceiling,
		},
		{
			name:    "nil observations with a status keep the ceiling",
			ceiling: ceiling,
			history: SiteHistory{LastStatus: "error"},
			want:    ceiling,
		},
		{
			name:    "a single fast sample is not yet a trend",
			ceiling: ceiling,
			history: SiteHistory{Observations: []SiteObservation{healthySample(400 * time.Millisecond)}},
			want:    ceiling,
		},
		{
			name:    "fast healthy history collapses to the floor",
			ceiling: ceiling,
			history: SiteHistory{Observations: []SiteObservation{
				healthySample(400 * time.Millisecond),
				healthySample(380 * time.Millisecond),
				healthySample(420 * time.Millisecond),
			}},
			want: adaptiveTimeoutFloor,
		},
		{
			name:    "moderate healthy history uses p95 times the safety factor",
			ceiling: ceiling,
			history: SiteHistory{Observations: []SiteObservation{
				healthySample(1200 * time.Millisecond),
				healthySample(900 * time.Millisecond),
				healthySample(1000 * time.Millisecond),
			}},
			want: 3600 * time.Millisecond,
		},
		{
			name:    "slow healthy history stays at the ceiling rather than exceeding it",
			ceiling: ceiling,
			history: SiteHistory{Observations: []SiteObservation{
				healthySample(8 * time.Second),
				healthySample(7 * time.Second),
				healthySample(9 * time.Second),
			}},
			want: ceiling,
		},
		{
			name:    "two consecutive timeouts shorten the leash",
			ceiling: ceiling,
			history: SiteHistory{Observations: []SiteObservation{
				timeoutSample(),
				timeoutSample(),
				healthySample(300 * time.Millisecond),
				healthySample(310 * time.Millisecond),
			}},
			want: ceiling / adaptiveFailFastDivisor,
		},
		{
			name:    "two consecutive transport failures shorten the leash",
			ceiling: ceiling,
			history: SiteHistory{Observations: []SiteObservation{
				failureSample(),
				failureSample(),
				healthySample(300 * time.Millisecond),
				healthySample(310 * time.Millisecond),
			}},
			want: ceiling / adaptiveFailFastDivisor,
		},
		{
			name:    "half the window failing shortens the leash even without a streak",
			ceiling: ceiling,
			history: SiteHistory{Observations: []SiteObservation{
				healthySample(300 * time.Millisecond),
				timeoutSample(),
				healthySample(320 * time.Millisecond),
				failureSample(),
			}},
			want: ceiling / adaptiveFailFastDivisor,
		},
		{
			name:    "one recent failure in a mostly healthy window keeps latency shaping",
			ceiling: ceiling,
			history: SiteHistory{Observations: []SiteObservation{
				timeoutSample(),
				healthySample(1000 * time.Millisecond),
				healthySample(900 * time.Millisecond),
				healthySample(950 * time.Millisecond),
				healthySample(1000 * time.Millisecond),
			}},
			want: 3 * time.Second,
		},
		{
			name:    "one lone failure is too little evidence to shorten anything",
			ceiling: ceiling,
			history: SiteHistory{Observations: []SiteObservation{timeoutSample()}},
			want:    ceiling,
		},
		{
			name:    "a persisted error status shortens the leash on its own",
			ceiling: ceiling,
			history: SiteHistory{
				LastStatus: "ERROR",
				Observations: []SiteObservation{
					healthySample(300 * time.Millisecond),
					healthySample(310 * time.Millisecond),
					healthySample(320 * time.Millisecond),
				},
			},
			want: ceiling / adaptiveFailFastDivisor,
		},
		{
			name:    "an active status does not shorten a healthy leash",
			ceiling: ceiling,
			history: SiteHistory{
				LastStatus: "active",
				Observations: []SiteObservation{
					healthySample(1000 * time.Millisecond),
					healthySample(1000 * time.Millisecond),
				},
			},
			want: 3 * time.Second,
		},
		{
			name:    "unreachable observations count as failures",
			ceiling: ceiling,
			history: SiteHistory{Observations: []SiteObservation{
				{Reachable: false},
				{Reachable: false},
			}},
			want: ceiling / adaptiveFailFastDivisor,
		},
		{
			name:    "a sub-floor ceiling is honored over the policy floor",
			ceiling: time.Second,
			history: SiteHistory{Observations: []SiteObservation{
				healthySample(50 * time.Millisecond),
				healthySample(60 * time.Millisecond),
			}},
			want: time.Second,
		},
		{
			name:    "a sub-floor ceiling is honored on the fail-fast branch too",
			ceiling: time.Second,
			history: SiteHistory{Observations: []SiteObservation{
				timeoutSample(),
				timeoutSample(),
			}},
			want: time.Second,
		},
		{
			name:    "a zero ceiling is returned untouched",
			ceiling: 0,
			history: SiteHistory{Observations: []SiteObservation{healthySample(time.Second)}},
			want:    0,
		},
		{
			name:    "a negative ceiling is returned untouched",
			ceiling: -time.Second,
			history: SiteHistory{Observations: []SiteObservation{healthySample(time.Second)}},
			want:    -time.Second,
		},
		{
			name:    "healthy samples with a zero response time are not usable evidence",
			ceiling: ceiling,
			history: SiteHistory{Observations: []SiteObservation{
				{Reachable: true},
				{Reachable: true},
				{Reachable: true},
			}},
			want: ceiling,
		},
		{
			name:    "the preclassify ceiling is respected like any other",
			ceiling: 15 * time.Second,
			history: SiteHistory{Observations: []SiteObservation{
				healthySample(200 * time.Millisecond),
				healthySample(250 * time.Millisecond),
			}},
			want: adaptiveTimeoutFloor,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := AdaptiveTimeout(testCase.ceiling, testCase.history)
			if got != testCase.want {
				t.Fatalf("AdaptiveTimeout(%v, %+v) = %v, want %v",
					testCase.ceiling, testCase.history, got, testCase.want)
			}
		})
	}
}

// TestAdaptiveTimeoutNeverExceedsCeiling is the envelope invariant: for every
// ceiling and every shape of history, the policy can only ever spend less.
func TestAdaptiveTimeoutNeverExceedsCeiling(t *testing.T) {
	t.Parallel()

	ceilings := []time.Duration{
		time.Millisecond, 500 * time.Millisecond, time.Second,
		adaptiveTimeoutFloor, 5 * time.Second, 10 * time.Second,
		15 * time.Second, 60 * time.Second,
	}
	histories := []SiteHistory{
		{},
		{LastStatus: "unknown"},
		{Observations: []SiteObservation{healthySample(time.Nanosecond)}},
		{Observations: []SiteObservation{healthySample(time.Hour), healthySample(time.Hour)}},
		{Observations: []SiteObservation{timeoutSample(), timeoutSample(), timeoutSample()}},
		{Observations: []SiteObservation{failureSample(), healthySample(time.Hour)}},
		{LastStatus: "error", Observations: []SiteObservation{healthySample(time.Millisecond)}},
		{Observations: []SiteObservation{
			healthySample(24 * time.Hour), healthySample(24 * time.Hour), timeoutSample(),
		}},
	}

	for _, ceiling := range ceilings {
		for _, history := range histories {
			got := AdaptiveTimeout(ceiling, history)
			if got > ceiling {
				t.Fatalf("AdaptiveTimeout(%v, %+v) = %v, which exceeds the ceiling", ceiling, history, got)
			}
			if got <= 0 {
				t.Fatalf("AdaptiveTimeout(%v, %+v) = %v, want a usable positive budget", ceiling, history, got)
			}
		}
	}
}

func TestIsTimeoutErrorClassifiesPersistedAuditText(t *testing.T) {
	t.Parallel()

	timeouts := []string{
		"context deadline exceeded",
		`Get "https://example.com": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`,
		"net/http: TLS handshake timeout",
		"dial tcp 1.2.3.4:443: i/o timeout",
		"read tcp: connection timed out",
	}
	for _, message := range timeouts {
		if !IsTimeoutError(message) {
			t.Fatalf("IsTimeoutError(%q) = false, want true", message)
		}
	}

	others := []string{
		"",
		"   ",
		"dial tcp: lookup example.com: no such host",
		"connection refused",
		"x509: certificate signed by unknown authority",
	}
	for _, message := range others {
		if IsTimeoutError(message) {
			t.Fatalf("IsTimeoutError(%q) = true, want false", message)
		}
	}
}
