package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// A schedule carrying an incremental mode stamps it onto every run it starts,
// overriding whatever mode the template stored. A schedule without one leaves
// the template untouched, which is the historical behaviour.
func TestScheduleStampsIncrementalModeOnEveryRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository := newScheduleTestRepo(t)
	t0 := time.Now().UTC().Truncate(time.Second)

	template := web.ScrapeTemplate{
		ID: "template-incremental", Name: "Weekly rescan",
		Configuration: validScheduledJobData(), CreatedAt: t0, UpdatedAt: t0,
	}
	if err := repository.SaveScrapeTemplate(ctx, template); err != nil {
		t.Fatalf("SaveScrapeTemplate() error = %v", err)
	}

	incremental := scheduleAutomationRecord("schedule-incremental", template.ID, t0)
	incremental.Spec.IncrementalMode = web.IncrementalModeNewChanged
	plain := scheduleAutomationRecord("schedule-plain", template.ID, t0)
	for _, schedule := range []web.ScheduleRecord{incremental, plain} {
		if err := repository.SaveSchedule(ctx, schedule); err != nil {
			t.Fatalf("SaveSchedule(%s) error = %v", schedule.ID, err)
		}
	}

	// The stored mode must survive the configuration round trip before it can
	// possibly reach a job.
	reloaded, err := repository.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules() error = %v", err)
	}
	var found bool
	for _, schedule := range reloaded {
		if schedule.ID != incremental.ID {
			continue
		}
		found = true
		if schedule.Spec.IncrementalMode != web.IncrementalModeNewChanged {
			t.Fatalf("reloaded incremental mode = %q, want %q",
				schedule.Spec.IncrementalMode, web.IncrementalModeNewChanged)
		}
	}
	if !found {
		t.Fatal("the incremental schedule did not round trip")
	}

	jobs, err := repository.StartDueSchedules(ctx, t0.Add(time.Minute), 10)
	if err != nil {
		t.Fatalf("StartDueSchedules() error = %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("started %d jobs, want 2", len(jobs))
	}
	for _, job := range jobs {
		switch {
		case strings.HasPrefix(job.Name, incremental.Name):
			if job.Data.IncrementalMode != web.IncrementalModeNewChanged {
				t.Errorf("incremental schedule job mode = %q, want %q",
					job.Data.IncrementalMode, web.IncrementalModeNewChanged)
			}
		case strings.HasPrefix(job.Name, plain.Name):
			if job.Data.IncrementalMode != "" {
				t.Errorf("plain schedule job mode = %q, want the template's empty mode",
					job.Data.IncrementalMode)
			}
		default:
			t.Errorf("unexpected scheduled job %q", job.Name)
		}
		if job.Data.TemplateID != template.ID {
			t.Errorf("job %q template link = %q, want %q", job.Name, job.Data.TemplateID, template.ID)
		}
	}
}

// A parameterised template regenerates its query lines on every scheduled run,
// which is what makes "one category applied to many cities" reusable.
func TestScheduleExpandsParameterisedTemplateOnEveryRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository := newScheduleTestRepo(t)
	t0 := time.Now().UTC().Truncate(time.Second)

	configuration := validScheduledJobData()
	configuration.Keywords = []string{"seed query"}
	configuration.Parameters = &web.JobParameters{
		Categories: []string{"dentist", "orthodontist"},
		Locations:  []string{"San Francisco", "Oakland"},
		Replace:    true,
	}
	template := web.ScrapeTemplate{
		ID: "template-parameterised", Name: "Bay Area dental",
		Configuration: configuration, CreatedAt: t0, UpdatedAt: t0,
	}
	if err := repository.SaveScrapeTemplate(ctx, template); err != nil {
		t.Fatalf("SaveScrapeTemplate() error = %v", err)
	}

	schedule := scheduleAutomationRecord("schedule-parameterised", template.ID, t0)
	if err := repository.SaveSchedule(ctx, schedule); err != nil {
		t.Fatalf("SaveSchedule() error = %v", err)
	}

	jobs, err := repository.StartDueSchedules(ctx, t0.Add(time.Minute), 10)
	if err != nil {
		t.Fatalf("StartDueSchedules() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("started %d jobs, want 1", len(jobs))
	}

	got := strings.Join(jobs[0].Data.Keywords, "|")
	want := "dentist in San Francisco|dentist in Oakland|orthodontist in San Francisco|orthodontist in Oakland"
	if got != want {
		t.Fatalf("generated queries = %q, want %q", got, want)
	}

	// Widening the template's location list changes the NEXT run without any
	// query text being edited: that is the whole point of a parameter.
	configuration.Parameters.Locations = append(configuration.Parameters.Locations, "Berkeley")
	template.Configuration = configuration
	if err := repository.SaveScrapeTemplate(ctx, template); err != nil {
		t.Fatalf("SaveScrapeTemplate(update) error = %v", err)
	}
	reloaded, err := repository.GetScrapeTemplate(ctx, template.ID)
	if err != nil {
		t.Fatalf("GetScrapeTemplate() error = %v", err)
	}
	expanded, err := web.ApplyJobParameters(reloaded.Configuration)
	if err != nil {
		t.Fatalf("ApplyJobParameters() error = %v", err)
	}
	if len(expanded.Keywords) != 6 {
		t.Fatalf("widened template expands to %d queries, want 6: %v", len(expanded.Keywords), expanded.Keywords)
	}
}

// Retention removes a run's history row and its job log, and reports the
// exports the service must delete — but never the job, its results, or its CSV.
func TestScheduleRetentionPrunesRunLogsAndReportsExports(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository := newScheduleTestRepo(t)
	t0 := time.Now().UTC().Truncate(time.Second)

	saveScheduleAutomationTemplate(t, repository, "template-retention-logs", t0)
	schedule := scheduleAutomationRecord("schedule-retention-logs", "template-retention-logs", t0)
	schedule.Enabled = false
	schedule.NextRunAt = nil
	schedule.RunsRetentionDays = 2
	if err := repository.SaveSchedule(ctx, schedule); err != nil {
		t.Fatalf("SaveSchedule() error = %v", err)
	}

	expiredJob := web.Job{
		ID: "job-expired", Name: "Expired run", Date: t0.Add(-5 * 24 * time.Hour),
		Status: web.StatusOK, Data: validScheduledJobData(),
	}
	keptJob := web.Job{
		ID: "job-kept", Name: "Recent run", Date: t0.Add(-time.Hour),
		Status: web.StatusOK, Data: validScheduledJobData(),
	}
	for _, job := range []web.Job{expiredJob, keptJob} {
		created := job
		if err := repository.Create(ctx, &created); err != nil {
			t.Fatalf("Create(%s) error = %v", job.ID, err)
		}
	}

	old := t0.Add(-3 * 24 * time.Hour)
	recent := t0.Add(-time.Hour)
	insertScheduleRunForJob(t, repository, schedule.ID, expiredJob.ID, old)
	insertScheduleRunForJob(t, repository, schedule.ID, keptJob.ID, recent)

	for _, jobID := range []string{expiredJob.ID, keptJob.ID} {
		if err := repository.RecordJobWorkerEvent(
			ctx, jobID, "worker", "information", "collected a page", nil,
		); err != nil {
			t.Fatalf("RecordJobWorkerEvent(%s) error = %v", jobID, err)
		}
	}
	insertCompletedJobExport(t, repository, "export-expired", expiredJob.ID)
	insertCompletedJobExport(t, repository, "export-kept", keptJob.ID)

	expiredExports, err := repository.ExpiredScheduleRunExports(ctx, t0)
	if err != nil {
		t.Fatalf("ExpiredScheduleRunExports() error = %v", err)
	}
	if len(expiredExports) != 1 || expiredExports[0] != "export-expired" {
		t.Fatalf("expired exports = %v, want [export-expired]", expiredExports)
	}

	pruned, err := repository.PruneScheduleRuns(ctx, t0)
	if err != nil {
		t.Fatalf("PruneScheduleRuns() error = %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}

	if count := jobEventCount(t, repository, expiredJob.ID); count != 0 {
		t.Fatalf("expired job kept %d log rows, want 0", count)
	}
	if count := jobEventCount(t, repository, keptJob.ID); count == 0 {
		t.Fatal("the retained run lost its job log")
	}

	// The collected data itself is never a retention candidate.
	for _, jobID := range []string{expiredJob.ID, keptJob.ID} {
		if _, err := repository.Get(ctx, jobID); err != nil {
			t.Fatalf("job %s was removed by retention: %v", jobID, err)
		}
	}

	// A second pass is a no-op, which is what makes the scheduler safe to run
	// on every tick.
	again, err := repository.PruneScheduleRuns(ctx, t0)
	if err != nil || again != 0 {
		t.Fatalf("second PruneScheduleRuns() = %d, %v; want 0, nil", again, err)
	}
}

func TestScrapeTemplateMetricsDeriveRunHistoryFromJobs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository := newScheduleTestRepo(t)
	t0 := time.Now().UTC().Truncate(time.Second)

	saveScheduleAutomationTemplate(t, repository, "template-metrics", t0)

	// A template nothing has run reports zeroes rather than an error.
	empty, err := repository.ScrapeTemplateMetrics(ctx, "template-metrics")
	if err != nil {
		t.Fatalf("ScrapeTemplateMetrics(empty) error = %v", err)
	}
	if empty.RunCount != 0 || empty.AverageResults != 0 || empty.AverageDuration != 0 {
		t.Fatalf("an unrun template reported %+v", empty)
	}

	for index, id := range []string{"metrics-job-1", "metrics-job-2"} {
		data := validScheduledJobData()
		data.TemplateID = "template-metrics"
		job := web.Job{ID: id, Name: "Run " + id, Date: t0, Status: web.StatusOK, Data: data}
		if err := repository.Create(ctx, &job); err != nil {
			t.Fatalf("Create(%s) error = %v", id, err)
		}
		started := t0.Add(-time.Duration(index+1) * time.Hour)
		finished := started.Add(time.Duration(index+1) * 10 * time.Minute)
		if _, err := repository.db.ExecContext(ctx,
			"UPDATE job_runtime SET started_at = ?, finished_at = ? WHERE job_id = ?",
			started.Unix(), finished.Unix(), id,
		); err != nil {
			t.Fatalf("stamp job runtime: %v", err)
		}
	}

	// A job created from a DIFFERENT template must not contaminate the average.
	other := validScheduledJobData()
	other.TemplateID = "template-other"
	unrelated := web.Job{ID: "metrics-job-other", Name: "Other", Date: t0, Status: web.StatusOK, Data: other}
	if err := repository.Create(ctx, &unrelated); err != nil {
		t.Fatalf("Create(other) error = %v", err)
	}

	metrics, err := repository.ScrapeTemplateMetrics(ctx, "template-metrics")
	if err != nil {
		t.Fatalf("ScrapeTemplateMetrics() error = %v", err)
	}
	if metrics.RunCount != 2 {
		t.Fatalf("run count = %d, want 2", metrics.RunCount)
	}
	if metrics.TimedRunCount != 2 {
		t.Fatalf("timed run count = %d, want 2", metrics.TimedRunCount)
	}
	// 10 minutes and 20 minutes average to 15.
	if metrics.AverageDuration != 15*time.Minute {
		t.Fatalf("average duration = %s, want 15m0s", metrics.AverageDuration)
	}
	if metrics.LastRunAt == nil {
		t.Fatal("metrics carry no last-run timestamp")
	}
}

func newScheduleTestRepo(t *testing.T) *repo {
	t.Helper()

	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete, ok := repository.(*repo)
	if !ok {
		t.Fatal("New() did not return the local SQLite repository")
	}
	t.Cleanup(func() { _ = concrete.db.Close() })

	return concrete
}

func insertScheduleRunForJob(t *testing.T, repository *repo, scheduleID, jobID string, at time.Time) {
	t.Helper()

	if _, err := repository.db.Exec(
		"INSERT INTO schedule_runs(schedule_id, job_id, state, scheduled_for, finished_at, attempt, error) "+
			"VALUES (?, ?, 'completed', ?, ?, 1, '')",
		scheduleID, jobID, at.Unix(), at.Unix(),
	); err != nil {
		t.Fatalf("insert schedule run for %s: %v", jobID, err)
	}
}

func insertCompletedJobExport(t *testing.T, repository *repo, exportID, jobID string) {
	t.Helper()

	if _, err := repository.db.Exec(
		"INSERT INTO exports(id, name, format, state, source_type, source_id, created_at) "+
			"VALUES (?, ?, 'csv', 'completed', 'job', ?, ?)",
		exportID, exportID, jobID, time.Now().UTC().Unix(),
	); err != nil {
		t.Fatalf("insert export %s: %v", exportID, err)
	}
}

func jobEventCount(t *testing.T, repository *repo, jobID string) int64 {
	t.Helper()

	var count int64
	if err := repository.db.QueryRow(
		"SELECT COUNT(*) FROM job_events WHERE job_id = ?", jobID,
	).Scan(&count); err != nil {
		t.Fatalf("count job events for %s: %v", jobID, err)
	}

	return count
}
