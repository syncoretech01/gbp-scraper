package runner_test

import (
	"strings"
	"testing"

	"github.com/gosom/google-maps-scraper/grid"
	"github.com/gosom/google-maps-scraper/runner"
)

func TestCreateGridSeedJobsRejectsInvalidZoom(t *testing.T) {
	t.Parallel()

	bbox := grid.BoundingBox{
		MinLat: 40.30,
		MinLon: -3.80,
		MaxLat: 40.50,
		MaxLon: -3.60,
	}

	_, err := runner.CreateGridSeedJobs(
		"en",
		strings.NewReader("coffee"),
		10,
		false,
		bbox,
		1.0,
		0,
		nil,
		nil,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "invalid zoom level") {
		t.Fatalf("expected invalid zoom level error, got %v", err)
	}
}

func TestCreateSeedJobsRejectsEmptyQueryBeforeCustomID(t *testing.T) {
	t.Parallel()

	_, err := runner.CreateSeedJobs(
		false,
		"en",
		strings.NewReader("  #!#my-id\n"),
		10,
		false,
		"",
		15,
		10000,
		nil,
		nil,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "empty query text") {
		t.Fatalf("expected empty query text error, got %v", err)
	}
}

func TestCreateGridSeedJobsRejectsEmptyQueryBeforeCustomID(t *testing.T) {
	t.Parallel()

	bbox := grid.BoundingBox{
		MinLat: 40.30,
		MinLon: -3.80,
		MaxLat: 40.50,
		MaxLon: -3.60,
	}

	_, err := runner.CreateGridSeedJobs(
		"en",
		strings.NewReader(" #!#my-id\n"),
		10,
		false,
		bbox,
		1.0,
		15,
		nil,
		nil,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "empty query text") {
		t.Fatalf("expected empty query text error, got %v", err)
	}
}

func TestSeedJobIDsAreStableAcrossRecovery(t *testing.T) {
	t.Parallel()

	// Recovery stability is what the web runner opts into. CLI runners keep the
	// historical random identity; see
	// TestSeedIDsAreRandomByDefaultAndDeterministicOnRequest.

	create := func() []string {
		jobs, err := runner.CreateSeedJobs(
			false, "en", strings.NewReader("dentist\ncoffee\n"), 10, false,
			"37.7749,-122.4194", 14, 10_000, nil, nil, false,
			runner.WithDeterministicSeedIDs(),
		)
		if err != nil {
			t.Fatalf("CreateSeedJobs() error = %v", err)
		}
		ids := make([]string, len(jobs))
		for index, job := range jobs {
			ids[index] = job.GetID()
		}
		return ids
	}
	first, second := create(), create()
	if len(first) != 2 || first[0] == first[1] || first[0] != second[0] || first[1] != second[1] {
		t.Fatalf("deterministic IDs: first=%v second=%v", first, second)
	}
}

func TestSeedIDsAreRandomByDefaultAndDeterministicOnRequest(t *testing.T) {
	t.Parallel()

	const queries = "dentist\ndental clinic\n"

	seedIDs := func(options ...runner.SeedOption) []string {
		t.Helper()

		jobs, err := runner.CreateSeedJobs(
			false, "en", strings.NewReader(queries), 10, false,
			"37.7749,-122.4194", 15, 10000, nil, nil, false, options...,
		)
		if err != nil {
			t.Fatalf("CreateSeedJobs() error = %v", err)
		}

		ids := make([]string, 0, len(jobs))
		for _, job := range jobs {
			ids = append(ids, job.GetID())
		}

		return ids
	}

	// The file, database, and Lambda runners must keep minting a fresh identity
	// per run: a repeated `-produce` has always enqueued new rows, and the
	// `input_id` CSV column has always been per-run.
	first, second := seedIDs(), seedIDs()
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("seed counts = %d and %d, want 2", len(first), len(second))
	}

	for index := range first {
		if first[index] == second[index] {
			t.Fatalf("default seed ID %d repeated across runs: %q", index, first[index])
		}
	}

	// The web runner opts in so a durable checkpoint can recognise a seed that
	// already completed.
	stable, stableAgain := seedIDs(runner.WithDeterministicSeedIDs()), seedIDs(runner.WithDeterministicSeedIDs())
	for index := range stable {
		if stable[index] != stableAgain[index] {
			t.Fatalf("deterministic seed ID %d changed: %q then %q", index, stable[index], stableAgain[index])
		}
	}

	if stable[0] == stable[1] {
		t.Fatalf("distinct queries shared a deterministic seed ID: %q", stable[0])
	}
}

func TestGridSeedIDsAreRandomByDefaultAndDeterministicOnRequest(t *testing.T) {
	t.Parallel()

	bbox, err := grid.ParseBoundingBox("37.708,-122.515,37.833,-122.354")
	if err != nil {
		t.Fatalf("ParseBoundingBox() error = %v", err)
	}

	gridIDs := func(options ...runner.SeedOption) []string {
		t.Helper()

		jobs, err := runner.CreateGridSeedJobs(
			"en", strings.NewReader("dentist\n"), 10, false, bbox, 5, 15, nil, nil, false, options...,
		)
		if err != nil {
			t.Fatalf("CreateGridSeedJobs() error = %v", err)
		}

		ids := make([]string, 0, len(jobs))
		for _, job := range jobs {
			ids = append(ids, job.GetID())
		}

		return ids
	}

	first, second := gridIDs(), gridIDs()
	if len(first) == 0 || len(first) != len(second) {
		t.Fatalf("grid seed counts = %d and %d", len(first), len(second))
	}

	if first[0] == second[0] {
		t.Fatalf("default grid cell ID repeated across runs: %q", first[0])
	}

	stable, stableAgain := gridIDs(runner.WithDeterministicSeedIDs()), gridIDs(runner.WithDeterministicSeedIDs())
	for index := range stable {
		if stable[index] != stableAgain[index] {
			t.Fatalf("deterministic grid cell ID %d changed: %q then %q", index, stable[index], stableAgain[index])
		}
	}
}
