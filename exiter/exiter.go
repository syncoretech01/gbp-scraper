package exiter

import (
	"context"
	"sync"
)

type Exiter interface {
	SetSeedCount(int)
	SetCancelFunc(context.CancelFunc)
	IncrSeedCompleted(int)
	IncrPlacesFound(int)
	IncrPlacesCompleted(int)
	Run(context.Context)
}

type exiter struct {
	seedCount       int
	seedCompleted   int
	placesFound     int
	placesCompleted int

	mu            *sync.Mutex
	cancelFunc    context.CancelFunc
	doneCh        chan struct{}
	maximumPlaces int
	limitReached  bool
}

// Option configures an exit monitor.
type Option func(*exiter)

// WithMaximumPlaces stops a run after at least limit listing-detail tasks
// have completed. In-flight work may produce a small bounded overshoot.
func WithMaximumPlaces(limit int) Option {
	return func(monitor *exiter) {
		if limit > 0 {
			monitor.maximumPlaces = limit
		}
	}
}

// Snapshot is a concurrency-safe view of pipeline counters.
type Snapshot struct {
	SeedsTotal      int  `json:"seeds_total"`
	SeedsCompleted  int  `json:"seeds_completed"`
	PlacesFound     int  `json:"places_found"`
	PlacesCompleted int  `json:"places_completed"`
	MaximumPlaces   int  `json:"maximum_places,omitempty"`
	LimitReached    bool `json:"limit_reached"`
}

func New(options ...Option) Exiter {
	monitor := &exiter{
		mu:     &sync.Mutex{},
		doneCh: make(chan struct{}, 1),
	}
	for _, option := range options {
		if option != nil {
			option(monitor)
		}
	}

	return monitor
}

func (e *exiter) SetSeedCount(val int) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.seedCount = val
}

func (e *exiter) SetCancelFunc(fn context.CancelFunc) {
	e.mu.Lock()
	e.cancelFunc = fn
	limitReached := e.limitReached
	e.mu.Unlock()
	if limitReached && fn != nil {
		fn()
	}
}

func (e *exiter) IncrSeedCompleted(val int) {
	e.mu.Lock()
	e.seedCompleted += val
	done := e.seedCompleted >= e.seedCount && e.placesCompleted >= e.placesFound
	e.mu.Unlock()

	if done {
		select {
		case e.doneCh <- struct{}{}:
		default:
		}
	}
}

func (e *exiter) IncrPlacesFound(val int) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.placesFound += val
}

func (e *exiter) IncrPlacesCompleted(val int) {
	e.mu.Lock()
	e.placesCompleted += val
	if e.maximumPlaces > 0 && e.placesCompleted >= e.maximumPlaces {
		e.limitReached = true
	}
	done := e.seedCompleted >= e.seedCount && e.placesCompleted >= e.placesFound
	limitReached := e.limitReached
	cancel := e.cancelFunc
	e.mu.Unlock()

	if limitReached && cancel != nil {
		cancel()
		return
	}

	if done {
		select {
		case e.doneCh <- struct{}{}:
		default:
		}
	}
}

// SnapshotOf returns current counters for monitors created by this package.
func SnapshotOf(value Exiter) Snapshot {
	monitor, ok := value.(*exiter)
	if !ok || monitor == nil {
		return Snapshot{}
	}
	monitor.mu.Lock()
	defer monitor.mu.Unlock()

	return Snapshot{
		SeedsTotal: monitor.seedCount, SeedsCompleted: monitor.seedCompleted,
		PlacesFound: monitor.placesFound, PlacesCompleted: monitor.placesCompleted,
		MaximumPlaces: monitor.maximumPlaces, LimitReached: monitor.limitReached,
	}
}

// LimitReached reports whether a configured record limit stopped the run.
func LimitReached(value Exiter) bool {
	return SnapshotOf(value).LimitReached
}

func (e *exiter) Run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-e.doneCh:
		e.mu.Lock()
		cancel := e.cancelFunc
		e.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
}
