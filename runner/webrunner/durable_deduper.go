package webrunner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"github.com/gosom/google-maps-scraper/deduper"
	"github.com/gosom/google-maps-scraper/web"
)

// listingKeyFlushThreshold is how many newly discovered identities are held in
// memory before they are written. A task typically discovers a few dozen, so
// the threshold turns a run's discovery into a handful of writes rather than
// one per listing.
const listingKeyFlushThreshold = 200

// listingKeyStore is the durable half of the deduplication state. It is an
// interface so the runner can be tested without a database.
type listingKeyStore interface {
	RememberJobListingKeys(ctx context.Context, jobID string, keys []string) (int, error)
	JobListingKeys(ctx context.Context, jobID string, limit int) ([]string, error)
}

// durableDeduper makes the scrape deduplication state survive a restart.
//
// The engine's deduper is an in-memory hash set, so an interrupted run
// previously re-visited every listing it had already discovered. This wrapper
// keeps that exact behaviour in the hot path — the in-memory set still decides
// whether a listing is new — and additionally records each new identity
// durably, seeding the set from those records when a run resumes.
//
// Identities are stored as one-way digests, so the durable record of what a
// job has seen can never become a second copy of the result set.
type durableDeduper struct {
	inner deduper.Deduper
	store listingKeyStore
	jobID string

	mu      sync.Mutex
	pending []string
}

// newDurableDeduper seeds an in-memory deduper from the durable listing
// identities already recorded for a job. A store that cannot serve them, or a
// read that fails, simply yields a fresh deduper: resumability is an
// optimisation here and must never stop a run from starting.
func newDurableDeduper(
	ctx context.Context,
	store listingKeyStore,
	jobID string,
	inner deduper.Deduper,
) *durableDeduper {
	wrapped := &durableDeduper{inner: inner, store: store, jobID: jobID}

	keys, err := store.JobListingKeys(ctx, jobID, web.MaximumListingKeysPerJob)
	if err != nil {
		return wrapped
	}

	for _, key := range keys {
		wrapped.inner.AddIfNotExists(ctx, key)
	}

	return wrapped
}

// AddIfNotExists reports whether a listing identity is new, exactly as the
// in-memory deduper does, and remembers a new identity for the next run.
func (dedup *durableDeduper) AddIfNotExists(ctx context.Context, key string) bool {
	digest := listingKeyDigest(key)

	added := dedup.inner.AddIfNotExists(ctx, digest)
	if !added {
		return false
	}

	dedup.mu.Lock()
	dedup.pending = append(dedup.pending, digest)
	shouldFlush := len(dedup.pending) >= listingKeyFlushThreshold
	dedup.mu.Unlock()

	if shouldFlush {
		dedup.Flush(ctx)
	}

	return true
}

// Flush writes the buffered identities. It is called at every task checkpoint
// and once the run ends, so an interrupted run loses at most the identities
// discovered since its last checkpoint — which the merge-deduplicated CSV
// already makes harmless.
//
// A failed write returns the identities to the buffer so the next flush
// retries them, and never surfaces as a run error.
func (dedup *durableDeduper) Flush(ctx context.Context) {
	dedup.mu.Lock()
	pending := dedup.pending
	dedup.pending = nil
	dedup.mu.Unlock()

	if len(pending) == 0 {
		return
	}

	if _, err := dedup.store.RememberJobListingKeys(ctx, dedup.jobID, pending); err != nil {
		dedup.mu.Lock()
		dedup.pending = append(pending, dedup.pending...)
		dedup.mu.Unlock()
	}
}

// listingKeyDigest turns a listing URL into the fixed-width identity that is
// stored and compared. Hashing keeps the durable record from becoming a
// second copy of the collected data and bounds the stored row size.
func listingKeyDigest(key string) string {
	sum := sha256.Sum256([]byte(key))

	return hex.EncodeToString(sum[:])
}

// flushListingKeys flushes a deduper that keeps durable state, and does
// nothing for the plain in-memory one.
func flushListingKeys(ctx context.Context, dedup deduper.Deduper) {
	if durable, ok := dedup.(*durableDeduper); ok {
		durable.Flush(ctx)
	}
}

// newJobDeduper returns the deduper one job runs with. A repository that can
// persist listing identities yields the durable wrapper; anything else yields
// the historical in-memory deduper unchanged.
func (w *webrunner) newJobDeduper(ctx context.Context, job *web.Job) deduper.Deduper {
	inner := deduper.New()

	if !w.svc.SupportsJobCheckpoints() || !w.svc.SupportsListingState() {
		return inner
	}

	return newDurableDeduper(ctx, jobListingKeyStore{svc: w.svc}, job.ID, inner)
}

// jobListingKeyStore adapts the local service to the store the durable
// deduper needs.
type jobListingKeyStore struct {
	svc *web.Service
}

// RememberJobListingKeys records newly discovered listing identities.
func (store jobListingKeyStore) RememberJobListingKeys(
	ctx context.Context,
	jobID string,
	keys []string,
) (int, error) {
	return store.svc.RememberJobListingKeys(ctx, jobID, keys)
}

// JobListingKeys reads the identities recorded by earlier runs of a job.
func (store jobListingKeyStore) JobListingKeys(
	ctx context.Context,
	jobID string,
	limit int,
) ([]string, error) {
	return store.svc.JobListingKeys(ctx, jobID, limit)
}
