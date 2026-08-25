package webrunner

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestAwaitEngineNeverWedgesTheJob proves the fix for the hang a controlled
// acceptance run reproduced: the scrape engine parked forever inside the
// upstream browser teardown, which takes neither a context nor a timeout, and
// the job could not reach a terminal state.
//
// The contract has two halves. While the task's context is live the engine is
// never cut short, however long it takes. Once the context is cancelled the
// engine gets a bounded grace period and then the task stops waiting.
func TestAwaitEngineNeverWedgesTheJob(t *testing.T) {
	t.Parallel()

	t.Run("returns the engine error when it finishes normally", func(t *testing.T) {
		t.Parallel()

		want := errors.New("scrape failed")

		got, returned := awaitEngine(context.Background(), time.Second, func() error { return want })
		if !returned || !errors.Is(got, want) {
			t.Fatalf("awaitEngine = (%v, %v), want (%v, true)", got, returned, want)
		}
	})

	t.Run("waits without bound while the context is live", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// A grace far shorter than the engine's work: it must not apply yet,
		// because the grace only starts once the context is done.
		started := time.Now()

		_, returned := awaitEngine(ctx, time.Millisecond, func() error {
			time.Sleep(150 * time.Millisecond)

			return nil
		})
		if !returned {
			t.Fatal("awaitEngine gave up on an engine that was still legitimately running")
		}

		if elapsed := time.Since(started); elapsed < 150*time.Millisecond {
			t.Fatalf("returned after %v, want to have waited for the engine", elapsed)
		}
	})

	t.Run("gives up on an engine that never returns after cancellation", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		// The wedged shape: exactly what playwright's browser-context close did
		// for twenty-one minutes. The goroutine is deliberately left parked.
		wedged := make(chan struct{})
		t.Cleanup(func() { close(wedged) })

		started := time.Now()

		err, returned := awaitEngine(ctx, 50*time.Millisecond, func() error {
			<-wedged

			return nil
		})
		if returned {
			t.Fatal("awaitEngine waited for a wedged engine; the job would hang")
		}

		if err != nil {
			t.Fatalf("err = %v, want nil when the engine never answered", err)
		}

		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Fatalf("gave up after %v, want promptly after the grace period", elapsed)
		}
	})

	t.Run("still collects a late engine that beats the grace period", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		want := errors.New("late but finished")

		got, returned := awaitEngine(ctx, 2*time.Second, func() error {
			time.Sleep(30 * time.Millisecond)

			return want
		})
		if !returned || !errors.Is(got, want) {
			t.Fatalf("awaitEngine = (%v, %v), want (%v, true)", got, returned, want)
		}
	})
}

// TestEngineShutdownTimeoutClassification proves the abandoned-engine outcome is
// reported as its own named cause while keeping the coarse bucket every
// scheduling decision already depends on.
func TestEngineShutdownTimeoutClassification(t *testing.T) {
	t.Parallel()

	classification := classifyFailureKind(errEngineShutdownTimeout)
	if classification.Fine != FailureKindEngineShutdownTimeout {
		t.Fatalf("fine kind = %q, want %q", classification.Fine, FailureKindEngineShutdownTimeout)
	}

	if classification.Coarse != coarseBrowserFailure {
		t.Fatalf("coarse bucket = %q, want %q so scheduling is unchanged",
			classification.Coarse, coarseBrowserFailure)
	}

	if got := coarseForKind(FailureKindEngineShutdownTimeout); got != coarseBrowserFailure {
		t.Fatalf("coarseForKind = %q, want %q", got, coarseBrowserFailure)
	}

	// The sentinel survives wrapping, which is how it reaches the classifier
	// from a joined error.
	wrapped := errors.Join(errors.New("close checkpoint worker"), errEngineShutdownTimeout)
	if classifyFailureKind(wrapped).Fine != FailureKindEngineShutdownTimeout {
		t.Fatal("a wrapped shutdown timeout lost its fine kind")
	}
}
