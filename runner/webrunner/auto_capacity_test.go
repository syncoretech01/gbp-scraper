package webrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// decodedEvent is one worker event with its context JSON parsed, which is how
// every capacity assertion in this file reads the evidence a run recorded.
type decodedEvent struct {
	Message string
	Context map[string]any
}

// findWorkerEvent returns the first event of a type a job recorded. It reads
// the same durable event log the operator sees.
func findWorkerEvent(t *testing.T, service *web.Service, jobID, eventType string) decodedEvent {
	t.Helper()

	events, err := service.EventsAfter(context.Background(), jobID, 0, 500)
	if err != nil {
		t.Fatalf("read job events: %v", err)
	}

	for _, event := range events {
		if event.Type != eventType {
			continue
		}

		decoded := decodedEvent{Message: event.Message, Context: map[string]any{}}
		if event.Context != "" {
			if err := json.Unmarshal([]byte(event.Context), &decoded.Context); err != nil {
				t.Fatalf("decode %s context %q: %v", eventType, event.Context, err)
			}
		}

		return decoded
	}

	t.Fatalf("job %s recorded no %s event", jobID, eventType)

	return decodedEvent{}
}

// countWorkerEvents reports how many events of a type a job recorded.
func countWorkerEvents(t *testing.T, service *web.Service, jobID, eventType string) int {
	t.Helper()

	events, err := service.EventsAfter(context.Background(), jobID, 0, 500)
	if err != nil {
		t.Fatalf("read job events: %v", err)
	}

	total := 0

	for _, event := range events {
		if event.Type == eventType {
			total++
		}
	}

	return total
}

// countingMateFactory builds the pool's fake engine: it records task overlap
// and writes one unique row per task, exactly as the pool tests do.
func countingMateFactory(tracker *poolTracker) func(context.Context, io.Writer, *web.Job) (mateRunner, error) {
	return func(_ context.Context, output io.Writer, _ *web.Job) (mateRunner, error) {
		return &countingMate{output: output, tracker: tracker}, nil
	}
}

// fastModeAcceptanceJob reproduces the exact configuration of the live fast
// job cfe2d653-0fe9-4f43-80b8-9187572a992c, whose task-pool announcement read
// "Running 4 task(s) in parallel with 1 worker concurrency each (4 browser(s)
// planned)" while Fast mode had launched no browser at all.
func fastModeAcceptanceJob() web.Job {
	job := testScrapeJob("cfe2d653-0fe9-4f43-80b8-9187572a992c")
	job.Data.FastMode = true
	job.Data.Concurrency = 4
	job.Data.TaskWorkers = 4
	job.Data.BrowserPool = 2
	job.Data.PagesBrowser = 2

	return job
}

// TestFastModePlanLaunchesNoBrowsers is the regression for the announcement
// defect: a Fast-mode plan launches zero Chromium processes, so every browser
// figure it reports must be zero. The superseded arithmetic floored a pool at
// one browser per worker and told the operator a browserless run was holding
// four.
func TestFastModePlanLaunchesNoBrowsers(t *testing.T) {
	t.Parallel()

	job := fastModeAcceptanceJob()

	// Fast mode passes a zero browser budget, exactly as the checkpoint runner
	// does, because it is exempt from the browser-denominated budget.
	plan := planTaskPool(&job, 4, 5, 0, 0)

	if plan.Workers != 4 || plan.PerTaskConcurrency != 1 {
		t.Fatalf("plan = %+v, want the live job's 4 workers at 1 concurrency each", plan)
	}

	if !plan.FastMode {
		t.Fatal("plan did not carry Fast mode, so it cannot report browsers honestly")
	}

	if got := plan.PlannedBrowsers(); got != 0 {
		t.Fatalf("PlannedBrowsers() = %d for a Fast-mode plan, want 0", got)
	}

	// The runtime footprint already reported zero; plan time must agree, or the
	// operator sees a run "planned" for browsers it then never shows.
	run := &taskPoolRun{job: &job}
	run.workers.Store(int64(plan.Workers))
	run.effectiveConcurrency.Store(int64(plan.Workers * plan.PerTaskConcurrency))
	run.browserBudget.Store(int64(plan.PerTaskBrowserPool))
	run.pagesBudget.Store(int64(plan.PerTaskPages))

	if browsers, _ := run.liveBrowserFootprint(); browsers != int64(plan.PlannedBrowsers()) {
		t.Fatalf("live footprint %d disagrees with the planned %d", browsers, plan.PlannedBrowsers())
	}

	// The same job in browser mode still plans real browsers, so the fix is
	// scoped to the mode that has none.
	browser := fastModeAcceptanceJob()
	browser.Data.FastMode = false

	if got := planTaskPool(&browser, 4, 5, 4, 8).PlannedBrowsers(); got < 1 {
		t.Fatalf("browser-mode PlannedBrowsers() = %d, want at least one", got)
	}
}

// TestFastModePoolAnnouncementNamesNoBrowsers proves the operator-facing text
// and event context, not just the arithmetic: running the live fast job records
// a task-pool event whose message has no browser claim and whose
// planned_browsers is zero.
func TestFastModePoolAnnouncementNamesNoBrowsers(t *testing.T) {
	t.Parallel()

	service, dataFolder := newPoolTestService(t)
	job := fastModeAcceptanceJob()
	job.Data.GridBBox = "37.700,-122.520,37.820,-122.360"
	job.Data.GridCellKM = 5
	job.Data.MaxTime = time.Minute

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	tracker := &poolTracker{}
	worker := &webrunner{
		svc:             service,
		cfg:             &runner.Config{DataFolder: dataFolder, Concurrency: 4},
		setupMate:       countingMateFactory(tracker),
		sampleResources: healthyResources,
	}

	if err := worker.scrapeJob(context.Background(), &job); err != nil {
		t.Fatalf("fast-mode scrape: %v", err)
	}

	event := findWorkerEvent(t, service, job.ID, "task-pool")

	if strings.Contains(event.Message, "browser(s) planned") {
		t.Fatalf("fast-mode task-pool message claims browsers: %q", event.Message)
	}

	if !strings.Contains(event.Message, "no browser") {
		t.Fatalf("fast-mode task-pool message = %q, want it to say no browser is used", event.Message)
	}

	if planned, ok := event.Context["planned_browsers"].(float64); !ok || planned != 0 {
		t.Fatalf("planned_browsers = %v, want 0", event.Context["planned_browsers"])
	}

	// The browser budget arithmetic is meaningless without browsers and is
	// omitted rather than reported as a misleading zero-with-constants.
	if _, present := event.Context["per_browser_cost_bytes"]; present {
		t.Fatal("fast-mode task-pool context carried a per-browser cost")
	}
}

// TestDecideWorkerTargetHoldsTheCeilingAbsolutely proves the physical bound is
// applied before every other rule and regardless of any cooldown: a run that is
// somehow above what memory and CPU now allow comes straight back down.
func TestDecideWorkerTargetHoldsTheCeilingAbsolutely(t *testing.T) {
	t.Parallel()

	// A perfectly healthy window that would otherwise justify growth.
	healthy := workerScalingSignals{
		Ceiling: 2, Pending: 100, Successes: 20,
		CPUPercent: 5, MemoryAvailable: 32 << 30,
		ScaleCooldownElapsed: false, RecoveryCooldownElapsed: false,
	}

	if got := decideWorkerTarget(6, healthy); got.Workers != 2 {
		t.Fatalf("worker target above the ceiling = %d, want it clamped to 2", got.Workers)
	}

	if got := decideWorkerTarget(2, healthy); got.Workers != 2 {
		t.Fatalf("worker target at the ceiling = %d, want it held at 2", got.Workers)
	}
}

// TestDecideWorkerTargetGrowsOnlyOnCorroboratedHealth walks every growth veto
// one at a time. Each is asserted alone, so a rule that stops working shows up
// as its own failure rather than being masked by the others.
func TestDecideWorkerTargetGrowsOnlyOnCorroboratedHealth(t *testing.T) {
	t.Parallel()

	healthy := func() workerScalingSignals {
		return workerScalingSignals{
			Ceiling: 8, Pending: 100, Successes: adaptiveRecoveryAttempts,
			CPUPercent: 10, MemoryAvailable: 32 << 30,
			BrowserCensus: 2, AllowedBrowsers: 2,
			ScaleCooldownElapsed: true, RecoveryCooldownElapsed: true,
		}
	}

	if got := decideWorkerTarget(2, healthy()); got.Workers != 3 {
		t.Fatalf("healthy window target = %d, want one step up to 3", got.Workers)
	}

	vetoes := map[string]func(*workerScalingSignals){
		"no work left to claim":       func(s *workerScalingSignals) { s.Pending = 2 },
		"an unfinished failure":       func(s *workerScalingSignals) { s.Failures = 1 },
		"an unfinished block":         func(s *workerScalingSignals) { s.Blocks = 1 },
		"too few corroborating wins":  func(s *workerScalingSignals) { s.Successes = adaptiveRecoveryAttempts - 1 },
		"the scale cooldown":          func(s *workerScalingSignals) { s.ScaleCooldownElapsed = false },
		"the post-reduction cooldown": func(s *workerScalingSignals) { s.RecoveryCooldownElapsed = false },
		"a busy CPU":                  func(s *workerScalingSignals) { s.CPUPercent = recoveryCPUPercent },
		"no room for another browser": func(s *workerScalingSignals) {
			s.MemoryAvailable = recoveryMemoryBytes + perBrowserPlanningCostBytes - 1
		},
		"browsers still shutting down": func(s *workerScalingSignals) {
			s.BrowserCensus = s.AllowedBrowsers + browserHeadroomSlack + 1
		},
		"degraded task latency": func(s *workerScalingSignals) {
			s.TaskSamples = autoWorkerLatencySamples
			s.TaskBest = time.Second
			s.TaskMean = time.Duration(float64(time.Second) * autoTaskLatencyDegradationRatio)
		},
		"degraded write latency": func(s *workerScalingSignals) {
			s.WriteSamples = autoWorkerLatencySamples
			s.WriteBest = time.Millisecond
			s.WriteMean = time.Duration(float64(time.Millisecond) * autoWriteLatencyDegradationRatio)
		},
	}

	for name, veto := range vetoes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			signals := healthy()
			veto(&signals)

			// A degraded WRITE latency is a reduction trigger, not merely a
			// veto, so the only thing asserted for every case is that the
			// controller does not take MORE capacity.
			if got := decideWorkerTarget(2, signals); got.Workers > 2 {
				t.Fatalf("%s still grew to %d workers", name, got.Workers)
			}
		})
	}
}

// TestDecideWorkerTargetCollapsesOnAdverseSignals proves decay is decisive:
// refusal and failure cascades halve the pool, resource pressure steps it down,
// and nothing ever falls below one worker.
func TestDecideWorkerTargetCollapsesOnAdverseSignals(t *testing.T) {
	t.Parallel()

	base := workerScalingSignals{
		Ceiling: 8, Pending: 100, Successes: 10,
		CPUPercent: 10, MemoryAvailable: 32 << 30,
		ScaleCooldownElapsed: true, RecoveryCooldownElapsed: true,
	}

	tests := []struct {
		name    string
		mutate  func(*workerScalingSignals)
		current int
		want    int
	}{
		{
			name:    "one platform refusal halves the pool",
			mutate:  func(s *workerScalingSignals) { s.Blocks = 1 },
			current: 8, want: 4,
		},
		{
			name: "a failing majority halves the pool",
			mutate: func(s *workerScalingSignals) {
				s.Failures = 4
				s.Successes = 2
			},
			current: 8, want: 4,
		},
		{
			name: "the memory ceiling steps it down",
			mutate: func(s *workerScalingSignals) {
				s.MemoryCeiling = 4 << 30
				s.MemoryUsed = 5 << 30
			},
			current: 4, want: 3,
		},
		{
			name:    "critical memory halves the pool",
			mutate:  func(s *workerScalingSignals) { s.MemoryAvailable = severeMemoryBytes - 1 },
			current: 6, want: 3,
		},
		{
			name:    "memory pressure steps it down",
			mutate:  func(s *workerScalingSignals) { s.MemoryAvailable = moderateMemoryBytes - 1 },
			current: 4, want: 3,
		},
		{
			name:    "a saturated CPU steps it down",
			mutate:  func(s *workerScalingSignals) { s.CPUPercent = autoWorkerCPUSaturatedPercent },
			current: 4, want: 3,
		},
		{
			name: "degraded database write latency steps it down",
			mutate: func(s *workerScalingSignals) {
				s.WriteSamples = autoWorkerLatencySamples
				s.WriteBest = time.Millisecond
				s.WriteMean = 10 * time.Millisecond
			},
			current: 4, want: 3,
		},
		{
			name:    "a block never drives the pool below one worker",
			mutate:  func(s *workerScalingSignals) { s.Blocks = 3 },
			current: 1, want: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			signals := base
			test.mutate(&signals)

			got := decideWorkerTarget(test.current, signals)
			if got.Workers != test.want {
				t.Fatalf("target = %d, want %d (%s)", got.Workers, test.want, got.Reason)
			}

			if test.want != test.current && got.Reason == "" {
				t.Fatal("a worker-count change was decided without an operator-facing reason")
			}
		})
	}
}

// TestDecideWorkerTargetReducesEvenDuringACooldown proves the asymmetry that
// keeps the controller safe: the scale cooldown throttles GROWTH, but a block
// or a memory cliff is acted on immediately.
func TestDecideWorkerTargetReducesEvenDuringACooldown(t *testing.T) {
	t.Parallel()

	signals := workerScalingSignals{
		Ceiling: 8, Pending: 100, Blocks: 1,
		CPUPercent: 10, MemoryAvailable: 32 << 30,
		ScaleCooldownElapsed: false, RecoveryCooldownElapsed: false,
	}

	if got := decideWorkerTarget(4, signals); got.Workers != 2 {
		t.Fatalf("blocked window during a cooldown = %d workers, want 2", got.Workers)
	}
}

// TestEffectiveCPUCoresReadsTheContainerQuota proves the CPU dimension is
// container-aware, so a two-core container does not plan for its host's cores.
func TestEffectiveCPUCoresReadsTheContainerQuota(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	// cgroup v2: "quota period" in microseconds.
	writeFile(t, filepath.Join(root, cgroupCPUMaxPath), "200000 100000\n")

	cores, ok := cgroupCPUCores(root)
	if !ok || cores != 2 {
		t.Fatalf("cgroup v2 quota cores = (%d, %t), want (2, true)", cores, ok)
	}

	if got := effectiveCPUCores(root); got > 2 {
		t.Fatalf("effective cores = %d, want the quota to bound it at 2", got)
	}

	// A fractional quota rounds up: 1.5 cores still permits two workers.
	writeFile(t, filepath.Join(root, cgroupCPUMaxPath), "150000 100000\n")

	cores, ok = cgroupCPUCores(root)
	if !ok || cores != 2 {
		t.Fatalf("fractional quota cores = (%d, %t), want (2, true)", cores, ok)
	}

	// An unlimited container reports the visible core count untouched.
	writeFile(t, filepath.Join(root, cgroupCPUMaxPath), "max 100000\n")

	if _, ok := cgroupCPUCores(root); ok {
		t.Fatal("an unlimited cpu.max was read as a quota")
	}

	if got := effectiveCPUCores(root); got < 1 {
		t.Fatalf("effective cores = %d, want at least one", got)
	}

	// No cgroup files at all (native Windows) must never report zero cores.
	if got := effectiveCPUCores(filepath.Join(root, "absent")); got < 1 {
		t.Fatalf("effective cores without cgroups = %d, want at least one", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestLatencySeriesTracksItsOwnBest proves the contention signal needs no
// absolute threshold: it compares a run against the best it itself achieved.
func TestLatencySeriesTracksItsOwnBest(t *testing.T) {
	t.Parallel()

	var series latencySeries

	for range 10 {
		series.observe(10 * time.Millisecond)
	}

	mean, best, samples := series.snapshot()
	if samples != 10 {
		t.Fatalf("samples = %d, want 10", samples)
	}

	if latencyDegraded(mean, best, samples, autoWriteLatencyDegradationRatio) {
		t.Fatalf("a steady series reported degradation (mean %s, best %s)", mean, best)
	}

	for range 20 {
		series.observe(200 * time.Millisecond)
	}

	mean, best, samples = series.snapshot()
	if !latencyDegraded(mean, best, samples, autoWriteLatencyDegradationRatio) {
		t.Fatalf("a 20x slowdown was not reported (mean %s, best %s)", mean, best)
	}

	// Too few samples is never evidence, however extreme.
	if latencyDegraded(time.Hour, time.Nanosecond, autoWorkerLatencySamples-1, 2) {
		t.Fatal("an unsampled series reported degradation")
	}
}

// TestDynamicWorkerGroupAcceptsLateSpawns proves the mechanism that lets the
// supervisor add a worker long after the initial fan-out: a plain WaitGroup
// panics if an Add races the counter reaching zero, and this group must instead
// either accept the worker or refuse it cleanly.
func TestDynamicWorkerGroupAcceptsLateSpawns(t *testing.T) {
	t.Parallel()

	group := newDynamicWorkerGroup()

	var (
		started atomic.Int64
		release = make(chan struct{})
	)

	if !group.spawn(func() { started.Add(1); <-release }) {
		t.Fatal("the first spawn was refused")
	}

	// A spawn issued while a worker is running is accepted.
	if !group.spawn(func() { started.Add(1); <-release }) {
		t.Fatal("a concurrent spawn was refused")
	}

	close(release)
	group.wait()

	if got := started.Load(); got != 2 {
		t.Fatalf("started workers = %d, want 2", got)
	}

	// Once wait has observed an idle pool the group is closed, so a late spawn
	// is refused rather than resurrecting a finished run.
	if group.spawn(func() { started.Add(1) }) {
		t.Fatal("a spawn after wait() was accepted")
	}

	if got := started.Load(); got != 2 {
		t.Fatalf("a refused spawn still ran work: %d", got)
	}
}

// TestDynamicWorkerGroupSurvivesConcurrentSpawnAndWait is the race the
// mechanism exists for: spawns issued from another goroutine while wait() is
// deciding the pool is idle. Every accepted spawn must be waited for, and no
// accepted spawn may run after wait returns.
func TestDynamicWorkerGroupSurvivesConcurrentSpawnAndWait(t *testing.T) {
	t.Parallel()

	group := newDynamicWorkerGroup()

	var (
		running  atomic.Int64
		finished atomic.Int64
		spawner  sync.WaitGroup
	)

	group.spawn(func() { time.Sleep(5 * time.Millisecond) })

	spawner.Add(1)

	go func() {
		defer spawner.Done()

		for range 200 {
			if group.spawn(func() {
				running.Add(1)
				time.Sleep(time.Millisecond)
				finished.Add(1)
			}) {
				continue
			}

			return
		}
	}()

	group.wait()
	spawner.Wait()

	if running.Load() != finished.Load() {
		t.Fatalf("wait() returned with %d worker(s) still running",
			running.Load()-finished.Load())
	}
}

// TestRetireWorkerShrinksOnlyToTheTargetAndNeverBelowOne proves the shrink half
// of the controller. A worker retires only between tasks, so this is the exact
// gate that has to be conservative: it must never retire the last worker and
// never overshoot the target, however many workers ask at once.
func TestRetireWorkerShrinksOnlyToTheTargetAndNeverBelowOne(t *testing.T) {
	t.Parallel()

	run := &taskPoolRun{}
	run.workers.Store(6)
	run.workerTarget.Store(2)

	retired := 0
	for run.retireWorker() {
		retired++

		if retired > 10 {
			t.Fatal("retireWorker never stopped")
		}
	}

	if retired != 4 || run.workers.Load() != 2 {
		t.Fatalf("retired %d workers leaving %d, want 4 leaving 2", retired, run.workers.Load())
	}

	// A target of zero (or below) still leaves the run one worker.
	run.workerTarget.Store(0)

	for run.retireWorker() {
	}

	if got := run.workers.Load(); got != 1 {
		t.Fatalf("workers after a zero target = %d, want the last worker kept", got)
	}
}

// TestRetireWorkerIsRaceFreeUnderConcurrentWorkers proves several workers
// consulting the gate at once retire exactly the surplus, never more. Over-
// retiring would silently narrow a run below the target it was told to hold.
func TestRetireWorkerIsRaceFreeUnderConcurrentWorkers(t *testing.T) {
	t.Parallel()

	run := &taskPoolRun{}
	run.workers.Store(8)
	run.workerTarget.Store(3)

	var (
		retired atomic.Int64
		group   sync.WaitGroup
	)

	for range 8 {
		group.Add(1)

		go func() {
			defer group.Done()

			for run.retireWorker() {
				retired.Add(1)
			}
		}()
	}

	group.Wait()

	if retired.Load() != 5 || run.workers.Load() != 3 {
		t.Fatalf("retired %d leaving %d workers, want 5 leaving 3",
			retired.Load(), run.workers.Load())
	}
}

// TestWorkerCeilingNeverExceedsTheBrowserBudget is the safety invariant of the
// whole throughput change, asserted directly against the controller's ceiling:
// whatever the operator asked for and whatever the host reports, workers times
// the per-worker browser pool stays inside the browser-denominated budget.
func TestWorkerCeilingNeverExceedsTheBrowserBudget(t *testing.T) {
	t.Parallel()

	worker := &webrunner{}

	memories := []uint64{0, 1 << 30, 2 << 30, 4 << 30, 8 << 30, 16 << 30, 64 << 30}
	cores := []int{1, 2, 4, 8, 32}
	pools := []int64{1, 2, 4}
	requested := []int{0, 1, 4, 16}

	for _, memory := range memories {
		for _, core := range cores {
			for _, pool := range pools {
				for _, want := range requested {
					job := testScrapeJob("ceiling")
					job.Data.FastMode = false
					job.Data.TaskWorkers = want

					run := &taskPoolRun{job: &job}
					run.browserBudget.Store(pool)

					sample := workerResourceSample{MemoryAvailableBytes: memory, CPUCores: core}

					ceiling, reason := worker.workerCeilingForRun(run, sample, 16)
					if ceiling < 1 {
						t.Fatalf("ceiling %d fell below one worker", ceiling)
					}

					if reason == "" {
						t.Fatal("a capacity ceiling was produced without naming its bound")
					}

					if memory == 0 {
						continue
					}

					budget := browserProcessBudget(sample)
					browsers := ceiling * int(pool)

					// A run always keeps one worker, and that worker holds its
					// whole pool, so the honest invariant is that the ceiling
					// never buys MORE than the budget beyond the irreducible
					// single worker. Whenever the budget can hold one worker's
					// pool at all, the strict bound applies.
					if budget >= int(pool) && browsers > budget {
						t.Fatalf(
							"memory=%d cores=%d pool=%d requested=%d: ceiling %d plans %d browsers, budget is %d",
							memory, core, pool, want, ceiling, browsers, budget,
						)
					}

					if budget < int(pool) && ceiling != 1 {
						t.Fatalf(
							"memory=%d cores=%d pool=%d: ceiling %d, want the irreducible single worker",
							memory, core, pool, ceiling,
						)
					}
				}
			}
		}
	}
}

// TestWorkerCeilingNeverExceedsTheOperatorsLoadBudget proves auto capacity
// reshapes the operator's concurrency budget between workers and per-task
// concurrency but never exceeds it, and never exceeds an explicit parallel-task
// choice either. Growing load past what the operator asked for is exactly the
// way a throughput change would buy speed with platform blocks.
func TestWorkerCeilingNeverExceedsTheOperatorsLoadBudget(t *testing.T) {
	t.Parallel()

	worker := &webrunner{}
	sample := workerResourceSample{MemoryAvailableBytes: 64 << 30, CPUCores: 32}

	job := testScrapeJob("load-budget")
	job.Data.FastMode = false

	run := &taskPoolRun{job: &job}
	run.browserBudget.Store(1)

	if got, _ := worker.workerCeilingForRun(run, sample, 4); got != 4 {
		t.Fatalf("ceiling with a concurrency budget of 4 = %d, want 4", got)
	}

	job.Data.TaskWorkers = 2

	got, reason := worker.workerCeilingForRun(run, sample, 16)
	if got != 2 {
		t.Fatalf("ceiling with an explicit 2 parallel tasks = %d, want 2", got)
	}

	if !strings.Contains(reason, "configured parallel-task count") {
		t.Fatalf("ceiling reason = %q, want it to name the operator's own setting", reason)
	}

	// Fast mode has no browser bound, but the load budget still binds it.
	fast := testScrapeJob("load-budget-fast")
	fast.Data.FastMode = true

	fastRun := &taskPoolRun{job: &fast}

	got, reason = worker.workerCeilingForRun(fastRun, workerResourceSample{}, 3)
	if got != 3 {
		t.Fatalf("fast-mode ceiling = %d, want the concurrency budget of 3", got)
	}

	// The first live fast-mode run of this controller clamped 4 workers to 2
	// and explained it as "the measured memory and CPU budget", which is a
	// bound Fast mode never consults. A clamp must name the bound that actually
	// produced it.
	if strings.Contains(reason, "memory") || strings.Contains(reason, "browser") {
		t.Fatalf("fast-mode ceiling reason = %q, want no memory or browser claim", reason)
	}

	if !strings.Contains(reason, "concurrency budget") {
		t.Fatalf("fast-mode ceiling reason = %q, want the concurrency budget named", reason)
	}
}

// TestAutoCapacityDoesNotRepeatAnInFlightReduction is the regression for the
// second defect the first live fast-mode run exposed: a reduction takes effect
// only when the surplus workers reach their next claim boundary, which may be a
// whole task away, and the controller recorded the identical "reduced 4 to 2"
// event on every window in between.
func TestAutoCapacityDoesNotRepeatAnInFlightReduction(t *testing.T) {
	t.Parallel()

	worker, run, _ := autoCapacityRun(t, 40)
	run.workers.Store(4)
	run.workerTarget.Store(4)

	sample, err := healthyResources(context.Background(), "")
	if err != nil {
		t.Fatalf("sample: %v", err)
	}

	// Three consecutive blocked windows with the workers still mid-task: the
	// decision is the same each time, so only the first is news.
	for range 3 {
		worker.adaptWorkerCount(run, sample, 0, 5, 1, 8)
	}

	if got := run.workerTarget.Load(); got != 2 {
		t.Fatalf("worker target = %d, want 2", got)
	}

	if got := countWorkerEvents(t, worker.svc, run.job.ID, "adaptive-workers"); got != 1 {
		t.Fatalf("recorded %d scaling events for one in-flight reduction, want 1", got)
	}

	// Once the workers actually retire, a further collapse is news again.
	for run.retireWorker() {
	}

	worker.adaptWorkerCount(run, sample, 0, 5, 1, 8)

	if got := run.workerTarget.Load(); got != 1 {
		t.Fatalf("worker target after a second block = %d, want 1", got)
	}

	if got := countWorkerEvents(t, worker.svc, run.job.ID, "adaptive-workers"); got != 2 {
		t.Fatalf("recorded %d scaling events, want 2", got)
	}

	run.workerGroup.wait()
}

// TestFastModeScalingEventCarriesNoBrowserArithmetic proves the third half of
// the same defect: a Fast-mode scaling decision must not attach a browser
// budget, a browser census or a memory-derived worker ceiling to its evidence,
// because none of them took part in it.
func TestFastModeScalingEventCarriesNoBrowserArithmetic(t *testing.T) {
	t.Parallel()

	worker, run, _ := autoCapacityRun(t, 40)
	run.job.Data.FastMode = true
	run.workers.Store(4)
	run.workerTarget.Store(4)

	sample, err := healthyResources(context.Background(), "")
	if err != nil {
		t.Fatalf("sample: %v", err)
	}

	worker.adaptWorkerCount(run, sample, 0, 5, 1, 8)

	event := findWorkerEvent(t, worker.svc, run.job.ID, "adaptive-workers")

	for _, key := range []string{
		"browser_budget_total", "browser_processes", "browser_memory_bytes",
		"auto_worker_ceiling", "per_task_browser_pool",
	} {
		if _, present := event.Context[key]; present {
			t.Errorf("fast-mode scaling event carried %q", key)
		}
	}

	run.workerGroup.wait()
}

// autoCapacityRun builds a live pool run against a real SQLite-backed service
// with a real durable plan, so the controller is exercised against the same
// pending-work query and the same event log production uses. The engine is the
// only thing stubbed.
func autoCapacityRun(t *testing.T, pendingTasks int) (*webrunner, *taskPoolRun, *atomic.Int64) {
	t.Helper()

	service, dataFolder := newPoolTestService(t)

	job := gridScrapeJob("7100e95b-28f9-4979-9e85-8cd2294f0173", 0)
	job.Data.FastMode = false
	job.Data.Adaptive = true

	if err := service.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job: %v", err)
	}

	definitions := make([]web.JobTaskDefinition, 0, pendingTasks)
	for index := range pendingTasks {
		definitions = append(definitions, web.JobTaskDefinition{
			Key:      fmt.Sprintf("task-%03d", index),
			Kind:     "map-search",
			Sequence: index,
			Query:    "coffee",
		})
	}

	if _, err := service.PrepareJobTasks(context.Background(), job.ID, definitions, 4); err != nil {
		t.Fatalf("prepare tasks: %v", err)
	}

	worker := &webrunner{
		svc:                 service,
		cfg:                 &runner.Config{DataFolder: dataFolder, Concurrency: 8},
		sampleResources:     healthyResources,
		workerScaleCooldown: time.Nanosecond,
	}

	run := &taskPoolRun{job: &job, workerGroup: newDynamicWorkerGroup(), scaleCooldown: time.Nanosecond}
	run.desiredConcurrency.Store(8)
	run.effectiveConcurrency.Store(8)
	run.workers.Store(2)
	run.workerTarget.Store(2)
	run.browserBudget.Store(1)
	run.pagesBudget.Store(1)

	spawned := &atomic.Int64{}
	run.spawnWorker = func() bool {
		spawned.Add(1)

		return run.workerGroup.spawn(func() {})
	}

	return worker, run, spawned
}

// TestAutoCapacityGrowsAHealthyRun is the throughput regression. The 180-task
// acceptance run measured 1.99 average parallelism over 52.5 minutes with zero
// failures, zero retries and zero blocks on a host whose own browser budget was
// eight — because the worker count was frozen at plan time and capped by a
// constant. A clean window with pending work and measured head-room must now
// take a worker back.
func TestAutoCapacityGrowsAHealthyRun(t *testing.T) {
	t.Parallel()

	worker, run, spawned := autoCapacityRun(t, 40)

	sample, err := healthyResources(context.Background(), "")
	if err != nil {
		t.Fatalf("sample: %v", err)
	}

	worker.adaptWorkerCount(run, sample, 0, adaptiveRecoveryAttempts, 0, 8)

	if got := run.workers.Load(); got != 3 {
		t.Fatalf("workers after a clean window = %d, want one step up to 3", got)
	}

	if got := spawned.Load(); got != 1 {
		t.Fatalf("spawned workers = %d, want exactly one", got)
	}

	event := findWorkerEvent(t, worker.svc, run.job.ID, "adaptive-workers")
	if !strings.Contains(event.Message, "increased parallel tasks from 2 to 3") {
		t.Fatalf("scaling event = %q, want it to name the increase", event.Message)
	}

	if reason, _ := event.Context["reason"].(string); reason == "" {
		t.Fatal("a worker-count change was recorded without a reason")
	}

	// Growth is one step at a time, never a jump to the ceiling.
	worker.adaptWorkerCount(run, sample, 0, adaptiveRecoveryAttempts, 0, 8)

	if got := run.workers.Load(); got != 4 {
		t.Fatalf("workers after a second clean window = %d, want 4", got)
	}

	run.workerGroup.wait()
}

// TestAutoCapacityStopsGrowingWhenThePlanRunsOut proves the controller spends
// browsers only on work that exists: a plan with fewer pending tasks than live
// workers buys nothing by adding another.
func TestAutoCapacityStopsGrowingWhenThePlanRunsOut(t *testing.T) {
	t.Parallel()

	worker, run, spawned := autoCapacityRun(t, 1)

	sample, err := healthyResources(context.Background(), "")
	if err != nil {
		t.Fatalf("sample: %v", err)
	}

	worker.adaptWorkerCount(run, sample, 0, 10, 0, 8)

	if got := run.workers.Load(); got != 2 {
		t.Fatalf("workers = %d, want the pool held at 2 with one task left", got)
	}

	if got := spawned.Load(); got != 0 {
		t.Fatalf("spawned %d workers for a nearly drained plan", got)
	}

	run.workerGroup.wait()
}

// TestAutoCapacityCollapsesOnABlockedWindow proves the safety half end to end:
// a platform refusal halves the worker target immediately, the change is
// recorded, and the surplus workers retire themselves at their next claim
// boundary rather than being killed mid-task.
func TestAutoCapacityCollapsesOnABlockedWindow(t *testing.T) {
	t.Parallel()

	worker, run, _ := autoCapacityRun(t, 40)
	run.workers.Store(4)
	run.workerTarget.Store(4)

	sample, err := healthyResources(context.Background(), "")
	if err != nil {
		t.Fatalf("sample: %v", err)
	}

	worker.adaptWorkerCount(run, sample, 0, 5, 1, 8)

	if got := run.workerTarget.Load(); got != 2 {
		t.Fatalf("worker target after a block = %d, want it halved to 2", got)
	}

	// The live count does not drop until workers reach their claim boundary:
	// a worker holding a lease is never abandoned to make room.
	if got := run.workers.Load(); got != 4 {
		t.Fatalf("live workers = %d, want the four in-flight workers untouched", got)
	}

	retired := 0
	for run.retireWorker() {
		retired++
	}

	if retired != 2 || run.workers.Load() != 2 {
		t.Fatalf("retired %d leaving %d, want 2 leaving 2", retired, run.workers.Load())
	}

	event := findWorkerEvent(t, worker.svc, run.job.ID, "adaptive-workers")
	if !strings.Contains(event.Message, "reduced parallel tasks from 4 to 2") {
		t.Fatalf("scaling event = %q, want it to name the reduction", event.Message)
	}

	if !strings.Contains(event.Message, "refused") {
		t.Fatalf("scaling event = %q, want the platform refusal named", event.Message)
	}

	run.workerGroup.wait()
}

// TestAutoCapacityStaysSilentWhenNothingChanges proves the controller does not
// spam the event log: a window that decides to hold records nothing.
func TestAutoCapacityStaysSilentWhenNothingChanges(t *testing.T) {
	t.Parallel()

	worker, run, _ := autoCapacityRun(t, 40)

	sample, err := healthyResources(context.Background(), "")
	if err != nil {
		t.Fatalf("sample: %v", err)
	}

	// Too few corroborating successes to grow, nothing adverse to shrink.
	worker.adaptWorkerCount(run, sample, 0, adaptiveRecoveryAttempts-1, 0, 8)

	if got := countWorkerEvents(t, worker.svc, run.job.ID, "adaptive-workers"); got != 0 {
		t.Fatalf("recorded %d scaling events for an unchanged pool", got)
	}

	if got := run.workers.Load(); got != 2 {
		t.Fatalf("workers = %d, want them unchanged at 2", got)
	}

	run.workerGroup.wait()
}

// TestAutoCapacityRetriesGrowthAfterASpawnIsRefused proves the liveness half of
// the duplicate-event suppression. Suppression is correct for a REDUCTION,
// which takes a whole task to land; applying it to growth would let one refused
// spawn leave an unreachable target behind, after which every later window
// would look like "already decided" and the pool would freeze at a width it
// never actually reached.
func TestAutoCapacityRetriesGrowthAfterASpawnIsRefused(t *testing.T) {
	t.Parallel()

	worker, run, spawned := autoCapacityRun(t, 40)

	refuse := true
	run.spawnWorker = func() bool {
		if refuse {
			return false
		}

		spawned.Add(1)

		return run.workerGroup.spawn(func() {})
	}

	sample, err := healthyResources(context.Background(), "")
	if err != nil {
		t.Fatalf("sample: %v", err)
	}

	worker.adaptWorkerCount(run, sample, 0, adaptiveRecoveryAttempts, 0, 8)

	if got := run.workers.Load(); got != 2 {
		t.Fatalf("workers after a refused spawn = %d, want the original 2", got)
	}

	if got := run.workerTarget.Load(); got != 2 {
		t.Fatalf("worker target after a refused spawn = %d, want it back at the reachable 2", got)
	}

	if got := countWorkerEvents(t, worker.svc, run.job.ID, "adaptive-workers"); got != 0 {
		t.Fatalf("recorded %d scaling events for a growth that never happened", got)
	}

	// The next healthy window must try again rather than treating the earlier
	// unreachable target as a decision already taken.
	refuse = false
	worker.adaptWorkerCount(run, sample, 0, adaptiveRecoveryAttempts, 0, 8)

	if got := run.workers.Load(); got != 3 {
		t.Fatalf("workers after the retry = %d, want 3", got)
	}

	if got := spawned.Load(); got != 1 {
		t.Fatalf("spawned = %d, want exactly one worker on the retry", got)
	}

	run.workerGroup.wait()
}

// TestAutoCapacityCorroboratesGrowthAcrossSamplingTicks is the regression for
// the flaw that would have made the whole growth half of auto capacity dead
// code in production.
//
// The concurrency controller re-decides every resourceSampleInterval (two
// seconds) and empties its counters each time. A browser-mode task takes tens
// of seconds — the acceptance run measured a 31s median — so a two-second
// window almost never holds the three corroborating successes a growth step
// requires. Reading the per-tick window, the controller could react to trouble
// but could never take capacity back on any real run.
func TestAutoCapacityCorroboratesGrowthAcrossSamplingTicks(t *testing.T) {
	t.Parallel()

	worker, run, spawned := autoCapacityRun(t, 40)

	sample, err := healthyResources(context.Background(), "")
	if err != nil {
		t.Fatalf("sample: %v", err)
	}

	// A settling window several sampling ticks long, as production has: the
	// cooldown is 45s against a 2s tick.
	const cooldown = 400 * time.Millisecond

	run.scaleCooldown = cooldown
	run.lastWorkerChangeAt.Store(time.Now().UnixNano())

	// A run whose tasks outlast the sampling tick: most ticks are empty, and a
	// success lands only occasionally. No single tick ever carries enough
	// evidence on its own.
	ticks := []int{0, 1, 0, 0, 1, 0, 0, 1, 0}
	for _, successes := range ticks {
		worker.adaptWorkerCount(run, sample, 0, successes, 0, 8)
	}

	if got := run.workers.Load(); got != 2 {
		t.Fatalf("workers grew to %d during the settling window, want it held at 2", got)
	}

	// Once the settling window closes, the accumulated evidence — three
	// successes no single tick ever held — buys exactly one worker.
	time.Sleep(cooldown + 50*time.Millisecond)
	worker.adaptWorkerCount(run, sample, 0, 0, 0, 8)

	if got := run.workers.Load(); got != 3 {
		t.Fatalf("workers after 3 successes spread over %d ticks = %d, want a growth step to 3",
			len(ticks), got)
	}

	if got := spawned.Load(); got != 1 {
		t.Fatalf("spawned = %d, want exactly one worker", got)
	}

	run.workerGroup.wait()
}

// TestAutoCapacityDoesNotReuseSpentEvidence proves the other half of the
// accumulating window: the successes that bought one worker may not buy the
// next one as well, and a block already acted on may not collapse the pool
// twice.
func TestAutoCapacityDoesNotReuseSpentEvidence(t *testing.T) {
	t.Parallel()

	worker, run, _ := autoCapacityRun(t, 40)

	sample, err := healthyResources(context.Background(), "")
	if err != nil {
		t.Fatalf("sample: %v", err)
	}

	// Ten successes buy exactly ONE step, not three.
	worker.adaptWorkerCount(run, sample, 0, 10, 0, 8)

	if got := run.workers.Load(); got != 3 {
		t.Fatalf("workers = %d, want a single step to 3", got)
	}

	// The window is spent, so an empty tick cannot grow again on the old
	// evidence.
	worker.adaptWorkerCount(run, sample, 0, 0, 0, 8)

	if got := run.workers.Load(); got != 3 {
		t.Fatalf("workers after an empty tick = %d, want them held at 3", got)
	}

	// A block collapses the pool once; the spent block may not collapse it
	// again on the following tick.
	worker.adaptWorkerCount(run, sample, 0, 0, 1, 8)

	if got := run.workerTarget.Load(); got != 1 {
		t.Fatalf("worker target after a block = %d, want 3 halved to 1", got)
	}

	for run.retireWorker() {
	}

	worker.adaptWorkerCount(run, sample, 0, 0, 0, 8)

	if got := run.workerTarget.Load(); got != 1 {
		t.Fatalf("worker target after the spent block = %d, want it held at 1", got)
	}

	run.workerGroup.wait()
}
