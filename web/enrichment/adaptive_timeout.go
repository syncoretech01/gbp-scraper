package enrichment

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Adaptive timeout policy constants. Every value is a divisor, multiplier, or
// floor applied to the caller's configured ceiling; none of them can raise the
// resulting budget above that ceiling.
const (
	// adaptiveTimeoutFloor is the smallest budget the policy will ever hand
	// back. Below roughly two seconds a healthy host on a slow local link
	// starts to look dead, which would corrupt website_status evidence.
	adaptiveTimeoutFloor = 2 * time.Second
	// adaptiveSafetyFactor multiplies the observed p95 response time. Three
	// times p95 absorbs ordinary jitter and a cold cache without paying for a
	// full ceiling on a host that has always answered quickly.
	adaptiveSafetyFactor = 3
	// adaptiveFailFastDivisor shortens the leash for hosts with a recent
	// failure history. A quarter of the ceiling still lets a merely slow host
	// answer while freeing the worker four times sooner on a dead one.
	adaptiveFailFastDivisor = 4
	// adaptiveMinHealthySamples is how many successful observations the policy
	// needs before it trusts a reduction. One lucky sample is not evidence.
	adaptiveMinHealthySamples = 2
	// adaptiveFailureStreak is the number of consecutive most-recent failures
	// that trips the fail-fast branch on its own.
	adaptiveFailureStreak = 2
	// adaptiveP95 is the quantile taken over successful response times.
	adaptiveP95 = 0.95
)

// SiteObservation is one historical probe outcome for a website host, read
// back from persisted audit evidence. It carries no clock and no identity:
// the adaptive policy only needs how long the probe took and how it ended.
type SiteObservation struct {
	// ResponseTime is the measured wall-clock duration of the probe.
	ResponseTime time.Duration `json:"response_time"`
	// Reachable reports whether the host answered at all.
	Reachable bool `json:"reachable"`
	// TimedOut reports whether the probe ended on a client or context deadline.
	TimedOut bool `json:"timed_out"`
	// Failed reports a transport, DNS, or TLS failure that is not a timeout.
	Failed bool `json:"failed"`
}

// healthy reports whether the observation is a usable latency sample.
func (observation SiteObservation) healthy() bool {
	return observation.Reachable && !observation.TimedOut && !observation.Failed &&
		observation.ResponseTime > 0
}

// unhealthy reports whether the observation is a failure of any kind.
func (observation SiteObservation) unhealthy() bool {
	return !observation.Reachable || observation.TimedOut || observation.Failed
}

// SiteHistory is the bounded observed history for one website host. It is the
// full input the adaptive timeout policy consumes, so a caller with no
// persistence at all can pass the zero value and keep today's behavior.
type SiteHistory struct {
	// Host is the normalized domain the observations belong to. It is
	// informational: the policy never parses or matches on it.
	Host string `json:"host,omitempty"`
	// LastStatus is the persisted businesses.website_status value
	// ("active", "inactive", "error", or "unknown") at readback time.
	LastStatus string `json:"last_status,omitempty"`
	// Observations are the most-recent-first probe outcomes, newest at index
	// zero, already bounded by the repository query.
	Observations []SiteObservation `json:"observations,omitempty"`
}

// Website status values persisted on businesses.website_status. Only the
// failure-flavored ones influence the policy.
const (
	websiteStatusError    = "error"
	websiteStatusInactive = "inactive"
)

// AdaptiveTimeout returns the per-request timeout to use for one website
// probe, given the configured ceiling and the bounded observed history for
// the target host. It is pure and deterministic: no clock, no I/O, no
// randomness, and the same inputs always yield the same budget.
//
// The rule, in order, and why it is shaped this way:
//
//  1. A non-positive ceiling is returned untouched. Normalization owns the
//     defaults; the policy never invents a budget the caller did not ask for.
//  2. An empty history returns the ceiling exactly. A first-ever probe of a
//     host must behave byte-identically to today.
//  3. Two or more consecutive failures at the head of the history, or a
//     failure share of half or more across a window large enough to hold a
//     streak, or a persisted status of "error"/"inactive", take the fail-fast
//     branch: ceiling divided
//     by four, floored. Throughput here is measured in unique businesses
//     audited, not in per-site persistence, so a host that has repeatedly
//     refused to answer earns a *shorter* leash, freeing the worker sooner.
//     Lengthening it would spend the whole envelope on the least promising
//     hosts.
//  4. Fewer than two healthy latency samples returns the ceiling. One fast
//     answer is luck, not a trend, and guessing low would misclassify a live
//     site as dead.
//  5. Otherwise the budget is the p95 of the healthy samples multiplied by a
//     safety factor of three, which absorbs jitter and cold caches while still
//     collapsing a 400ms host from a ten-second ceiling to the floor.
//
// Every branch is clamped into [adaptiveTimeoutFloor, ceiling], and the floor
// itself is capped by the ceiling, so the returned value is never larger than
// the configured ceiling for any input. That invariant is what keeps the
// resource envelope fixed: the policy can only ever spend less.
func AdaptiveTimeout(ceiling time.Duration, history SiteHistory) time.Duration {
	if ceiling <= 0 {
		return ceiling
	}

	observations := history.Observations
	if len(observations) == 0 {
		return ceiling
	}

	if adaptiveShouldFailFast(history) {
		return clampAdaptiveTimeout(ceiling/adaptiveFailFastDivisor, ceiling)
	}

	samples := healthyLatencies(observations)
	if len(samples) < adaptiveMinHealthySamples {
		return ceiling
	}

	return clampAdaptiveTimeout(quantileDuration(samples, adaptiveP95)*adaptiveSafetyFactor, ceiling)
}

// adaptiveShouldFailFast reports whether the observed history is bad enough
// that shortening the leash frees more worker time than it loses evidence.
func adaptiveShouldFailFast(history SiteHistory) bool {
	switch strings.TrimSpace(strings.ToLower(history.LastStatus)) {
	case websiteStatusError, websiteStatusInactive:
		return true
	}

	streak := 0
	failures := 0
	for index, observation := range history.Observations {
		if !observation.unhealthy() {
			continue
		}
		failures++
		if index == streak {
			streak++
		}
	}

	if streak >= adaptiveFailureStreak {
		return true
	}

	// Half or more of a bounded window failing is a persistent problem rather
	// than one unlucky probe. A window too small to hold a streak cannot show
	// persistence at all, so it is left at the configured ceiling.
	if len(history.Observations) < adaptiveFailureStreak {
		return false
	}

	return failures*2 >= len(history.Observations)
}

// healthyLatencies returns the ascending successful response times.
func healthyLatencies(observations []SiteObservation) []time.Duration {
	samples := make([]time.Duration, 0, len(observations))
	for _, observation := range observations {
		if observation.healthy() {
			samples = append(samples, observation.ResponseTime)
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })

	return samples
}

// quantileDuration returns the nearest-rank quantile of an ascending slice.
// The slice must not be empty.
func quantileDuration(ascending []time.Duration, quantile float64) time.Duration {
	rank := int(math.Ceil(quantile * float64(len(ascending))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(ascending) {
		rank = len(ascending)
	}

	return ascending[rank-1]
}

// clampAdaptiveTimeout confines a candidate budget to the policy floor and the
// configured ceiling. The ceiling always wins, including over the floor, so a
// caller configuring a sub-floor ceiling still gets exactly what it asked for.
func clampAdaptiveTimeout(candidate, ceiling time.Duration) time.Duration {
	floor := adaptiveTimeoutFloor
	if floor > ceiling {
		floor = ceiling
	}
	if candidate < floor {
		candidate = floor
	}
	if candidate > ceiling {
		candidate = ceiling
	}

	return candidate
}

// Timeout error fragments observed from net/http and context deadlines. They
// are matched case-insensitively against persisted audit error text.
var timeoutErrorFragments = []string{
	"context deadline exceeded",
	"client.timeout exceeded",
	"timeout awaiting response headers",
	"tls handshake timeout",
	"i/o timeout",
	"timed out",
	"timeout",
}

// IsTimeoutError reports whether persisted audit error text describes a
// deadline rather than a refusal, a DNS miss, or a TLS rejection. Repositories
// use it to classify stored evidence into SiteObservation flags without
// keeping the original error value.
func IsTimeoutError(message string) bool {
	lowered := strings.ToLower(strings.TrimSpace(message))
	if lowered == "" {
		return false
	}
	for _, fragment := range timeoutErrorFragments {
		if strings.Contains(lowered, fragment) {
			return true
		}
	}

	return false
}
