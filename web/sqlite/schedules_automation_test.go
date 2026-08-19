package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

func saveScheduleAutomationTemplate(t *testing.T, repository *repo, id string, now time.Time) {
	t.Helper()
	template := web.ScrapeTemplate{
		ID: id, Name: "Template " + id, Configuration: validScheduledJobData(),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.SaveScrapeTemplate(context.Background(), template); err != nil {
		t.Fatalf("SaveScrapeTemplate(%s) error = %v", id, err)
	}
}

// scheduleAutomationRecord returns an enabled hourly schedule due at t0.
func scheduleAutomationRecord(id, templateID string, t0 time.Time) web.ScheduleRecord {
	due := t0
	return web.ScheduleRecord{
		ID: id, Name: "Automation " + id, TemplateID: templateID, Timezone: "UTC", Enabled: true,
		Spec: web.ScheduleSpec{
			Recurrence: "hourly", FirstRunAt: t0,
			OverlapPolicy: web.ScheduleOverlapQueue, MissedPolicy: "run_once",
		},
		NextRunAt: &due, CreatedAt: t0, UpdatedAt: t0,
	}
}

func markScheduleAutomationJobRunning(t *testing.T, repository *repo, job web.Job) {
	t.Helper()
	job.Status = web.StatusWorking
	if err := repository.Update(context.Background(), &job); err != nil {
		t.Fatalf("Update(%s) error = %v", job.ID, err)
	}
}

func failScheduleAutomationJob(t *testing.T, repository *repo, job web.Job) {
	t.Helper()
	markScheduleAutomationJobRunning(t, repository, job)
	outcome := jobruntime.Outcome{State: jobruntime.StateFailed, Reason: jobruntime.StopReasonFatalError}
	if _, err := repository.SetOutcome(context.Background(), job.ID, outcome, "fatal scrape error"); err != nil {
		t.Fatalf("SetOutcome(%s failed) error = %v", job.ID, err)
	}
}

func completeScheduleAutomationJob(t *testing.T, repository *repo, job web.Job) {
	t.Helper()
	markScheduleAutomationJobRunning(t, repository, job)
	outcome := jobruntime.Outcome{State: jobruntime.StateCompleted, Reason: jobruntime.StopReasonCompleted}
	if _, err := repository.SetOutcome(context.Background(), job.ID, outcome, "finished"); err != nil {
		t.Fatalf("SetOutcome(%s completed) error = %v", job.ID, err)
	}
}

func insertScheduleAutomationRun(
	t *testing.T,
	repository *repo,
	scheduleID, state string,
	scheduledFor time.Time,
	finishedAt *time.Time,
) {
	t.Helper()
	var finished any
	if finishedAt != nil {
		finished = finishedAt.Unix()
	}
	if _, err := repository.db.Exec(
		"INSERT INTO schedule_runs(schedule_id, state, scheduled_for, finished_at, attempt, error) VALUES (?, ?, ?, ?, 1, '')",
		scheduleID, state, scheduledFor.Unix(), finished,
	); err != nil {
		t.Fatalf("insert schedule run for %s: %v", scheduleID, err)
	}
}

func TestReplaceOverlapPolicyCancelsActiveJobAndQueuesNext(t *testing.T) {
	t.Parallel()

	repository, dataDirectory, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()
	ctx := context.Background()
	svc := web.NewService(repository, dataDirectory)
	t0 := time.Now().UTC().Truncate(time.Second)

	saveScheduleAutomationTemplate(t, repository, "template-replace", t0)
	schedule := scheduleAutomationRecord("schedule-replace", "template-replace", t0)
	schedule.Spec.OverlapPolicy = web.ScheduleOverlapReplace
	if err := repository.SaveSchedule(ctx, schedule); err != nil {
		t.Fatalf("SaveSchedule() error = %v", err)
	}

	first, err := svc.StartDueSchedules(ctx, t0, 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("first StartDueSchedules() = %+v, %v", first, err)
	}

	schedules, err := repository.ListSchedules(ctx)
	if err != nil || len(schedules) != 1 || schedules[0].NextRunAt == nil {
		t.Fatalf("ListSchedules() = %+v, %v", schedules, err)
	}
	due2 := *schedules[0].NextRunAt

	second, err := svc.StartDueSchedules(ctx, due2, 10)
	if err != nil || len(second) != 1 {
		t.Fatalf("second StartDueSchedules() = %+v, %v", second, err)
	}

	runtime, err := repository.GetRuntime(ctx, first[0].ID)
	if err != nil {
		t.Fatalf("GetRuntime() error = %v", err)
	}
	if runtime.State != jobruntime.StateCancelled {
		t.Fatalf("replaced job state = %s, want cancelled", runtime.State)
	}
	newRuntime, err := repository.GetRuntime(ctx, second[0].ID)
	if err != nil || newRuntime.State != jobruntime.StateQueued {
		t.Fatalf("replacement job runtime = %+v, %v", newRuntime, err)
	}

	// The next pass settles the cancelled run into durable history.
	if _, err := svc.StartDueSchedules(ctx, due2.Add(time.Second), 10); err != nil {
		t.Fatalf("settling StartDueSchedules() error = %v", err)
	}
	runs, err := repository.ListScheduleRunsForSchedule(ctx, schedule.ID, 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("ListScheduleRunsForSchedule() = %+v, %v", runs, err)
	}
	if runs[0].State != "queued" || runs[0].JobID != second[0].ID {
		t.Fatalf("newest run = %+v", runs[0])
	}
	if runs[1].State != "cancelled" || runs[1].JobID != first[0].ID || runs[1].FinishedAt == nil {
		t.Fatalf("replaced run = %+v", runs[1])
	}
}

func TestFailedScheduledRunRetriesWithBackoffUntilBudget(t *testing.T) {
	t.Parallel()

	repository, dataDirectory, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()
	ctx := context.Background()
	svc := web.NewService(repository, dataDirectory)
	t0 := time.Now().UTC().Truncate(time.Second)

	saveScheduleAutomationTemplate(t, repository, "template-retry", t0)
	schedule := scheduleAutomationRecord("schedule-retry", "template-retry", t0)
	schedule.RetryCount = 1
	schedule.RetryBackoffSeconds = 30
	if err := repository.SaveSchedule(ctx, schedule); err != nil {
		t.Fatalf("SaveSchedule() error = %v", err)
	}

	first, err := svc.StartDueSchedules(ctx, t0, 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("first StartDueSchedules() = %+v, %v", first, err)
	}
	failScheduleAutomationJob(t, repository, first[0])

	// The failure is observed and a retry is planned, but not started yet.
	t1 := t0.Add(2 * time.Second)
	jobs, err := svc.StartDueSchedules(ctx, t1, 10)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("observing StartDueSchedules() = %+v, %v", jobs, err)
	}
	runs, err := repository.ListScheduleRunsForSchedule(ctx, schedule.ID, 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs after failure = %+v, %v", runs, err)
	}
	retryAt := t1.Add(30 * time.Second)
	if runs[0].State != "retry_pending" || runs[0].Attempt != 2 || runs[0].ScheduledFor.Unix() != retryAt.Unix() {
		t.Fatalf("planned retry = %+v, want retry_pending attempt 2 at %v", runs[0], retryAt)
	}
	if runs[1].State != "failed" || runs[1].Attempt != 1 || runs[1].Error != "fatal scrape error" {
		t.Fatalf("failed run = %+v", runs[1])
	}

	// One second before the backoff elapses nothing may start.
	jobs, err = svc.StartDueSchedules(ctx, retryAt.Add(-time.Second), 10)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("early StartDueSchedules() = %+v, %v", jobs, err)
	}

	// At the backoff boundary the retry attempt starts and attaches its job.
	retryJobs, err := svc.StartDueSchedules(ctx, retryAt, 10)
	if err != nil || len(retryJobs) != 1 {
		t.Fatalf("retry StartDueSchedules() = %+v, %v", retryJobs, err)
	}
	if !strings.Contains(retryJobs[0].Name, "(retry 1)") {
		t.Fatalf("retry job name = %q", retryJobs[0].Name)
	}
	runs, err = repository.ListScheduleRunsForSchedule(ctx, schedule.ID, 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs after retry start = %+v, %v", runs, err)
	}
	if runs[0].State != "queued" || runs[0].Attempt != 2 || runs[0].JobID != retryJobs[0].ID {
		t.Fatalf("started retry run = %+v", runs[0])
	}

	// A second failure exhausts the budget: no further attempt is planned.
	failScheduleAutomationJob(t, repository, retryJobs[0])
	jobs, err = svc.StartDueSchedules(ctx, retryAt.Add(2*time.Second), 10)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("exhausted StartDueSchedules() = %+v, %v", jobs, err)
	}
	runs, err = repository.ListScheduleRunsForSchedule(ctx, schedule.ID, 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs after exhausted budget = %+v, %v", runs, err)
	}
	for _, run := range runs {
		if run.State != "failed" {
			t.Fatalf("run = %+v, want both attempts failed", run)
		}
	}
}

func TestScheduleRunRetentionPrunesOnlyExpiredRunsPerSchedule(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	saveScheduleAutomationTemplate(t, repository, "template-retention", t0)
	limited := scheduleAutomationRecord("schedule-retention-limited", "template-retention", t0)
	limited.Enabled = false
	limited.NextRunAt = nil
	limited.RunsRetentionDays = 2
	keeper := scheduleAutomationRecord("schedule-retention-keeper", "template-retention", t0)
	keeper.Enabled = false
	keeper.NextRunAt = nil
	for _, schedule := range []web.ScheduleRecord{limited, keeper} {
		if err := repository.SaveSchedule(ctx, schedule); err != nil {
			t.Fatalf("SaveSchedule(%s) error = %v", schedule.ID, err)
		}
	}

	old := t0.Add(-3 * 24 * time.Hour)
	veryOld := t0.Add(-30 * 24 * time.Hour)
	recent := t0.Add(-time.Hour)
	insertScheduleAutomationRun(t, repository, limited.ID, "completed", old, &old)
	insertScheduleAutomationRun(t, repository, limited.ID, "completed", recent, &recent)
	insertScheduleAutomationRun(t, repository, limited.ID, "queued", old, nil)
	insertScheduleAutomationRun(t, repository, keeper.ID, "completed", veryOld, &veryOld)

	pruned, err := repository.PruneScheduleRuns(ctx, t0)
	if err != nil {
		t.Fatalf("PruneScheduleRuns() error = %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}

	limitedRuns, err := repository.ListScheduleRunsForSchedule(ctx, limited.ID, 10)
	if err != nil || len(limitedRuns) != 2 {
		t.Fatalf("limited schedule runs = %+v, %v", limitedRuns, err)
	}
	for _, run := range limitedRuns {
		if run.State == "completed" && run.ScheduledFor.Unix() == old.Unix() {
			t.Fatalf("expired run survived: %+v", run)
		}
	}
	keeperRuns, err := repository.ListScheduleRunsForSchedule(ctx, keeper.ID, 10)
	if err != nil || len(keeperRuns) != 1 {
		t.Fatalf("keeper schedule runs = %+v, %v", keeperRuns, err)
	}
}

func TestScheduleAutomationFieldsRoundTrip(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	saveScheduleAutomationTemplate(t, repository, "template-fields", t0)
	schedule := scheduleAutomationRecord("schedule-fields", "template-fields", t0)
	schedule.RetryCount = 3
	schedule.RetryBackoffSeconds = 45
	schedule.AutoExportFormat = "csv"
	schedule.RunsRetentionDays = 7
	if err := repository.SaveSchedule(ctx, schedule); err != nil {
		t.Fatalf("SaveSchedule() error = %v", err)
	}

	got, err := repository.GetSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("GetSchedule() error = %v", err)
	}
	if got.RetryCount != 3 || got.RetryBackoffSeconds != 45 ||
		got.AutoExportFormat != "csv" || got.RunsRetentionDays != 7 {
		t.Fatalf("schedule after save = %+v", got)
	}

	got.RetryCount = 0
	got.RetryBackoffSeconds = 120
	got.AutoExportFormat = ""
	got.RunsRetentionDays = 0
	if err := repository.SaveSchedule(ctx, got); err != nil {
		t.Fatalf("second SaveSchedule() error = %v", err)
	}
	updated, err := repository.GetSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("second GetSchedule() error = %v", err)
	}
	if updated.RetryCount != 0 || updated.RetryBackoffSeconds != 120 ||
		updated.AutoExportFormat != "" || updated.RunsRetentionDays != 0 {
		t.Fatalf("schedule after update = %+v", updated)
	}

	listed, err := repository.ListSchedules(ctx)
	if err != nil || len(listed) != 1 || listed[0].RetryBackoffSeconds != 120 {
		t.Fatalf("ListSchedules() = %+v, %v", listed, err)
	}

	if _, err := repository.GetSchedule(ctx, "missing-schedule"); err == nil {
		t.Fatal("GetSchedule(missing) returned no error")
	}
}

func TestCompletedScheduledRunBuildsAutomaticExport(t *testing.T) {
	t.Parallel()

	repository, dataDirectory, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()
	ctx := context.Background()
	svc := web.NewService(repository, dataDirectory)
	t0 := time.Now().UTC().Truncate(time.Second)

	saveScheduleAutomationTemplate(t, repository, "template-export", t0)
	schedule := scheduleAutomationRecord("schedule-export", "template-export", t0)
	schedule.AutoExportFormat = "csv"
	if err := repository.SaveSchedule(ctx, schedule); err != nil {
		t.Fatalf("SaveSchedule() error = %v", err)
	}

	first, err := svc.StartDueSchedules(ctx, t0, 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("first StartDueSchedules() = %+v, %v", first, err)
	}
	completeScheduleAutomationJob(t, repository, first[0])

	if _, err := svc.StartDueSchedules(ctx, t0.Add(2*time.Second), 10); err != nil {
		t.Fatalf("settling StartDueSchedules() error = %v", err)
	}

	exports, err := repository.ListExports(ctx, 10)
	if err != nil {
		t.Fatalf("ListExports() error = %v", err)
	}
	if len(exports) != 1 {
		t.Fatalf("exports = %+v", exports)
	}
	record := exports[0]
	if record.State != "completed" || record.Format != "csv" ||
		record.SourceType != "schedule" || record.SourceID != schedule.ID || record.RelativePath == "" {
		t.Fatalf("export record = %+v", record)
	}
	exportPath := filepath.Join(dataDirectory, filepath.FromSlash(record.RelativePath))
	if _, err := os.Stat(exportPath); err != nil {
		t.Fatalf("generated export file: %v", err)
	}

	runs, err := repository.ListScheduleRunsForSchedule(ctx, schedule.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].State != "completed" || runs[0].FinishedAt == nil {
		t.Fatalf("runs after completion = %+v, %v", runs, err)
	}

	// A later pass must not build a second export for the same run.
	if _, err := svc.StartDueSchedules(ctx, t0.Add(3*time.Second), 10); err != nil {
		t.Fatalf("repeat StartDueSchedules() error = %v", err)
	}
	exports, err = repository.ListExports(ctx, 10)
	if err != nil || len(exports) != 1 {
		t.Fatalf("exports after repeat pass = %+v, %v", exports, err)
	}
}
