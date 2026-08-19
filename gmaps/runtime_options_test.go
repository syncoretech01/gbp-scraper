package gmaps

import (
	"context"
	"testing"
	"time"
)

func TestRuntimeOptionsApplyToSeedsAndChildren(t *testing.T) {
	t.Parallel()

	options := RuntimeOptions{
		Timeout:        30 * time.Second,
		MaxRetries:     7,
		MaxRetryDelay:  9 * time.Second,
		RandomDelayMin: time.Millisecond,
		RandomDelayMax: 2 * time.Millisecond,
	}
	seed := NewGmapJob("seed", "en", "dentist", 10, true, "37.77,-122.42", 14)
	ConfigureRuntime(seed, options)
	if seed.Timeout != options.Timeout || seed.MaxRetries != options.MaxRetries ||
		seed.MaxRetryDelay != options.MaxRetryDelay || seed.Runtime != options {
		t.Fatalf("configured seed = %+v", seed)
	}

	place := NewPlaceJob(seed.ID, "en", "https://www.google.com/maps/place/example", true, false, withPlaceJobRuntime(seed.Runtime))
	if place.Timeout != options.Timeout || place.MaxRetries != options.MaxRetries || place.Runtime != options {
		t.Fatalf("configured place = %+v", place)
	}
	email := NewEmailJob(place.ID, &Entry{}, withEmailJobRuntime(place.Runtime))
	if email.Timeout != options.Timeout || email.MaxRetries != options.MaxRetries || email.Runtime != options {
		t.Fatalf("configured email = %+v", email)
	}
}

func TestRuntimeDelayHonorsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitRuntimeDelay(ctx, RuntimeOptions{RandomDelayMin: time.Minute, RandomDelayMax: time.Minute}); err == nil {
		t.Fatal("waitRuntimeDelay() ignored cancellation")
	}
}

func TestRuntimeOptionsAllowRetriesToBeDisabledExplicitly(t *testing.T) {
	t.Parallel()

	seed := NewGmapJob("seed", "en", "dentist", 10, false, "37.77,-122.42", 14)
	if seed.MaxRetries == 0 {
		t.Fatal("test requires the scraper's default retry count to be non-zero")
	}
	ConfigureRuntime(seed, RuntimeOptions{RetriesConfigured: true})
	if seed.MaxRetries != 0 || seed.MaxRetryDelay != 0 {
		t.Fatalf("explicit zero retry configuration was not applied: retries=%d delay=%s", seed.MaxRetries, seed.MaxRetryDelay)
	}
}
