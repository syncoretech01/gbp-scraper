package webrunner

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

func TestStickyAssignmentPinsAndCapsProxies(t *testing.T) {
	t.Parallel()

	state := newLiveRunState(time.Now().Add(time.Hour))
	state.setProxyPlan(&web.ProxyPlan{
		PoolID:           "pool-1",
		Strategy:         "sticky_cell",
		Proxies:          []string{"http://proxy-a:1", "http://proxy-b:1", "http://proxy-c:1"},
		MaxTasksPerProxy: 2,
	}, false)

	taskInCell := func(cell string) web.JobTask {
		return web.JobTask{Key: "task-" + cell, Query: "dentist", SourceCell: cell}
	}

	// The same cell always lands on the same proxy.
	first, err := state.assignTaskProxies(taskInCell("cell-7"))
	if err != nil {
		t.Fatalf("assign: %v", err)
	}

	second, err := state.assignTaskProxies(taskInCell("cell-7"))
	if err != nil {
		t.Fatalf("assign again: %v", err)
	}

	if !first.override || len(first.proxies) != 1 || first.proxies[0] != second.proxies[0] {
		t.Fatalf("sticky assignment not stable: %v then %v", first.proxies, second.proxies)
	}

	// The cap removes a proxy once it has taken its share of tasks.
	if _, err := state.assignTaskProxies(taskInCell("cell-7")); err != nil {
		t.Fatalf("third assignment should fall over to another proxy: %v", err)
	}

	// A failed proxy is never assigned again, and exhausting every proxy is an
	// explicit error the pool turns into a pause.
	for index := range 3 {
		if state.markProxyFailed(index) != (index == 2) {
			t.Fatalf("markProxyFailed(%d) exhaustion signal wrong", index)
		}
	}

	if _, err := state.assignTaskProxies(taskInCell("cell-9")); !errors.Is(err, errNoUsableProxies) {
		t.Fatalf("exhausted pool error = %v", err)
	}
}

func TestNonStickyAssignmentKeepsWholePoolOrdered(t *testing.T) {
	t.Parallel()

	state := newLiveRunState(time.Now().Add(time.Hour))
	state.setProxyPlan(&web.ProxyPlan{
		PoolID:   "pool-1",
		Strategy: "round_robin",
		Proxies:  []string{"http://proxy-a:1", "http://proxy-b:1"},
	}, false)

	assignment, err := state.assignTaskProxies(web.JobTask{Key: "task-1", Query: "dentist"})
	if err != nil {
		t.Fatalf("assign: %v", err)
	}

	if len(assignment.proxies) != 2 || assignment.index != -1 {
		t.Fatalf("non-sticky assignment = %+v, want the whole pool unattributed", assignment)
	}

	// Direct mode overrides with no proxies at all.
	state.setProxyPlan(nil, true)

	direct, err := state.assignTaskProxies(web.JobTask{Key: "task-2"})
	if err != nil || !direct.override || len(direct.proxies) != 0 {
		t.Fatalf("direct assignment = %+v, %v", direct, err)
	}
}

func TestDeadlineExtensionIsIdempotentPerObservedTotal(t *testing.T) {
	t.Parallel()

	base := time.Now().Add(10 * time.Minute)
	state := newLiveRunState(base)

	if delta := state.applyExtension(900); delta != 900 {
		t.Fatalf("first extension delta = %d, want 900", delta)
	}

	// Re-reading the same durable total must not extend twice.
	if delta := state.applyExtension(900); delta != 0 {
		t.Fatalf("repeated extension delta = %d, want 0", delta)
	}

	if delta := state.applyExtension(1200); delta != 300 {
		t.Fatalf("incremental extension delta = %d, want 300", delta)
	}

	want := base.Add(1200 * time.Second)
	if got := state.deadline(); got.Sub(want) > time.Second || want.Sub(got) > time.Second {
		t.Fatalf("deadline = %v, want about %v", got, want)
	}
}

func TestClassifyTaskFailureMapsSignatures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err  error
		want string
	}{
		{errors.New("playwright: target closed"), "browser-failure"},
		{errors.New("chromium exited unexpectedly"), "browser-failure"},
		{errors.New("proxyconnect tcp: dial failed"), "proxy-failure"},
		{errors.New("socks5 handshake rejected"), "proxy-failure"},
		{errors.New("context deadline exceeded"), "website-timeout"},
		{errors.New("could not parse listing payload"), "parsing-failure"},
		{errors.New("something else entirely"), "task-failed"},
	}

	for _, test := range tests {
		if got := classifyTaskFailure(test.err); got != test.want {
			t.Fatalf("classifyTaskFailure(%v) = %s, want %s", test.err, got, test.want)
		}
	}
}

func TestLiveConcurrencyOverrideAndRuntimeExtensionReachTheRun(t *testing.T) {
	t.Parallel()

	service, dataFolder := newPoolTestService(t)
	job := gridScrapeJob("99999999-9999-4999-8999-999999999999", 2)
	// A short base runtime that the extension must rescue: without it the run
	// would hit its deadline mid-way.
	job.Data.MaxTime = 200 * time.Second

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	var (
		applied        atomic.Bool
		lowestObserved atomic.Int64
	)

	lowestObserved.Store(64)

	tracker := &poolTracker{}
	worker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 4},
		setupMate: func(_ context.Context, output io.Writer, taskJob *web.Job) (mateRunner, error) {
			// Record what each new task actually ran with; once the override
			// lands, later tasks take a smaller share (override 2 across 2
			// workers -> 1 each).
			observed := int64(taskJob.Data.Concurrency)
			for {
				current := lowestObserved.Load()
				if observed >= current || lowestObserved.CompareAndSwap(current, observed) {
					break
				}
			}

			return &countingMate{output: output, tracker: tracker, onStart: func(taskCtx context.Context, _ string) error {
				if applied.CompareAndSwap(false, true) {
					if err := service.SetJobConcurrencyOverride(context.Background(), job.ID, 2); err != nil {
						return err
					}

					if err := service.ExtendJobRuntime(context.Background(), job.ID, 30*time.Minute); err != nil {
						return err
					}
				}

				// Every task holds briefly so the queue outlives at least one
				// supervisor tick and later tasks observe the override.
				select {
				case <-taskCtx.Done():
					return taskCtx.Err()
				case <-time.After(700 * time.Millisecond):
				}

				return nil
			}}, nil
		},
		sampleResources: healthyResources,
	}

	if err := worker.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("scrape with live controls: %v", err)
	}

	execution, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}

	if execution.Tasks.Completed != execution.Tasks.Total || execution.Tasks.Failed != 0 {
		t.Fatalf("tasks = %#v, want all completed", execution.Tasks)
	}

	// The override reached at least one later task as a reduced share.
	if lowestObserved.Load() != 1 {
		t.Fatalf("lowest per-task concurrency = %d, want 1 after the override", lowestObserved.Load())
	}

	// The control events are durable and visible.
	events, err := service.EventsAfter(context.Background(), job.ID, 0, 200)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}

	var sawExtension, sawConcurrency bool

	for _, event := range events {
		switch event.Type {
		case "runtime-extended":
			sawExtension = true
		case "concurrency-changed":
			sawConcurrency = true
		}
	}

	if !sawExtension || !sawConcurrency {
		t.Fatalf("control events missing: extension=%v concurrency=%v", sawExtension, sawConcurrency)
	}
}

func TestExhaustedStickyPoolPausesTheJobAsProxiesUnavailable(t *testing.T) {
	t.Parallel()

	service, dataFolder := newPoolTestService(t)
	job := gridScrapeJob("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", 2)

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	tracker := &poolTracker{}
	worker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 4},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			return &countingMate{output: output, tracker: tracker, onStart: func(context.Context, string) error {
				// Every attempt fails with a proxy-classified error, so the
				// single-proxy pool becomes unusable almost immediately.
				return errors.New("proxyconnect tcp: connection refused")
			}}, nil
		},
		sampleResources: healthyResources,
		resolveProxyPlanForTest: func(context.Context, string) (web.ProxyPlan, error) {
			return web.ProxyPlan{
				PoolID: "pool-1", Strategy: "sticky_cell",
				Proxies: []string{"http://only-proxy:1"},
			}, nil
		},
	}
	job.Data.ProxyPoolID = "pool-1"

	if err := worker.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("scrape with dead pool: %v", err)
	}

	runtime, err := service.GetRuntime(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}

	if runtime.State != jobruntime.StatePaused || runtime.OutcomeReason != jobruntime.StopReasonProxiesUnavailable {
		t.Fatalf("runtime = %s/%s, want paused for proxies_unavailable", runtime.State, runtime.OutcomeReason)
	}

	// Work is preserved for a resume after the pool recovers.
	execution, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}

	if execution.Tasks.Remaining() == 0 {
		t.Fatalf("no resumable work left: %#v", execution.Tasks)
	}
}

func TestRetryCurrentRequeuesInFlightTasksWithoutConsumingAttempts(t *testing.T) {
	t.Parallel()

	service, dataFolder := newPoolTestService(t)
	job := gridScrapeJob("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", 1)
	job.Data.GridCellKM = 12 // few tasks, one worker

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	var requested atomic.Bool

	tracker := &poolTracker{}
	worker := &webrunner{
		svc: service,
		cfg: &runner.Config{DataFolder: dataFolder, Concurrency: 2},
		setupMate: func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
			return &countingMate{output: output, tracker: tracker, onStart: func(taskCtx context.Context, _ string) error {
				if requested.CompareAndSwap(false, true) {
					if err := service.RequestJobRetryCurrent(context.Background(), job.ID); err != nil {
						return err
					}

					// Wait for the supervisor to cancel this task.
					<-taskCtx.Done()

					return taskCtx.Err()
				}

				return nil
			}}, nil
		},
		sampleResources: healthyResources,
	}

	if err := worker.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("scrape with retry-current: %v", err)
	}

	execution, err := service.GetJobExecution(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}

	// The interrupted task was requeued and then completed; nothing failed.
	if execution.Tasks.Completed != execution.Tasks.Total || execution.Tasks.Failed != 0 {
		t.Fatalf("tasks = %#v, want everything completed with no failures", execution.Tasks)
	}

	events, err := service.EventsAfter(context.Background(), job.ID, 0, 200)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}

	found := false

	for _, event := range events {
		if event.Type == "retry-current" {
			found = true
		}
	}

	if !found {
		t.Fatal("retry-current event was not recorded")
	}

}
