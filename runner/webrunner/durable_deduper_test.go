package webrunner

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gosom/google-maps-scraper/deduper"
)

// listingKeyStoreStub is an in-memory stand-in for the durable store.
type listingKeyStoreStub struct {
	mu       sync.Mutex
	keys     map[string]struct{}
	order    []string
	writes   int
	readErr  error
	writeErr error
}

func newListingKeyStoreStub(existing ...string) *listingKeyStoreStub {
	store := &listingKeyStoreStub{keys: make(map[string]struct{}, len(existing))}
	for _, key := range existing {
		store.keys[key] = struct{}{}
		store.order = append(store.order, key)
	}

	return store
}

func (store *listingKeyStoreStub) RememberJobListingKeys(
	_ context.Context, _ string, keys []string,
) (int, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	if store.writeErr != nil {
		return 0, store.writeErr
	}

	store.writes++
	inserted := 0

	for _, key := range keys {
		if _, exists := store.keys[key]; exists {
			continue
		}

		store.keys[key] = struct{}{}
		store.order = append(store.order, key)
		inserted++
	}

	return inserted, nil
}

func (store *listingKeyStoreStub) JobListingKeys(
	_ context.Context, _ string, limit int,
) ([]string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	if store.readErr != nil {
		return nil, store.readErr
	}

	if limit > 0 && limit < len(store.order) {
		return append([]string(nil), store.order[:limit]...), nil
	}

	return append([]string(nil), store.order...), nil
}

func (store *listingKeyStoreStub) count() int {
	store.mu.Lock()
	defer store.mu.Unlock()

	return len(store.keys)
}

func TestDurableDeduperRecordsNewListingsAndSkipsRepeats(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newListingKeyStoreStub()
	dedup := newDurableDeduper(ctx, store, "job-1", deduper.New())

	if !dedup.AddIfNotExists(ctx, "https://maps.google.com/place-a") {
		t.Fatal("the first sighting of a listing must be new")
	}

	if dedup.AddIfNotExists(ctx, "https://maps.google.com/place-a") {
		t.Fatal("a repeated listing must not be new")
	}

	if !dedup.AddIfNotExists(ctx, "https://maps.google.com/place-b") {
		t.Fatal("a second distinct listing must be new")
	}

	// Nothing is written until a durable boundary is reached.
	if store.count() != 0 {
		t.Fatalf("store holds %d keys before the flush, want 0", store.count())
	}

	dedup.Flush(ctx)

	if store.count() != 2 {
		t.Fatalf("store holds %d keys after the flush, want 2", store.count())
	}
}

func TestDurableDeduperResumesFromRecordedListings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newListingKeyStoreStub()

	first := newDurableDeduper(ctx, store, "job-1", deduper.New())
	first.AddIfNotExists(ctx, "https://maps.google.com/place-a")
	first.AddIfNotExists(ctx, "https://maps.google.com/place-b")
	first.Flush(ctx)

	// A restarted run must treat both listings as already seen.
	second := newDurableDeduper(ctx, store, "job-1", deduper.New())

	if second.AddIfNotExists(ctx, "https://maps.google.com/place-a") {
		t.Fatal("a listing recorded by an earlier run was treated as new after restart")
	}

	if !second.AddIfNotExists(ctx, "https://maps.google.com/place-c") {
		t.Fatal("an unseen listing must still be new after restart")
	}

	second.Flush(ctx)

	if store.count() != 3 {
		t.Fatalf("store holds %d keys, want 3", store.count())
	}
}

func TestDurableDeduperFlushesOnceTheBufferFills(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newListingKeyStoreStub()
	dedup := newDurableDeduper(ctx, store, "job-1", deduper.New())

	for index := 0; index < listingKeyFlushThreshold; index++ {
		dedup.AddIfNotExists(ctx, "https://maps.google.com/place-"+string(rune('a'+index%26))+string(rune('a'+index/26)))
	}

	if store.writes == 0 {
		t.Fatal("a full buffer must flush without waiting for a checkpoint")
	}
}

func TestDurableDeduperKeepsKeysWhenTheWriteFails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newListingKeyStoreStub()
	store.writeErr = errors.New("database is locked")

	dedup := newDurableDeduper(ctx, store, "job-1", deduper.New())
	dedup.AddIfNotExists(ctx, "https://maps.google.com/place-a")
	dedup.Flush(ctx)

	if store.count() != 0 {
		t.Fatalf("store unexpectedly accepted %d keys", store.count())
	}

	// The failed keys must be retried, not lost.
	store.writeErr = nil
	dedup.Flush(ctx)

	if store.count() != 1 {
		t.Fatalf("store holds %d keys after the retry, want 1", store.count())
	}
}

func TestDurableDeduperStartsCleanWhenTheStoreCannotBeRead(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newListingKeyStoreStub("already-recorded")
	store.readErr = errors.New("database is locked")

	dedup := newDurableDeduper(ctx, store, "job-1", deduper.New())

	// A failed read must never stop a run: the deduper simply starts empty.
	if !dedup.AddIfNotExists(ctx, "https://maps.google.com/place-a") {
		t.Fatal("a run must start even when the durable state cannot be read")
	}
}

func TestListingKeyDigestIsStableAndDoesNotStoreTheURL(t *testing.T) {
	t.Parallel()

	url := "https://maps.google.com/place-a"
	digest := listingKeyDigest(url)

	if digest != listingKeyDigest(url) {
		t.Fatal("the digest of one listing must be stable")
	}

	if digest == url || len(digest) != 64 {
		t.Fatalf("digest = %q, want a 64 character one-way hash", digest)
	}

	if listingKeyDigest(url) == listingKeyDigest(url+"-b") {
		t.Fatal("distinct listings must not share a digest")
	}
}

func TestFlushListingKeysIgnoresThePlainDeduper(t *testing.T) {
	t.Parallel()

	// The historical in-memory deduper must remain usable unchanged.
	flushListingKeys(context.Background(), deduper.New())
}
