package web

import (
	"context"
	"strings"
	"testing"
)

// acceptanceCoverageTotals is the durable checkpoint sum of job 7100e95b: 180
// finished searches that between them accepted 555 observations, 224 of which
// re-found a business an earlier search had already collected, leaving the 331
// businesses the result file holds.
func acceptanceCoverageTotals() CoverageTotals {
	return CoverageTotals{
		TasksTotal: 180, TasksDone: 180,
		RowsAdded: 555, RowsReplaced: 224, DuplicatesSkipped: 0,
	}
}

func TestNewRunObservationsSeparatesObservationsFromBusinesses(t *testing.T) {
	observations := NewRunObservations(acceptanceCoverageTotals())

	if !observations.Available {
		t.Fatal("a plan with 180 finished searches reports no observations")
	}
	if observations.Observations != 555 {
		t.Fatalf("observations = %d, want 555", observations.Observations)
	}
	if observations.RepeatObservations != 224 {
		t.Fatalf("repeat observations = %d, want 224", observations.RepeatObservations)
	}
	if observations.UniqueBusinesses != 331 {
		t.Fatalf("unique businesses = %d, want 331 (555 - 224)", observations.UniqueBusinesses)
	}
	if got := observations.RepeatSharePercent(); got != 40.4 {
		t.Fatalf("repeat share = %.1f%%, want 40.4%%", got)
	}
}

// TestRunObservationsDistinguishRepeatsFromEntityMerges is the regression for
// issue P: a run may not describe checkpoint replacement as a merge.
func TestRunObservationsDistinguishRepeatsFromEntityMerges(t *testing.T) {
	observations := NewRunObservations(acceptanceCoverageTotals())

	if observations.HasEntityMerges {
		t.Fatal("entity merges claimed before any merge evidence was supplied")
	}

	observations = observations.WithEntityMerges(0).WithUnresolvedDuplicates(33)
	if !observations.HasEntityMerges || observations.EntityMerges != 0 {
		t.Fatalf("entity merges = %d (known %v), want a known zero",
			observations.EntityMerges, observations.HasEntityMerges)
	}
	if observations.RepeatObservations == observations.EntityMerges {
		t.Fatal("224 repeated observations and 0 entity merges must not be the same number")
	}
	if observations.UnresolvedDuplicates != 33 {
		t.Fatalf("unresolved duplicates = %d, want 33", observations.UnresolvedDuplicates)
	}
}

func TestRunObservationsUnavailableBeforeAnySearchFinishes(t *testing.T) {
	observations := NewRunObservations(CoverageTotals{TasksTotal: 180})

	if observations.Available {
		t.Fatal("a plan with no finished search reported observation counts")
	}
}

func TestRunObservationsPreferTheCommittedBusinessCount(t *testing.T) {
	// A run resumed from an earlier attempt's checkpoint can replace rows it
	// did not itself write, so the subtraction understates what was kept. The
	// committed file wins.
	observations := NewRunObservations(CoverageTotals{TasksDone: 4, RowsAdded: 40, RowsReplaced: 39}).
		WithUniqueBusinesses(120)

	if observations.UniqueBusinesses != 120 {
		t.Fatalf("unique businesses = %d, want the committed 120", observations.UniqueBusinesses)
	}
	if observations.Observations != 40 || observations.RepeatObservations != 39 {
		t.Fatal("committing a business count must not rewrite the observation counts")
	}
}

// TestDeduplicatingMetricsUseTheHonestVocabulary is the regression for the
// exact monitor wording issue P reported: "555 rows added, 224 rows replaced,
// 331 final businesses, Duplicates merged 0".
func TestDeduplicatingMetricsUseTheHonestVocabulary(t *testing.T) {
	input := jobPipelineInput{
		Stats:      ResultStats{Rows: 331, UniqueBusinesses: 331, Duplicates: 0},
		Coverage:   acceptanceCoverageTotals(),
		Facts:      JobPipelineFacts{Merged: 0},
		HasFacts:   true,
		RawRecords: 331,
	}

	byLabel := map[string]jobPipelineMetric{}
	for _, metric := range deduplicatingMetrics(input) {
		if metric.Group != pipelineGroupRunCount {
			t.Fatalf("metric %q is not tagged with the run-count vocabulary", metric.Label)
		}
		byLabel[metric.Label] = metric
	}

	if got := byLabel["Maps observations"].Value; got != "555" {
		t.Fatalf("Maps observations = %q, want 555", got)
	}
	if got := byLabel["Repeated observations"].Value; !strings.HasPrefix(got, "224 ") {
		t.Fatalf("Repeated observations = %q, want it to lead with 224", got)
	}
	if got := byLabel["Unique businesses"].Value; got != "331" {
		t.Fatalf("Unique businesses = %q, want 331", got)
	}
	if got := byLabel["Entity merges"].Value; got != "0" {
		t.Fatalf("Entity merges = %q, want 0", got)
	}

	// The old vocabulary must be gone: no metric may present replacement or
	// file-level repetition as a merge.
	for label := range byLabel {
		lowered := strings.ToLower(label)
		if strings.Contains(lowered, "merge") && label != "Entity merges" {
			t.Fatalf("metric %q still calls something other than an entity merge a merge", label)
		}
		if strings.Contains(lowered, "raw record") || strings.Contains(lowered, "duplicate match") {
			t.Fatalf("metric %q still uses the old ambiguous wording", label)
		}
	}
}

func TestDeduplicatingMetricsReportMissingEvidenceRatherThanZero(t *testing.T) {
	input := jobPipelineInput{Stats: ResultStats{UniqueBusinesses: 12}}

	for _, metric := range deduplicatingMetrics(input) {
		switch metric.Label {
		case "Maps observations", "Repeated observations", "Entity merges",
			"Unresolved duplicate candidates":
			if metric.Value != notReported {
				t.Fatalf("%q reported %q with no evidence, want %q",
					metric.Label, metric.Value, notReported)
			}
		case "Unique businesses":
			if metric.Value != "12" {
				t.Fatalf("unique businesses = %q, want the committed 12", metric.Value)
			}
		}
	}
}

func TestEveryRunCountMetricCarriesItsDefinition(t *testing.T) {
	input := jobPipelineInput{
		Stats:    ResultStats{UniqueBusinesses: 331},
		Coverage: acceptanceCoverageTotals(),
		Facts:    JobPipelineFacts{Merged: 0},
		HasFacts: true,
	}

	for _, metric := range deduplicatingMetrics(input) {
		if strings.TrimSpace(metric.Note) == "" {
			t.Fatalf("metric %q ships without the definition that makes it honest", metric.Label)
		}
	}
}

func TestResultStatsDuplicatesAreFileLocalAndDocumented(t *testing.T) {
	// Two rows for the same place: the file itself repeats a business. This is
	// what ResultStats.Duplicates counts, and it is the only thing it counts.
	csv := strings.Join([]string{
		"title,latitude,longitude,place_id",
		"Ink One,34.05,-118.25,p1",
		"Ink One,34.05,-118.25,p1",
		"Ink Two,34.06,-118.26,p2",
	}, "\n") + "\n"

	stats, err := summarizeResults(context.Background(), strings.NewReader(csv))
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}

	if stats.Rows != 3 || stats.UniqueBusinesses != 2 || stats.Duplicates != 1 {
		t.Fatalf("rows/unique/duplicates = %d/%d/%d, want 3/2/1",
			stats.Rows, stats.UniqueBusinesses, stats.Duplicates)
	}
}
