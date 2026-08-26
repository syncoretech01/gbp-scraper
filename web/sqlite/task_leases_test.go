package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// newTaskLeaseFixture creates a job with sequential map-grid tasks named
// cell-0, cell-1, ... so lease tests can claim them in a known order.
func newTaskLeaseFixture(t *testing.T, name string, keys ...string) (*repo, web.Job, func()) {
	t.Helper()
	repository, closeDatabase := newLifecycleTestRepository(t, name)
	ctx := context.Background()
	job := lifecycleTestJob(name+"-job", time.Now().UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		closeDatabase()
		t.Fatalf("create job: %v", err)
	}
	definitions := make([]web.JobTaskDefinition, 0, len(keys))
	for index, key := range keys {
		definitions = append(definitions, web.JobTaskDefinition{
			Key: key, Kind: "map-grid-cell", Sequence: index, Query: "dentists", SourceCell: key,
		})
	}
	if _, err := repository.PrepareJobTasks(ctx, job.ID, definitions, 3); err != nil {
		closeDatabase()
		t.Fatalf("prepare tasks: %v", err)
	}

	return repository, job, closeDatabase
}

type taskLeaseRow struct {
	state          string
	owner          string
	attempts       int
	leaseExpiresAt sql.NullInt64
	startedAt      sql.NullInt64
}

func readTaskLeaseRow(t *testing.T, repository *repo, jobID, taskKey string) taskLeaseRow {
	t.Helper()
	var row taskLeaseRow
	if err := repository.db.QueryRow(
		`SELECT state, lease_owner, attempts, lease_expires_at, started_at
		FROM job_tasks WHERE job_id = ? AND task_key = ?`,
		jobID,
		taskKey,
	).Scan(&row.state, &row.owner, &row.attempts, &row.leaseExpiresAt, &row.startedAt); err != nil {
		t.Fatalf("read task %q: %v", taskKey, err)
	}

	return row
}

func expireTaskLease(t *testing.T, repository *repo, jobID, taskKey string) {
	t.Helper()
	past := time.Now().UTC().Unix() - 120
	if _, err := repository.db.Exec(
		`UPDATE job_tasks SET lease_expires_at = ? WHERE job_id = ? AND task_key = ?`,
		past,
		jobID,
		taskKey,
	); err != nil {
		t.Fatalf("expire lease for %q: %v", taskKey, err)
	}
}

func countJobCheckpoints(t *testing.T, repository *repo, jobID string) int {
	t.Helper()
	var count int
	if err := repository.db.QueryRow(
		`SELECT COUNT(*) FROM job_checkpoints WHERE job_id = ?`,
		jobID,
	).Scan(&count); err != nil {
		t.Fatalf("count checkpoints: %v", err)
	}

	return count
}

func readTaskAggregates(t *testing.T, repository *repo, jobID string) (completed, active int64) {
	t.Helper()
	if err := repository.db.QueryRow(
		`SELECT job_runtime.completed_tasks, job_progress.active_tasks
		FROM job_runtime JOIN job_progress ON job_progress.job_id = job_runtime.job_id
		WHERE job_runtime.job_id = ?`,
		jobID,
	).Scan(&completed, &active); err != nil {
		t.Fatalf("read task aggregates: %v", err)
	}

	return completed, active
}

// reassignTaskLease walks the audited defect scenario: worker A claims, its
// lease expires and is reclaimed, and worker B claims the same task.
func reassignTaskLease(t *testing.T, repository *repo, jobID, taskKey string) {
	t.Helper()
	ctx := context.Background()
	claimed, found, err := repository.ClaimNextJobTask(ctx, jobID, "worker-a", time.Minute)
	if err != nil || !found || claimed.Key != taskKey {
		t.Fatalf("claim as worker-a = %#v, %v, %v", claimed, found, err)
	}
	expireTaskLease(t, repository, jobID, taskKey)
	if reclaimed, err := repository.ReclaimExpiredJobTasks(ctx, jobID); err != nil || reclaimed != 1 {
		t.Fatalf("reclaim expired lease = %d, %v", reclaimed, err)
	}
	claimed, found, err = repository.ClaimNextJobTask(ctx, jobID, "worker-b", time.Minute)
	if err != nil || !found || claimed.Key != taskKey {
		t.Fatalf("claim as worker-b = %#v, %v, %v", claimed, found, err)
	}
}

func TestCompleteJobTaskAsRejectsStaleOwnerAfterReassignment(t *testing.T) {
	t.Parallel()
	repository, job, closeDatabase := newTaskLeaseFixture(t, "lease-stale-complete", "cell-0")
	defer closeDatabase()
	ctx := context.Background()
	reassignTaskLease(t, repository, job.ID, "cell-0")

	err := repository.CompleteJobTaskAs(ctx, job.ID, "cell-0", "worker-a", web.JobTaskCheckpoint{RowsAdded: 5})
	if !errors.Is(err, web.ErrCheckpointLeaseLost) {
		t.Fatalf("stale complete error = %v, want ErrCheckpointLeaseLost", err)
	}
	row := readTaskLeaseRow(t, repository, job.ID, "cell-0")
	if row.state != taskStateRunning || row.owner != "worker-b" {
		t.Fatalf("task after stale complete = %#v", row)
	}
	if count := countJobCheckpoints(t, repository, job.ID); count != 0 {
		t.Fatalf("checkpoints after stale complete = %d", count)
	}
	completed, active := readTaskAggregates(t, repository, job.ID)
	if completed != 0 || active != 1 {
		t.Fatalf("aggregates after stale complete = completed %d, active %d", completed, active)
	}

	if err := repository.CompleteJobTaskAs(ctx, job.ID, "cell-0", "worker-b", web.JobTaskCheckpoint{RowsAdded: 5}); err != nil {
		t.Fatalf("complete as current owner: %v", err)
	}
	row = readTaskLeaseRow(t, repository, job.ID, "cell-0")
	if row.state != taskStateCompleted || row.owner != "" {
		t.Fatalf("task after owner complete = %#v", row)
	}
	if count := countJobCheckpoints(t, repository, job.ID); count != 1 {
		t.Fatalf("checkpoints after owner complete = %d", count)
	}
	completed, active = readTaskAggregates(t, repository, job.ID)
	if completed != 1 || active != 0 {
		t.Fatalf("aggregates after owner complete = completed %d, active %d", completed, active)
	}
}

func TestFailJobTaskAsRejectsStaleOwnerAfterReassignment(t *testing.T) {
	t.Parallel()
	repository, job, closeDatabase := newTaskLeaseFixture(t, "lease-stale-fail", "cell-0")
	defer closeDatabase()
	ctx := context.Background()
	reassignTaskLease(t, repository, job.ID, "cell-0")

	err := repository.FailJobTaskAs(
		ctx, job.ID, "cell-0", "worker-a", errors.New("stale browser crashed"), true, web.JobTaskCheckpoint{},
	)
	if !errors.Is(err, web.ErrCheckpointLeaseLost) {
		t.Fatalf("stale fail error = %v, want ErrCheckpointLeaseLost", err)
	}
	row := readTaskLeaseRow(t, repository, job.ID, "cell-0")
	if row.state != taskStateRunning || row.owner != "worker-b" {
		t.Fatalf("task after stale fail = %#v", row)
	}
	if count := countJobCheckpoints(t, repository, job.ID); count != 0 {
		t.Fatalf("checkpoints after stale fail = %d", count)
	}

	if err := repository.FailJobTaskAs(
		ctx, job.ID, "cell-0", "worker-b", errors.New("owned attempt failed"), true, web.JobTaskCheckpoint{},
	); err != nil {
		t.Fatalf("fail as current owner: %v", err)
	}
	row = readTaskLeaseRow(t, repository, job.ID, "cell-0")
	if row.state != taskStatePending || row.owner != "" {
		t.Fatalf("task after owner fail = %#v", row)
	}
}

func TestCompleteJobTaskAsIsIdempotentForCompletedTasks(t *testing.T) {
	t.Parallel()
	repository, job, closeDatabase := newTaskLeaseFixture(t, "lease-idempotent", "cell-0")
	defer closeDatabase()
	ctx := context.Background()
	if _, found, err := repository.ClaimNextJobTask(ctx, job.ID, "worker-b", time.Minute); err != nil || !found {
		t.Fatalf("claim task = %v, %v", found, err)
	}
	if err := repository.CompleteJobTaskAs(ctx, job.ID, "cell-0", "worker-b", web.JobTaskCheckpoint{RowsAdded: 3}); err != nil {
		t.Fatalf("first complete: %v", err)
	}

	if err := repository.CompleteJobTaskAs(ctx, job.ID, "cell-0", "worker-b", web.JobTaskCheckpoint{RowsAdded: 3}); err != nil {
		t.Fatalf("repeat complete by the same owner = %v, want nil", err)
	}
	if err := repository.CompleteJobTaskAs(ctx, job.ID, "cell-0", "worker-a", web.JobTaskCheckpoint{}); err != nil {
		t.Fatalf("repeat complete by another owner = %v, want nil", err)
	}
	if count := countJobCheckpoints(t, repository, job.ID); count != 1 {
		t.Fatalf("checkpoints after repeated completes = %d, want 1", count)
	}
	completed, _ := readTaskAggregates(t, repository, job.ID)
	if completed != 1 {
		t.Fatalf("completed aggregate after repeated completes = %d", completed)
	}
}

func TestOwnerlessFinishKeepsWorkingForStartJobTaskPath(t *testing.T) {
	t.Parallel()
	repository, job, closeDatabase := newTaskLeaseFixture(t, "lease-less-finish", "cell-0", "cell-1")
	defer closeDatabase()
	ctx := context.Background()

	if _, err := repository.StartJobTask(ctx, job.ID, "cell-0"); err != nil {
		t.Fatalf("start first task: %v", err)
	}
	// StartJobTask stores lease_owner = '', which is exactly what the
	// ownerless delegates match.
	row := readTaskLeaseRow(t, repository, job.ID, "cell-0")
	if row.state != taskStateRunning || row.owner != "" {
		t.Fatalf("started task = %#v", row)
	}
	if err := repository.CompleteJobTask(ctx, job.ID, "cell-0", web.JobTaskCheckpoint{RowsAdded: 2}); err != nil {
		t.Fatalf("ownerless complete: %v", err)
	}
	if row = readTaskLeaseRow(t, repository, job.ID, "cell-0"); row.state != taskStateCompleted {
		t.Fatalf("task after ownerless complete = %#v", row)
	}

	if _, err := repository.StartJobTask(ctx, job.ID, "cell-1"); err != nil {
		t.Fatalf("start second task: %v", err)
	}
	if err := repository.FailJobTask(
		ctx, job.ID, "cell-1", errors.New("attempt failed"), false, web.JobTaskCheckpoint{},
	); err != nil {
		t.Fatalf("ownerless fail: %v", err)
	}
	if row = readTaskLeaseRow(t, repository, job.ID, "cell-1"); row.state != taskStateFailed {
		t.Fatalf("task after ownerless fail = %#v", row)
	}
}

func TestReclaimStaleJobTasksRecoversExpiredLeasesOnInactiveJobs(t *testing.T) {
	t.Parallel()
	repository, job, closeDatabase := newTaskLeaseFixture(t, "lease-stale-sweep", "cell-0", "cell-1", "cell-2")
	defer closeDatabase()
	ctx := context.Background()

	if _, found, err := repository.ClaimNextJobTask(ctx, job.ID, "worker-a", time.Minute); err != nil || !found {
		t.Fatalf("claim first task = %v, %v", found, err)
	}
	if _, found, err := repository.ClaimNextJobTask(ctx, job.ID, "worker-b", time.Hour); err != nil || !found {
		t.Fatalf("claim second task = %v, %v", found, err)
	}
	expireTaskLease(t, repository, job.ID, "cell-0")
	// Hand-craft a further stale running row with zero recorded attempts to
	// prove the attempt refund never goes negative.
	past := time.Now().UTC().Unix() - 120
	if _, err := repository.db.Exec(
		`UPDATE job_tasks SET state = 'running', attempts = 0, lease_owner = 'ghost',
			lease_expires_at = ?, started_at = ?, updated_at = ?
		WHERE job_id = ? AND task_key = 'cell-2'`,
		past,
		past,
		past,
		job.ID,
	); err != nil {
		t.Fatalf("craft stale task: %v", err)
	}
	// Park the job outside the active states RecoverAbandonedJobs handles.
	if _, err := repository.db.Exec(
		`UPDATE job_runtime SET state = ? WHERE job_id = ?`,
		string(jobruntime.StatePaused),
		job.ID,
	); err != nil {
		t.Fatalf("pause job: %v", err)
	}

	// Regression for the audited gap: startup job recovery skips paused jobs,
	// so both stale leases survive it.
	if recovered, err := repository.RecoverAbandonedJobs(ctx); err != nil || recovered != 0 {
		t.Fatalf("recover abandoned jobs = %d, %v", recovered, err)
	}
	if row := readTaskLeaseRow(t, repository, job.ID, "cell-0"); row.state != taskStateRunning || row.owner != "worker-a" {
		t.Fatalf("stale task after job recovery = %#v", row)
	}

	reclaimed, err := repository.ReclaimStaleJobTasks(ctx)
	if err != nil || reclaimed != 2 {
		t.Fatalf("reclaim stale tasks = %d, %v", reclaimed, err)
	}
	expired := readTaskLeaseRow(t, repository, job.ID, "cell-0")
	if expired.state != taskStatePending || expired.owner != "" ||
		expired.attempts != 0 || expired.leaseExpiresAt.Valid || expired.startedAt.Valid {
		t.Fatalf("reclaimed task = %#v", expired)
	}
	crafted := readTaskLeaseRow(t, repository, job.ID, "cell-2")
	if crafted.state != taskStatePending || crafted.attempts != 0 || crafted.owner != "" {
		t.Fatalf("crafted stale task = %#v", crafted)
	}
	healthy := readTaskLeaseRow(t, repository, job.ID, "cell-1")
	if healthy.state != taskStateRunning || healthy.owner != "worker-b" || !healthy.leaseExpiresAt.Valid {
		t.Fatalf("unexpired task = %#v", healthy)
	}
	if _, active := readTaskAggregates(t, repository, job.ID); active != 1 {
		t.Fatalf("active aggregate after sweep = %d, want 1", active)
	}
	if reclaimed, err = repository.ReclaimStaleJobTasks(ctx); err != nil || reclaimed != 0 {
		t.Fatalf("repeat stale sweep = %d, %v", reclaimed, err)
	}
}

func TestConcurrentReclaimAndClaimNeverDoubleClaims(t *testing.T) {
	t.Parallel()
	repository, job, closeDatabase := newTaskLeaseFixture(t, "lease-claim-race", "cell-0")
	defer closeDatabase()
	ctx := context.Background()
	if _, found, err := repository.ClaimNextJobTask(ctx, job.ID, "stale-worker", time.Minute); err != nil || !found {
		t.Fatalf("claim as stale worker = %v, %v", found, err)
	}
	expireTaskLease(t, repository, job.ID, "cell-0")

	start := make(chan struct{})
	errs := make(chan error, 3)
	winners := make(chan string, 2)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		<-start
		_, err := repository.ReclaimExpiredJobTasks(ctx, job.ID)
		errs <- err
	}()
	for _, owner := range []string{"racer-1", "racer-2"} {
		go func(owner string) {
			defer wg.Done()
			<-start
			_, found, err := repository.ClaimNextJobTask(ctx, job.ID, owner, time.Minute)
			errs <- err
			if found {
				winners <- owner
			}
		}(owner)
	}
	close(start)
	wg.Wait()
	close(errs)
	close(winners)

	for err := range errs {
		if err != nil {
			t.Fatalf("racing lease operation: %v", err)
		}
	}
	claims := make([]string, 0, 2)
	for owner := range winners {
		claims = append(claims, owner)
	}
	if len(claims) != 1 {
		t.Fatalf("successful claims = %v, want exactly one", claims)
	}
	row := readTaskLeaseRow(t, repository, job.ID, "cell-0")
	if row.state != taskStateRunning || row.owner != claims[0] || row.attempts != 1 {
		t.Fatalf("task after race = %#v, winner %q", row, claims[0])
	}
}
