package sqlite

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

func TestLifecycleCreateAndPendingSelectionUseCanonicalFIFO(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "create-fifo")
	defer closeDatabase()
	ctx := context.Background()

	draft := lifecycleTestJob("draft", time.Unix(100, 0).UTC())
	first := lifecycleTestJob("first", time.Unix(300, 0).UTC())
	second := lifecycleTestJob("second", time.Unix(200, 0).UTC())
	if err := repository.CreateWithState(ctx, &draft, jobruntime.StateDraft); err != nil {
		t.Fatalf("create draft: %v", err)
	}
	if err := repository.CreateWithState(ctx, &first, jobruntime.StateQueued); err != nil {
		t.Fatalf("create first queued job: %v", err)
	}
	if err := repository.CreateWithState(ctx, &second, jobruntime.StateQueued); err != nil {
		t.Fatalf("create second queued job: %v", err)
	}

	pending, err := repository.Select(ctx, web.SelectParams{Status: web.StatusPending, Limit: 10})
	if err != nil {
		t.Fatalf("select pending jobs: %v", err)
	}
	assertJobIDs(t, pending, "first", "second")

	draftRuntime, err := repository.GetRuntime(ctx, draft.ID)
	if err != nil {
		t.Fatalf("read draft runtime: %v", err)
	}
	if draftRuntime.State != jobruntime.StateDraft || draftRuntime.StateVersion != 0 {
		t.Fatalf("draft runtime = %#v", draftRuntime)
	}

	if _, _, err := repository.ApplyControl(ctx, first.ID, jobruntime.ControlPause); err != nil {
		t.Fatalf("pause first queued job: %v", err)
	}
	if _, _, err := repository.ApplyControl(ctx, draft.ID, jobruntime.ControlStart); err != nil {
		t.Fatalf("start draft: %v", err)
	}
	pending, err = repository.Select(ctx, web.SelectParams{Status: web.StatusPending, Limit: 10})
	if err != nil {
		t.Fatalf("select pending jobs after controls: %v", err)
	}
	assertJobIDs(t, pending, "second", "draft")

	events, err := repository.EventsAfter(ctx, draft.ID, 0, 10)
	if err != nil {
		t.Fatalf("read draft events: %v", err)
	}
	if len(events) != 2 || events[0].Type != "created" || events[1].Type != "control" || events[0].ID >= events[1].ID {
		t.Fatalf("draft events = %#v", events)
	}
}

func TestLifecycleControlsAreIdempotentAndLegacyWorkerStartIsDurable(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "controls")
	defer closeDatabase()
	ctx := context.Background()
	job := lifecycleTestJob("job-1", time.Unix(100, 0).UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create queued job: %v", err)
	}

	job.Status = web.StatusWorking
	if err := repository.Update(ctx, &job); err != nil {
		t.Fatalf("apply legacy worker start: %v", err)
	}
	runtime, err := repository.GetRuntime(ctx, job.ID)
	if err != nil {
		t.Fatalf("read running runtime: %v", err)
	}
	if runtime.State != jobruntime.StateRunning || runtime.StartedAt == nil || runtime.StateVersion != 1 {
		t.Fatalf("runtime after legacy start = %#v", runtime)
	}

	runtime, decision, err := repository.ApplyControl(ctx, job.ID, jobruntime.ControlPause)
	if err != nil {
		t.Fatalf("request pause: %v", err)
	}
	if decision.Disposition != jobruntime.ControlRequested || runtime.State != jobruntime.StateRunning ||
		runtime.RequestedStop != jobruntime.StopReasonPauseRequested {
		t.Fatalf("pause result runtime=%#v decision=%#v", runtime, decision)
	}
	pauseVersion := runtime.StateVersion
	eventsBeforeNoop := lifecycleEventCount(t, repository, job.ID)

	runtime, decision, err = repository.ApplyControl(ctx, job.ID, jobruntime.ControlPause)
	if err != nil {
		t.Fatalf("repeat pause: %v", err)
	}
	if decision.Disposition != jobruntime.ControlNoop || runtime.StateVersion != pauseVersion {
		t.Fatalf("repeated pause runtime=%#v decision=%#v", runtime, decision)
	}
	if got := lifecycleEventCount(t, repository, job.ID); got != eventsBeforeNoop {
		t.Fatalf("no-op pause added an event: before=%d after=%d", eventsBeforeNoop, got)
	}

	runtime, decision, err = repository.ApplyControl(ctx, job.ID, jobruntime.ControlResume)
	if err != nil {
		t.Fatalf("withdraw pause: %v", err)
	}
	if decision.Disposition != jobruntime.ControlApplied || runtime.RequestedStop != jobruntime.StopReasonNone ||
		runtime.State != jobruntime.StateRunning {
		t.Fatalf("resume result runtime=%#v decision=%#v", runtime, decision)
	}

	runtime, decision, err = repository.ApplyControl(ctx, job.ID, jobruntime.ControlCancel)
	if err != nil {
		t.Fatalf("request cancel: %v", err)
	}
	if decision.Disposition != jobruntime.ControlRequested || runtime.State != jobruntime.StateCancelling ||
		runtime.RequestedStop != jobruntime.StopReasonUserCancelled {
		t.Fatalf("cancel result runtime=%#v decision=%#v", runtime, decision)
	}

	runtime, err = repository.SetOutcome(ctx, job.ID, jobruntime.Outcome{
		State:       jobruntime.StateCancelled,
		Reason:      jobruntime.StopReasonUserCancelled,
		Recoverable: true,
	}, "cancelled at a safe checkpoint")
	if err != nil {
		t.Fatalf("store cancellation outcome: %v", err)
	}
	if runtime.State != jobruntime.StateCancelled || runtime.OutcomeReason != jobruntime.StopReasonUserCancelled ||
		runtime.RequestedStop != jobruntime.StopReasonNone || runtime.FinishedAt == nil {
		t.Fatalf("cancelled runtime = %#v", runtime)
	}
	stored, err := repository.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("read legacy job: %v", err)
	}
	if stored.Status != web.StatusOK {
		t.Fatalf("cancelled legacy status = %q, want %q", stored.Status, web.StatusOK)
	}

	_, rejected, err := repository.ApplyControl(ctx, job.ID, jobruntime.ControlStart)
	if !errors.Is(err, jobruntime.ErrControlRejected) || rejected.Disposition != jobruntime.ControlRejected {
		t.Fatalf("start cancelled job error=%v decision=%#v", err, rejected)
	}
}

func TestLifecyclePartialOutcomeIsRedactedAndProtectedFromLegacyCompletion(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "partial-redaction")
	defer closeDatabase()
	ctx := context.Background()
	job := lifecycleTestJob("job-1", time.Unix(100, 0).UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create queued job: %v", err)
	}
	job.Status = web.StatusWorking
	if err := repository.Update(ctx, &job); err != nil {
		t.Fatalf("start job: %v", err)
	}

	secretMessage := "runtime limit at http://alice:secret@example.test/path?api_key=token123 password=hunter2"
	outcome := jobruntime.Outcome{
		State:             jobruntime.StatePartial,
		Reason:            jobruntime.StopReasonRuntimeLimit,
		Recoverable:       true,
		HasPartialResults: true,
	}
	runtime, err := repository.SetOutcome(ctx, job.ID, outcome, secretMessage)
	if err != nil {
		t.Fatalf("store partial outcome: %v", err)
	}
	if runtime.State != jobruntime.StatePartial || runtime.OutcomeReason != jobruntime.StopReasonRuntimeLimit ||
		runtime.Progress == 100 || runtime.FinishedAt == nil {
		t.Fatalf("partial runtime = %#v", runtime)
	}
	if strings.Contains(runtime.Message, "secret") || strings.Contains(runtime.Message, "token123") ||
		strings.Contains(runtime.Message, "hunter2") || !strings.Contains(runtime.Message, jobruntime.RedactedValue) {
		t.Fatalf("runtime message was not redacted: %q", runtime.Message)
	}

	eventsBeforeNoop := lifecycleEventCount(t, repository, job.ID)
	if _, err := repository.SetOutcome(ctx, job.ID, outcome, "password=another-secret"); err != nil {
		t.Fatalf("repeat partial outcome: %v", err)
	}
	if got := lifecycleEventCount(t, repository, job.ID); got != eventsBeforeNoop {
		t.Fatalf("repeated outcome added an event: before=%d after=%d", eventsBeforeNoop, got)
	}

	job.Status = web.StatusOK
	if err := repository.Update(ctx, &job); err != nil {
		t.Fatalf("apply legacy completion after partial outcome: %v", err)
	}
	runtime, err = repository.GetRuntime(ctx, job.ID)
	if err != nil {
		t.Fatalf("read protected partial runtime: %v", err)
	}
	if runtime.State != jobruntime.StatePartial || runtime.OutcomeReason != jobruntime.StopReasonRuntimeLimit {
		t.Fatalf("legacy completion overwrote canonical partial outcome: %#v", runtime)
	}
	stored, err := repository.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("read legacy projection: %v", err)
	}
	if stored.Status != web.StatusOK {
		t.Fatalf("partial legacy status = %q, want ok", stored.Status)
	}

	events, err := repository.EventsAfter(ctx, job.ID, 0, 100)
	if err != nil {
		t.Fatalf("read redacted events: %v", err)
	}
	for _, event := range events {
		combined := event.Message + event.Context
		if strings.Contains(combined, "secret") || strings.Contains(combined, "token123") || strings.Contains(combined, "hunter2") {
			t.Fatalf("event %d leaked a credential: %#v", event.ID, event)
		}
	}
	if len(events) < 2 {
		t.Fatalf("events = %#v", events)
	}
	after := events[0].ID
	replayed, err := repository.EventsAfter(ctx, job.ID, after, 1)
	if err != nil {
		t.Fatalf("replay events: %v", err)
	}
	if len(replayed) != 1 || replayed[0].ID <= after {
		t.Fatalf("replayed events after %d = %#v", after, replayed)
	}
}

func TestLifecycleConcurrentStartCreatesOneTransition(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "concurrent-start")
	defer closeDatabase()
	ctx := context.Background()
	job := lifecycleTestJob("job-1", time.Unix(100, 0).UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateDraft); err != nil {
		t.Fatalf("create draft: %v", err)
	}

	const callers = 8
	var applied atomic.Int64
	var noops atomic.Int64
	var waitGroup sync.WaitGroup
	errorsFound := make(chan error, callers)
	for range callers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, decision, err := repository.ApplyControl(ctx, job.ID, jobruntime.ControlStart)
			if err != nil {
				errorsFound <- err

				return
			}
			switch decision.Disposition {
			case jobruntime.ControlApplied:
				applied.Add(1)
			case jobruntime.ControlNoop:
				noops.Add(1)
			default:
				errorsFound <- errors.New("unexpected start disposition: " + string(decision.Disposition))
			}
		}()
	}
	waitGroup.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent start: %v", err)
	}
	if applied.Load() != 1 || noops.Load() != callers-1 {
		t.Fatalf("concurrent starts applied=%d noops=%d", applied.Load(), noops.Load())
	}

	runtime, err := repository.GetRuntime(ctx, job.ID)
	if err != nil {
		t.Fatalf("read runtime: %v", err)
	}
	if runtime.State != jobruntime.StateQueued || runtime.StateVersion != 1 {
		t.Fatalf("runtime after concurrent starts = %#v", runtime)
	}
	if got := lifecycleEventCount(t, repository, job.ID); got != 2 {
		t.Fatalf("event count after concurrent starts = %d, want 2", got)
	}
}

func TestLifecycleMigrationRecoversActiveWorkAndSeedsFIFO(t *testing.T) {
	t.Parallel()

	db := openTestDatabase(t, "file:lifecycle-v5-migration?mode=memory&cache=shared")
	defer db.Close()
	createLegacyJobsTable(t, db)

	jobs := []struct {
		id        string
		status    string
		createdAt int64
	}{
		{id: "active", status: web.StatusWorking, createdAt: 300},
		{id: "second", status: web.StatusPending, createdAt: 200},
		{id: "first", status: web.StatusPending, createdAt: 100},
	}
	for _, item := range jobs {
		if _, err := db.Exec(
			`INSERT INTO jobs(id, name, status, data, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			item.id,
			item.id,
			item.status,
			legacyJobData,
			item.createdAt,
			item.createdAt,
		); err != nil {
			t.Fatalf("insert legacy job %q: %v", item.id, err)
		}
	}
	for _, migration := range schemaMigrations {
		if err := applyMigration(db, migration); err != nil {
			t.Fatalf("apply migration %d: %v", migration.version, err)
		}
	}

	var state, status string
	var recoveryRequired int
	if err := db.QueryRow(
		`SELECT job_runtime.state, jobs.status, job_runtime.recovery_required
		FROM job_runtime JOIN jobs ON jobs.id = job_runtime.job_id
		WHERE job_runtime.job_id = 'active'`,
	).Scan(&state, &status, &recoveryRequired); err != nil {
		t.Fatalf("read recovered runtime: %v", err)
	}
	if state != string(jobruntime.StatePaused) || status != web.StatusPending || recoveryRequired != 1 {
		t.Fatalf("recovered runtime state=%q status=%q recovery=%d", state, status, recoveryRequired)
	}

	rows, err := db.Query(
		`SELECT job_id, queue_seq FROM job_runtime WHERE state = 'queued' ORDER BY queue_seq`,
	)
	if err != nil {
		t.Fatalf("read migrated queue: %v", err)
	}
	defer rows.Close()
	var queueIDs []string
	var queueSequences []int64
	for rows.Next() {
		var id string
		var sequence int64
		if err := rows.Scan(&id, &sequence); err != nil {
			t.Fatalf("scan migrated queue: %v", err)
		}
		queueIDs = append(queueIDs, id)
		queueSequences = append(queueSequences, sequence)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read migrated queue: %v", err)
	}
	if strings.Join(queueIDs, ",") != "first,second" || len(queueSequences) != 2 ||
		queueSequences[0] != 1 || queueSequences[1] != 2 {
		t.Fatalf("migrated queue ids=%v sequences=%v", queueIDs, queueSequences)
	}

	var progressRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM job_progress`).Scan(&progressRows); err != nil {
		t.Fatalf("count migrated progress rows: %v", err)
	}
	if progressRows != len(jobs) {
		t.Fatalf("progress rows = %d, want %d", progressRows, len(jobs))
	}
}

func newLifecycleTestRepository(t *testing.T, name string) (*repo, func()) {
	t.Helper()

	db, err := initDatabase("file:" + name + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("initialize lifecycle database: %v", err)
	}

	return &repo{db: db}, func() {
		if err := db.Close(); err != nil {
			t.Errorf("close lifecycle database: %v", err)
		}
	}
}

func lifecycleTestJob(id string, createdAt time.Time) web.Job {
	return web.Job{
		ID:     id,
		Name:   "Dentists " + id,
		Date:   createdAt,
		Status: web.StatusPending,
		Data: web.JobData{
			Keywords: []string{"dentists"},
			Lang:     "en",
			Zoom:     12,
			Depth:    10,
			MaxTime:  30 * time.Minute,
		},
	}
}

func assertJobIDs(t *testing.T, jobs []web.Job, want ...string) {
	t.Helper()
	if len(jobs) != len(want) {
		t.Fatalf("job count = %d, want %d (%#v)", len(jobs), len(want), jobs)
	}
	for index := range want {
		if jobs[index].ID != want[index] {
			t.Fatalf("jobs[%d].ID = %q, want %q (%#v)", index, jobs[index].ID, want[index], jobs)
		}
	}
}

func lifecycleEventCount(t *testing.T, repository *repo, id string) int {
	t.Helper()
	events, err := repository.EventsAfter(context.Background(), id, 0, maximumEventLimit)
	if err != nil {
		t.Fatalf("read lifecycle events: %v", err)
	}

	return len(events)
}
