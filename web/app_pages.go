package web

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

type appPageData struct {
	Title     string
	Subtitle  string
	ActiveNav string
	Theme     string
	CSRFToken string
	Activity  appActivity
	Features  appFeatureFlags
	Flash     []appNotice
	Page      any
}

// appFeatureFlags keeps navigation and shortcuts honest while workspace
// modules are introduced incrementally. A feature is only exposed after its
// page and every visible mutation on that page have a registered backend.
type appFeatureFlags struct {
	SavedSearches bool
	Schedules     bool
	Proxies       bool
	Exports       bool
	API           bool
	System        bool
	Settings      bool
	Onboarding    bool
}

type appActivity struct {
	Running int
	Queued  int
}

type appNotice struct {
	Level   string
	Title   string
	Message string
}

type dashboardPageData struct {
	Metrics          dashboardMetrics
	CollectionByDate []dashboardChartPoint
	CollectionMax    int
	Availability     []dashboardAvailability
	RecentJobs       []dashboardJob
}

type dashboardMetrics struct {
	UniqueBusinesses int
	RawRecords       int
	Duplicates       int
	CollectedToday   int
	CollectedWeek    int
	CollectedMonth   int
	ActiveJobs       int
	QueuedJobs       int
	PausedJobs       int
	CompletedJobs    int
	PartialJobs      int
	FailedJobs       int
	CancelledJobs    int
	EmailCoverage    int
	Emails           int
	Phones           int
	PlacesPerMinute  string
	AverageDuration  string
	DatabaseSize     string
	DiskFree         string
}

type dashboardChartPoint struct {
	Label string
	Value int
}

type dashboardAvailability struct {
	Label   string
	Percent int
}

type dashboardJob struct {
	ID            string
	Name          string
	State         string
	Stage         string
	Percent       int
	ETA           string
	UniqueRecords int
	Emails        int
	Runtime       string
	CanPause      bool
	CanResume     bool
	HasResults    bool
}

type newScrapePageData struct {
	CategoryGroups []namedAppOption
	SavedAreas     []namedAppOption
	ProxyPools     []proxyPoolOption
	FieldGroups    scrapeFieldGroups
	Defaults       scrapeDefaults
	Initial        wizardInitialValues
	TemplateID     string
}

type wizardInitialValues struct {
	Name          string
	Keywords      string
	LocationLabel string
	Latitude      string
	Longitude     string
}

type namedAppOption struct {
	ID   string
	Name string
}

type proxyPoolOption struct {
	ID           string
	Name         string
	HealthyCount int
}

type scrapeFieldGroups struct {
	Business []scrapeFieldOption
}

type scrapeFieldOption struct {
	Key         string
	Label       string
	Description string
	Selected    bool
}

func (s *Server) dashboardPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	page, activity, err := s.buildDashboard(r)
	if err != nil {
		http.Error(w, "could not load dashboard", http.StatusInternalServerError)

		return
	}

	s.renderAppPage(w, "dashboard", appPageData{
		Title:     "Dashboard",
		Subtitle:  "Jobs, local results, data quality, and system activity at a glance.",
		ActiveNav: "dashboard",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page:      page,
	})
}

func (s *Server) newScrapePage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	activity, _ := s.appActivity(r)
	defaults := s.loadScrapeDefaults(r)
	initial := wizardInitialValues{
		Name:          "San Francisco dentists",
		Keywords:      "dentists in San Francisco\ndental clinics in San Francisco",
		LocationLabel: "San Francisco, California, United States",
		Latitude:      "37.7749",
		Longitude:     "-122.4194",
	}
	templateID := strings.TrimSpace(r.URL.Query().Get("template"))
	if templateID != "" {
		template, err := s.svc.GetScrapeTemplate(r.Context(), templateID)
		if err != nil {
			if errors.Is(err, ErrReusableNotFound) {
				http.Error(w, "scrape template not found", http.StatusNotFound)
			} else {
				http.Error(w, "could not load scrape template", http.StatusInternalServerError)
			}
			return
		}
		initial.Name = template.Name
		initial.Keywords = strings.Join(template.Configuration.Keywords, "\n")
		initial.LocationLabel = template.Configuration.LocationLabel
		if initial.LocationLabel == "" {
			initial.LocationLabel = "Saved template location"
		}
		initial.Latitude = template.Configuration.Lat
		initial.Longitude = template.Configuration.Lon
		defaults = scrapeDefaultsFromJobData(defaults, template.Configuration)
	}
	proxyOptions := make([]proxyPoolOption, 0)
	if pools, err := s.svc.ListProxyPools(r.Context()); err == nil {
		for _, pool := range pools {
			if pool.EnabledCount > 0 {
				proxyOptions = append(proxyOptions, proxyPoolOption{
					ID: pool.ID, Name: pool.Name, HealthyCount: int(pool.HealthyCount),
				})
			}
		}
	}
	s.renderAppPage(w, "new_scrape", appPageData{
		Title:     "New scrape",
		Subtitle:  "Configure a complete, local business-research job in seven guided steps.",
		ActiveNav: "new-scrape",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page: newScrapePageData{Defaults: defaults, Initial: initial, TemplateID: templateID, ProxyPools: proxyOptions, FieldGroups: scrapeFieldGroups{Business: []scrapeFieldOption{
			{Key: "title", Label: "Name", Description: "Business title as shown on Maps.", Selected: true},
			{Key: "category", Label: "Categories", Description: "Primary and additional categories.", Selected: true},
			{Key: "status", Label: "Business status", Description: "Open or closed signal where available.", Selected: true},
		}}},
	})
}

func scrapeDefaultsFromJobData(defaults scrapeDefaults, data JobData) scrapeDefaults {
	if data.Lang != "" {
		defaults.Language = data.Lang
	}
	if data.Zoom > 0 {
		defaults.Zoom = data.Zoom
	}
	if data.Depth > 0 {
		defaults.Depth = data.Depth
	}
	if data.MaxTime > 0 {
		defaults.MaxTime = data.MaxTime.String()
	}
	if data.Concurrency > 0 {
		defaults.Concurrency = data.Concurrency
	}
	if data.BrowserPool > 0 {
		defaults.BrowserPool = data.BrowserPool
	}
	if data.PagesBrowser > 0 {
		defaults.PagesBrowser = data.PagesBrowser
	}
	if data.Radius > 0 {
		defaults.Radius = data.Radius
	}
	if data.GridCellKM > 0 {
		defaults.GridCellKM = fmt.Sprintf("%.3g", data.GridCellKM)
	}
	defaults.Email = data.Email
	defaults.ExtraReviews = data.ExtraReviews
	return defaults
}

func (s *Server) buildDashboard(r *http.Request) (dashboardPageData, appActivity, error) {
	if s.svc == nil || s.svc.repo == nil {
		return dashboardPageData{}, appActivity{}, nil
	}

	jobs, err := s.svc.All(r.Context())
	if err != nil {
		return dashboardPageData{}, appActivity{}, err
	}

	now := time.Now().UTC()
	startToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	startWeek := startToday.AddDate(0, 0, -6)
	startMonth := startToday.AddDate(0, 0, -29)

	page := dashboardPageData{}
	activity := appActivity{}
	byDate := make(map[string]int)
	var totalRuntime time.Duration
	var runtimeJobs int
	var rateRecords int64

	for index, job := range jobs {
		runtime, runtimeErr := s.svc.GetRuntime(r.Context(), job.ID)
		if runtimeErr != nil {
			return dashboardPageData{}, appActivity{}, runtimeErr
		}

		state := string(runtime.State)
		switch state {
		case "queued":
			activity.Queued++
		case "running":
			activity.Running++
		case "paused":
			page.Metrics.PausedJobs++
		case "completed":
			page.Metrics.CompletedJobs++
		case "partial":
			page.Metrics.PartialJobs++
		case "failed":
			page.Metrics.FailedJobs++
		case "cancelled":
			page.Metrics.CancelledJobs++
		}
		if runtime.StartedAt != nil {
			end := now
			if runtime.FinishedAt != nil {
				end = *runtime.FinishedAt
			}
			if elapsed := end.Sub(*runtime.StartedAt); elapsed > 0 {
				totalRuntime += elapsed
				runtimeJobs++
				rateRecords += runtime.RawRecords
			}
		}

		stats, statsErr := s.svc.GetResultStats(r.Context(), job.ID)
		if statsErr != nil && !errors.Is(statsErr, ErrPlacesNotFound) {
			return dashboardPageData{}, appActivity{}, statsErr
		}

		page.Metrics.RawRecords += stats.Rows
		page.Metrics.UniqueBusinesses += stats.UniqueBusinesses
		page.Metrics.Duplicates += stats.Duplicates
		page.Metrics.Emails += stats.WithEmail
		page.Metrics.Phones += stats.WithPhone

		if !job.Date.Before(startMonth) {
			page.Metrics.CollectedMonth += stats.UniqueBusinesses
			byDate[job.Date.Format("2006-01-02")] += stats.UniqueBusinesses
		}

		if !job.Date.Before(startWeek) {
			page.Metrics.CollectedWeek += stats.UniqueBusinesses
		}

		if !job.Date.Before(startToday) {
			page.Metrics.CollectedToday += stats.UniqueBusinesses
		}

		if index < 10 {
			percent := int(runtime.Progress + 0.5)
			stage := humanStage(runtime.Stage)
			if state == "completed" {
				percent = 100
			}

			canPause := state == "queued" || state == "starting" || state == "running"
			canResume := state == "paused"

			page.RecentJobs = append(page.RecentJobs, dashboardJob{
				ID:            job.ID,
				Name:          job.Name,
				State:         state,
				Stage:         stage,
				Percent:       percent,
				ETA:           "unknown",
				UniqueRecords: stats.UniqueBusinesses,
				Emails:        stats.WithEmail,
				Runtime:       runtimeLabel(runtime),
				CanPause:      canPause,
				CanResume:     canResume,
				HasResults:    stats.Rows > 0,
			})
		}
	}

	page.Metrics.ActiveJobs = activity.Running
	page.Metrics.QueuedJobs = activity.Queued
	page.Metrics.PlacesPerMinute = "not recorded"
	page.Metrics.AverageDuration = "not recorded"
	if totalRuntime > 0 {
		page.Metrics.PlacesPerMinute = fmt.Sprintf("%.1f", float64(rateRecords)/totalRuntime.Minutes())
	}
	if runtimeJobs > 0 {
		page.Metrics.AverageDuration = humanDuration(totalRuntime / time.Duration(runtimeJobs))
	}
	page.Metrics.DiskFree = "see System"
	if overview, overviewErr := s.svc.ResultOverview(r.Context()); overviewErr == nil {
		page.Metrics.RawRecords = int(overview.RawRecords)
		page.Metrics.UniqueBusinesses = int(overview.UniqueBusinesses)
		page.Metrics.Duplicates = max(0, int(overview.RawRecords-overview.UniqueBusinesses))
		page.Metrics.Emails = int(overview.Emails)
		page.Metrics.Phones = int(overview.Phones)
	}

	if page.Metrics.UniqueBusinesses > 0 {
		page.Metrics.EmailCoverage = percentage(page.Metrics.Emails, page.Metrics.UniqueBusinesses)
	}

	page.Availability = []dashboardAvailability{
		{Label: "Email", Percent: percentage(page.Metrics.Emails, page.Metrics.UniqueBusinesses)},
		{Label: "Phone", Percent: percentage(page.Metrics.Phones, page.Metrics.UniqueBusinesses)},
	}

	labels := make([]string, 0, len(byDate))
	for label := range byDate {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	for _, label := range labels {
		value := byDate[label]
		page.CollectionByDate = append(page.CollectionByDate, dashboardChartPoint{Label: label, Value: value})
		if value > page.CollectionMax {
			page.CollectionMax = value
		}
	}

	databasePath := filepath.Join(s.svc.dataFolder, "jobs.db")
	if info, statErr := os.Stat(databasePath); statErr == nil {
		page.Metrics.DatabaseSize = humanBytes(info.Size())
	} else {
		page.Metrics.DatabaseSize = "not created"
	}

	return page, activity, nil
}

func (s *Server) appActivity(r *http.Request) (appActivity, error) {
	if s.svc == nil || s.svc.repo == nil {
		return appActivity{}, nil
	}

	jobs, err := s.svc.All(r.Context())
	if err != nil {
		return appActivity{}, err
	}

	activity := appActivity{}
	for _, job := range jobs {
		runtime, runtimeErr := s.svc.GetRuntime(r.Context(), job.ID)
		if runtimeErr != nil {
			return appActivity{}, runtimeErr
		}

		switch runtime.State {
		case jobruntime.StateQueued:
			activity.Queued++
		case jobruntime.StateStarting, jobruntime.StateRunning, jobruntime.StateCancelling:
			activity.Running++
		}
	}

	return activity, nil
}

func humanStage(stage jobruntime.Stage) string {
	if stage == jobruntime.StageNone {
		return "Waiting to start"
	}

	words := strings.Fields(strings.ReplaceAll(string(stage), "_", " "))
	for index := range words {
		words[index] = strings.ToUpper(words[index][:1]) + words[index][1:]
	}

	return strings.Join(words, " ")
}

func runtimeLabel(runtime JobRuntime) string {
	if runtime.StartedAt == nil {
		return "not started"
	}

	end := time.Now().UTC()
	if runtime.FinishedAt != nil {
		end = *runtime.FinishedAt
	}

	duration := end.Sub(*runtime.StartedAt)
	if duration < 0 {
		return "unknown"
	}

	return duration.Round(time.Second).String()
}

func percentage(value, total int) int {
	if value <= 0 || total <= 0 {
		return 0
	}

	return min(100, int(float64(value)/float64(total)*100+0.5))
}

func humanBytes(value int64) string {
	const unit = int64(1024)
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}

	divisor := unit
	exponent := 0
	for amount := value / unit; amount >= unit && exponent < 4; amount /= unit {
		divisor *= unit
		exponent++
	}

	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(divisor), "KMGTPE"[exponent])
}
