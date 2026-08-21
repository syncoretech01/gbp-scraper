package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// dashboardAnalyticsRepositoryStub serves the exact projection the dashboard
// asks the database for, so the page-level assertions below are about what the
// operator can see rather than about SQL.
type dashboardAnalyticsRepositoryStub struct {
	fixedJobRepository

	analytics DashboardAnalytics
}

func (r *dashboardAnalyticsRepositoryStub) DashboardAnalytics(
	context.Context,
	time.Time,
) (DashboardAnalytics, error) {
	return r.analytics, nil
}

func newDashboardSpecServer(t *testing.T) *Server {
	t.Helper()

	const jobID = "44444444-4444-4444-4444-444444444444"

	dir := t.TempDir()
	writeCSV(t, dir, jobID, strings.Join([]string{
		"place_id,title,website,phone,emails",
		"one,Alpha,https://alpha.test,+1 555,[hello@alpha.test]",
		"two,Beta,,+1 556,[]",
		"three,Gamma,https://gamma.test,,[]",
		"four,Delta,https://delta.test,+1 557,[hi@delta.test]",
	}, "\n"))

	repo := &dashboardAnalyticsRepositoryStub{
		fixedJobRepository: fixedJobRepository{job: Job{
			ID: jobID, Name: "Austin plumbers", Date: time.Now().UTC(), Status: StatusOK,
		}},
		analytics: DashboardAnalytics{
			CollectedToday: 4,
			CollectedWeek:  4,
			CollectedMonth: 4,
			Availability: DashboardAvailabilitySummary{
				Websites:        3,
				Emails:          2,
				Phones:          3,
				SocialProfiles:  1,
				WebsiteActive:   2,
				WebsiteInactive: 1,
			},
			SpeedTrends: []DashboardSpeedTrend{{
				Label:            "2026-08-19",
				PlacesPerMinute:  12.5,
				WarningEvents:    7,
				BlockEvents:      3,
				BlockRatePercent: 23.5,
			}},
			JobTrends: []DashboardJobTrend{{
				Label: "2026-08-19", Completed: 2, Partial: 1, Failed: 1, Cancelled: 0,
			}},
			Proxy: DashboardProxySummary{
				Total: 5, Enabled: 4, Healthy: 3, Successes: 90, Failures: 10, Blocks: 5,
				AverageLatencyMS: 320,
			},
		},
	}

	srv, err := New(NewService(repo, dir), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return srv
}

func renderDashboard(t *testing.T, srv *Server) string {
	t.Helper()

	recorder := httptest.NewRecorder()
	srv.dashboardPage(recorder, httptest.NewRequest(http.MethodGet, "/app/dashboard", http.NoBody))
	if recorder.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	return recorder.Body.String()
}

// The specification's summary metrics are worthless as internal fields: each
// one has to reach the page. These assertions fail if a metric is computed but
// never rendered, which is exactly how they were missing before.
func TestDashboardRendersDiscoveryProxyAndStorageMetrics(t *testing.T) {
	t.Parallel()

	body := renderDashboard(t, newDashboardSpecServer(t))

	for _, want := range []string{
		// Websites, phones, emails, and social profiles discovered, each with
		// the count behind the percentage.
		`data-availability="Website"`,
		`data-availability="Email"`,
		`data-availability="Phone"`,
		`data-availability="Social profile"`,
		`class="dash-availability-count t-caption"`,
		// Proxy success rate, block rate, and healthy proxy count.
		`data-metric="proxy-success-rate"`,
		`data-metric="proxy-block-rate"`,
		`data-metric="healthy-proxies"`,
		// Storage breakdown and remaining disk capacity.
		`data-storage-breakdown`,
		`data-storage="database"`,
		`data-storage="exports"`,
		`data-storage="screenshots"`,
		`data-storage="logs"`,
		`data-storage="disk-free"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard is missing %q", want)
		}
	}

	// 90 successes out of 100 recorded outcomes, 5 blocks against the same
	// denominator, 3 of 5 proxies healthy.
	for _, want := range []string{">90.0%<", ">5.0%<", `data-metric="healthy-proxies">3<`} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard proxy metrics missing %q", want)
		}
	}
}

func TestDashboardRendersJobTrendsSpeedAndBlockRate(t *testing.T) {
	t.Parallel()

	body := renderDashboard(t, newDashboardSpecServer(t))

	if !strings.Contains(body, "data-speed-trend") {
		t.Fatal("dashboard is missing the speed and block-rate table")
	}
	for _, want := range []string{">12.5<", ">3<", ">23.5%<", "Job outcomes by day", ">Blocks<", ">Block rate<"} {
		if !strings.Contains(body, want) {
			t.Fatalf("speed and outcome trends missing %q", want)
		}
	}
}

func TestDashboardRendersWebsiteActiveVersusInactive(t *testing.T) {
	t.Parallel()

	body := renderDashboard(t, newDashboardSpecServer(t))

	for _, want := range []string{
		"Website active versus inactive",
		`data-website-status="Website reachable"`,
		`data-website-status="Website unreachable"`,
		`data-website-status="Website never checked"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("website reachability split missing %q", want)
		}
	}
}

// Every quick action the specification names must be a real form or link on
// the recent-activity card, not a route an operator has to know about.
func TestDashboardRecentActivityOffersEveryQuickAction(t *testing.T) {
	t.Parallel()

	const jobID = "66666666-6666-6666-6666-666666666666"

	dir := t.TempDir()
	writeCSV(t, dir, jobID, "place_id,title\none,Alpha\n")

	repository := &fakeLifecycleRepository{
		job: Job{ID: jobID, Name: "Paused campaign", Date: time.Now().UTC(), Status: StatusPending},
	}
	repository.runtime = JobRuntime{JobID: jobID, State: "paused", TotalTasks: 4, Completed: 2}

	srv, err := New(NewService(repository, dir), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := renderDashboard(t, srv)

	for _, want := range []string{
		`href="/app/jobs/` + jobID + `"`,
		`data-action="resume-job"`,
		`data-action="cancel-job"`,
		`data-action="download-partial"`,
		`data-action="duplicate-job"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("recent activity quick actions missing %q", want)
		}
	}
}

func TestDashboardAvailabilityCountsMatchTheDiscoveredTotals(t *testing.T) {
	t.Parallel()

	srv := newDashboardSpecServer(t)
	page, _, err := srv.buildDashboard(httptest.NewRequest(http.MethodGet, "/app/dashboard", http.NoBody))
	if err != nil {
		t.Fatalf("buildDashboard: %v", err)
	}

	want := map[string]int{"Website": 3, "Email": 2, "Phone": 3, "Social profile": 1}
	if len(page.Availability) != len(want) {
		t.Fatalf("availability rows = %d, want %d", len(page.Availability), len(want))
	}
	for _, row := range page.Availability {
		expected, known := want[row.Label]
		if !known {
			t.Fatalf("unexpected availability row %q", row.Label)
		}
		if row.Count != expected {
			t.Fatalf("%s count = %d, want %d", row.Label, row.Count, expected)
		}
		if row.Total != page.Metrics.UniqueBusinesses {
			t.Fatalf("%s total = %d, want %d", row.Label, row.Total, page.Metrics.UniqueBusinesses)
		}
	}

	// Reachable plus unreachable plus never-checked must account for every
	// discovered website, or the split is inventing or losing evidence.
	total := 0
	for _, row := range page.WebsiteStatus {
		total += row.Count
	}
	if total != page.Metrics.Websites {
		t.Fatalf("website status rows total %d, want %d discovered websites", total, page.Metrics.Websites)
	}
}
