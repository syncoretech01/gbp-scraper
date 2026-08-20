package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

func coverageTestPlan(t *testing.T, repository *repo, jobID string) []web.JobTaskDefinition {
	t.Helper()

	ctx := context.Background()
	job := lifecycleTestJob(jobID, time.Now().UTC())

	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	definitions := []web.JobTaskDefinition{
		{Key: "task-a", Kind: "map-query", Sequence: 0, Query: "dentist in Springfield IL 62701"},
		{Key: "task-b", Kind: "map-query", Sequence: 1, Query: "dentist in Chatham IL 62629"},
		{Key: "task-c", Kind: "map-query", Sequence: 2, Query: "dentist in Rochester IL 62563"},
	}

	if _, err := repository.PrepareJobTasks(ctx, jobID, definitions, 3); err != nil {
		t.Fatalf("prepare tasks: %v", err)
	}

	return definitions
}

func TestSkipPendingJobTasksIsTerminal(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "coverage-skip")
	defer closeDatabase()

	ctx := context.Background()
	definitions := coverageTestPlan(t, repository, "coverage-skip-job")

	if _, err := repository.StartJobTask(ctx, "coverage-skip-job", "task-a"); err != nil {
		t.Fatalf("start first task: %v", err)
	}

	if err := repository.CompleteJobTask(
		ctx, "coverage-skip-job", "task-a", web.JobTaskCheckpoint{RowsAdded: 4},
	); err != nil {
		t.Fatalf("complete first task: %v", err)
	}

	skipped, err := repository.SkipPendingJobTasks(ctx, "coverage-skip-job", web.CoverageSkipReason)
	if err != nil {
		t.Fatalf("skip pending tasks: %v", err)
	}

	if skipped != 2 {
		t.Fatalf("skipped = %d, want 2", skipped)
	}

	// Skipped tasks are terminal: not claimable, not part of the unfinished
	// plan, and untouched by lease reclaim.
	if _, found, claimErr := repository.ClaimNextJobTask(ctx, "coverage-skip-job", "owner-1", time.Minute); claimErr != nil {
		t.Fatalf("claim after skip: %v", claimErr)
	} else if found {
		t.Fatal("claim after skip found a task; skipped tasks must not be claimable")
	}

	unfinished, err := repository.unfinishedJobTasks(ctx, "coverage-skip-job")
	if err != nil {
		t.Fatalf("read unfinished tasks: %v", err)
	}

	if len(unfinished) != 0 {
		t.Fatalf("unfinished after skip = %#v, want none", unfinished)
	}

	if reclaimed, reclaimErr := repository.ReclaimExpiredJobTasks(ctx, "coverage-skip-job"); reclaimErr != nil || reclaimed != 0 {
		t.Fatalf("reclaim after skip = %d, %v; want 0, nil", reclaimed, reclaimErr)
	}

	// A restart re-upserts the plan; skipped tasks must not be resurrected.
	pending, err := repository.PrepareJobTasks(ctx, "coverage-skip-job", definitions, 3)
	if err != nil {
		t.Fatalf("re-prepare tasks: %v", err)
	}

	if len(pending) != 0 {
		t.Fatalf("re-prepare resurrected %d task(s): %#v", len(pending), pending)
	}

	snapshot, err := repository.GetJobExecution(ctx, "coverage-skip-job")
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}

	if snapshot.Tasks.Skipped != 2 || snapshot.Tasks.Completed != 1 || snapshot.Tasks.Pending != 0 {
		t.Fatalf("task summary = %#v", snapshot.Tasks)
	}

	rows, err := repository.JobCoverageTasks(ctx, "coverage-skip-job")
	if err != nil {
		t.Fatalf("read coverage tasks: %v", err)
	}

	skippedRows := 0

	for _, row := range rows {
		if row.State == "skipped" {
			skippedRows++

			if row.LastError != web.CoverageSkipReason {
				t.Fatalf("skipped task reason = %q, want %q", row.LastError, web.CoverageSkipReason)
			}
		}
	}

	if skippedRows != 2 {
		t.Fatalf("coverage rows show %d skipped task(s), want 2", skippedRows)
	}
}

func TestAppendJobTasksSequencingIdempotencyAndPriorityClaimOrder(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "coverage-append")
	defer closeDatabase()

	ctx := context.Background()
	coverageTestPlan(t, repository, "coverage-append-job")

	appended := []web.JobTaskDefinition{
		{
			Key: "exp-1", Kind: "map-query", Sequence: 3,
			Query:  "dentist in Divernon IL 62530",
			Origin: web.CoverageExpansionOriginPrefix + "62701", Priority: 1,
		},
		{
			Key: "exp-2", Kind: "map-query", Sequence: 4,
			Query:  "dentist in Auburn IL 62615",
			Origin: web.CoverageExpansionOriginPrefix + "62701", Priority: 1,
		},
	}

	inserted, err := repository.AppendJobTasks(ctx, "coverage-append-job", appended, 3)
	if err != nil {
		t.Fatalf("append tasks: %v", err)
	}

	if len(inserted) != 2 {
		t.Fatalf("inserted = %d, want 2", len(inserted))
	}

	if inserted[0].Origin != web.CoverageExpansionOriginPrefix+"62701" || inserted[0].Priority != 1 {
		t.Fatalf("inserted task = %#v", inserted[0])
	}

	// Idempotent: repeating the append inserts nothing and errors nothing.
	again, err := repository.AppendJobTasks(ctx, "coverage-append-job", appended, 3)
	if err != nil {
		t.Fatalf("re-append tasks: %v", err)
	}

	if len(again) != 0 {
		t.Fatalf("re-append inserted %d task(s), want 0", len(again))
	}

	seedState, err := repository.JobCoverageSeedState(ctx, "coverage-append-job")
	if err != nil {
		t.Fatalf("read seed state: %v", err)
	}

	if seedState.MaxSequence != 4 || seedState.ExpansionTasks != 2 || len(seedState.Queries) != 5 {
		t.Fatalf("seed state = %#v", seedState)
	}

	// Priority claims first even though the appended sequence is higher.
	claimed, found, err := repository.ClaimNextJobTask(ctx, "coverage-append-job", "owner-1", time.Minute)
	if err != nil || !found {
		t.Fatalf("claim = %v, found %v", err, found)
	}

	if claimed.Key != "exp-1" {
		t.Fatalf("claimed %q, want the priority task exp-1 first", claimed.Key)
	}
}

func TestClaimHonoursNotBeforeBackoff(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "coverage-backoff")
	defer closeDatabase()

	ctx := context.Background()
	coverageTestPlan(t, repository, "coverage-backoff-job")

	// Defer the first-in-line task into the future: the claim must take the
	// next eligible one instead.
	if err := repository.DeferJobTask(
		ctx, "coverage-backoff-job", "task-a", time.Now().UTC().Add(time.Hour),
	); err != nil {
		t.Fatalf("defer task: %v", err)
	}

	claimed, found, err := repository.ClaimNextJobTask(ctx, "coverage-backoff-job", "owner-1", time.Minute)
	if err != nil || !found {
		t.Fatalf("claim = %v, found %v", err, found)
	}

	if claimed.Key != "task-b" {
		t.Fatalf("claimed %q, want task-b while task-a is backed off", claimed.Key)
	}

	// A lapsed backoff makes the task eligible again, and the claim resets
	// not_before so a later failure recomputes it.
	if err := repository.DeferJobTask(
		ctx, "coverage-backoff-job", "task-a", time.Now().UTC().Add(-time.Second),
	); err != nil {
		t.Fatalf("re-defer task: %v", err)
	}

	claimed, found, err = repository.ClaimNextJobTask(ctx, "coverage-backoff-job", "owner-2", time.Minute)
	if err != nil || !found {
		t.Fatalf("second claim = %v, found %v", err, found)
	}

	if claimed.Key != "task-a" {
		t.Fatalf("claimed %q, want task-a after its backoff lapsed", claimed.Key)
	}

	var notBefore any
	if err := repository.db.QueryRow(
		"SELECT not_before FROM job_tasks WHERE job_id = ? AND task_key = ?",
		"coverage-backoff-job", "task-a",
	).Scan(&notBefore); err != nil {
		t.Fatalf("read not_before: %v", err)
	}

	if notBefore != nil {
		t.Fatalf("not_before after claim = %v, want NULL", notBefore)
	}
}

func TestUpsertProxyTaskStatAggregatesAndResetsStreaks(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	ctx := context.Background()

	pool, imported, err := repository.ImportProxyPool(ctx, "Stats", "round_robin", []string{
		"http://user:pass@10.0.0.1:8080",
		"http://user:pass@10.0.0.2:8080",
	})
	if err != nil || imported != 2 {
		t.Fatalf("import pool = %d, %v", imported, err)
	}

	proxies, err := repository.ListProxies(ctx, pool.ID)
	if err != nil || len(proxies) != 2 {
		t.Fatalf("list proxies = %d, %v", len(proxies), err)
	}

	target := proxies[0].ID

	for _, outcome := range []struct {
		success bool
		message string
	}{
		{false, "proxy tunnel refused"},
		{false, "proxy tunnel refused again"},
		{true, ""},
		{false, "browser crashed"},
	} {
		if err := repository.UpsertProxyTaskStat(ctx, web.ProxyTaskStatInput{
			ProxyID:         target,
			PoolID:          pool.ID,
			Success:         outcome.success,
			DurationSeconds: 1.5,
			LastError:       outcome.message,
		}); err != nil {
			t.Fatalf("upsert stat: %v", err)
		}
	}

	var (
		successes, failures, streak int64
		totalSeconds                float64
		lastError                   string
	)

	if err := repository.db.QueryRow(
		`SELECT task_successes, task_failures, consecutive_failures, total_task_seconds, last_error
		FROM proxy_task_stats WHERE proxy_id = ?`,
		target,
	).Scan(&successes, &failures, &streak, &totalSeconds, &lastError); err != nil {
		t.Fatalf("read stats: %v", err)
	}

	if successes != 1 || failures != 3 {
		t.Fatalf("stats = %d successes, %d failures; want 1 and 3", successes, failures)
	}

	// The success in between reset the streak; the trailing failure restarts
	// it at one.
	if streak != 1 {
		t.Fatalf("consecutive failures = %d, want 1", streak)
	}

	if totalSeconds < 5.9 || totalSeconds > 6.1 {
		t.Fatalf("total seconds = %f, want about 6", totalSeconds)
	}

	if lastError != "browser crashed" {
		t.Fatalf("last error = %q", lastError)
	}

	health, err := repository.ProxyTaskHealthByURL(ctx, pool.ID)
	if err != nil {
		t.Fatalf("read health: %v", err)
	}

	if len(health) != 2 {
		t.Fatalf("health entries = %d, want 2", len(health))
	}

	tracked, ok := health["http://user:pass@10.0.0.1:8080"]
	if !ok {
		t.Fatalf("health lacks the first proxy URL: %#v", health)
	}

	if tracked.ProxyID != target || tracked.ConsecutiveFailures != 1 || tracked.Successes != 1 || tracked.Failures != 3 {
		t.Fatalf("tracked health = %#v", tracked)
	}

	fresh, ok := health["http://user:pass@10.0.0.2:8080"]
	if !ok || fresh.ConsecutiveFailures != 0 || fresh.Successes != 0 || fresh.Failures != 0 {
		t.Fatalf("fresh proxy health = %#v, ok=%v", fresh, ok)
	}
}
