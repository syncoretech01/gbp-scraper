package web

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// benchmarkPageData is the pre-formatted view model for the server-rendered
// benchmark page. All numbers arrive as display strings so the template stays
// free of logic beyond ranges and empty states.
type benchmarkPageData struct {
	JobID          string
	JobName        string
	EngineVersion  string
	SchemaVersion  int
	GeneratedAt    string
	APIPath        string
	Metrics        []benchmarkMetricView
	TaskRows       []benchmarkKeyValueView
	YieldByQuery   []benchmarkYieldView
	YieldByZip     []benchmarkYieldView
	YieldBySynonym []benchmarkYieldView
	Saturation     []benchmarkSaturationView
	Failures       []benchmarkFailureView
	Proxies        []benchmarkProxyView
	WebsiteStatus  []benchmarkCountView
	ProspectTiers  []benchmarkCountView
	ProspectStatus []benchmarkCountView
	Email          benchmarkEmailView
	RuntimeRows    []benchmarkKeyValueView
}

type benchmarkMetricView struct {
	Label  string
	Value  string
	Detail string
}

type benchmarkKeyValueView struct {
	Label string
	Value string
}

type benchmarkYieldView struct {
	Key               string
	Tasks             int64
	RowsAdded         int64
	DuplicatesSkipped int64
	UniqueRatio       string
}

type benchmarkSaturationView struct {
	Seq                int
	TaskKey            string
	RowsAdded          int64
	DuplicatesSkipped  int64
	CumulativeNewRatio string
}

type benchmarkFailureView struct {
	Class   string
	Count   int64
	Retries int64
	Sample  string
}

type benchmarkProxyView struct {
	Name        string
	Pool        string
	Successes   int64
	Failures    int64
	Consecutive int64
	AverageTask string
	LastError   string
}

type benchmarkCountView struct {
	Label string
	Count int64
}

type benchmarkEmailView struct {
	WithEmail string
	WithPhone string
	WithBoth  string
	Total     int64
}

// jobBenchmarkPage renders the read-only production benchmark report for one
// job using the shared application layout.
func (s *Server) jobBenchmarkPage(w http.ResponseWriter, r *http.Request) {
	id, ok := getIDFromRequest(r)
	if !ok {
		http.Error(w, "invalid job ID", http.StatusUnprocessableEntity)

		return
	}
	if job, err := s.svc.Get(r.Context(), id.String()); err != nil || job.ID == "" {
		http.Error(w, "job not found", http.StatusNotFound)

		return
	}

	report, err := s.svc.JobBenchmark(r.Context(), id.String())
	if err != nil {
		if errors.Is(err, ErrBenchmarkUnsupported) {
			http.Error(w, "benchmark evidence is unavailable", http.StatusNotImplemented)

			return
		}
		if errors.Is(err, ErrLifecycleNotFound) || errors.Is(err, ErrNotFound) {
			http.Error(w, "job not found", http.StatusNotFound)

			return
		}
		http.Error(w, "could not assemble the benchmark report", http.StatusInternalServerError)

		return
	}

	activity, _ := s.appActivity(r)
	s.renderAppPage(w, "benchmark", appPageData{
		Title:     report.JobName + " · Benchmark",
		Subtitle:  "Durable acceptance evidence for this run: yield, saturation, failures, proxies, and business outcomes.",
		ActiveNav: "jobs",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page:      buildBenchmarkPageData(report),
	})
}

func buildBenchmarkPageData(report BenchmarkReport) benchmarkPageData {
	page := benchmarkPageData{
		JobID:         report.JobID,
		JobName:       report.JobName,
		EngineVersion: report.EngineVersion,
		SchemaVersion: report.SchemaVersion,
		GeneratedAt:   formatDate(report.GeneratedAt),
		APIPath:       "/api/v1/jobs/" + report.JobID + "/benchmark",
	}

	page.Metrics = []benchmarkMetricView{
		{Label: "Unique businesses", Value: fmt.Sprintf("%d", report.Totals.UniqueBusinesses),
			Detail: fmt.Sprintf("%d discovered rows", report.Totals.TotalDiscoveredRows)},
		{Label: "New businesses/minute", Value: fmt.Sprintf("%.2f", report.Totals.NewBusinessesPerMinute),
			Detail: "unique over active runtime"},
		{Label: "Duplicate rate", Value: benchmarkPercent(report.Totals.DuplicateRate),
			Detail: fmt.Sprintf("%d duplicates skipped", report.Totals.DuplicatesSkipped)},
		{Label: "Rows added", Value: fmt.Sprintf("%d", report.Totals.RowsAdded),
			Detail: fmt.Sprintf("%d replaced", report.Totals.RowsReplaced)},
		{Label: "Tasks completed", Value: fmt.Sprintf("%d", report.Totals.TasksCompleted),
			Detail: fmt.Sprintf("of %d planned + %d expanded", report.Totals.TasksPlanned, report.Totals.TasksExpanded)},
		{Label: "Failures / retries", Value: fmt.Sprintf("%d / %d", report.Totals.TasksFailed, report.Totals.Retries),
			Detail: fmt.Sprintf("%d skipped", report.Totals.TasksSkipped)},
	}

	page.TaskRows = []benchmarkKeyValueView{
		{Label: "Planned tasks", Value: fmt.Sprintf("%d", report.Totals.TasksPlanned)},
		{Label: "Expansion tasks", Value: fmt.Sprintf("%d", report.Totals.TasksExpanded)},
		{Label: "Completed", Value: fmt.Sprintf("%d", report.Totals.TasksCompleted)},
		{Label: "Failed", Value: fmt.Sprintf("%d", report.Totals.TasksFailed)},
		{Label: "Skipped", Value: fmt.Sprintf("%d", report.Totals.TasksSkipped)},
		{Label: "Attempts", Value: fmt.Sprintf("%d", report.Totals.Attempts)},
		{Label: "Retries (attempts beyond first)", Value: fmt.Sprintf("%d", report.Totals.Retries)},
	}

	page.YieldByQuery = benchmarkYieldViews(report.YieldByQuery)
	page.YieldByZip = benchmarkYieldViews(report.YieldByZip)
	page.YieldBySynonym = benchmarkYieldViews(report.YieldBySynonym)

	for _, point := range report.SaturationTrend {
		page.Saturation = append(page.Saturation, benchmarkSaturationView{
			Seq:                point.Seq,
			TaskKey:            point.TaskKey,
			RowsAdded:          point.RowsAdded,
			DuplicatesSkipped:  point.DuplicatesSkipped,
			CumulativeNewRatio: benchmarkPercent(point.CumulativeNewRatio),
		})
	}
	for _, failure := range report.Failures {
		page.Failures = append(page.Failures, benchmarkFailureView(failure))
	}
	for _, proxy := range report.ProxyPerformance {
		name := proxy.ProxyName
		if name == "" {
			name = proxy.ProxyID
		}
		page.Proxies = append(page.Proxies, benchmarkProxyView{
			Name:        name,
			Pool:        proxy.PoolID,
			Successes:   proxy.TaskSuccesses,
			Failures:    proxy.TaskFailures,
			Consecutive: proxy.ConsecutiveFailures,
			AverageTask: fmt.Sprintf("%.2fs", proxy.AverageTaskSeconds),
			LastError:   proxy.LastError,
		})
	}

	page.WebsiteStatus = benchmarkCountViews(report.WebsiteStatusDistribution)
	page.ProspectTiers = benchmarkCountViews(report.ProspectTierDistribution)
	page.ProspectStatus = benchmarkCountViews(report.ProspectStatusDistribution)
	page.Email = benchmarkEmailView{
		WithEmail: benchmarkShare(report.EmailAvailability.WithEmail, report.EmailAvailability.Total),
		WithPhone: benchmarkShare(report.EmailAvailability.WithPhone, report.EmailAvailability.Total),
		WithBoth:  benchmarkShare(report.EmailAvailability.WithBoth, report.EmailAvailability.Total),
		Total:     report.EmailAvailability.Total,
	}

	page.RuntimeRows = []benchmarkKeyValueView{
		{Label: "Created", Value: benchmarkTimestamp(report.Runtime.CreatedAt)},
		{Label: "Started", Value: benchmarkTimestamp(report.Runtime.StartedAt)},
		{Label: "Finished", Value: benchmarkTimestamp(report.Runtime.FinishedAt)},
		{Label: "Active runtime", Value: benchmarkWall(report.Runtime.WallSeconds)},
		{Label: "Tasks/minute", Value: fmt.Sprintf("%.2f", report.Runtime.TasksPerMinute)},
		{Label: "Raw records", Value: fmt.Sprintf("%d", report.Runtime.RawRecords)},
		{Label: "Unique records", Value: fmt.Sprintf("%d", report.Runtime.UniqueRecords)},
		{Label: "Duplicate records", Value: fmt.Sprintf("%d", report.Runtime.DuplicateRecords)},
	}

	return page
}

func benchmarkYieldViews(rows []BenchmarkYieldRow) []benchmarkYieldView {
	views := make([]benchmarkYieldView, 0, len(rows))
	for _, row := range rows {
		views = append(views, benchmarkYieldView{
			Key:               row.Key,
			Tasks:             row.Tasks,
			RowsAdded:         row.RowsAdded,
			DuplicatesSkipped: row.DuplicatesSkipped,
			UniqueRatio:       benchmarkPercent(row.UniqueRatio),
		})
	}

	return views
}

func benchmarkCountViews(rows []BenchmarkDistributionRow) []benchmarkCountView {
	views := make([]benchmarkCountView, 0, len(rows))
	for _, row := range rows {
		views = append(views, benchmarkCountView(row))
	}

	return views
}

func benchmarkPercent(ratio float64) string {
	return fmt.Sprintf("%.1f%%", ratio*100) //nolint:mnd // ratio to percent
}

func benchmarkShare(part, whole int64) string {
	if whole <= 0 {
		return fmt.Sprintf("%d", part)
	}

	return fmt.Sprintf("%d (%s)", part, benchmarkPercent(float64(part)/float64(whole)))
}

func benchmarkTimestamp(unix int64) string {
	if unix <= 0 {
		return "not recorded"
	}

	return formatDate(time.Unix(unix, 0).UTC())
}

func benchmarkWall(seconds float64) string {
	if seconds <= 0 {
		return "not recorded"
	}

	return humanDuration(time.Duration(seconds * float64(time.Second)))
}
