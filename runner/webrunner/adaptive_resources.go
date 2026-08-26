package webrunner

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// Adaptive performance measures four local dimensions before it lets a run
// take more capacity: CPU, RAM, free disk, and the number of browser
// processes this application actually has alive. A fifth dimension, the
// block rate, is derived from how recent task attempts failed.
//
// None of the measurements adds network traffic or concurrency: the browser
// census reads the local process table, and the block rate reuses the failure
// classification the pool already computes for every finished attempt.
const (
	// browserCensusInterval spaces out process-table reads. Concurrency
	// adaptation samples every resourceSampleInterval, which is far too hot
	// for a full process enumeration, so the census is cached in between.
	browserCensusInterval = 10 * time.Second
	// browserCensusTimeout bounds one process-table read. A slow or locked
	// table must never delay the supervisor loop.
	browserCensusTimeout = 3 * time.Second
	// browserCensusMaxProcesses bounds how many processes one census will
	// inspect, so an unusually busy host cannot turn the census into a stall.
	browserCensusMaxProcesses = 4096
	// browserAncestorDepth bounds the parent walk used to attribute a browser
	// process to this application.
	browserAncestorDepth = 8

	// recoveryCPUPercent is the highest CPU load at which a run may take
	// concurrency back. Above it the host is already busy, so a clean failure
	// window is not evidence that more parallelism is safe.
	recoveryCPUPercent = 70
	// recoveryMemoryBytes is the available memory a run must still see before
	// it may recover a concurrency step.
	recoveryMemoryBytes uint64 = 2 << 30
	// moderateMemoryBytes and severeMemoryBytes are the RAM-pressure steps at
	// which the browser pool and pages-per-browser budgets shrink.
	moderateMemoryBytes uint64 = 2 << 30
	severeMemoryBytes   uint64 = 1 << 30
	// browserHeadroomSlack tolerates browsers that are still shutting down
	// when the census runs, so a normal teardown does not block recovery.
	browserHeadroomSlack = 2
	// bytesPerMebibyteShift converts a byte count to mebibytes for the
	// operator-facing text of a worker event. Operators configure the memory
	// ceiling in MB, so the evidence has to read back in the same unit.
	bytesPerMebibyteShift = 20

	// browserWorkerMemoryReservationBytes is the host memory one browser-mode
	// task worker is budgeted. Each worker owns a SEPARATE scrapemate app and
	// therefore a SEPARATE Playwright browser pool that never drops below a
	// single --single-process Chromium, so the default fan-out multiplies
	// browser processes one-for-one with workers. The reservation is
	// deliberately generous — under-committing browsers costs throughput, but
	// over-committing them is the exact failure the incident run showed (four
	// independent single-process Chromium browsers cascading into
	// browser-failure). The docker specialist's measured per-browser cost
	// supersedes this estimate; keep the two reconciled. [harden/scheduler-adaptive]
	browserWorkerMemoryReservationBytes uint64 = 3 << 30
	// maxDefaultBrowserWorkers hard-caps the DEFAULT browser-mode fan-out. One
	// worker is one scrapemate app managing its own browser pool with the
	// engine's page/browser reuse limits — the engine-native, tested topology
	// the controlled conc-1 test proved works. Two is the cautious upper bound
	// a well-resourced host may take; the adaptive controller collapses it back
	// to one on the first browser-failure or block burst. An operator who sets
	// TaskWorkers explicitly opts out of this cap entirely.
	maxDefaultBrowserWorkers = 2
	// safeBrowserWorkerFallback is the browser-mode fan-out used when no memory
	// measurement is available. It is the proven-safe single-app topology.
	safeBrowserWorkerFallback = 1

	// perBrowserPlanningCostBytes is the planning cost of ONE --single-process
	// Chromium browser. The synthetic measurement was ~300MB steady RSS, linear
	// in the browser count; a live Maps tab carries tiles, consent frames and
	// network buffers and lands in a 450-750MB band. 600MiB is the live band's
	// midpoint: the tail above it is absorbed by browserBudgetReserveBytes plus
	// the adaptive pressure ladder, while a flat worst-case figure would
	// under-commit healthy hosts for no measured benefit.
	perBrowserPlanningCostBytes uint64 = 600 << 20
	// browserBudgetReserveBytes is memory the browser budget must never touch:
	// the Go service, SQLite and its WAL, the Playwright node driver, the OS
	// page cache and whatever /dev/shm is actually holding.
	browserBudgetReserveBytes uint64 = 1536 << 20

	// adaptiveFailureBurst is the smallest number of failed attempts in one
	// adaptation window that, forming a majority, is decisive evidence to halve
	// the failure budget. Two corroborating failures rule out a single
	// transient blip while still collapsing a browser-failure cascade on the
	// first window it appears, rather than waiting for a large window to fill.
	// [harden/scheduler-adaptive]
	adaptiveFailureBurst = 2
	// adaptiveRecoveryAttempts is the smallest clean window that may recover a
	// single failure-budget step. Recovery stays deliberately slower than decay.
	adaptiveRecoveryAttempts = 3
)

// memoryCeilingExceeded reports whether the sampled memory use has reached an
// operator-configured ceiling.
//
// The ceiling is enforced here, at the application level, because that is the
// only place this project controls: the scraping engine builds its own browser
// launch arguments and exposes no hook for a per-process memory limit, and
// forking it is out of scope. What this application does control is how much
// work it asks for, so the ceiling acts on exactly that.
//
// The measurement is MemoryUsedBytes, the same host memory-in-use figure the
// job monitor already reports, so an operator sets the ceiling against the
// number the UI shows them. A zero ceiling is "no ceiling" and reproduces the
// behaviour every run had before the control existed.
func memoryCeilingExceeded(ceiling uint64, sample workerResourceSample) bool {
	return ceiling > 0 && sample.MemoryUsedBytes >= ceiling
}

// browserProcessNames are the executable names a Playwright-driven browser
// runs under. Matching is case-insensitive and ignores a trailing extension.
var browserProcessNames = []string{
	"chrome", "chromium", "headless_shell", "msedge", "firefox",
}

// browserCensus caches the most recent count of browser processes owned by
// this application. The zero value is ready to use.
type browserCensus struct {
	mu        sync.Mutex
	count     int
	sampledAt time.Time
}

// count returns the cached browser-process count, refreshing it at most once
// per browserCensusInterval. A failed census keeps the previous value: the
// measurement is evidence for adaptation, never a reason to fail a run.
func (census *browserCensus) countBrowsers(ctx context.Context, selfPID int32) int {
	census.mu.Lock()
	defer census.mu.Unlock()

	if !census.sampledAt.IsZero() && time.Since(census.sampledAt) < browserCensusInterval {
		return census.count
	}

	censusCtx, cancel := context.WithTimeout(ctx, browserCensusTimeout)
	defer cancel()

	observed, err := countManagedBrowserProcesses(censusCtx, selfPID)
	if err != nil {
		return census.count
	}

	census.count = observed
	census.sampledAt = time.Now()

	return census.count
}

// countManagedBrowserProcesses counts the browser processes whose parent
// chain reaches selfPID. Browsers the operator started themselves therefore
// never inflate the measurement, and an unattributable process is skipped
// rather than guessed at.
func countManagedBrowserProcesses(ctx context.Context, selfPID int32) (int, error) {
	processes, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return 0, err
	}

	if len(processes) > browserCensusMaxProcesses {
		processes = processes[:browserCensusMaxProcesses]
	}

	parents := make(map[int32]int32, len(processes))
	candidates := make([]int32, 0, 8)

	for _, candidate := range processes {
		if err := ctx.Err(); err != nil {
			return 0, err
		}

		parent, parentErr := candidate.PpidWithContext(ctx)
		if parentErr != nil {
			continue
		}

		parents[candidate.Pid] = parent

		name, nameErr := candidate.NameWithContext(ctx)
		if nameErr != nil {
			continue
		}

		if isBrowserProcessName(name) {
			candidates = append(candidates, candidate.Pid)
		}
	}

	owned := 0

	for _, pid := range candidates {
		if hasAncestor(parents, pid, selfPID) {
			owned++
		}
	}

	return owned, nil
}

// isBrowserProcessName reports whether an executable name belongs to a
// browser the scraper can launch.
func isBrowserProcessName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimSuffix(name, ".exe")

	for _, candidate := range browserProcessNames {
		if name == candidate {
			return true
		}
	}

	return false
}

// hasAncestor walks a bounded parent chain looking for one process ID.
func hasAncestor(parents map[int32]int32, pid, ancestor int32) bool {
	for depth := 0; depth < browserAncestorDepth; depth++ {
		parent, found := parents[pid]
		if !found || parent <= 0 || parent == pid {
			return false
		}

		if parent == ancestor {
			return true
		}

		pid = parent
	}

	return false
}

// blockedFailureMarkers are the substrings that identify a refusal by the
// platform rather than a local fault: an explicit rate limit, a consent or
// interstitial redirect, or a challenge page. They are matched against the
// attempt error, which is the only block evidence available without changing
// the scraping engine.
var blockedFailureMarkers = []string{
	"429", "too many requests", "rate limit", "rate-limit", "ratelimit",
	"captcha", "recaptcha", "unusual traffic", "/sorry/", "sorry/index",
	"consent.google", "http 403", "status 403", "status code 403",
	"access denied", "temporarily blocked", "blocked by",
}

// isBlockedFailure reports whether an attempt error looks like a platform
// block or rate limit. It is deliberately conservative: an unrecognised error
// stays an ordinary failure, so the block rate never overstates itself.
func isBlockedFailure(message string) bool {
	for _, marker := range blockedFailureMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}

	return false
}

// decideBlockBudget is the pure block-rate rule. Blocks are treated more
// harshly than ordinary failures because continuing at the same concurrency
// makes them worse:
//
//   - any window containing a block halves the budget (never below one);
//   - a window with at least three attempts and no block recovers one step;
//   - a quiet window leaves the budget unchanged.
func decideBlockBudget(current, desired, blocks, attempts int) int {
	if current < 1 {
		current = 1
	}

	if current > desired {
		current = desired
	}

	switch {
	case blocks > 0:
		return max(1, current/2)
	case attempts >= 3 && current < desired:
		return current + 1
	default:
		return current
	}
}

// recoveryHasHeadroom reports whether the four measured local dimensions plus
// the block rate all allow a run to take a concurrency step back. Every
// condition must hold: a clean failure window on its own is not enough.
//
// allowedBrowsers is the browser count the current plan expects; a census
// above it (beyond a small teardown slack) means browsers from the previous
// budget are still alive, so more parallelism would overshoot the host.
//
// memoryCeiling is the operator's optional memory ceiling. While it is
// exceeded there is no head-room by definition, so no budget may recover.
func recoveryHasHeadroom(sample workerResourceSample, blocks, allowedBrowsers int, memoryCeiling uint64) bool {
	if blocks > 0 {
		return false
	}

	if memoryCeilingExceeded(memoryCeiling, sample) {
		return false
	}

	if sample.CPUPercent >= recoveryCPUPercent {
		return false
	}

	if sample.MemoryAvailableBytes > 0 && sample.MemoryAvailableBytes < recoveryMemoryBytes {
		return false
	}

	if allowedBrowsers > 0 && sample.BrowserProcesses > allowedBrowsers+browserHeadroomSlack {
		return false
	}

	return true
}

// browserModeWorkerBudget derives how many browser-mode task workers the
// measured host memory can safely run at once. It exists because the task pool
// gives every worker its own scrapemate app and therefore its own browser pool
// that never shrinks below one browser: the number of simultaneous browsers a
// browser-mode job launches can never fall below its worker count, no matter
// how the concurrency budget is divided or how far the adaptive controller
// later lowers concurrency. Bounding the DEFAULT fan-out here is the only point
// at which the browser total can be held to what the host can support.
//
// The result is floored at one (a browser job always runs), capped at
// maxDefaultBrowserWorkers, and falls back to the single-app topology when no
// memory reading is available. It is a hard ceiling: planTaskPool lowers an
// explicitly configured TaskWorkers past it too, because launching more
// browsers than RAM holds crashes them regardless of who chose the number.
// Fast mode (pure HTTP, no browser) never consults this budget at all.
//
// This bounds the WORKER count. The browser TOTAL — workers multiplied by each
// worker's derived pool — is bounded separately by browserProcessBudget, which
// is denominated in browsers rather than workers.
func browserModeWorkerBudget(sample workerResourceSample) int {
	available := sample.MemoryAvailableBytes
	if available == 0 {
		return safeBrowserWorkerFallback
	}

	budget := int(available / browserWorkerMemoryReservationBytes)
	if budget < 1 {
		budget = 1
	}

	if budget > maxDefaultBrowserWorkers {
		budget = maxDefaultBrowserWorkers
	}

	return budget
}

// browserProcessBudget derives how many simultaneous Chromium processes the
// measured memory can hold: (available - reserve) / per-browser cost, floored
// at one so a browser job always runs.
//
// This exists because the worker budget alone cannot bound browsers. Each
// worker's scrapemate app derives its own pool as ceil(concurrency / pages),
// so with pool and pages unset the browser total equals the concurrency budget
// REGARDLESS of how few workers carry it — the two live acceptance runs proved
// it: requested concurrency 4 planned as 2x2 and as 1x4, four browsers either
// way. The budget is therefore denominated in the unit that actually costs
// memory, and planTaskPool enforces workers x per-worker-pool <= this value.
//
// There is deliberately no upper constant: a large host earns a large budget
// and is then clamped by actual demand, instead of being pinned to a number
// sized for the smallest machine. Fast mode never consults this.
func browserProcessBudget(sample workerResourceSample) int {
	available := sample.MemoryAvailableBytes
	if available == 0 {
		return 1
	}

	usable := available - min(available, browserBudgetReserveBytes)

	budget := int(usable / perBrowserPlanningCostBytes)
	if budget < 1 {
		budget = 1
	}

	return budget
}

// adaptiveBrowserBudget reduces the per-task browser pool and pages-per-browser
// budgets under RAM pressure. Zero desired values mean "engine default" and are
// only replaced when memory pressure makes an explicit, smaller budget safer.
//
// The result never exceeds the configured budget, so this can only ever lower
// the memory the run asks for.
//
// memoryCeiling is the operator's optional ceiling. Crossing it applies the
// severe step directly — one browser, one page — regardless of how much memory
// the host still reports as available, because the ceiling is a statement
// about how much this run may use rather than about how loaded the host is.
func adaptiveBrowserBudget(
	desiredPool, desiredPages int,
	sample workerResourceSample,
	memoryCeiling uint64,
) (pool, pages int) {
	pool, pages = desiredPool, desiredPages

	if memoryCeilingExceeded(memoryCeiling, sample) {
		return 1, 1
	}

	available := sample.MemoryAvailableBytes
	if available == 0 {
		return pool, pages
	}

	switch {
	case available < severeMemoryBytes:
		pool, pages = 1, 1
	case available < moderateMemoryBytes:
		if desiredPool > 0 {
			pool = max(1, desiredPool/2)
		} else {
			pool = 1
		}

		if desiredPages > 0 {
			pages = max(1, desiredPages/2)
		} else {
			pages = 1
		}
	}

	if desiredPool > 0 && pool > desiredPool {
		pool = desiredPool
	}

	if desiredPages > 0 && pages > desiredPages {
		pages = desiredPages
	}

	return pool, pages
}
