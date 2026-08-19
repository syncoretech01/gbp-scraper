package gmaps

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/gosom/scrapemate"
)

// RuntimeOptions applies bounded per-request behavior to a seed and every
// child listing/contact job it creates.
type RuntimeOptions struct {
	Timeout           time.Duration
	MaxRetries        int
	MaxRetryDelay     time.Duration
	RetriesConfigured bool
	RandomDelayMin    time.Duration
	RandomDelayMax    time.Duration
}

// ConfigureRuntime applies runtime options to a supported Maps seed job.
func ConfigureRuntime(job scrapemate.IJob, options RuntimeOptions) {
	switch item := job.(type) {
	case *GmapJob:
		item.Runtime = options
		applyRuntimeOptions(&item.Job, options)
	case *SearchJob:
		item.Runtime = options
		applyRuntimeOptions(&item.Job, options)
	case *PlaceJob:
		item.Runtime = options
		applyRuntimeOptions(&item.Job, options)
	case *EmailExtractJob:
		item.Runtime = options
		applyRuntimeOptions(&item.Job, options)
	}
}

func applyRuntimeOptions(job *scrapemate.Job, options RuntimeOptions) {
	if options.Timeout > 0 {
		job.Timeout = options.Timeout
	}
	if options.RetriesConfigured || options.MaxRetries > 0 {
		job.MaxRetries = options.MaxRetries
	}
	if options.RetriesConfigured || options.MaxRetryDelay > 0 {
		job.MaxRetryDelay = options.MaxRetryDelay
	}
}

func waitRuntimeDelay(ctx context.Context, options RuntimeOptions) error {
	minimum := options.RandomDelayMin
	maximum := options.RandomDelayMax
	if minimum <= 0 && maximum <= 0 {
		return nil
	}
	if minimum < 0 {
		minimum = 0
	}
	if maximum < minimum {
		maximum = minimum
	}
	delay := minimum
	if span := maximum - minimum; span > 0 {
		delay += time.Duration(rand.Int64N(int64(span) + 1))
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func withPlaceJobRuntime(options RuntimeOptions) PlaceJobOptions {
	return func(job *PlaceJob) {
		job.Runtime = options
		applyRuntimeOptions(&job.Job, options)
	}
}

func withEmailJobRuntime(options RuntimeOptions) EmailExtractJobOptions {
	return func(job *EmailExtractJob) {
		job.Runtime = options
		applyRuntimeOptions(&job.Job, options)
	}
}
