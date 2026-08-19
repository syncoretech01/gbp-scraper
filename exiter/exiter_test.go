package exiter

import (
	"context"
	"testing"
	"time"
)

func TestMaximumPlacesCancelsAndExposesSnapshot(t *testing.T) {
	t.Parallel()

	monitor := New(WithMaximumPlaces(2))
	ctx, cancel := context.WithCancel(context.Background())
	monitor.SetCancelFunc(cancel)
	monitor.SetSeedCount(1)
	monitor.IncrPlacesFound(4)
	monitor.IncrSeedCompleted(1)
	monitor.IncrPlacesCompleted(1)
	if LimitReached(monitor) {
		t.Fatal("record limit reached too early")
	}
	monitor.IncrPlacesCompleted(1)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("maximum places did not cancel the run")
	}
	snapshot := SnapshotOf(monitor)
	if !snapshot.LimitReached || snapshot.MaximumPlaces != 2 || snapshot.PlacesFound != 4 || snapshot.PlacesCompleted != 2 {
		t.Fatalf("SnapshotOf() = %+v", snapshot)
	}
}
