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

func assertLabelPresent(t *testing.T, name string, points []web.DashboardCountPoint, want string) {
	t.Helper()

	for _, point := range points {
		if point.Label == want {
			return
		}
	}

	t.Fatalf("%s breakdown missing %q: %+v", name, want, points)
}
