package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// DashboardAnalytics runs about ten hand-written statements whose error is
// swallowed by the dashboard page, so a broken query degrades silently to an
// empty dashboard rather than failing. These tests execute every one of them
// against a real schema with seeded rows.

func TestDashboardAnalyticsAggregatesSeededResults(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	job := resultImportJob("job-dashboard", time.Now().UTC())
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "dashboard.csv")
	writeLegacyResultRows(t, path, map[string]string{
		"title": "Harbor Dental", "category": "Dentist", "place_id": "dashboard-place",
		"address": "1 Market St, San Francisco, CA 94105, United States",
		"website": "https://harbordental.example", "review_rating": "4.6",
		"review_count": "120", "status": "Open",
	})

	if _, err := repository.ImportLegacyCSV(ctx, job, path); err != nil {
		t.Fatalf("ImportLegacyCSV() error = %v", err)
	}

	since := time.Now().UTC().AddDate(0, 0, -30)

	analytics, err := repository.DashboardAnalytics(ctx, since)
	if err != nil {
		t.Fatalf("DashboardAnalytics() error = %v", err)
	}

	if analytics.CollectedToday != 1 || analytics.CollectedWeek != 1 || analytics.CollectedMonth != 1 {
		t.Fatalf("collection counters = today %d, week %d, month %d",
			analytics.CollectedToday, analytics.CollectedWeek, analytics.CollectedMonth)
	}

	if analytics.Availability.Websites != 1 {
		t.Fatalf("website availability = %d, want 1", analytics.Availability.Websites)
	}

	if len(analytics.CollectionByDate) != 1 || analytics.CollectionByDate[0].Value != 1 {
		t.Fatalf("collection by date = %+v", analytics.CollectionByDate)
	}

	assertLabelPresent(t, "cities", analytics.Cities, "San Francisco")
	assertLabelPresent(t, "categories", analytics.Categories, "Dentist")
	assertLabelPresent(t, "rating bands", analytics.RatingBands, "4.5–5.0")

	if len(analytics.Statuses) == 0 {
		t.Fatal("status breakdown is empty")
	}
}

func TestDashboardSpeedTrendsCountEmittedWarningEvents(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	job := lifecycleTestJob("job-speed-trend", time.Now().UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("CreateWithState() error = %v", err)
	}

	// Only the severities the worker actually emits may be counted. A filter on a
	// value no writer produces would leave this series permanently zero.
	for _, event := range []struct {
		kind     string
		severity string
	}{
		{kind: "low-disk", severity: "warning"},
		{kind: "adaptive-performance", severity: "information"},
		{kind: "task-failed", severity: "error"},
	} {
		if err := repository.RecordJobWorkerEvent(
			ctx, job.ID, event.kind, event.severity, "seeded "+event.kind, nil,
		); err != nil {
			t.Fatalf("RecordJobWorkerEvent(%s) error = %v", event.kind, err)
		}
	}

	if err := repository.UpdateJobWorkerProgress(ctx, job.ID, web.JobWorkerProgress{
		PlacesPerMinute: 12.5, EffectiveWorkers: 4,
	}); err != nil {
		t.Fatalf("UpdateJobWorkerProgress() error = %v", err)
	}

	trends, err := repository.dashboardSpeedTrends(ctx, time.Now().UTC().AddDate(0, 0, -7))
	if err != nil {
		t.Fatalf("dashboardSpeedTrends() error = %v", err)
	}

	if len(trends) != 1 {
		t.Fatalf("speed trends = %+v, want one day", trends)
	}

	if trends[0].WarningEvents != 2 {
		t.Fatalf("warning events = %d, want 2 (the warning and the error)", trends[0].WarningEvents)
	}

	if trends[0].PlacesPerMinute != 12.5 {
		t.Fatalf("places per minute = %.2f, want 12.5", trends[0].PlacesPerMinute)
	}
}

func TestDashboardJobTrendsAndProxySummaryQuery(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	job := lifecycleTestJob("job-outcome-trend", time.Now().UTC())
	if err := repository.CreateWithState(ctx, &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("CreateWithState() error = %v", err)
	}

	since := time.Now().UTC().AddDate(0, 0, -7)

	// Both queries must execute against the real schema. Their values depend on
	// lifecycle transitions elsewhere; the assertion here is that neither the SQL
	// nor the scan drifts away from the columns it reads.
	if _, err := repository.dashboardJobTrends(ctx, since); err != nil {
		t.Fatalf("dashboardJobTrends() error = %v", err)
	}

	summary, err := repository.dashboardProxySummary(ctx)
	if err != nil {
		t.Fatalf("dashboardProxySummary() error = %v", err)
	}

	if summary.Total != 0 || summary.Healthy != 0 {
		t.Fatalf("empty proxy summary = %+v", summary)
	}
}

func TestDashboardProxyLatencyBucketsAndPoolReliability(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	primary, imported, err := repository.ImportProxyPool(ctx, "Primary", "round_robin", []string{
		"http://user:secret@127.0.0.1:8080", // #nosec G101 -- synthetic proxy credential for a local test database.
		"http://user:secret@127.0.0.1:8081",
		"http://user:secret@127.0.0.1:8082",
	})
	if err != nil || imported != 3 {
		t.Fatalf("ImportProxyPool(Primary) = %d, %v", imported, err)
	}

	backup, imported, err := repository.ImportProxyPool(ctx, "Backup", "round_robin", []string{"socks5://10.0.0.1:1080"})
	if err != nil || imported != 1 {
		t.Fatalf("ImportProxyPool(Backup) = %d, %v", imported, err)
	}

	primaries, err := repository.ListProxies(ctx, primary.ID)
	if err != nil || len(primaries) != 3 {
		t.Fatalf("ListProxies(Primary) = %+v, %v", primaries, err)
	}

	backups, err := repository.ListProxies(ctx, backup.ID)
	if err != nil || len(backups) != 1 {
		t.Fatalf("ListProxies(Backup) = %+v, %v", backups, err)
	}

	now := time.Now().UTC()
	recordSample := func(proxyID, status string, latencyMS int64, checkedAt time.Time) {
		t.Helper()

		if err := repository.RecordProxyTest(ctx, proxyID, web.ProxyTestResult{
			Status: status, LatencyMS: &latencyMS, CheckedAt: checkedAt,
		}); err != nil {
			t.Fatalf("RecordProxyTest(%s, %s) error = %v", proxyID, status, err)
		}
	}

	// Fast proxy: one healthy sample below 200 ms.
	recordSample(primaries[0].ID, "healthy", 120, now.Add(-2*time.Minute))
	// Degraded proxy: the newer, slower failing sample must win the bucket.
	recordSample(primaries[1].ID, "healthy", 850, now.Add(-2*time.Minute))
	recordSample(primaries[1].ID, "offline", 1200, now.Add(-time.Minute))
	// primaries[2] is never tested and must land in the Unknown bucket.

	// The backup proxy records only a failure and is then disabled, so it may
	// not appear in the enabled-only latency buckets but its pool still owns
	// an honest 0% reliability figure.
	recordSample(backups[0].ID, "offline", 300, now.Add(-time.Minute))
	if err := repository.SetProxyEnabled(ctx, backups[0].ID, false); err != nil {
		t.Fatalf("SetProxyEnabled() error = %v", err)
	}

	buckets, err := repository.dashboardProxyLatencyBuckets(ctx)
	if err != nil {
		t.Fatalf("dashboardProxyLatencyBuckets() error = %v", err)
	}
	wantBuckets := []web.DashboardCountPoint{
		{Label: "<200 ms", Value: 1},
		{Label: "1000+ ms", Value: 1},
		{Label: "Unknown", Value: 1},
	}
	if len(buckets) != len(wantBuckets) {
		t.Fatalf("latency buckets = %+v, want %+v", buckets, wantBuckets)
	}
	for index, want := range wantBuckets {
		if buckets[index] != want {
			t.Fatalf("latency bucket %d = %+v, want %+v", index, buckets[index], want)
		}
	}

	reliability, err := repository.dashboardProxyPoolReliability(ctx)
	if err != nil {
		t.Fatalf("dashboardProxyPoolReliability() error = %v", err)
	}
	// Primary saw 2 successes and 1 failure (67%), Backup only 1 failure (0%);
	// pools sort by name.
	wantReliability := []web.DashboardCountPoint{
		{Label: "Backup", Value: 0},
		{Label: "Primary", Value: 67},
	}
	if len(reliability) != len(wantReliability) {
		t.Fatalf("pool reliability = %+v, want %+v", reliability, wantReliability)
	}
	for index, want := range wantReliability {
		if reliability[index] != want {
			t.Fatalf("pool reliability %d = %+v, want %+v", index, reliability[index], want)
		}
	}

	analytics, err := repository.DashboardAnalytics(ctx, now.AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("DashboardAnalytics() error = %v", err)
	}
	if len(analytics.ProxyLatencyBuckets) != len(wantBuckets) || len(analytics.ProxyReliability) != len(wantReliability) {
		t.Fatalf("analytics projection: latency = %+v, reliability = %+v",
			analytics.ProxyLatencyBuckets, analytics.ProxyReliability)
	}
}

func assertLabelPresent(t *testing.T, name string, points []web.DashboardCountPoint, want string) {
	t.Helper()

	for _, point := range points {
		if point.Label == want {
			return
		}
	}

	t.Fatalf("%s breakdown missing %q: %+v", name, want, points)
}
