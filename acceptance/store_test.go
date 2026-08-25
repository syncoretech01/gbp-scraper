package acceptance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestStoreSaveWritesJSONSummaryAndLog(t *testing.T) {
	fake := newFakeServer(t, defaultScenario())
	client := newClientFor(t, fake)
	record, err := runFast(client, testConfig())
	if err != nil {
		t.Fatalf("runFast: %v", err)
	}

	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	paths, err := store.Save(record)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// JSON round-trips back to an equal record.
	raw, err := os.ReadFile(paths.JSON)
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	var reloaded ExperimentRecord
	if err := json.Unmarshal(raw, &reloaded); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	if reloaded.Run.JobID != record.Run.JobID || reloaded.Outcomes.DiscoveredRows != record.Outcomes.DiscoveredRows {
		t.Errorf("round-trip mismatch: %+v", reloaded.Outcomes)
	}

	// Summary is human-readable and mentions the experiment.
	summary, err := os.ReadFile(paths.Summary)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !strings.Contains(string(summary), "Experiment T") {
		t.Errorf("summary missing header: %s", summary)
	}

	// Durable log has exactly one JSON line.
	log, err := os.ReadFile(filepath.Join(dir, recordsLogName))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(lines) != 1 {
		t.Fatalf("records.jsonl lines = %d, want 1", len(lines))
	}
	var logged ExperimentRecord
	if err := json.Unmarshal([]byte(lines[0]), &logged); err != nil {
		t.Fatalf("log line invalid json: %v", err)
	}
}

func TestSameConfigProducesSameRecordedShape(t *testing.T) {
	fake := newFakeServer(t, defaultScenario())
	client := newClientFor(t, fake)

	first, err := runFast(client, testConfig())
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	second, err := runFast(client, testConfig())
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}

	if shapeOf(t, first) != shapeOf(t, second) {
		t.Errorf("two runs of the same config produced different record shapes")
	}
}

func TestRepeatabilityAcrossIdenticalRunsHasZeroVariance(t *testing.T) {
	fake := newFakeServer(t, defaultScenario())
	client := newClientFor(t, fake)

	config := testConfig()
	config.Repeat = 3
	records, report, err := RunRepeated(context.Background(), client, config, RunOptions{
		PollInterval: time.Millisecond, MaxWait: time.Minute, SampleResources: true,
		sleep: func(context.Context, time.Duration) {},
	})
	if err != nil {
		t.Fatalf("RunRepeated: %v", err)
	}
	if len(records) != 3 || report.Repeats != 3 {
		t.Fatalf("records=%d repeats=%d, want 3", len(records), report.Repeats)
	}

	// Deterministic fake => identical runs => zero variance and CV.
	for _, name := range []string{"unique_businesses", "rows_per_minute", "browser_failure_rate", "final_effective_concurrency"} {
		stats := report.Variance[name]
		if stats.N != 3 {
			t.Errorf("%s N = %d, want 3", name, stats.N)
		}
		if stats.StdDev != 0 || stats.CV != 0 {
			t.Errorf("%s stddev=%v cv=%v, want 0 for identical runs", name, stats.StdDev, stats.CV)
		}
	}
	if report.Variance["unique_businesses"].Mean != 100 {
		t.Errorf("unique_businesses mean = %v, want 100", report.Variance["unique_businesses"].Mean)
	}
}

func TestRepeatabilityVarianceOfDifferingRuns(t *testing.T) {
	records := []ExperimentRecord{
		{Experiment: "X", Outcomes: recordedOutcome{UniqueBusinesses: 90}},
		{Experiment: "X", Outcomes: recordedOutcome{UniqueBusinesses: 110}},
	}
	report := Repeatability(records)
	stats := report.Variance["unique_businesses"]
	if stats.Mean != 100 {
		t.Errorf("mean = %v, want 100", stats.Mean)
	}
	if stats.Min != 90 || stats.Max != 110 {
		t.Errorf("range = [%v,%v], want [90,110]", stats.Min, stats.Max)
	}
	// Population stddev of {90,110} about mean 100 is 10; CV = 0.1.
	if stats.StdDev != 10 {
		t.Errorf("stddev = %v, want 10", stats.StdDev)
	}
	if stats.CV != 0.1 {
		t.Errorf("cv = %v, want 0.1", stats.CV)
	}
}

// shapeOf returns a stable string describing the JSON key structure of a
// record, ignoring values, so two records can be compared for identical shape.
func shapeOf(t *testing.T, record ExperimentRecord) string {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	paths := []string{}
	collectPaths("", generic, &paths)
	sort.Strings(paths)

	return strings.Join(paths, "\n")
}

func collectPaths(prefix string, value any, paths *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			collectPaths(prefix+"."+key, child, paths)
		}
	case []any:
		// Record the array container; element shapes vary by count, not schema.
		*paths = append(*paths, prefix+"[]")
	default:
		*paths = append(*paths, prefix)
	}
}
