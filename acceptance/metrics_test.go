package acceptance

import (
	"math"
	"testing"
)

func TestSafeRatio(t *testing.T) {
	if got := safeRatio(1, 0); got != 0 {
		t.Errorf("division by zero = %v, want 0", got)
	}
	if got := safeRatio(1, 3); math.Abs(got-0.3333) > 0.0001 {
		t.Errorf("1/3 = %v, want ~0.3333", got)
	}
}

func TestRowsPerMinute(t *testing.T) {
	if got := rowsPerMinute(130, 120); got != 65 {
		t.Errorf("rows/min = %v, want 65", got)
	}
	if got := rowsPerMinute(10, 0); got != 0 {
		t.Errorf("zero wall = %v, want 0", got)
	}
}

func TestTaskSuccessRate(t *testing.T) {
	tasks := taskSummary{Completed: 40, Failed: 6, Skipped: 2}
	if got := taskSuccessRate(tasks); math.Abs(got-40.0/48.0) > 0.0001 {
		t.Errorf("success rate = %v", got)
	}
	if got := taskSuccessRate(taskSummary{}); got != 0 {
		t.Errorf("empty success rate = %v, want 0", got)
	}
}

func TestRateAgainstFinished(t *testing.T) {
	if got := rateAgainstFinished(3, 48); math.Abs(got-3.0/51.0) > 0.0001 {
		t.Errorf("rate = %v, want ~0.0588", got)
	}
	if got := rateAgainstFinished(0, 0); got != 0 {
		t.Errorf("empty rate = %v, want 0", got)
	}
}

func TestSumKinds(t *testing.T) {
	kinds := map[string]int64{"blocked": 2, "rate-limit": 1, "browser-failure": 3, "timeout": 1}
	if got := sumKinds(kinds, blockKinds); got != 3 { // blocked + rate-limit
		t.Errorf("block sum = %d, want 3", got)
	}
	if got := sumKinds(kinds, browserFailureKinds); got != 3 {
		t.Errorf("browser sum = %d, want 3", got)
	}
}

func TestFailureKindsFromBenchmarkMapping(t *testing.T) {
	classes := []failureClass{
		{Class: "browser", Count: 4},
		{Class: "blocked", Count: 2},
		{Class: "proxy", Count: 1},
		{Class: "timeout", Count: 3},
		{Class: "other", Count: 5},
	}
	kinds := failureKindsFromBenchmark(classes)
	if kinds["browser-failure"] != 4 || kinds["blocked"] != 2 || kinds["proxy-failure"] != 1 ||
		kinds["timeout"] != 3 || kinds["other"] != 5 {
		t.Errorf("mapped kinds = %v", kinds)
	}
}

func TestSummariseStats(t *testing.T) {
	stats := summarise([]float64{90, 110})
	if stats.Mean != 100 || stats.Min != 90 || stats.Max != 110 {
		t.Errorf("stats = %+v", stats)
	}
	if stats.StdDev != 10 || stats.CV != 0.1 {
		t.Errorf("stddev/cv = %v/%v", stats.StdDev, stats.CV)
	}
	empty := summarise(nil)
	if empty.N != 0 || empty.Mean != 0 {
		t.Errorf("empty stats = %+v", empty)
	}
}
