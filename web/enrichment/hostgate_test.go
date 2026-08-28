//nolint:testpackage // Package-internal tests exercise the gate's own bookkeeping.
package enrichment

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHostGateBoundsConcurrencyPerHost proves that a worker pool cannot open
// more simultaneous requests against one host than the gate allows, while a
// different host proceeds independently.
func TestHostGateBoundsConcurrencyPerHost(t *testing.T) {
	t.Parallel()

	gate := NewHostGate(HostGateConfig{MaxConcurrentPerHost: 2})

	var (
		mutex   sync.Mutex
		current int
		peak    int
	)

	hold := make(chan struct{})
	started := make(chan struct{}, 5)

	var group sync.WaitGroup

	for range 5 {
		group.Add(1)

		go func() {
			defer group.Done()

			release, err := gate.Acquire(context.Background(), "https://shared.example/page")
			if err != nil {
				t.Errorf("Acquire() error = %v", err)

				return
			}

			mutex.Lock()
			current++
			if current > peak {
				peak = current
			}
			mutex.Unlock()

			started <- struct{}{}
			<-hold

			mutex.Lock()
			current--
			mutex.Unlock()

			release()
		}()
	}

	// Two workers must be admitted; a third must not be.
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("the gate admitted fewer workers than its limit")
		}
	}

	select {
	case <-started:
		t.Fatal("the gate admitted a third concurrent request to one host")
	case <-time.After(50 * time.Millisecond):
	}

	// A different host shares no budget.
	release, err := gate.Acquire(context.Background(), "https://other.example/")
	if err != nil {
		t.Fatalf("Acquire(other host) error = %v", err)
	}

	release()
	close(hold)
	group.Wait()

	if peak > 2 {
		t.Fatalf("peak concurrency for one host = %d, want at most 2", peak)
	}
}

// TestHostGateSpacesRequestsToOneHost proves the minimum interval is applied
// per host, using an injected clock so the test never actually sleeps.
func TestHostGateSpacesRequestsToOneHost(t *testing.T) {
	t.Parallel()

	gate, ok := NewHostGate(HostGateConfig{
		MaxConcurrentPerHost: 1,
		MinInterval:          2 * time.Second,
	}).(*hostGate)
	if !ok {
		t.Fatal("NewHostGate did not return the local gate implementation")
	}

	clock := time.Unix(1787927323, 0)
	var slept atomic.Int64

	gate.now = func() time.Time { return clock }
	gate.sleep = func(_ context.Context, delay time.Duration) error {
		slept.Add(int64(delay))
		clock = clock.Add(delay)

		return nil
	}

	first, err := gate.Acquire(context.Background(), "polite.example")
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}

	first()

	second, err := gate.Acquire(context.Background(), "polite.example")
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}

	second()

	if waited := time.Duration(slept.Load()); waited != 2*time.Second {
		t.Fatalf("spacing wait = %s, want 2s", waited)
	}
}

// TestHostGateCancellationReleasesTheSlot proves a cancelled wait never leaks a
// permit, which would otherwise wedge every later request to that host.
func TestHostGateCancellationReleasesTheSlot(t *testing.T) {
	t.Parallel()

	gate, ok := NewHostGate(HostGateConfig{
		MaxConcurrentPerHost: 1,
		MinInterval:          time.Hour,
	}).(*hostGate)
	if !ok {
		t.Fatal("NewHostGate did not return the local gate implementation")
	}

	var clockMutex sync.Mutex

	clock := time.Unix(1787927323, 0)

	gate.now = func() time.Time {
		clockMutex.Lock()
		defer clockMutex.Unlock()

		return clock
	}
	gate.sleep = func(ctx context.Context, delay time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}

		clockMutex.Lock()
		clock = clock.Add(delay)
		clockMutex.Unlock()

		return nil
	}

	first, err := gate.Acquire(context.Background(), "slow.example")
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}

	first()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := gate.Acquire(ctx, "slow.example"); err == nil {
		t.Fatal("a cancelled Acquire returned a slot")
	}

	done := make(chan struct{})

	go func() {
		release, acquireErr := gate.Acquire(context.Background(), "slow.example")
		if acquireErr == nil {
			release()
		}

		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled wait leaked the host permit")
	}
}

func TestNormalizeGateHostCollapsesEquivalentHosts(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct{ in, want string }{
		{in: "https://WWW.Example.COM/contact", want: "example.com"},
		{in: "example.com:8443", want: "example.com"},
		{in: "www.example.com.", want: "example.com"},
		{in: "http://example.com", want: "example.com"},
		{in: "", want: ""},
	} {
		if got := NormalizeGateHost(testCase.in); got != testCase.want {
			t.Fatalf("NormalizeGateHost(%q) = %q, want %q", testCase.in, got, testCase.want)
		}
	}
}
