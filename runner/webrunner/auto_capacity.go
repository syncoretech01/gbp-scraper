package webrunner

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// Auto capacity derives one machine's safe throughput instead of shipping one
// number for every machine.
//
// # Why this exists
//
// The hardening phase bounded the browser fan-out with two budgets: a
// browser-denominated memory budget (browserProcessBudget) and a flat worker
// cap of two. The flat cap turned out to be the binding constraint on every
// host, and it was not derived from anything the host reported: the 180-task
// acceptance run measured 7.17 GiB available and a browser budget of EIGHT
// browsers, and still ran two workers, because two was the constant. Its
// measured parallelism was 1.99 over 52.5 minutes with zero failures, zero
// retries and zero blocks — a perfectly healthy run leaving three quarters of
// its own measured-safe capacity unused.
//
// Auto capacity replaces that constant with a derived ceiling and gives the run
// a controller that can move the worker count WHILE it runs. The physical
// invariant the hardening phase established is untouched and remains the outer
// bound of everything here:
//
//	workers * browsers-per-worker <= browserProcessBudget(sample)
//
// Nothing in this file may raise that budget. plannedBrowserCostBytes can only
// make a browser cost MORE than the planning constant, never less, so feeding
// a measurement in can only ever shrink the budget.
const (
	// perWorkerOverheadBytes is the non-browser memory one extra task worker
	// costs: its own scrapemate app, the Playwright node driver it spawns, its
	// CSV writer and its per-task temporary result file. It is charged ON TOP
	// of that worker's browsers, so the worker ceiling and the browser budget
	// price the same memory once each instead of twice. The superseded
	// 3 GiB-per-worker reservation priced one worker as five browsers, which
	// is what pinned every host to two workers.
	perWorkerOverheadBytes uint64 = 256 << 20

	// measuredBrowserFloorBytes and measuredBrowserCeilingBytes bound the
	// per-browser cost learned from the live process census. A census taken
	// while browsers are still warming up reads low, and one taken during a
	// leak reads absurdly high; both are clamped so a single odd sample cannot
	// move the plan far.
	measuredBrowserFloorBytes   uint64 = 300 << 20
	measuredBrowserCeilingBytes uint64 = 1536 << 20
	// browserCensusTrustMinimum is how many live browsers the census must have
	// attributed to this application before its mean RSS is trusted as a
	// measurement. One process is a sample of one, usually mid-launch.
	browserCensusTrustMinimum = 2

	// autoWorkerCPUSaturatedPercent is the CPU load at which the controller
	// takes a worker back. It sits far above recoveryCPUPercent, which only
	// vetoes GROWTH: a busy host must stop climbing long before it starts
	// shrinking, or the two rules fight each other.
	autoWorkerCPUSaturatedPercent = 85

	// autoWorkerLatencySamples is how many measurements a latency series needs
	// before the controller will act on it at all.
	autoWorkerLatencySamples = 5
	// autoWriteLatencyDegradationRatio is how far the durable finish write (the
	// CSV merge plus the ownership-checked completion) may drift above the best
	// value this run itself measured before SQLite/write pressure costs a
	// worker. Finish writes are short and low-variance, so a 3x drift is real
	// contention rather than noise.
	autoWriteLatencyDegradationRatio = 3.0
	// autoTaskLatencyDegradationRatio is the equivalent for whole-task latency.
	// Task duration varies with cell density — the acceptance run measured a
	// 31s median against a 185s maximum — so it is only ever a veto on growth,
	// never a reason to shrink.
	autoTaskLatencyDegradationRatio = 2.0

	// autoWorkerScaleCooldown is how long the controller waits after ANY
	// worker-count change before changing it again. A new worker needs a whole
	// task to show what it costs, so reacting faster than one task measures
	// nothing.
	autoWorkerScaleCooldown = 45 * time.Second

	// latencyEWMAAlpha weights the newest latency sample. A quarter keeps the
	// series responsive to a real trend while one slow task cannot move it far.
	latencyEWMAAlpha = 0.25
)

// cgroupCPUMaxPath, cgroupCPUQuotaPath and cgroupCPUPeriodPath are the cgroup
// v2 and v1 files describing a container's CPU allowance. Reading them is the
// CPU equivalent of the existing memory cgroup read: inside a CPU-limited
// container runtime.NumCPU reports the HOST's cores, which would let the
// ceiling plan for parallelism the container can never actually be given.
const (
	cgroupCPUMaxPath    = "cpu.max"
	cgroupCPUQuotaPath  = "cpu/cpu.cfs_quota_us"
	cgroupCPUPeriodPath = "cpu/cpu.cfs_period_us"
)

// cachedCPUCores memoises the cgroup-aware core count for the default cgroup
// root. A container's CPU quota cannot change under a running process, and the
// resource sampler asks for this every couple of seconds, so the two file reads
// behind it are done once per process rather than once per sample. A test that
// passes its own root is never served from the cache.
var cachedCPUCores struct {
	once  sync.Once
	value int
}

// hostCPUCores is the cached reading for this process's own cgroup.
func hostCPUCores() int {
	cachedCPUCores.once.Do(func() {
		cachedCPUCores.value = effectiveCPUCores(cgroupRoot)
	})

	return cachedCPUCores.value
}

// effectiveCPUCores reports how many CPUs this process may actually use: the
// cgroup quota when one is set and tighter, otherwise the visible core count.
// It never returns less than one.
func effectiveCPUCores(cgroupPath string) int {
	cores := runtime.NumCPU()
	if cores < 1 {
		cores = 1
	}

	if quota, ok := cgroupCPUCores(cgroupPath); ok && quota < cores {
		cores = quota
	}

	if cores < 1 {
		cores = 1
	}

	return cores
}

// cgroupCPUCores reads the container CPU allowance in whole cores, rounded up
// so a 1.5-core quota still permits two workers. It reports false when no
// readable quota exists, which is the normal case on native Windows and on an
// unlimited container.
func cgroupCPUCores(root string) (int, bool) {
	if raw, err := os.ReadFile(filepath.Join(root, cgroupCPUMaxPath)); err == nil {
		fields := strings.Fields(string(raw))
		if len(fields) != 2 || fields[0] == "max" {
			return 0, false
		}

		quota, quotaErr := strconv.ParseInt(fields[0], 10, 64)
		period, periodErr := strconv.ParseInt(fields[1], 10, 64)

		if quotaErr != nil || periodErr != nil || quota <= 0 || period <= 0 {
			return 0, false
		}

		return int((quota + period - 1) / period), true
	}

	quotaRaw, quotaErr := os.ReadFile(filepath.Join(root, cgroupCPUQuotaPath))
	periodRaw, periodErr := os.ReadFile(filepath.Join(root, cgroupCPUPeriodPath))

	if quotaErr != nil || periodErr != nil {
		return 0, false
	}

	quota, err := strconv.ParseInt(strings.TrimSpace(string(quotaRaw)), 10, 64)
	if err != nil || quota <= 0 {
		return 0, false
	}

	period, err := strconv.ParseInt(strings.TrimSpace(string(periodRaw)), 10, 64)
	if err != nil || period <= 0 {
		return 0, false
	}

	return int((quota + period - 1) / period), true
}

// plannedBrowserCostBytes is what one browser is charged when a budget is
// computed. It starts at the planning constant and is raised — never lowered —
// by what the live census measured, so a host whose browsers are fatter than
// the model gets a smaller budget, and a host whose browsers are leaner keeps
// exactly the conservative budget the hardening phase set.
func plannedBrowserCostBytes(sample workerResourceSample) uint64 {
	if sample.BrowserProcesses < browserCensusTrustMinimum || sample.BrowserMemoryBytes == 0 {
		return perBrowserPlanningCostBytes
	}

	mean := sample.BrowserMemoryBytes / uint64(sample.BrowserProcesses)

	if mean < measuredBrowserFloorBytes {
		mean = measuredBrowserFloorBytes
	}

	if mean > measuredBrowserCeilingBytes {
		mean = measuredBrowserCeilingBytes
	}

	if mean < perBrowserPlanningCostBytes {
		return perBrowserPlanningCostBytes
	}

	return mean
}

// autoWorkerCeiling is the highest browser-mode task-worker count this host may
// run, derived from what was measured rather than from a constant:
//
//   - memory: (available - reserve) / (one browser + one worker's overhead)
//   - CPU: the effective core count, cgroup-quota aware
//   - the browser-denominated budget, because every worker holds >= 1 browser
//   - web.MaximumJobTaskWorkers, the product's own bound
//
// It floors at one, because a browser job always runs, and falls back to the
// proven single-app topology when no memory reading is available.
func autoWorkerCeiling(sample workerResourceSample) int {
	available := sample.MemoryAvailableBytes
	if available == 0 {
		return safeBrowserWorkerFallback
	}

	usable := available - min(available, browserBudgetReserveBytes)
	perWorker := plannedBrowserCostBytes(sample) + perWorkerOverheadBytes

	ceiling := int(usable / perWorker)

	if cores := autoCPUWorkerBudget(sample); ceiling > cores {
		ceiling = cores
	}

	if browsers := browserProcessBudget(sample); ceiling > browsers {
		ceiling = browsers
	}

	if ceiling > web.MaximumJobTaskWorkers {
		ceiling = web.MaximumJobTaskWorkers
	}

	if ceiling < 1 {
		ceiling = 1
	}

	return ceiling
}

// autoCPUWorkerBudget bounds workers by CPU. One browser-mode worker drives one
// browser that spends most of its wall time waiting on the network, so a core
// each is a generous but real bound. Instantaneous load is deliberately NOT
// used here: the run's own browsers appear in it, so a load-scaled ceiling
// would ratchet itself down as the run grew. Load enters the controller
// instead, as a growth veto (recoveryCPUPercent) and a shrink trigger
// (autoWorkerCPUSaturatedPercent).
func autoCPUWorkerBudget(sample workerResourceSample) int {
	cores := sample.CPUCores
	if cores < 1 {
		cores = 1
	}

	return cores
}

// latencySeries is a small thread-safe exponentially weighted mean that also
// remembers the best mean it ever held. The controller compares the two, so
// "slow" is measured against what this run achieved on this machine and needs
// no absolute threshold and therefore no per-machine tuning.
type latencySeries struct {
	mu      sync.Mutex
	mean    time.Duration
	best    time.Duration
	samples int64
}

func (series *latencySeries) observe(value time.Duration) {
	if value <= 0 {
		return
	}

	series.mu.Lock()
	defer series.mu.Unlock()

	series.samples++

	if series.mean == 0 {
		series.mean = value
	} else {
		series.mean = time.Duration(
			latencyEWMAAlpha*float64(value) + (1-latencyEWMAAlpha)*float64(series.mean),
		)
	}

	if series.best == 0 || series.mean < series.best {
		series.best = series.mean
	}
}

func (series *latencySeries) snapshot() (mean, best time.Duration, samples int64) {
	series.mu.Lock()
	defer series.mu.Unlock()

	return series.mean, series.best, series.samples
}

// latencyDegraded reports whether an observed mean has drifted past a ratio of
// the best mean the run itself measured, with enough samples to mean anything.
func latencyDegraded(mean, best time.Duration, samples int64, ratio float64) bool {
	if samples < autoWorkerLatencySamples || mean <= 0 || best <= 0 {
		return false
	}

	return float64(mean) >= float64(best)*ratio
}

// workerScalingSignals is every measurement one adaptation window feeds the
// auto worker controller. It is a plain value, so the decision below is a pure
// function testable without a host, a browser or a database.
type workerScalingSignals struct {
	// Ceiling is autoWorkerCeiling narrowed by this run's own bounds: the
	// operator's worker choice, the concurrency budget, and the pending work.
	Ceiling int
	// CeilingReason names WHICH of those bounds is currently the tightest, so
	// a clamp explains itself honestly. Reporting every clamp as a memory
	// decision was wrong for the common case where the operator's own
	// concurrency budget is what binds — and simply false in Fast mode, which
	// consults no memory or browser bound at all.
	CeilingReason string
	// Pending is how much claimable work is left. Another worker is pointless
	// without a task for it to claim.
	Pending int

	// The task outcomes this decision weighs. The caller chooses their window:
	// a reduction reads the sampling tick that just ended, so trouble is acted
	// on the moment it appears, while growth reads outcomes accumulated over a
	// whole worker-scale cooldown. The distinction is not cosmetic — a browser
	// task takes tens of seconds against a two-second sampling tick, so a
	// per-tick window would essentially never hold the corroborating successes
	// growth requires, and the controller could only ever shrink.
	Failures  int
	Successes int
	Blocks    int

	CPUPercent      float64
	MemoryAvailable uint64
	MemoryUsed      uint64
	MemoryCeiling   uint64

	// BrowserCensus is what the process table actually showed and
	// AllowedBrowsers what the current plan expects. A census above the
	// allowance means browsers from an earlier budget are still alive.
	BrowserCensus   int
	AllowedBrowsers int

	TaskMean, TaskBest   time.Duration
	TaskSamples          int64
	WriteMean, WriteBest time.Duration
	WriteSamples         int64

	// ScaleCooldownElapsed gates BOTH directions of change, so the controller
	// always measures a worker for at least one task before judging it.
	ScaleCooldownElapsed bool
	// RecoveryCooldownElapsed is the existing post-reduction settling window.
	// It gates growth only.
	RecoveryCooldownElapsed bool
}

// workerScalingDecision is the controller's output: the worker count the run
// should converge on, and the operator-facing reason it changed.
type workerScalingDecision struct {
	Workers int
	Reason  string
}

// decideWorkerTarget is the auto worker controller.
//
// It reduces on the first sign of trouble and grows one worker at a time from
// corroborated evidence, so decay is always faster than recovery. Order
// matters: the physical ceiling is applied before anything else, and every
// reduction trigger is checked before any growth is considered, so a window
// that is simultaneously clean and over budget still shrinks.
//
// It is pure: every measurement arrives in signals, and the caller owns both
// the windows those measurements were taken over and what to do with the
// answer. That is what makes the whole ladder of rules testable without a host,
// a browser or a database.
func decideWorkerTarget(current int, signals workerScalingSignals) workerScalingDecision {
	ceiling := max(1, signals.Ceiling)
	current = max(1, current)

	hold := workerScalingDecision{Workers: current}

	// The ceiling is physical: memory and CPU do not negotiate, so it applies
	// whether or not a cooldown has elapsed.
	if current > ceiling {
		reason := signals.CeilingReason
		if reason == "" {
			reason = "the current capacity ceiling supports fewer parallel tasks"
		}

		return workerScalingDecision{Workers: ceiling, Reason: reason}
	}

	writeDegraded := latencyDegraded(
		signals.WriteMean, signals.WriteBest, signals.WriteSamples, autoWriteLatencyDegradationRatio,
	)
	taskDegraded := latencyDegraded(
		signals.TaskMean, signals.TaskBest, signals.TaskSamples, autoTaskLatencyDegradationRatio,
	)

	if reduced, reason, reduce := workerReduction(current, signals, writeDegraded); reduce {
		return workerScalingDecision{Workers: reduced, Reason: reason}
	}

	if !signals.ScaleCooldownElapsed {
		return hold
	}

	if !canGrowWorkers(current, ceiling, signals, taskDegraded, writeDegraded) {
		return hold
	}

	return workerScalingDecision{
		Workers: current + 1,
		Reason:  "a clean task window with measured memory, CPU and browser head-room",
	}
}

// workerReduction holds every reason to take a worker back. Blocks and failure
// bursts halve the count, because a cascade already under way only gets worse
// at the current width; the resource triggers step down by one, because they
// describe pressure rather than refusal.
func workerReduction(current int, signals workerScalingSignals, writeDegraded bool) (int, string, bool) {
	switch {
	case signals.Blocks > 0:
		return max(1, current/2), "the platform refused an attempt (block or rate limit)", true

	case signals.Failures >= adaptiveFailureBurst &&
		signals.Failures*2 >= signals.Failures+signals.Successes:
		return max(1, current/2), "a majority of the attempts in this window failed", true

	case signals.MemoryCeiling > 0 && signals.MemoryUsed >= signals.MemoryCeiling:
		return max(1, current-1), "the configured memory ceiling was reached", true

	case signals.MemoryAvailable > 0 && signals.MemoryAvailable < severeMemoryBytes:
		return max(1, current/2), "available memory fell to a critical level", true

	case signals.MemoryAvailable > 0 && signals.MemoryAvailable < moderateMemoryBytes:
		return max(1, current-1), "available memory fell under pressure", true

	case signals.CPUPercent >= autoWorkerCPUSaturatedPercent:
		return max(1, current-1), "the host CPU is saturated", true

	case writeDegraded:
		return max(1, current-1), "database write latency degraded at the current width", true
	}

	return current, "", false
}

// canGrowWorkers reports whether every growth precondition holds at once. Any
// single adverse signal vetoes the step, which is what keeps recovery slower
// than decay.
func canGrowWorkers(current, ceiling int, signals workerScalingSignals, taskDegraded, writeDegraded bool) bool {
	if current >= ceiling {
		return false
	}

	// Another worker with no task to claim costs a browser and buys nothing.
	if signals.Pending <= current {
		return false
	}

	if signals.Failures > 0 || signals.Blocks > 0 {
		return false
	}

	// Corroboration: one lucky task is not evidence that the run can take more.
	if signals.Successes < adaptiveRecoveryAttempts {
		return false
	}

	if !signals.RecoveryCooldownElapsed {
		return false
	}

	if signals.CPUPercent >= recoveryCPUPercent {
		return false
	}

	// One more worker is one more browser, so the memory that browser needs has
	// to be measurably there ON TOP of the recovery reserve.
	if signals.MemoryAvailable > 0 &&
		signals.MemoryAvailable < recoveryMemoryBytes+perBrowserPlanningCostBytes {
		return false
	}

	if signals.AllowedBrowsers > 0 &&
		signals.BrowserCensus > signals.AllowedBrowsers+browserHeadroomSlack {
		return false
	}

	return !taskDegraded && !writeDegraded
}
