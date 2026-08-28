package webrunner

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestIsBrowserProcessNameMatchesLaunchedBrowsers(t *testing.T) {
	t.Parallel()

	matches := []string{"chrome", "Chrome.exe", "chromium", "headless_shell", "HEADLESS_SHELL.EXE", "msedge.exe", "firefox"}
	for _, name := range matches {
		if !isBrowserProcessName(name) {
			t.Errorf("isBrowserProcessName(%q) = false, want true", name)
		}
	}

	others := []string{"", "node", "go", "chromedriver", "explorer.exe", "chrome_helper"}
	for _, name := range others {
		if isBrowserProcessName(name) {
			t.Errorf("isBrowserProcessName(%q) = true, want false", name)
		}
	}
}

func TestHasAncestorWalksABoundedParentChain(t *testing.T) {
	t.Parallel()

	parents := map[int32]int32{
		500: 400, // browser
		400: 300, // driver
		300: 100, // this application
		100: 1,
		900: 800, // an unrelated browser started by the operator
		800: 1,
	}

	if !hasAncestor(parents, 500, 100) {
		t.Fatal("a browser below the application was not attributed to it")
	}

	if hasAncestor(parents, 900, 100) {
		t.Fatal("an unrelated browser was attributed to the application")
	}

	// A cycle must terminate rather than spin.
	if hasAncestor(map[int32]int32{1: 2, 2: 1}, 1, 99) {
		t.Fatal("a cyclic parent chain reported a false ancestor")
	}
}

func TestCountManagedBrowserProcessesReadsTheLocalProcessTable(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), browserCensusTimeout)
	defer cancel()

	// The census must read the real process table without error on this
	// host. This test asserts it is measurable, not that any browser runs.
	count, resident, err := countManagedBrowserProcesses(ctx, int32(os.Getpid()))
	if err != nil {
		t.Skipf("process enumeration is unavailable on this host: %v", err)
	}

	if count < 0 {
		t.Fatalf("browser census = %d, want a non-negative count", count)
	}

	// Resident memory is measured alongside the count so the per-browser
	// planning cost can be an observation rather than an estimate. With no
	// browser running it is legitimately zero; it may never be measured
	// without also counting the process it belongs to.
	if count == 0 && resident != 0 {
		t.Fatalf("census measured %d bytes across zero browsers", resident)
	}
}

func TestBrowserCensusCachesBetweenSamples(t *testing.T) {
	t.Parallel()

	census := &browserCensus{count: 7, memoryBytes: 7 << 20, sampledAt: time.Now()}

	got, resident := census.countBrowsers(context.Background(), int32(os.Getpid()))
	if got != 7 || resident != 7<<20 {
		t.Fatalf("cached census = (%d, %d), want (7, %d)", got, resident, uint64(7<<20))
	}

	// A cancelled context cannot produce a fresh value, so the previous one
	// must survive rather than collapsing to zero. That matters for the
	// memory reading too: a zeroed measurement would make browsers look free.
	stale := &browserCensus{
		count: 4, memoryBytes: 4 << 20, sampledAt: time.Now().Add(-2 * browserCensusInterval),
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	got, resident = stale.countBrowsers(cancelled, int32(os.Getpid()))
	if got != 4 || resident != 4<<20 {
		t.Fatalf("census after a failed refresh = (%d, %d), want the previous (4, %d)",
			got, resident, uint64(4<<20))
	}
}

func TestIsBlockedFailureRecognisesPlatformRefusals(t *testing.T) {
	t.Parallel()

	blocked := []string{
		"http 429 too many requests",
		"redirected to /sorry/index",
		"consent.google.com interstitial",
		"captcha challenge presented",
		"status 403 from maps",
		"unusual traffic from your network",
	}
	for _, message := range blocked {
		if !isBlockedFailure(message) {
			t.Errorf("isBlockedFailure(%q) = false, want true", message)
		}
	}

	ordinary := []string{
		"proxyconnect tcp: dial failed",
		"context deadline exceeded",
		"chromium exited unexpectedly",
		"could not parse listing payload",
		"407 proxy authentication required",
	}
	for _, message := range ordinary {
		if isBlockedFailure(message) {
			t.Errorf("isBlockedFailure(%q) = true, want false", message)
		}
	}
}

func TestClassifyTaskFailureReportsBlocksAndBacksOffLongest(t *testing.T) {
	t.Parallel()

	if got := classifyTaskFailure(errors.New("maps returned HTTP 429 Too Many Requests")); got != "blocked" {
		t.Fatalf("classifyTaskFailure(rate limit) = %s, want blocked", got)
	}

	if got := classifyTaskFailure(errors.New("navigation hit /sorry/index captcha")); got != "blocked" {
		t.Fatalf("classifyTaskFailure(challenge) = %s, want blocked", got)
	}

	blocked := taskFailureBackoff("blocked", 1)
	for _, kind := range []string{"browser-failure", "proxy-failure", "website-timeout", "parsing-failure", "task-failed"} {
		if other := taskFailureBackoff(kind, 1); blocked <= other {
			t.Fatalf("blocked backoff %s is not longer than %s backoff %s", blocked, kind, other)
		}
	}

	if got := taskFailureBackoff("blocked", 100); got != maximumTaskFailureBackoff {
		t.Fatalf("blocked backoff at attempt 100 = %s, want the %s cap", got, maximumTaskFailureBackoff)
	}
}

func TestDecideBlockBudgetHalvesOnAnyBlockAndRecoversSlowly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		current, desired int
		blocks, attempts int
		want             int
	}{
		{name: "quiet window holds", current: 8, desired: 8, want: 8},
		{name: "a single block halves", current: 8, desired: 8, blocks: 1, attempts: 6, want: 4},
		{name: "halving floors at one", current: 1, desired: 8, blocks: 3, attempts: 3, want: 1},
		{name: "clean window recovers one step", current: 4, desired: 8, attempts: 3, want: 5},
		{name: "clean window at desired holds", current: 8, desired: 8, attempts: 9, want: 8},
		{name: "short clean window holds", current: 4, desired: 8, attempts: 2, want: 4},
		{name: "budget never exceeds desired", current: 12, desired: 8, attempts: 5, want: 8},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := decideBlockBudget(test.current, test.desired, test.blocks, test.attempts)
			if got != test.want {
				t.Fatalf("decideBlockBudget(%d, %d, %d, %d) = %d, want %d",
					test.current, test.desired, test.blocks, test.attempts, got, test.want)
			}
		})
	}

	// One block must always cost more than one clean window regains.
	for budget := 2; budget <= 16; budget *= 2 {
		lost := budget - decideBlockBudget(budget, 16, 1, 4)
		regained := decideBlockBudget(budget, 16, 0, 4) - budget

		if lost < regained {
			t.Fatalf("budget %d: block lost %d but a clean window regained %d", budget, lost, regained)
		}
	}
}

func TestRecoveryHasHeadroomRequiresEveryMeasuredDimension(t *testing.T) {
	t.Parallel()

	healthy := workerResourceSample{
		CPUPercent: 20, MemoryUsedBytes: 2 << 30,
		MemoryAvailableBytes: 8 << 30, DiskFreeBytes: 64 << 30, BrowserProcesses: 4,
	}

	if !recoveryHasHeadroom(healthy, 0, 4, 0) {
		t.Fatal("a healthy sample with no blocks must allow recovery")
	}

	if recoveryHasHeadroom(healthy, 1, 4, 0) {
		t.Fatal("a window containing a block must veto recovery")
	}

	busy := healthy
	busy.CPUPercent = recoveryCPUPercent

	if recoveryHasHeadroom(busy, 0, 4, 0) {
		t.Fatal("a busy CPU must veto recovery")
	}

	tight := healthy
	tight.MemoryAvailableBytes = recoveryMemoryBytes - 1

	if recoveryHasHeadroom(tight, 0, 4, 0) {
		t.Fatal("low available memory must veto recovery")
	}

	crowded := healthy
	crowded.BrowserProcesses = 4 + browserHeadroomSlack + 1

	if recoveryHasHeadroom(crowded, 0, 4, 0) {
		t.Fatal("more live browsers than the plan allows must veto recovery")
	}

	unknownMemory := healthy
	unknownMemory.MemoryAvailableBytes = 0

	if !recoveryHasHeadroom(unknownMemory, 0, 4, 0) {
		t.Fatal("an unmeasured memory reading must not block recovery on its own")
	}
}

func TestAdaptiveBrowserBudgetShrinksUnderMemoryPressure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                      string
		desiredPool, desiredPages int
		available                 uint64
		wantPool, wantPages       int
	}{
		{name: "healthy memory keeps the configured budget", desiredPool: 4, desiredPages: 3, available: 8 << 30, wantPool: 4, wantPages: 3},
		{name: "unmeasured memory keeps the configured budget", desiredPool: 4, desiredPages: 3, wantPool: 4, wantPages: 3},
		{name: "moderate pressure halves both budgets", desiredPool: 4, desiredPages: 3, available: (2 << 30) - 1, wantPool: 2, wantPages: 1},
		{name: "moderate pressure pins engine defaults to one", available: (2 << 30) - 1, wantPool: 1, wantPages: 1},
		{name: "severe pressure pins both to one", desiredPool: 8, desiredPages: 6, available: (1 << 30) - 1, wantPool: 1, wantPages: 1},
		{name: "budgets never exceed the configured values", desiredPool: 1, desiredPages: 1, available: 8 << 30, wantPool: 1, wantPages: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			pool, pages := adaptiveBrowserBudget(
				test.desiredPool, test.desiredPages,
				workerResourceSample{MemoryAvailableBytes: test.available},
				0,
			)
			if pool != test.wantPool || pages != test.wantPages {
				t.Fatalf("adaptiveBrowserBudget(%d, %d, %d bytes) = (%d, %d), want (%d, %d)",
					test.desiredPool, test.desiredPages, test.available, pool, pages, test.wantPool, test.wantPages)
			}
		})
	}
}
