package web

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

type appPageData struct {
	Title       string
	Subtitle    string
	ActiveNav   string
	Theme       string
	Preferences appPreferences
	CSRFToken   string
	AuthEnabled bool
	Activity    appActivity
	Features    appFeatureFlags
	Flash       []appNotice
	Page        any
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
	Cities           []dashboardChartPoint
	Categories       []dashboardChartPoint
	Statuses         []dashboardChartPoint
	RatingBands      []dashboardChartPoint
	JobTrends        []DashboardJobTrend
	SpeedTrends      []DashboardSpeedTrend
	ProxyLatency     []dashboardChartPoint
	ProxyReliability []dashboardChartPoint
	RecentJobs       []dashboardJob
	Prospects        dashboardProspectSummary
}

// dashboardProspectSummary feeds the Prospecting card: stored GBP website
// statuses, worth-calling tiers, and the scored total. Supported stays false
// when the repository cannot serve prospect signals so the card is skipped
// silently instead of rendering dead controls.
type dashboardProspectSummary struct {
	Supported bool
	Scored    int
	ByStatus  []dashboardProspectPoint
	ByTier    []dashboardProspectPoint
}

// dashboardProspectPoint pairs one taxonomy label with its count and a
// CSS-safe badge state.
type dashboardProspectPoint struct {
	Label string
	State string
	Value int
}

type dashboardMetrics struct {
	UniqueBusinesses  int
	RawRecords        int
	Duplicates        int
	CollectedToday    int
	CollectedWeek     int
	CollectedMonth    int
	ActiveJobs        int
	QueuedJobs        int
	PausedJobs        int
	CompletedJobs     int
	PartialJobs       int
	FailedJobs        int
	CancelledJobs     int
	EmailCoverage     int
	WebsiteCoverage   int
	PhoneCoverage     int
	SocialCoverage    int
	Websites          int
	Emails            int
	Phones            int
	SocialProfiles    int
	ActiveWebsites    int
	InactiveWebsites  int
	PlacesPerMinute   string
	AverageDuration   string
	ProxySuccessRate  string
	ProxyBlockRate    string
	HealthyProxies    int
	TotalProxies      int
	ProxyLatency      string
	DatabaseSize      string
	DiskFree          string
	ExportStorage     string
	ScreenshotStorage string
	LogStorage        string
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
	RawRecords    int64
	UniqueRecords int
	Emails        int
	Runtime       string
	CanPause      bool
	CanResume     bool
	CanCancel     bool
	CanRetry      bool
	HasResults    bool
}

type newScrapePageData struct {
	CategoryGroups []namedAppOption
	SavedAreas     []namedAppOption
	ProxyPools     []proxyPoolOption
	FieldGroups    scrapeFieldGroups
	Defaults       scrapeDefaults
	LocalAI        localAISettings
	Initial        wizardInitialValues
	TemplateID     string
	// KeywordSets feeds the step-1 "insert a saved set" picker. The controls
	// only render when the repository can store sets (KeywordSetsSupported),
	// so a legacy database shows no dead buttons.
	KeywordSets          []KeywordSet
	KeywordSetsSupported bool
	// ProspectQueriesSupported gates the step-1 GBP prospecting coverage
	// generator so a repository without prospect support shows no dead form.
	ProspectQueriesSupported bool
	// DefaultProxyPoolID preselects the operator's default pool in step 6.
	DefaultProxyPoolID string
}

type wizardInitialValues struct {
	Name          string
	Keywords      string
	LocationLabel string
	Latitude      string
	Longitude     string
	GeographyMode string
	SavedAreaID   string
	SavedAreaName string
	AreaGeoJSON   string
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
	localAI := defaultLocalAISettings()
	if values, settingsErr := s.svc.LoadSettings(r.Context()); settingsErr == nil {
		localAI = localAISettingsFromMap(values)
	}
	initial := wizardInitialValues{
		Name:          "San Francisco dentists",
		Keywords:      "dentists in San Francisco\ndental clinics in San Francisco",
		LocationLabel: "San Francisco, California, United States",
		Latitude:      "37.7749",
		Longitude:     "-122.4194",
		GeographyMode: "bbox",
	}
	// Saved scrape defaults replace the built-in example location, while a
	// duplicated job, saved area, or template below still wins over both.
	if defaults.LocationLabel != "" {
		initial.LocationLabel = defaults.LocationLabel
	}
	if defaults.Lat != "" {
		initial.Latitude = defaults.Lat
	}
	if defaults.Lon != "" {
		initial.Longitude = defaults.Lon
	}
	duplicateJobID := strings.TrimSpace(r.URL.Query().Get("duplicate_job"))
	if duplicateJobID != "" {
		source, sourceErr := s.svc.Get(r.Context(), duplicateJobID)
		if sourceErr != nil {
			http.Error(w, "source job not found", http.StatusNotFound)
			return
		}
		initial.Name = "Copy of " + source.Name
		initial.Keywords = strings.Join(source.Data.Keywords, "\n")
		initial.LocationLabel = source.Data.LocationLabel
		initial.Latitude = source.Data.Lat
		initial.Longitude = source.Data.Lon
		initial.SavedAreaID = source.Data.SavedAreaID
		initial.AreaGeoJSON = source.Data.AreaGeoJSON
		if source.Data.FastMode {
			initial.GeographyMode = "circle"
		} else if source.Data.AreaGeoJSON != "" {
			if geometry, geometryErr := ParseMapGeometry([]byte(source.Data.AreaGeoJSON)); geometryErr == nil {
				initial.GeographyMode = geometry.Kind()
			}
		}
		defaults = scrapeDefaultsFromJobData(defaults, source.Data)
	}
	savedAreas := make([]namedAppOption, 0)
	if areas, err := s.svc.ListSavedAreas(r.Context(), maximumSavedAreaList); err == nil {
		for _, area := range areas {
			savedAreas = append(savedAreas, namedAppOption{ID: area.ID, Name: area.Name})
		}
	}
	areaID := strings.TrimSpace(r.URL.Query().Get("area_id"))
	if areaID != "" {
		area, err := s.svc.GetSavedArea(r.Context(), areaID)
		if err != nil {
			if errors.Is(err, ErrSavedAreaNotFound) || errors.Is(err, ErrInvalidMapGeometry) {
				http.Error(w, "saved area not found", http.StatusNotFound)
			} else {
				http.Error(w, "could not load saved area", http.StatusInternalServerError)
			}
			return
		}
		geometry, err := ParseMapGeometry(area.GeoJSON)
		if err != nil {
			http.Error(w, "saved area is invalid", http.StatusUnprocessableEntity)
			return
		}
		centre := geometry.Centre()
		initial.LocationLabel = area.Name
		initial.Latitude = strconv.FormatFloat(centre.Latitude, 'f', 7, 64)
		initial.Longitude = strconv.FormatFloat(centre.Longitude, 'f', 7, 64)
		initial.GeographyMode = geometry.Kind()
		initial.SavedAreaID = area.ID
		initial.SavedAreaName = area.Name
		initial.AreaGeoJSON = string(geometry.GeoJSON())
		if radius, ok := geometry.CircleRadiusMetres(); ok {
			defaults.Radius = max(100, int(math.Ceil(radius)))
		}
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
	// A stored default pool is only preselected while it is still offered;
	// a deleted or disabled pool silently falls back to a direct connection.
	defaultProxyPoolID := ""
	for _, pool := range proxyOptions {
		if pool.ID == defaults.ProxyPoolID {
			defaultProxyPoolID = pool.ID

			break
		}
	}
	// A repository without keyword-set support (ErrKeywordSetsUnsupported)
	// simply renders the wizard without the saved-set controls.
	keywordSetsSupported := s.svc.SupportsKeywordSets()
	keywordSets := make([]KeywordSet, 0)
	if sets, setsErr := s.svc.ListKeywordSets(r.Context()); setsErr == nil {
		keywordSets = sets
	}
	s.renderAppPage(w, "new_scrape", appPageData{
		Title:     "New scrape",
		Subtitle:  "Configure a complete, local business-research job in seven guided steps.",
		ActiveNav: "new-scrape",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page: newScrapePageData{Defaults: defaults, LocalAI: localAI, Initial: initial, TemplateID: templateID, ProxyPools: proxyOptions, DefaultProxyPoolID: defaultProxyPoolID, SavedAreas: savedAreas, KeywordSets: keywordSets, KeywordSetsSupported: keywordSetsSupported, ProspectQueriesSupported: s.svc.SupportsProspects(), FieldGroups: scrapeFieldGroups{Business: []scrapeFieldOption{
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
		defaults.TaskWorkers = data.TaskWorkers
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
		page.Metrics.Websites += stats.WithWebsite

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
			eta := "unknown"
			if state == "completed" {
				percent = 100
			}
			if execution, executionErr := s.svc.GetJobExecution(r.Context(), job.ID); executionErr == nil {
				if execution.Progress.Stage != jobruntime.StageNone {
					stage = humanStage(execution.Progress.Stage)
				}
				if execution.Progress.ETASeconds != nil {
					eta = humanDuration(time.Duration(*execution.Progress.ETASeconds) * time.Second)
				}
			}
			if eta == "unknown" && runtime.StartedAt != nil && percent > 0 && percent < 100 {
				elapsed := now.Sub(*runtime.StartedAt)
				if elapsed > 0 {
					eta = humanDuration(time.Duration(float64(elapsed) * float64(100-percent) / float64(percent)))
				}
			}

			canPause := state == "queued" || state == "starting" || state == "running"
			canResume := state == "paused"
			canCancel := lifecycleControlAllowed(runtime, jobruntime.ControlCancel)
			canRetry := lifecycleControlAllowed(runtime, jobruntime.ControlRestart) &&
				(state == "partial" || state == "failed" || state == "cancelled")

			page.RecentJobs = append(page.RecentJobs, dashboardJob{
				ID:            job.ID,
				Name:          job.Name,
				State:         state,
				Stage:         stage,
				Percent:       percent,
				ETA:           eta,
				RawRecords:    runtime.RawRecords,
				UniqueRecords: stats.UniqueBusinesses,
				Emails:        stats.WithEmail,
				Runtime:       runtimeLabel(runtime),
				CanPause:      canPause,
				CanResume:     canResume,
				CanCancel:     canCancel,
				CanRetry:      canRetry,
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
	page.Metrics.DiskFree = "not available"
	if overview, overviewErr := s.svc.ResultOverview(r.Context()); overviewErr == nil {
		page.Metrics.RawRecords = int(overview.RawRecords)
		page.Metrics.UniqueBusinesses = int(overview.UniqueBusinesses)
		page.Metrics.Duplicates = max(0, int(overview.RawRecords-overview.UniqueBusinesses))
		page.Metrics.Emails = int(overview.Emails)
		page.Metrics.Phones = int(overview.Phones)
		page.Metrics.Websites = int(overview.Websites)
	}
	if analytics, analyticsErr := s.svc.DashboardAnalytics(r.Context(), startMonth); analyticsErr == nil {
		page.Metrics.CollectedToday = int(analytics.CollectedToday)
		page.Metrics.CollectedWeek = int(analytics.CollectedWeek)
		page.Metrics.CollectedMonth = int(analytics.CollectedMonth)
		page.Metrics.Websites = int(analytics.Availability.Websites)
		page.Metrics.Emails = int(analytics.Availability.Emails)
		page.Metrics.Phones = int(analytics.Availability.Phones)
		page.Metrics.SocialProfiles = int(analytics.Availability.SocialProfiles)
		page.Metrics.ActiveWebsites = int(analytics.Availability.WebsiteActive)
		page.Metrics.InactiveWebsites = int(analytics.Availability.WebsiteInactive)
		page.CollectionByDate = dashboardPoints(analytics.CollectionByDate)
		page.Cities = dashboardPoints(analytics.Cities)
		page.Categories = dashboardPoints(analytics.Categories)
		page.Statuses = dashboardPoints(analytics.Statuses)
		page.RatingBands = dashboardPoints(analytics.RatingBands)
		page.JobTrends = analytics.JobTrends
		page.SpeedTrends = analytics.SpeedTrends
		page.ProxyLatency = dashboardPoints(analytics.ProxyLatencyBuckets)
		page.ProxyReliability = dashboardPoints(analytics.ProxyReliability)
		page.Metrics.TotalProxies = int(analytics.Proxy.Total)
		page.Metrics.HealthyProxies = int(analytics.Proxy.Healthy)
		page.Metrics.ProxySuccessRate = ratioLabel(analytics.Proxy.Successes, analytics.Proxy.Successes+analytics.Proxy.Failures)
		page.Metrics.ProxyBlockRate = ratioLabel(analytics.Proxy.Blocks, analytics.Proxy.Successes+analytics.Proxy.Failures)
		if analytics.Proxy.Total > 0 {
			page.Metrics.ProxyLatency = fmt.Sprintf("%.0f ms", analytics.Proxy.AverageLatencyMS)
		} else {
			page.Metrics.ProxyLatency = "not configured"
		}
	}

	// The Prospecting card degrades honestly: an unsupported repository skips
	// the card entirely and a summary error just leaves the empty state.
	if s.svc.SupportsProspects() {
		page.Prospects.Supported = true
		if summary, summaryErr := s.svc.ProspectSummaryData(r.Context()); summaryErr == nil {
			page.Prospects.Scored = int(summary.Scored)
			page.Prospects.ByStatus = dashboardProspectPoints(summary.ByStatus)
			page.Prospects.ByTier = dashboardProspectPoints(summary.ByTier)
		}
	}

	if page.Metrics.UniqueBusinesses > 0 {
		page.Metrics.EmailCoverage = percentage(page.Metrics.Emails, page.Metrics.UniqueBusinesses)
		page.Metrics.WebsiteCoverage = percentage(page.Metrics.Websites, page.Metrics.UniqueBusinesses)
		page.Metrics.PhoneCoverage = percentage(page.Metrics.Phones, page.Metrics.UniqueBusinesses)
		page.Metrics.SocialCoverage = percentage(page.Metrics.SocialProfiles, page.Metrics.UniqueBusinesses)
	}

	page.Availability = []dashboardAvailability{
		{Label: "Website", Percent: page.Metrics.WebsiteCoverage},
		{Label: "Email", Percent: percentage(page.Metrics.Emails, page.Metrics.UniqueBusinesses)},
		{Label: "Phone", Percent: percentage(page.Metrics.Phones, page.Metrics.UniqueBusinesses)},
		{Label: "Social profile", Percent: page.Metrics.SocialCoverage},
	}

	labels := make([]string, 0, len(byDate))
	for label := range byDate {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	if len(page.CollectionByDate) == 0 {
		for _, label := range labels {
			value := byDate[label]
			page.CollectionByDate = append(page.CollectionByDate, dashboardChartPoint{Label: label, Value: value})
		}
	}
	for _, point := range page.CollectionByDate {
		value := point.Value
		if value > page.CollectionMax {
			page.CollectionMax = value
		}
	}

	if snapshot, snapshotErr := s.svc.SystemDatabaseSnapshot(r.Context()); snapshotErr == nil {
		page.Metrics.DatabaseSize = humanBytes(snapshot.DatabaseBytes)
	}
	if page.Metrics.DatabaseSize == "" {
		page.Metrics.DatabaseSize = "not created"
	}
	if storage, storageErr := s.workspaceStorageUsage(r.Context()); storageErr == nil {
		page.Metrics.ExportStorage = humanBytes(storage.ExportsBytes)
		page.Metrics.ScreenshotStorage = humanBytes(storage.ScreenshotsBytes)
		page.Metrics.LogStorage = humanBytes(storage.LogsBytes)
	}
	metricsContext, cancel := context.WithTimeout(r.Context(), systemMetricsTimeout)
	defer cancel()
	if resources, resourceErr := s.systemProbe.Resources(metricsContext, s.svc.dataFolder); resourceErr == nil {
		page.Metrics.DiskFree = humanBytes(int64(resources.DiskFreeBytes))
	}

	return page, activity, nil
}

// dashboardProspectPoints labels each taxonomy count with a CSS-safe badge
// state for the Prospecting card.
func dashboardProspectPoints(points []DashboardCountPoint) []dashboardProspectPoint {
	converted := make([]dashboardProspectPoint, 0, len(points))
	for _, point := range points {
		converted = append(converted, dashboardProspectPoint{
			Label: point.Label,
			State: prospectStateClass(point.Label),
			Value: int(point.Value),
		})
	}

	return converted
}

func dashboardPoints(points []DashboardCountPoint) []dashboardChartPoint {
	converted := make([]dashboardChartPoint, 0, len(points))
	for _, point := range points {
		converted = append(converted, dashboardChartPoint{Label: point.Label, Value: int(point.Value)})
	}

	return converted
}

func ratioLabel(numerator, denominator int64) string {
	if denominator <= 0 {
		return "not recorded"
	}

	return fmt.Sprintf("%.1f%%", 100*float64(numerator)/float64(denominator))
}

func lifecycleControlAllowed(runtime JobRuntime, control jobruntime.Control) bool {
	decision, err := jobruntime.DecideControl(runtime.State, runtime.RequestedStop, control)

	return err == nil && decision.Disposition != jobruntime.ControlRejected && decision.Disposition != jobruntime.ControlNoop
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
