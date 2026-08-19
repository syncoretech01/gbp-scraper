package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

func TestCheckpointTasksResumeOnlyUnfinishedWork(t *testing.T) {
	t.Parallel()
	repository, closeDatabase := newLifecycleTestRepository(t, "checkpoint-resume")
	defer closeDatabase()
	ctx := context.Background()
	job := lifecycleTestJob("checkpoint-job", time.Now().UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}
	definitions := []web.JobTaskDefinition{
		{Key: "query-a/cell-1", Kind: "map-grid-cell", Sequence: 0, Query: "dentists", SourceCell: "cell-1"},
		{Key: "query-a/cell-2", Kind: "map-grid-cell", Sequence: 1, Query: "dentists", SourceCell: "cell-2"},
	}
	pending, err := repository.PrepareJobTasks(ctx, job.ID, definitions, 3)
	if err != nil {
		t.Fatalf("prepare tasks: %v", err)
	}
	if len(pending) != 2 || pending[0].Key != definitions[0].Key || pending[1].Key != definitions[1].Key {
		t.Fatalf("pending tasks = %#v", pending)
	}
	if _, err := repository.StartJobTask(ctx, job.ID, definitions[0].Key); err != nil {
		t.Fatalf("start first task: %v", err)
	}
	if err := repository.CompleteJobTask(ctx, job.ID, definitions[0].Key, web.JobTaskCheckpoint{RowsAdded: 7}); err != nil {
		t.Fatalf("complete first task: %v", err)
	}

	pending, err = repository.PrepareJobTasks(ctx, job.ID, definitions, 3)
	if err != nil {
		t.Fatalf("prepare resumed tasks: %v", err)
	}
	if len(pending) != 1 || pending[0].Key != definitions[1].Key {
		t.Fatalf("resumed pending tasks = %#v", pending)
	}
	snapshot, err := repository.GetJobExecution(ctx, job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if snapshot.Tasks.Total != 2 || snapshot.Tasks.Completed != 1 || snapshot.Tasks.Pending != 1 {
		t.Fatalf("task summary = %#v", snapshot.Tasks)
	}
	if snapshot.Checkpoint == nil || snapshot.Checkpoint.TaskKey != definitions[0].Key || snapshot.Checkpoint.Payload == nil {
		t.Fatalf("checkpoint = %#v", snapshot.Checkpoint)
	}
}

func TestCheckpointTaskRetriesUseDurableAttempts(t *testing.T) {
	t.Parallel()
	repository, closeDatabase := newLifecycleTestRepository(t, "checkpoint-attempts")
	defer closeDatabase()
	ctx := context.Background()
	job := lifecycleTestJob("retry-job", time.Now().UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}
	definitions := []web.JobTaskDefinition{{Key: "query", Kind: "map-query", Query: "dentists"}}
	if _, err := repository.PrepareJobTasks(ctx, job.ID, definitions, 2); err != nil {
		t.Fatalf("prepare task: %v", err)
	}
	first, err := repository.StartJobTask(ctx, job.ID, "query")
	if err != nil || first.Attempts != 1 {
		t.Fatalf("first attempt = %#v, %v", first, err)
	}
	if err := repository.FailJobTask(ctx, job.ID, "query", errors.New("browser token=secret crashed"), true, web.JobTaskCheckpoint{}); err != nil {
		t.Fatalf("fail retryable task: %v", err)
	}
	second, err := repository.StartJobTask(ctx, job.ID, "query")
	if err != nil || second.Attempts != 2 {
		t.Fatalf("second attempt = %#v, %v", second, err)
	}
	if err := repository.FailJobTask(ctx, job.ID, "query", errors.New("still failed"), false, web.JobTaskCheckpoint{}); err != nil {
		t.Fatalf("fail exhausted task: %v", err)
	}
	snapshot, err := repository.GetJobExecution(ctx, job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if snapshot.Tasks.Failed != 1 || snapshot.Tasks.Retries != 1 {
		t.Fatalf("retry summary = %#v", snapshot.Tasks)
	}
}

func TestRecoverAbandonedJobsRunsOnEveryLaunchAndIsIdempotent(t *testing.T) {
	t.Parallel()
	repository, closeDatabase := newLifecycleTestRepository(t, "checkpoint-recovery")
	defer closeDatabase()
	ctx := context.Background()
	job := lifecycleTestJob("abandoned-job", time.Now().UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := repository.PrepareJobTasks(ctx, job.ID, []web.JobTaskDefinition{{Key: "cell", Kind: "map-grid-cell"}}, 3); err != nil {
		t.Fatalf("prepare task: %v", err)
	}
	job.Status = web.StatusWorking
	job.Date = time.Now().UTC()
	if err := repository.Update(ctx, &job); err != nil {
		t.Fatalf("start job: %v", err)
	}
	if _, err := repository.StartJobTask(ctx, job.ID, "cell"); err != nil {
		t.Fatalf("start task: %v", err)
	}

	recovered, err := repository.RecoverAbandonedJobs(ctx)
	if err != nil || recovered != 1 {
		t.Fatalf("recover abandoned jobs = %d, %v", recovered, err)
	}
	recovered, err = repository.RecoverAbandonedJobs(ctx)
	if err != nil || recovered != 0 {
		t.Fatalf("repeat recovery = %d, %v", recovered, err)
	}
	runtime, err := repository.GetRuntime(ctx, job.ID)
	if err != nil {
		t.Fatalf("get recovered runtime: %v", err)
	}
	if runtime.State != jobruntime.StatePaused || runtime.OutcomeReason != jobruntime.StopReasonShutdown {
		t.Fatalf("recovered runtime = %#v", runtime)
	}
	snapshot, err := repository.GetJobExecution(ctx, job.ID)
	if err != nil {
		t.Fatalf("get recovered execution: %v", err)
	}
	if !snapshot.RecoveryRequired || snapshot.Tasks.Pending != 1 || snapshot.Tasks.Running != 0 {
		t.Fatalf("recovered execution = %#v", snapshot)
	}
	legacy, err := repository.Get(ctx, job.ID)
	if err != nil || legacy.Status != web.StatusPending {
		t.Fatalf("legacy recovered job = %#v, %v", legacy, err)
	}
}

func TestWorkerProgressPersistsResourceEvidence(t *testing.T) {
	t.Parallel()
	repository, closeDatabase := newLifecycleTestRepository(t, "checkpoint-resources")
	defer closeDatabase()
	ctx := context.Background()
	job := lifecycleTestJob("resource-job", time.Now().UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}
	eta := int64(42)
	progress := web.JobWorkerProgress{
		Stage: jobruntime.StageSearchingMaps, ActiveTasks: 1, PlacesPerMinute: 12.5,
		ETASeconds: &eta, CurrentQuery: "dentists", CurrentCell: "cell-7",
		BrowserCount: 2, ActivePages: 3, CPUPercent: 47.5,
		MemoryBytes: 512 << 20, DiskFreeBytes: 8 << 30, DatabaseWrites: 9,
		DesiredWorkers: 4, EffectiveWorkers: 2, UpdatedAt: time.Now().UTC(),
	}
	if err := repository.UpdateJobWorkerProgress(ctx, job.ID, progress); err != nil {
		t.Fatalf("update progress: %v", err)
	}
	snapshot, err := repository.GetJobExecution(ctx, job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if snapshot.Progress.CurrentQuery != progress.CurrentQuery ||
		snapshot.Progress.CurrentCell != progress.CurrentCell ||
		snapshot.Progress.DiskFreeBytes != progress.DiskFreeBytes ||
		snapshot.Progress.EffectiveWorkers != 2 || snapshot.Progress.ETASeconds == nil ||
		*snapshot.Progress.ETASeconds != eta {
		t.Fatalf("worker progress = %#v", snapshot.Progress)
	}
}
