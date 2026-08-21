package web

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// benchmarkCompareLimit bounds how many candidate runs the compare control
// offers. The list is a convenience, not an archive: the JSON endpoint takes
// any two job IDs.
const benchmarkCompareLimit = 25

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
	// ComparePath and CompareOptions drive the compare affordance. The list
	// holds every other job the workspace can serve evidence for; it is empty
	// on a single-run workspace and the control is then not rendered at all.
	ComparePath    string
	CompareOptions []benchmarkCompareOption
}

// benchmarkCompareOption is one selectable candidate run for the
// GET /api/v1/benchmark/compare affordance.
type benchmarkCompareOption struct {
	ID    string
	Name  string
	When  string
	State string
}

type benchmarkMetricView struct {
	Label  string
	Value  string
	Detail string
	// Empty marks a headline whose evidence was never recorded. The template
	// renders it through the design system's muted empty-state treatment
	// instead of shouting a placeholder sentence at headline weight.
	Empty bool
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
	// RatioPercent is the same ratio as a 0-100 integer so the template can
	// size the reinforcing meter without doing arithmetic.
	RatioPercent int
	// RowShare sizes the rows-added meter relative to the best row in the
	// same table, which is what makes a long yield table scannable.
	RowShare int
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
	// Share is the count as a percentage of the distribution total, and
	// Percent sizes the row's meter against the largest row.
	Share   string
	Percent int
	Tone    string
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

	page := buildBenchmarkPageData(report)
	page.CompareOptions = s.benchmarkCompareOptions(r, report.JobID)

	activity, _ := s.appActivity(r)
	s.renderAppPage(w, "benchmark", appPageData{
		Title:     report.JobName + " · Benchmark",
		Subtitle:  "Durable acceptance evidence for this run: yield, saturation, failures, proxies, and business outcomes.",
		ActiveNav: "jobs",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page:      page,
	})
}

// benchmarkCompareOptions lists the other runs this workspace can compare
// against. A listing failure is not an error for the report itself, so the
// affordance simply disappears rather than failing the page.
func (s *Server) benchmarkCompareOptions(r *http.Request, currentID string) []benchmarkCompareOption {
	jobs, err := s.svc.All(r.Context())
	if err != nil {
		return nil
	}

	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].Date.After(jobs[j].Date) })

	options := make([]benchmarkCompareOption, 0, benchmarkCompareLimit)

	for _, job := range jobs {
		if job.ID == currentID || job.ID == "" {
			continue
		}

		options = append(options, benchmarkCompareOption{
			ID:    job.ID,
			Name:  job.Name,
			When:  formatDate(job.Date),
			State: job.Status,
		})

		if len(options) == benchmarkCompareLimit {
			break
		}
	}

	return options
}

func buildBenchmarkPageData(report BenchmarkReport) benchmarkPageData {
	page := benchmarkPageData{
		JobID:         report.JobID,
		JobName:       report.JobName,
		EngineVersion: report.EngineVersion,
		SchemaVersion: report.SchemaVersion,
		GeneratedAt:   formatDate(report.GeneratedAt),
		APIPath:       "/api/v1/jobs/" + report.JobID + "/benchmark",
		ComparePath:   "/api/v1/benchmark/compare",
	}

	// The evidence strip leads with the five numbers an operator judges a run
	// by. Wall time is the one that is routinely missing on a run the durable
	// runtime never timed, so it carries the empty-state flag rather than the
	// words "not recorded" at headline weight.
	wall := benchmarkWall(report.Runtime.WallSeconds)
	page.Metrics = []benchmarkMetricView{
		{Label: "Unique businesses", Value: fmt.Sprintf("%d", report.Totals.UniqueBusinesses),
			Detail: fmt.Sprintf("%d discovered rows", report.Totals.TotalDiscoveredRows)},
		{Label: "New businesses/minute", Value: fmt.Sprintf("%.2f", report.Totals.NewBusinessesPerMinute),
			Detail: "unique over active runtime"},
		{Label: "Duplicate rate", Value: benchmarkPercent(report.Totals.DuplicateRate),
			Detail: fmt.Sprintf("%d duplicates skipped", report.Totals.DuplicatesSkipped)},
		{Label: "Wall time", Value: benchmarkOrPlaceholder(wall), Empty: wall == "",
			Detail: fmt.Sprintf("%.2f tasks per minute", report.Runtime.TasksPerMinute)},
		{Label: "Failures / retries", Value: fmt.Sprintf("%d / %d", report.Totals.TasksFailed, report.Totals.Retries),
			Detail: fmt.Sprintf("%d skipped", report.Totals.TasksSkipped)},
		{Label: "Tasks completed", Value: fmt.Sprintf("%d", report.Totals.TasksCompleted),
			Detail: fmt.Sprintf("of %d planned + %d expanded", report.Totals.TasksPlanned, report.Totals.TasksExpanded)},
	}

	page.TaskRows = []benchmarkKeyValueView{
		{Label: "Planned tasks", Value: fmt.Sprintf("%d", report.Totals.TasksPlanned)},
		{Label: "Expansion tasks", Value: fmt.Sprintf("%d", report.Totals.TasksExpanded)},
		{Label: "Completed", Value: fmt.Sprintf("%d", report.Totals.TasksCompleted)},
		{Label: "Failed", Value: fmt.Sprintf("%d", report.Totals.TasksFailed)},
		{Label: "Skipped", Value: fmt.Sprintf("%d", report.Totals.TasksSkipped)},
		{Label: "Attempts", Value: fmt.Sprintf("%d", report.Totals.Attempts)},
		{Label: "Retries", Value: fmt.Sprintf("%d", report.Totals.Retries)},
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

	page.WebsiteStatus = benchmarkCountViews(report.WebsiteStatusDistribution, "")
	page.ProspectTiers = benchmarkCountViews(report.ProspectTierDistribution, "success")
	page.ProspectStatus = benchmarkCountViews(report.ProspectStatusDistribution, "special")
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
	var peak int64
	for _, row := range rows {
		if row.RowsAdded > peak {
			peak = row.RowsAdded
		}
	}

	views := make([]benchmarkYieldView, 0, len(rows))

	for _, row := range rows {
		views = append(views, benchmarkYieldView{
			Key:               row.Key,
			Tasks:             row.Tasks,
			RowsAdded:         row.RowsAdded,
			DuplicatesSkipped: row.DuplicatesSkipped,
			UniqueRatio:       benchmarkPercent(row.UniqueRatio),
			RatioPercent:      benchmarkMeter(row.UniqueRatio*benchmarkPercentScale, benchmarkPercentScale),
			RowShare:          benchmarkMeter(float64(row.RowsAdded), float64(peak)),
		})
	}

	return views
}

// benchmarkCountViews turns a distribution into meter-backed rows. Each row is
// sized against the largest row so the shape of the distribution is readable
// at a glance, while the printed count stays the actual value.
func benchmarkCountViews(rows []BenchmarkDistributionRow, tone string) []benchmarkCountView {
	var peak, total int64

	for _, row := range rows {
		total += row.Count

		if row.Count > peak {
			peak = row.Count
		}
	}

	views := make([]benchmarkCountView, 0, len(rows))

	for _, row := range rows {
		share := ""
		if total > 0 {
			share = benchmarkPercent(float64(row.Count) / float64(total))
		}

		views = append(views, benchmarkCountView{
			Label:   row.Label,
			Count:   row.Count,
			Share:   share,
			Percent: benchmarkMeter(float64(row.Count), float64(peak)),
			Tone:    tone,
		})
	}

	return views
}

// benchmarkPercentScale converts a 0-1 ratio into the 0-100 meter domain.
const benchmarkPercentScale = 100

// benchmarkMeter clamps value/limit into an integer percentage a template can
// hand straight to a CSS custom property.
func benchmarkMeter(value, limit float64) int {
	if limit <= 0 || value <= 0 {
		return 0
	}

	percent := int((value / limit) * benchmarkPercentScale)
	if percent > benchmarkPercentScale {
		return benchmarkPercentScale
	}

	return percent
}

// benchmarkOrPlaceholder keeps the printed placeholder in exactly one place;
// callers pair it with an Empty flag so the view can style the absence.
func benchmarkOrPlaceholder(value string) string {
	if value == "" {
		return "Not recorded"
	}

	return value
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

// benchmarkTimestamp and benchmarkWall return an empty string for evidence the
// run never recorded. The template turns that into the design system's muted
// inline empty state, so an operator reads an absence instead of four
// identical "not recorded" sentences sitting in value position.
func benchmarkTimestamp(unix int64) string {
	if unix <= 0 {
		return ""
	}

	return formatDate(time.Unix(unix, 0).UTC())
}

func benchmarkWall(seconds float64) string {
	if seconds <= 0 {
		return ""
	}

	return humanDuration(time.Duration(seconds * float64(time.Second)))
}
