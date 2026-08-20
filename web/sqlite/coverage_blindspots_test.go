package sqlite

import (
	"context"
	"testing"

	"github.com/gosom/google-maps-scraper/web"
)

func TestJobCoverageTasksReadsTheTruncationSignal(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "coverage-truncation")
	defer closeDatabase()

	ctx := context.Background()
	coverageTestPlan(t, repository, "coverage-truncation-job")

	if _, err := repository.StartJobTask(ctx, "coverage-truncation-job", "task-a"); err != nil {
		t.Fatalf("start truncated task: %v", err)
	}

	if err := repository.CompleteJobTask(ctx, "coverage-truncation-job", "task-a", web.JobTaskCheckpoint{
		RowsAdded: 120, Truncated: true, TruncationCap: 120,
	}); err != nil {
		t.Fatalf("complete truncated task: %v", err)
	}

	if _, err := repository.StartJobTask(ctx, "coverage-truncation-job", "task-b"); err != nil {
		t.Fatalf("start plain task: %v", err)
	}

	// A checkpoint written without the signal must read back as unset, not
	// as an error and not as true.
	if err := repository.CompleteJobTask(ctx, "coverage-truncation-job", "task-b", web.JobTaskCheckpoint{
		RowsAdded: 3,
	}); err != nil {
		t.Fatalf("complete plain task: %v", err)
	}

	rows, err := repository.JobCoverageTasks(ctx, "coverage-truncation-job")
	if err != nil {
		t.Fatalf("read coverage tasks: %v", err)
	}

	if len(rows) != 3 {
		t.Fatalf("coverage rows = %d, want 3", len(rows))
	}

	byKey := make(map[string]web.CoverageTaskRow, len(rows))
	for _, row := range rows {
		byKey[row.TaskKey] = row
	}

	if !byKey["task-a"].Truncated {
		t.Fatalf("task-a = %#v, want the truncation signal", byKey["task-a"])
	}

	if byKey["task-b"].Truncated {
		t.Fatalf("task-b = %#v, want no truncation signal", byKey["task-b"])
	}

	// A task that never ran has no checkpoint at all.
	if byKey["task-c"].Truncated {
		t.Fatalf("task-c = %#v, want no truncation signal", byKey["task-c"])
	}
}

func TestJobCoverageSeedStateCountsRefinementsAgainstTheSharedBudget(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "coverage-appended-budget")
	defer closeDatabase()

	ctx := context.Background()
	coverageTestPlan(t, repository, "coverage-appended-budget-job")

	if _, err := repository.AppendJobTasks(ctx, "coverage-appended-budget-job", []web.JobTaskDefinition{
		{
			Key: "exp-1", Kind: "map-query", Sequence: 3,
			Query: "dentist in Chatham IL 62629", Origin: web.CoverageExpansionOriginPrefix + "62701",
		},
		{
			Key: "ref-1", Kind: "map-query", Sequence: 4,
			Query: "dentist in Springfield IL 62701", Origin: web.CoverageRefinementOriginPrefix + "62701",
		},
	}, 3); err != nil {
		t.Fatalf("append tasks: %v", err)
	}

	state, err := repository.JobCoverageSeedState(ctx, "coverage-appended-budget-job")
	if err != nil {
		t.Fatalf("read seed state: %v", err)
	}

	// Both kinds of appended task draw on MaxExpansions, so a restart must
	// see both or it would hand out a fresh budget.
	if state.ExpansionTasks != 2 {
		t.Fatalf("appended tasks = %d, want 2", state.ExpansionTasks)
	}

	if state.MaxSequence != 4 {
		t.Fatalf("max sequence = %d, want 4", state.MaxSequence)
	}
}
