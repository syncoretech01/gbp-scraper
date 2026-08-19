package webrunner

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
	"github.com/gosom/google-maps-scraper/web/resultimport"
	"github.com/gosom/google-maps-scraper/web/sqlite"
	"github.com/gosom/scrapemate"
)

// gridScrapeJob produces a job whose plan expands into several grid tasks, so a
// pool has real parallel work to schedule.
func gridScrapeJob(id string, workers int) web.Job {
	job := testScrapeJob(id)
	job.Data.FastMode = false
	job.Data.Concurrency = 8
	job.Data.BrowserPool = 8
	job.Data.TaskWorkers = workers
	job.Data.GridBBox = "37.700,-122.520,37.820,-122.360"
	job.Data.GridCellKM = 5
	job.Data.MaxTime = 2 * time.Minute

	return job
}

func newPoolTestService(t *testing.T) (*web.Service, string) {
	t.Helper()

	dataFolder := t.TempDir()

	repository, err := sqlite.New(filepath.Join(dataFolder, "jobs.db"))
	if err != nil {
		t.Fatalf("create SQLite repository: %v", err)
	}

	t.Cleanup(func() {
		if closer, ok := repository.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	})

	return web.NewService(repository, dataFolder), dataFolder
}

func healthyResources(context.Context, string) (workerResourceSample, error) {
	return workerResourceSample{
		CPUPercent: 10, MemoryUsedBytes: 2 << 30,
		MemoryAvailableBytes: 8 << 30, DiskFreeBytes: 32 << 30,
	}, nil
}

// countingMate records concurrent Start calls so a test can prove the pool
// honours its bound, and writes one unique row per task.
type countingMate struct {
	output   io.Writer
	tracker  *poolTracker
	onStart  func(ctx context.Context, seedID string) error
	seedName func(jobs []scrapemate.IJob) string
}

type poolTracker struct {
	mu       sync.Mutex
	active   int
	maxSeen  int
	started  []string
	finished int32
}

func (tracker *poolTracker) enter(seed string) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	tracker.active++
	if tracker.active > tracker.maxSeen {
		tracker.maxSeen = tracker.active
	}

	tracker.started = append(tracker.started, seed)
}

func (tracker *poolTracker) exit() {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	tracker.active--
	atomic.AddInt32(&tracker.finished, 1)
}

func (tracker *poolTracker) peak() int {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	return tracker.maxSeen
}

func (tracker *poolTracker) startedSeeds() []string {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	out := append([]string(nil), tracker.started...)
	sort.Strings(out)

	return out
}

func (mate *countingMate) Start(ctx context.Context, jobs ...scrapemate.IJob) error {
	seed := "unknown"
	if len(jobs) > 0 {
		seed = jobs[0].GetID()
	}

	mate.tracker.enter(seed)
	defer mate.tracker.exit()

	if mate.onStart != nil {
		if err := mate.onStart(ctx, seed); err != nil {
			return err
		}
	}

	header := resultimport.LegacyHeaders()
	row := make([]string, len(header))

	for index, name := range header {
		switch name {
		case "place_id":
			// One distinct business per task, so a duplicated task execution or
			// a lost merge is visible in the final CSV.
			row[index] = "place-" + seed
		case "title":
			row[index] = "Business " + seed
		case "address":
			// The merge treats a shared address as the same business, so each
			// task must produce a genuinely distinct record.
			row[index] = seed + " Market Street, San Francisco"
		case "latitude":
			row[index] = "37.7749"
		case "longitude":
			row[index] = "-122.4194"
		}
	}

	writer := csv.NewWriter(mate.output)
	if err := writer.Write(header); err != nil {
		return err
	}

	if err := writer.Write(row); err != nil {
		return err
	}

	writer.Flush()

	return writer.Error()
}

func (*countingMate) Close() error { return nil }

func readResultPlaceIDs(t *testing.T, path string) []string {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open result CSV: %v", err)
	}

	defer func() { _ = file.Close() }()

	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatalf("read result CSV: %v", err)
	}

	if len(records) == 0 {
		return nil
	}

	column := -1

	for index, name := range records[0] {
		if name == "place_id" {
			column = index

			break
		}
	}

	if column < 0 {
		t.Fatalf("result CSV has no place_id column: %v", records[0])
	}

	ids := make([]string, 0, len(records)-1)
	for _, record := range records[1:] {
		if column < len(record) {
			ids = append(ids, record[column])
		}
	}

	sort.Strings(ids)

	return ids
}

// startBarrier proves overlap deterministically instead of relying on timing.
// The first `width` tasks to arrive rendezvous with each other; once that has
// happened the barrier stays open so the remaining tasks run freely. A
// sequential runner can never satisfy the rendezvous and falls through on the
// timeout with a peak of one, which is exactly the failure the test looks for.
type startBarrier struct {
	width   int32
	arrived atomic.Int32
	opened  chan struct{}
	once    sync.Once
	timeout time.Duration
}

func newStartBarrier(width int, timeout time.Duration) *startBarrier {
	return &startBarrier{width: int32(width), opened: make(chan struct{}), timeout: timeout}
}

func (barrier *startBarrier) arrive(ctx context.Context) {
	if barrier.arrived.Add(1) >= barrier.width {
		barrier.once.Do(func() { close(barrier.opened) })
	}

	select {
	case <-barrier.opened:
	case <-ctx.Done():
	case <-time.After(barrier.timeout):
	}
}

func TestTaskPoolPlanDividesTheBudgetInsteadOfMultiplyingIt(t *testing.T) {
	t.Parallel()

	job := gridScrapeJob("plan-job", 4)
	job.Data.Concurrency = 8
	job.Data.BrowserPool = 8

	plan := planTaskPool(&job, 8, 32)

	if plan.Workers != 4 {
		t.Fatalf("workers = %d, want 4", plan.Workers)
	}

	// The whole point of the division: four parallel tasks at two workers each
	// is the same total load as one task at eight.
	if plan.Workers*plan.PerTaskConcurrency != 8 {
		t.Fatalf("total concurrency = %d, want 8", plan.Workers*plan.PerTaskConcurrency)
	}

	if plan.Workers*plan.PerTaskBrowserPool != 8 {
		t.Fatalf("total browser pool = %d, want 8", plan.Workers*plan.PerTaskBrowserPool)
	}

	// A plan never spawns more workers than there is work for.
	if narrow := planTaskPool(&job, 8, 2); narrow.Workers != 2 {
		t.Fatalf("workers for two pending tasks = %d, want 2", narrow.Workers)
	}

	// An unset value still yields a usable, bounded default.
	job.Data.TaskWorkers = 0

	if fallback := planTaskPool(&job, 8, 32); fallback.Workers < 1 || fallback.Workers > defaultTaskWorkers {
		t.Fatalf("default workers = %d, want 1..%d", fallback.Workers, defaultTaskWorkers)
	}

	// The bound is enforced even if a job asks for more.
	job.Data.TaskWorkers = web.MaximumJobTaskWorkers + 50

	if capped := planTaskPool(&job, 64, 1000); capped.Workers != web.MaximumJobTaskWorkers {
		t.Fatalf("capped workers = %d, want %d", capped.Workers, web.MaximumJobTaskWorkers)
	}
}

func TestConcurrentTaskPoolRunsEveryTaskOnceWithinItsBound(t *testing.T) {
	t.Parallel()

	service, dataFolder := newPoolTestService(t)
	job := gridScrapeJob("22222222-2222-4222-8222-222222222222", 4)

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	tracker := &poolTracker{}
	barrier := newStartBarrier(4, 10*time.Second)
	worker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 8},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			return &countingMate{output: output, tracker: tracker, onStart: func(ctx context.Context, _ string) error {
				barrier.arrive(ctx)

				return nil
			}}, nil
		},
		sampleResources: healthyResources,
	}

	if err := worker.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("concurrent scrape: %v", err)
	}

	execution, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}

	if execution.Tasks.Total < 2 {
		t.Fatalf("expected a multi-task plan, got %#v", execution.Tasks)
	}

	if execution.Tasks.Completed != execution.Tasks.Total || execution.Tasks.Pending != 0 {
		t.Fatalf("tasks = %#v, want all completed", execution.Tasks)
	}

	// Bounded: never more tasks in flight than the pool allows.
	if peak := tracker.peak(); peak > 4 {
		t.Fatalf("peak concurrent tasks = %d, want at most 4", peak)
	}

	// Actually concurrent: the four-way rendezvous can only complete if four
	// tasks were genuinely in flight together.
	if peak := tracker.peak(); peak < 4 {
		t.Fatalf("peak concurrent tasks = %d, want the pool to overlap four tasks", peak)
	}

	// Exactly once: every planned task contributed one unique row.
	ids := readResultPlaceIDs(t, filepath.Join(dataFolder, job.ID+".csv"))
	if int64(len(ids)) != execution.Tasks.Total {
		t.Fatalf("result rows = %d, want %d", len(ids), execution.Tasks.Total)
	}

	for index := 1; index < len(ids); index++ {
		if ids[index] == ids[index-1] {
			t.Fatalf("duplicate committed row %q", ids[index])
		}
	}

	runtime, err := service.GetRuntime(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}

	if runtime.State != jobruntime.StateCompleted {
		t.Fatalf("state = %s, want completed", runtime.State)
	}
}

func TestCancelledTaskPoolReleasesInFlightWorkAndKeepsCommittedRows(t *testing.T) {
	t.Parallel()

	service, dataFolder := newPoolTestService(t)
	job := gridScrapeJob("33333333-3333-4333-8333-333333333333", 3)

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var completed atomic.Int32

	tracker := &poolTracker{}
	worker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 6},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			return &countingMate{output: output, tracker: tracker, onStart: func(taskCtx context.Context, _ string) error {
				select {
				case <-taskCtx.Done():
					return taskCtx.Err()
				case <-time.After(20 * time.Millisecond):
				}

				// Let the first wave commit, then cancel while the next wave is
				// still in flight.
				if completed.Add(1) == 4 {
					cancel()
				}

				return nil
			}}, nil
		},
		sampleResources: healthyResources,
	}

	if err := worker.scrapeJob(ctx, &job); err != nil {
		t.Fatalf("cancelled scrape returned an error: %v", err)
	}

	execution, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}

	// Interrupted work must be resumable, not failed: releasing a lease returns
	// the task to pending without spending one of its attempts.
	if execution.Tasks.Pending == 0 {
		t.Fatalf("cancelled run left no resumable task: %#v", execution.Tasks)
	}

	if execution.Tasks.Failed != 0 {
		t.Fatalf("cancellation recorded %d failed task(s); it must stay resumable", execution.Tasks.Failed)
	}

	if execution.Tasks.Running != 0 {
		t.Fatalf("cancelled run left %d task(s) leased", execution.Tasks.Running)
	}

	// Rows committed before the cancel survive it.
	ids := readResultPlaceIDs(t, filepath.Join(dataFolder, job.ID+".csv"))
	if len(ids) == 0 {
		t.Fatal("cancelled run discarded every committed row")
	}

	if int64(len(ids)) > execution.Tasks.Total {
		t.Fatalf("committed %d rows for %d tasks", len(ids), execution.Tasks.Total)
	}
}

func TestRestartAfterCancellationSkipsCompletedTasksAndDoesNotDuplicateRows(t *testing.T) {
	t.Parallel()

	service, dataFolder := newPoolTestService(t)
	job := gridScrapeJob("44444444-4444-4444-8444-444444444444", 3)

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()

	var seen atomic.Int32

	firstTracker := &poolTracker{}
	worker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 6},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			return &countingMate{output: output, tracker: firstTracker, onStart: func(taskCtx context.Context, _ string) error {
				select {
				case <-taskCtx.Done():
					return taskCtx.Err()
				case <-time.After(10 * time.Millisecond):
				}

				// Cancel only after a full wave has committed, so the restart
				// has both completed and pending tasks to reason about.
				if seen.Add(1) == 4 {
					cancelFirst()
				}

				return nil
			}}, nil
		},
		sampleResources: healthyResources,
	}

	if err := worker.scrapeJob(firstCtx, &job); err != nil {
		t.Fatalf("first run: %v", err)
	}

	interrupted, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get interrupted execution: %v", err)
	}

	if interrupted.Tasks.Completed == 0 || interrupted.Tasks.Pending == 0 {
		t.Fatalf("first run should have completed some and left some: %#v", interrupted.Tasks)
	}

	completedFirst := interrupted.Tasks.Completed

	// Resume the job and run it to completion.
	if _, _, err := service.ApplyControl(context.Background(), job.ID, jobruntime.ControlResume); err != nil {
		t.Fatalf("resume job: %v", err)
	}

	secondTracker := &poolTracker{}
	worker.setupMate = func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
		return &countingMate{output: output, tracker: secondTracker}, nil
	}

	if err := worker.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("resumed run: %v", err)
	}

	final, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get final execution: %v", err)
	}

	if final.Tasks.Completed != final.Tasks.Total || final.Tasks.Pending != 0 {
		t.Fatalf("resumed tasks = %#v, want all completed", final.Tasks)
	}

	// Exact completed-task recovery: the resume ran only what was left.
	ranOnResume := int64(len(secondTracker.startedSeeds()))
	if ranOnResume > final.Tasks.Total-completedFirst {
		t.Fatalf("resume re-ran %d task(s) but only %d remained",
			ranOnResume, final.Tasks.Total-completedFirst)
	}

	// Idempotent writes: one row per task even across two runs.
	ids := readResultPlaceIDs(t, filepath.Join(dataFolder, job.ID+".csv"))
	if int64(len(ids)) != final.Tasks.Total {
		t.Fatalf("result rows after resume = %d, want %d", len(ids), final.Tasks.Total)
	}

	for index := 1; index < len(ids); index++ {
		if ids[index] == ids[index-1] {
			t.Fatalf("resume duplicated committed row %q", ids[index])
		}
	}
}

func TestExpiredTaskLeaseIsReclaimedAndRerun(t *testing.T) {
	t.Parallel()

	service, dataFolder := newPoolTestService(t)
	job := gridScrapeJob("55555555-5555-4555-8555-555555555555", 1)

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	definitions := []web.JobTaskDefinition{
		{Key: "task-a", Kind: "map-query", Sequence: 0, Query: "coffee"},
		{Key: "task-b", Kind: "map-query", Sequence: 1, Query: "tea"},
	}

	if _, err := service.PrepareJobTasks(context.Background(), job.ID, definitions, 3); err != nil {
		t.Fatalf("prepare tasks: %v", err)
	}

	// A worker leases a task and then stops reporting.
	leased, found, err := service.ClaimNextJobTask(context.Background(), job.ID, "dead-worker", time.Second)
	if err != nil || !found {
		t.Fatalf("claim task = %v, found = %v", err, found)
	}

	if leased.Key != "task-a" {
		t.Fatalf("claimed %q, want the first task in sequence", leased.Key)
	}

	// While the lease is live nobody else may take that task.
	other, found, err := service.ClaimNextJobTask(context.Background(), job.ID, "second-worker", time.Minute)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}

	if found && other.Key == leased.Key {
		t.Fatalf("two workers leased %q at once", leased.Key)
	}

	if found {
		if err := service.ReleaseJobTask(context.Background(), job.ID, other.Key, "second-worker", "test"); err != nil {
			t.Fatalf("release second task: %v", err)
		}
	}

	// Lease deadlines are stored with one-second granularity, so wait past a
	// full second boundary before expecting expiry.
	time.Sleep(2100 * time.Millisecond)

	recovered, err := service.ReclaimExpiredJobTasks(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}

	if recovered < 1 {
		t.Fatalf("reclaimed %d lease(s), want at least 1", recovered)
	}

	// The dead worker can no longer finish the task it lost.
	heartbeatErr := service.HeartbeatJobTask(
		context.Background(), job.ID, leased.Key, "dead-worker", time.Minute,
	)
	if heartbeatErr == nil {
		t.Fatal("a reclaimed lease still accepted a heartbeat")
	}

	if !strings.Contains(heartbeatErr.Error(), "lease") {
		t.Fatalf("heartbeat error = %v, want a lease error", heartbeatErr)
	}

	// The reclaim did not spend an attempt, so the task is fully runnable again.
	reclaimed, found, err := service.ClaimNextJobTask(context.Background(), job.ID, "fresh-worker", time.Minute)
	if err != nil || !found {
		t.Fatalf("re-claim after expiry = %v, found = %v", err, found)
	}

	if reclaimed.Key != leased.Key {
		t.Fatalf("re-claimed %q, want the reclaimed task %q", reclaimed.Key, leased.Key)
	}

	if reclaimed.Attempts > leased.Attempts {
		t.Fatalf("reclaim consumed an attempt: %d then %d", leased.Attempts, reclaimed.Attempts)
	}

	_ = dataFolder
}

func TestConcurrentClaimsNeverHandOutTheSameTaskTwice(t *testing.T) {
	t.Parallel()

	service, _ := newPoolTestService(t)
	job := gridScrapeJob("66666666-6666-4666-8666-666666666667", 1)

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	const taskCount = 24

	definitions := make([]web.JobTaskDefinition, 0, taskCount)
	for index := range taskCount {
		definitions = append(definitions, web.JobTaskDefinition{
			Key: fmt.Sprintf("task-%02d", index), Kind: "map-query",
			Sequence: index, Query: fmt.Sprintf("query %d", index),
		})
	}

	if _, err := service.PrepareJobTasks(context.Background(), job.ID, definitions, 1); err != nil {
		t.Fatalf("prepare tasks: %v", err)
	}

	var (
		mu      sync.Mutex
		claimed = make(map[string]string)
		group   sync.WaitGroup
	)

	for worker := range 8 {
		owner := fmt.Sprintf("worker-%d", worker)

		group.Add(1)

		go func() {
			defer group.Done()

			for {
				task, found, err := service.ClaimNextJobTask(
					context.Background(), job.ID, owner, time.Minute,
				)
				if err != nil || !found {
					return
				}

				mu.Lock()
				if previous, exists := claimed[task.Key]; exists {
					mu.Unlock()
					t.Errorf("task %q leased by both %q and %q", task.Key, previous, owner)

					return
				}

				claimed[task.Key] = owner
				mu.Unlock()

				if err := service.CompleteJobTask(
					context.Background(), job.ID, task.Key, web.JobTaskCheckpoint{},
				); err != nil {
					t.Errorf("complete %q: %v", task.Key, err)

					return
				}
			}
		}()
	}

	group.Wait()

	mu.Lock()
	total := len(claimed)
	mu.Unlock()

	if total != taskCount {
		t.Fatalf("claimed %d distinct tasks, want %d", total, taskCount)
	}

	execution, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}

	if execution.Tasks.Completed != taskCount {
		t.Fatalf("completed = %d, want %d", execution.Tasks.Completed, taskCount)
	}
}

func TestOneFailingTaskRetriesWithoutFailingTheWholeJob(t *testing.T) {
	t.Parallel()

	service, dataFolder := newPoolTestService(t)
	job := gridScrapeJob("77777777-7777-4777-8777-777777777777", 3)
	job.Data.RetryCount = 2
	job.Data.RetryConfigured = true

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	var (
		mu         sync.Mutex
		doomedSeed string
		attempts   int
	)

	tracker := &poolTracker{}
	worker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 6},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			return &countingMate{output: output, tracker: tracker, onStart: func(_ context.Context, seed string) error {
				mu.Lock()
				defer mu.Unlock()

				// The first task to run becomes the one that keeps crashing,
				// standing in for a browser that dies on one grid cell.
				if doomedSeed == "" {
					doomedSeed = seed
				}

				if seed == doomedSeed {
					attempts++

					return fmt.Errorf("browser crashed on seed %s", seed)
				}

				return nil
			}}, nil
		},
		sampleResources: healthyResources,
	}

	if err := worker.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("scrape with a failing task: %v", err)
	}

	execution, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}

	// Every other task still finished: one crashing cell does not stop the job.
	if execution.Tasks.Completed != execution.Tasks.Total-1 {
		t.Fatalf("completed = %d of %d, want all but the failing task",
			execution.Tasks.Completed, execution.Tasks.Total)
	}

	if execution.Tasks.Failed != 1 {
		t.Fatalf("failed = %d, want exactly the crashing task", execution.Tasks.Failed)
	}

	// The failing task was retried up to its configured attempt limit.
	mu.Lock()
	observed := attempts
	mu.Unlock()

	if observed < 2 {
		t.Fatalf("failing task ran %d time(s), want it retried", observed)
	}

	// A job that lost one task of many is partial, not failed.
	runtime, err := service.GetRuntime(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}

	if runtime.State == jobruntime.StateFailed {
		t.Fatalf("one crashing task failed the whole job: %#v", runtime)
	}

	if runtime.OutcomeReason != jobruntime.StopReasonTaskFailures {
		t.Fatalf("outcome reason = %s, want task failures", runtime.OutcomeReason)
	}

	// The successful tasks' rows are all committed.
	ids := readResultPlaceIDs(t, filepath.Join(dataFolder, job.ID+".csv"))
	if int64(len(ids)) != execution.Tasks.Total-1 {
		t.Fatalf("committed %d row(s), want %d", len(ids), execution.Tasks.Total-1)
	}
}

func TestInterruptedJobReportsRecoveryStatusAndLastCheckpoint(t *testing.T) {
	t.Parallel()

	service, dataFolder := newPoolTestService(t)
	job := gridScrapeJob("88888888-8888-4888-8888-888888888888", 2)

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var seen atomic.Int32

	tracker := &poolTracker{}
	worker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 4},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			return &countingMate{output: output, tracker: tracker, onStart: func(taskCtx context.Context, _ string) error {
				select {
				case <-taskCtx.Done():
					return taskCtx.Err()
				case <-time.After(10 * time.Millisecond):
				}

				// Cancel only after earlier waves have merged, so the job has a
				// committed checkpoint to report alongside its remaining work.
				if seen.Add(1) == 5 {
					cancel()
				}

				return nil
			}}, nil
		},
		sampleResources: healthyResources,
	}

	if err := worker.scrapeJob(ctx, &job); err != nil {
		t.Fatalf("interrupted scrape: %v", err)
	}

	execution, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}

	// The durable snapshot must carry both a committed checkpoint and an honest
	// remaining count, which is what the monitor card renders.
	if execution.Checkpoint == nil {
		t.Fatal("interrupted job committed no checkpoint to report")
	}

	if execution.Checkpoint.CreatedAt.IsZero() {
		t.Fatal("checkpoint has no timestamp to display")
	}

	if execution.Tasks.Remaining() == 0 {
		t.Fatalf("interrupted job reports nothing remaining: %#v", execution.Tasks)
	}

	message := web.RecoveryStatusMessage(execution)
	if message == "" {
		t.Fatal("recovery status message is empty")
	}

	if !strings.Contains(message, "task") {
		t.Fatalf("recovery message does not describe remaining work: %q", message)
	}
}

func TestDecideFailureBudgetHalvesOnFailuresAndRecoversSlowly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		current, desired    int
		failures, successes int
		want                int
	}{
		{name: "quiet window changes nothing", current: 8, desired: 8, want: 8},
		{name: "small window changes nothing", current: 8, desired: 8, failures: 1, successes: 1, want: 8},
		{name: "half failed halves", current: 8, desired: 8, failures: 2, successes: 2, want: 4},
		{name: "mostly failed halves", current: 8, desired: 8, failures: 5, successes: 1, want: 4},
		{name: "halving floors at one", current: 1, desired: 8, failures: 4, successes: 0, want: 1},
		{name: "mixed window below half holds", current: 8, desired: 8, failures: 1, successes: 4, want: 8},
		{name: "clean window recovers one step", current: 4, desired: 8, failures: 0, successes: 3, want: 5},
		{name: "clean window at desired holds", current: 8, desired: 8, failures: 0, successes: 6, want: 8},
		{name: "recovery is one step even after a long clean window", current: 2, desired: 8, failures: 0, successes: 20, want: 3},
		{name: "budget never exceeds desired", current: 12, desired: 8, failures: 0, successes: 3, want: 8},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := decideFailureBudget(test.current, test.desired, test.failures, test.successes)
			if got != test.want {
				t.Fatalf("decideFailureBudget(%d, %d, %d, %d) = %d, want %d",
					test.current, test.desired, test.failures, test.successes, got, test.want)
			}
		})
	}

	// Decay must always be faster than recovery: from any budget, one bad
	// window loses more than one clean window regains.
	for budget := 2; budget <= 16; budget *= 2 {
		lost := budget - decideFailureBudget(budget, 16, 4, 0)
		regained := decideFailureBudget(budget, 16, 0, 3) - budget

		if lost < regained {
			t.Fatalf("budget %d: lost %d but regained %d — recovery must be slower than decay", budget, lost, regained)
		}
	}
}
