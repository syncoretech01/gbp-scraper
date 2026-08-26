//nolint:testpackage // These tests need unexported planning and containment internals.
package webrunner

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/web"
)

// TestBrowserBudgetBoundsTotalBrowsers proves the fix for the unit mismatch
// the acceptance matrix exposed: the old budget was denominated in WORKERS
// (3GiB each) while the cost is denominated in BROWSERS, so requested
// concurrency 4 planned as 2x2 and as 1x4 still launched four browsers. The
// browser-total budget must bound workers x browsersPerWorker itself.
func TestBrowserBudgetBoundsTotalBrowsers(t *testing.T) {
	t.Parallel()

	// The two live observations, replayed against a budget of two browsers.
	for _, shape := range []struct {
		name        string
		taskWorkers int
	}{
		{name: "D shape (2 workers x 2)", taskWorkers: 0},
		{name: "E shape (1 worker x 4)", taskWorkers: 1},
	} {
		job := gridScrapeJob("budget-"+shape.name, shape.taskWorkers)
		job.Data.Concurrency = 4
		job.Data.BrowserPool = 0
		job.Data.PagesBrowser = 0

		plan := planTaskPool(&job, 4, 48, 2, 2)

		if browsers := plan.Workers * browsersPerWorker(plan, plan.PerTaskPages); browsers > 2 {
			t.Fatalf("%s: plans %d browsers under a budget of 2", shape.name, browsers)
		}

		// Throughput is preserved by packing pages, not by cutting concurrency.
		if plan.Workers*plan.PerTaskConcurrency != 4 {
			t.Fatalf("%s: effective concurrency = %d, want the requested 4",
				shape.name, plan.Workers*plan.PerTaskConcurrency)
		}
	}

	// Property sweep: the invariant holds across the whole configuration space.
	for conc := 1; conc <= 64; conc *= 2 {
		for _, workers := range []int{0, 1, 2, 4, 8, 16} {
			for _, pool := range []int{0, 1, 4, 32} {
				for _, pages := range []int{0, 1, 2, 8} {
					for _, budget := range []int{1, 2, 3, 5, 8, 32} {
						job := gridScrapeJob("sweep", workers)
						job.Data.Concurrency = conc
						job.Data.BrowserPool = pool
						job.Data.PagesBrowser = pages

						plan := planTaskPool(&job, conc, 48, 0, budget)

						if got := plan.Workers * browsersPerWorker(plan, plan.PerTaskPages); got > budget {
							t.Fatalf("conc=%d workers=%d pool=%d pages=%d budget=%d: %d browsers planned",
								conc, workers, pool, pages, budget, got)
						}
					}
				}
			}
		}
	}
}

// TestBrowserBudgetAmpleHostIsUnchanged proves healthy hosts keep today's
// exact topology: with a budget at least as large as the engine would derive
// anyway, the plan is identical to the unbudgeted one except that the derived
// pool is now explicit.
func TestBrowserBudgetAmpleHostIsUnchanged(t *testing.T) {
	t.Parallel()

	job := gridScrapeJob("ample", 0)
	job.Data.Concurrency = 4
	job.Data.BrowserPool = 0
	job.Data.PagesBrowser = 0

	unbudgeted := planTaskPool(&job, 4, 48, 2, 0)
	ample := planTaskPool(&job, 4, 48, 2, 17)

	if ample.Workers != unbudgeted.Workers || ample.PerTaskConcurrency != unbudgeted.PerTaskConcurrency {
		t.Fatalf("ample budget changed the plan: %+v vs %+v", ample, unbudgeted)
	}

	engineDerived := browsersPerWorker(unbudgeted, job.Data.PagesBrowser)
	if ample.PerTaskBrowserPool != engineDerived {
		t.Fatalf("ample pool = %d, want the engine-derived %d made explicit",
			ample.PerTaskBrowserPool, engineDerived)
	}

	if ample.CappedExplicit {
		t.Fatal("ample budget flagged an explicit cap that did not happen")
	}
}

// TestBrowserBudgetCapsExplicitValuesVisibly proves the physical ceiling
// lowers operator-chosen values too — an OOM kill loses the whole run no
// matter who chose the number — and that doing so is flagged for the
// capacity-capped event rather than silent.
func TestBrowserBudgetCapsExplicitValuesVisibly(t *testing.T) {
	t.Parallel()

	job := gridScrapeJob("explicit", 8)
	job.Data.Concurrency = 8
	job.Data.BrowserPool = 32

	plan := planTaskPool(&job, 8, 48, 0, 3)

	if got := plan.Workers * browsersPerWorker(plan, plan.PerTaskPages); got > 3 {
		t.Fatalf("explicit config planned %d browsers past the budget of 3", got)
	}

	if !plan.CappedExplicit {
		t.Fatal("capping an explicit worker/pool choice must be flagged, not silent")
	}

	// Fast mode passes zero budgets and must keep its full fan-out.
	fast := gridScrapeJob("fast", 8)
	fast.Data.FastMode = true
	fast.Data.Concurrency = 8

	fastPlan := planTaskPool(&fast, 8, 48, 0, 0)
	if fastPlan.Workers != 8 {
		t.Fatalf("Fast mode workers = %d, want the explicit 8 untouched", fastPlan.Workers)
	}
}

// TestBrowserProcessBudgetArithmetic pins the budget function: an explicit
// reserve comes off the top, the remainder divides by the per-browser cost,
// the floor is one, and a missing measurement falls back to one.
func TestBrowserProcessBudgetArithmetic(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		available uint64
		want      int
	}{
		{name: "no measurement", available: 0, want: 1},
		{name: "below reserve", available: 1 << 30, want: 1},
		{name: "container-like 4GiB", available: 4 << 30, want: 4},
		{name: "workstation 12GiB", available: 12 << 30, want: 17},
	}

	for _, tc := range cases {
		got := browserProcessBudget(workerResourceSample{MemoryAvailableBytes: tc.available})
		if got != tc.want {
			t.Fatalf("%s: budget = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestCgroupAvailableBytes proves the container-limit reader: cgroup v2 and v1
// layouts, the "max" sentinel, an absent file, and an exhausted limit.
func TestCgroupAvailableBytes(t *testing.T) {
	t.Parallel()

	write := func(t *testing.T, path, content string) {
		t.Helper()

		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("v2 limit minus usage", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		write(t, filepath.Join(root, "memory.max"), "2147483648")
		write(t, filepath.Join(root, "memory.current"), "1073741824")

		got, ok := cgroupAvailableBytes(root)
		if !ok || got != 1<<30 {
			t.Fatalf("= (%d, %v), want (1GiB, true)", got, ok)
		}
	})

	t.Run("v2 max sentinel means no limit", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		write(t, filepath.Join(root, "memory.max"), "max")

		if _, ok := cgroupAvailableBytes(root); ok {
			t.Fatal("'max' must read as no usable limit")
		}
	})

	t.Run("v1 fallback", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		write(t, filepath.Join(root, "memory", "memory.limit_in_bytes"), "1073741824")
		write(t, filepath.Join(root, "memory", "memory.usage_in_bytes"), "536870912")

		got, ok := cgroupAvailableBytes(root)
		if !ok || got != 512<<20 {
			t.Fatalf("= (%d, %v), want (512MiB, true)", got, ok)
		}
	})

	t.Run("absent files mean not a container", func(t *testing.T) {
		t.Parallel()

		if _, ok := cgroupAvailableBytes(t.TempDir()); ok {
			t.Fatal("no cgroup files must mean no limit")
		}
	})

	t.Run("exhausted limit reads as almost nothing, not as unknown", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		write(t, filepath.Join(root, "memory.max"), "1073741824")
		write(t, filepath.Join(root, "memory.current"), "2073741824")

		got, ok := cgroupAvailableBytes(root)
		if !ok || got != 1 {
			t.Fatalf("= (%d, %v), want (1, true)", got, ok)
		}
	})
}

// TestEngineContainmentAdoptAndReclaim proves the containment registry: an
// adopted engine is counted, its detached monitor reclaims the file handle and
// counters the moment the wedged call finally returns, and repeated wedges
// accumulate ONLY in the registry — the active-engine gauge that gates the
// janitor returns to zero.
func TestEngineContainmentAdoptAndReclaim(t *testing.T) {
	t.Parallel()

	containment := newEngineContainment()

	const wedges = 5

	reclaimed := make(chan abandonedEngine, wedges)
	dones := make([]chan error, 0, wedges)
	paths := make([]string, 0, wedges)

	for i := range wedges {
		outfile, err := os.CreateTemp(t.TempDir(), "wedge-*.csv")
		if err != nil {
			t.Fatal(err)
		}

		done := make(chan error, 1)
		dones = append(dones, done)
		paths = append(paths, outfile.Name())

		containment.engineStarted()
		containment.adopt("job-1", "task", outfile.Name(), outfile, done,
			func(engine abandonedEngine, _ time.Duration) { reclaimed <- engine })

		_ = i
	}

	if got := containment.AbandonedNow(); got != wedges {
		t.Fatalf("abandoned now = %d, want %d", got, wedges)
	}

	if got := containment.activeEngines.Load(); got != 0 {
		t.Fatalf("active engines = %d, want 0 — adoption must release the active slot", got)
	}

	if got := containment.abandonedTotal.Load(); got != wedges {
		t.Fatalf("abandoned total = %d, want %d", got, wedges)
	}

	// The wedged calls finally return (in production: the janitor killed the
	// drivers and the transport abort unwedged them).
	for _, done := range dones {
		done <- errors.New("late teardown")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && containment.reclaimedTotal.Load() < wedges {
		time.Sleep(5 * time.Millisecond)
	}

	if got := containment.reclaimedTotal.Load(); got != wedges {
		t.Fatalf("reclaimed total = %d, want %d", got, wedges)
	}

	if got := containment.AbandonedNow(); got != 0 {
		t.Fatalf("abandoned now = %d after reclaim, want 0", got)
	}

	for range wedges {
		select {
		case <-reclaimed:
		case <-time.After(time.Second):
			t.Fatal("reclaim callback missing")
		}
	}

	for _, path := range paths {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("run file %s not removed after reclaim", path)
		}
	}
}

// TestSelectOrphanEngineProcesses proves the janitor's target selection: only
// run-driver Node processes and known browser executables, and only when the
// parent chain reaches this process — an operator's own Chrome or Node is
// never a candidate.
func TestSelectOrphanEngineProcesses(t *testing.T) {
	t.Parallel()

	const selfPID = int32(100)

	procs := []janitorProcess{
		{PID: 200, PPID: selfPID, Name: "node", Cmdline: "node /ms-playwright/cli.js run-driver"},
		{PID: 201, PPID: 200, Name: "chrome", Cmdline: "chrome --single-process"},
		{PID: 202, PPID: selfPID, Name: "node.exe", Cmdline: "node cli.js run-driver"},
		// Distractors that must never be selected:
		{PID: 300, PPID: 1, Name: "chrome", Cmdline: "the operator's own browser"},
		{PID: 301, PPID: 1, Name: "node", Cmdline: "node server.js run-driver"},
		{PID: 302, PPID: selfPID, Name: "node", Cmdline: "node --version"},
		{PID: 303, PPID: selfPID, Name: "sqlite3", Cmdline: ""},
		{PID: selfPID, PPID: 1, Name: "google-maps-scraper", Cmdline: ""},
	}

	drivers, browsers := selectOrphanEngineProcesses(procs, selfPID)

	if len(drivers) != 2 || drivers[0] != 200 || drivers[1] != 202 {
		t.Fatalf("drivers = %v, want [200 202]", drivers)
	}

	if len(browsers) != 1 || browsers[0] != 201 {
		t.Fatalf("browsers = %v, want [201]", browsers)
	}
}

// TestHousekeepingSurvivesABlockedJob proves the scheduler isolation: a job
// that blocks forever occupies only the job slot, while the heartbeat and
// schedule materialization keep ticking in their own loop.
func TestHousekeepingSurvivesABlockedJob(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	repository := &blockedJobRepo{}
	repository.pending.Store(true)

	seeded := testScrapeJob("blocked-job")
	if err := repository.Create(ctx, &seeded); err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	entered := make(chan struct{})

	worker := &webrunner{
		svc:          web.NewService(repository, t.TempDir()),
		cfg:          &runner.Config{DataFolder: t.TempDir(), Concurrency: 1},
		containment:  newEngineContainment(),
		pollInterval: 5 * time.Millisecond,
		setupMate: func(context.Context, io.Writer, *web.Job) (mateRunner, error) {
			close(entered)
			<-release

			return fakeMate{}, nil
		},
	}

	done := make(chan struct{}, 2)
	go func() { _ = worker.housekeepingLoop(ctx); done <- struct{}{} }()
	go func() { _ = worker.jobLoop(ctx); done <- struct{}{} }()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the job never started")
	}

	before := repository.schedulePolls.Load()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && repository.schedulePolls.Load() < before+5 {
		time.Sleep(5 * time.Millisecond)
	}

	if got := repository.schedulePolls.Load(); got < before+5 {
		t.Fatalf("schedule polls advanced only %d while a job was blocked; housekeeping is not isolated", got-before)
	}

	close(release)

	// Let the released job run to its terminal state before cancelling, so
	// every file handle it holds is closed by its own normal path (Windows
	// cannot delete the temp dir around an open handle).
	waitDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(waitDeadline) {
		job, err := repository.Get(context.Background(), "blocked-job")
		if err == nil && job.Status != web.StatusPending && job.Status != web.StatusWorking {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done
	<-done
}

// TestDefaultAllowedSecondsAccountsForEnrichment pins the deadline model: the
// walk-only rate is unchanged, and enrichment doubles the modelled per-seed
// cost — the controlled variance runs measured the identical workload at
// ~290s without enrichment and 712-903s with it.
func TestDefaultAllowedSecondsAccountsForEnrichment(t *testing.T) {
	t.Parallel()

	if got := defaultAllowedSeconds(16, 10, false); got != 16*10*10/50+120 {
		t.Fatalf("walk-only window = %d, want the legacy formula unchanged", got)
	}

	walk := defaultAllowedSeconds(16, 10, false)
	enriched := defaultAllowedSeconds(16, 10, true)

	if enriched-120 != 2*(walk-120) {
		t.Fatalf("enriched window = %d for walk %d, want the per-seed cost doubled", enriched, walk)
	}

	if got := defaultAllowedSeconds(0, 10, true); got != 120 {
		t.Fatalf("empty plan window = %d, want the 120s floor term", got)
	}
}

// TestNormalizeCheckpointRunErrorSurfacesTruncation proves the silent-loss fix:
// a cancellation that arrives while found places are still uncommitted is a
// truncation error, not a success — while the two legitimate cancel shapes
// (internal engine cancel, and the task's own exiter finishing everything)
// stay successes.
func TestNormalizeCheckpointRunErrorSurfacesTruncation(t *testing.T) {
	t.Parallel()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	live := context.Background()

	complete := exiter.Snapshot{SeedsCompleted: 1, PlacesFound: 20, PlacesCompleted: 20}
	truncated := exiter.Snapshot{SeedsCompleted: 1, PlacesFound: 20, PlacesCompleted: 12}

	if err := normalizeCheckpointRunError(cancelled, context.Canceled, complete); err != nil {
		t.Fatalf("internally complete cancel = %v, want success", err)
	}

	if err := normalizeCheckpointRunError(live, context.Canceled, truncated); err != nil {
		t.Fatalf("engine-internal cancel with live context = %v, want the legacy success", err)
	}

	err := normalizeCheckpointRunError(cancelled, context.Canceled, truncated)
	if !errors.Is(err, errTaskTruncated) {
		t.Fatalf("truncated cancel = %v, want errTaskTruncated", err)
	}

	if classification := classifyFailureKind(err); classification.Fine != FailureKindTaskTruncated {
		t.Fatalf("fine kind = %q, want %q", classification.Fine, FailureKindTaskTruncated)
	}

	if err := normalizeCheckpointRunError(cancelled, errors.New("real failure"), complete); err == nil {
		t.Fatal("a genuine error must never be normalized away")
	}
}

// blockedJobRepo serves exactly one pending job. It embeds the schedule-store
// fake so the service recognises schedule support and routes StartDueSchedules
// to the counted method.
type blockedJobRepo struct {
	scheduleFailureRepo
	pending atomic.Bool
}

func (r *blockedJobRepo) Select(context.Context, web.SelectParams) ([]web.Job, error) {
	if r.pending.CompareAndSwap(true, false) {
		job := testScrapeJob("blocked-job")

		return []web.Job{job}, nil
	}

	return nil, nil
}
