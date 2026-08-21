package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// JobPipelineFacts joins four tables the live monitor depends on. A silent
// failure there degrades every pipeline stage to "not reported yet", so the
// query is exercised against a real schema with seeded rows.
func TestJobPipelineFactsAggregatesPlanEventsAndBusinesses(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	job := lifecycleTestJob("job-pipeline-facts", time.Now().UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("CreateWithState() error = %v", err)
	}

	if _, err := repository.PrepareJobTasks(ctx, job.ID, []web.JobTaskDefinition{
		{Key: "t-1", Kind: "search", Sequence: 1, Query: "dentists", SourceCell: "cell-a"},
		{Key: "t-2", Kind: "search", Sequence: 2, Query: "dentists", SourceCell: "cell-b"},
		{Key: "t-3", Kind: "search", Sequence: 3, Query: "orthodontists", SourceCell: "cell-a"},
	}, 3); err != nil {
		t.Fatalf("PrepareJobTasks() error = %v", err)
	}

	if _, err := repository.StartJobTask(ctx, job.ID, "t-1"); err != nil {
		t.Fatalf("StartJobTask() error = %v", err)
	}
	if err := repository.CompleteJobTask(ctx, job.ID, "t-1", web.JobTaskCheckpoint{
		State: "completed", RowsAdded: 4,
	}); err != nil {
		t.Fatalf("CompleteJobTask() error = %v", err)
	}

	for _, event := range []struct {
		kind     string
		severity string
	}{
		{kind: "proxy-failure", severity: "warning"},
		{kind: "proxy-failure", severity: "warning"},
		{kind: "browser-failure", severity: "warning"},
		{kind: "task-commit-failed", severity: "error"},
		{kind: "task-pool", severity: "information"},
	} {
		if err := repository.RecordJobWorkerEvent(
			ctx, job.ID, event.kind, event.severity, "seeded "+event.kind, nil,
		); err != nil {
			t.Fatalf("RecordJobWorkerEvent(%s) error = %v", event.kind, err)
		}
	}

	// Businesses reach a job through business_sources, which the legacy CSV
	// import writes; using it keeps the fixture honest about the real path.
	importJob := job
	path := filepath.Join(t.TempDir(), "pipeline.csv")
	writeLegacyResultRows(t, path, map[string]string{
		"title": "Harbor Dental", "category": "Dentist", "place_id": "pipeline-place",
		"address": "1 Market St, San Francisco, CA 94105, United States",
		"website": "https://harbordental.example", "phone": "+1 415 555 0100",
		"emails": "[hello@harbordental.example]", "review_rating": "4.6",
	})
	if _, err := repository.ImportLegacyCSV(ctx, importJob, path); err != nil {
		t.Fatalf("ImportLegacyCSV() error = %v", err)
	}

	facts, err := repository.JobPipelineFacts(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobPipelineFacts() error = %v", err)
	}

	if facts.QueriesPlanned != 2 {
		t.Fatalf("queries planned = %d, want 2 distinct queries", facts.QueriesPlanned)
	}
	if facts.CellsPlanned != 2 {
		t.Fatalf("cells planned = %d, want 2 distinct cells", facts.CellsPlanned)
	}
	if facts.TasksTotal != 3 || facts.TasksCompleted != 1 {
		t.Fatalf("task counters = %#v", facts)
	}
	if facts.EventsByType["proxy-failure"] != 2 || facts.EventsByType["browser-failure"] != 1 {
		t.Fatalf("events by type = %#v", facts.EventsByType)
	}
	if facts.Warnings != 3 || facts.Errors != 1 {
		t.Fatalf("severity totals = %d warnings, %d errors", facts.Warnings, facts.Errors)
	}
	if facts.BlockEvents() != 2 {
		t.Fatalf("block events = %d, want the two proxy failures", facts.BlockEvents())
	}
	// Two blocks against one finished task: 2 / (2 + 1) = 66.7%.
	if rate := facts.BlockRatePercent(); rate < 66 || rate > 67 {
		t.Fatalf("block rate = %.2f, want about 66.7", rate)
	}
	// The imported row reaches the job through business_sources, carries a
	// website, and carries a normalized phone.
	if facts.UniqueBusinesses != 1 || facts.WithWebsite != 1 || facts.WithPhone != 1 {
		t.Fatalf("business facts = %#v", facts)
	}
	if facts.Merged != 0 {
		t.Fatalf("merged = %d, want 0 for a single imported business", facts.Merged)
	}
}

func TestJobPipelineFactsAreEmptyForAJobWithNoEvidence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	job := lifecycleTestJob("job-pipeline-empty", time.Now().UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateDraft); err != nil {
		t.Fatalf("CreateWithState() error = %v", err)
	}

	facts, err := repository.JobPipelineFacts(ctx, job.ID)
	if err != nil {
		t.Fatalf("JobPipelineFacts() error = %v", err)
	}

	if facts.TasksTotal != 0 || facts.UniqueBusinesses != 0 || facts.WebsitesChecked != 0 {
		t.Fatalf("a job with no evidence reported counters: %#v", facts)
	}
	if facts.BlockRatePercent() != 0 {
		t.Fatalf("block rate without evidence = %.2f, want 0", facts.BlockRatePercent())
	}
	// Creating a job writes an informational event; it must not be counted as
	// a warning or an error.
	if facts.Errors != 0 || facts.Warnings != 0 {
		t.Fatalf("severity totals = %d warnings, %d errors, want 0/0", facts.Warnings, facts.Errors)
	}
}
