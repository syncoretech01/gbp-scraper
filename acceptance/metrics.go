package acceptance

import (
	"encoding/json"
	"math"
)

// benchmarkFailureKindKeys are the top-level benchmark-report keys the harness
// probes for a structured fine failure-kind breakdown. The classification
// specialist may land such a field under any of these names; the harness binds
// to whichever appears and tolerates all being absent.
var benchmarkFailureKindKeys = []string{"failure_kinds", "failure_kind_counts", "failure_kinds_by_type"}

// failureKindsFromBenchmarkExtra returns the structured fine failure-kind map
// the classification specialist may have landed on the benchmark report, or an
// empty map when no such field is present yet. It never errors on absence.
func failureKindsFromBenchmarkExtra(report benchmarkReport) map[string]int64 {
	for _, key := range benchmarkFailureKindKeys {
		raw, ok := report.Extra[key]
		if !ok || len(raw) == 0 {
			continue
		}
		kinds := map[string]int64{}
		if err := json.Unmarshal(raw, &kinds); err == nil && len(kinds) > 0 {
			return kinds
		}
	}

	return map[string]int64{}
}

const (
	// ratioPrecision rounds derived ratios so two records for identical
	// evidence diff cleanly instead of differing in float noise.
	ratioPrecision = 10000
	secondsPerMin  = 60
)

// blockKinds are the failure kinds that mean a request was refused rather than
// merely degraded. It mirrors the application's own block-event grouping.
var blockKinds = []string{"blocked", "rate-limit", "captcha", "proxy-failure"}

// browserFailureKinds are the failure kinds that mean the browser itself
// failed to run the attempt.
var browserFailureKinds = []string{"browser-failure"}

// safeRatio returns part/whole rounded to ratioPrecision, or 0 when whole is
// not positive, so no metric ever divides by zero.
func safeRatio(part, whole float64) float64 {
	if whole <= 0 {
		return 0
	}

	return roundRatio(part / whole)
}

// roundRatio rounds a value to the stable ratio precision.
func roundRatio(value float64) float64 {
	return math.Round(value*ratioPrecision) / ratioPrecision
}

// rowsPerMinute is discovered rows over the wall-clock minutes of the run.
func rowsPerMinute(rows int64, wallSeconds float64) float64 {
	if wallSeconds <= 0 {
		return 0
	}

	return roundRatio(float64(rows) / (wallSeconds / secondsPerMin))
}

// finishedTasks is the number of tasks that reached a terminal state.
func finishedTasks(tasks taskSummary) int64 {
	return tasks.Completed + tasks.Failed + tasks.Skipped
}

// taskSuccessRate is completed over terminal tasks, as a fraction in [0,1].
func taskSuccessRate(tasks taskSummary) float64 {
	return safeRatio(float64(tasks.Completed), float64(finishedTasks(tasks)))
}

// rateAgainstFinished expresses an event count as a share of itself plus the
// finished tasks, matching the application's block-rate definition. The result
// is a fraction in [0,1] so it stays comparable across run sizes.
func rateAgainstFinished(count, finished int64) float64 {
	denominator := count + finished
	if denominator <= 0 {
		return 0
	}

	return safeRatio(float64(count), float64(denominator))
}

// sumKinds totals the counts of the named kinds present in the map.
func sumKinds(kinds map[string]int64, names []string) int64 {
	var total int64
	for _, name := range names {
		total += kinds[name]
	}

	return total
}

// failureKindsFromBenchmark derives a fine failure-kind map from the coarse
// benchmark failure classes when no worker events were available. It maps the
// benchmark's class vocabulary onto the event-kind vocabulary so downstream
// rate computations behave identically whichever source was used.
func failureKindsFromBenchmark(classes []failureClass) map[string]int64 {
	kinds := map[string]int64{}
	for _, class := range classes {
		switch class.Class {
		case "blocked":
			kinds["blocked"] += class.Count
		case "browser":
			kinds["browser-failure"] += class.Count
		case "proxy":
			kinds["proxy-failure"] += class.Count
		case "timeout":
			kinds["timeout"] += class.Count
		case "network":
			kinds["network-failure"] += class.Count
		default:
			kinds[class.Class] += class.Count
		}
	}

	return kinds
}
