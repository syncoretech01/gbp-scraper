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
	Metrics dashboardMetrics
	// Attention is the ordered next-best-action list. Every entry is derived
	// from a number this handler actually loaded and links to a route the
	// server already serves, so the region can never dead-end.
	Attention []dashboardAttentionItem
	// RunningJobs holds only the jobs an operator can still act on right now
	// (queued, starting, running, cancelling, paused). Campaigns holds the
	// terminal runs, summarised by what they yielded rather than by progress.
	RunningJobs      []dashboardJob
	Campaigns        []dashboardJob
	CollectionByDate []dashboardChartPoint
	CollectionMax    int
	Availability     []dashboardAvailability
	// WebsiteStatus splits the discovered websites into reachable, unreachable,
	// and never-checked. It is deliberately separate from Availability: one
	// answers "does the business have a website", the other "did ours load".
	WebsiteStatus []dashboardAvailability
	Cities        []dashboardChartPoint
	Categories       []dashboardChartPoint
	Statuses         []dashboardChartPoint
	RatingBands      []dashboardChartPoint
	JobTrends        []DashboardJobTrend
	SpeedTrends      []DashboardSpeedTrend
	ProxyLatency     []dashboardChartPoint
	ProxyReliability []dashboardChartPoint
	Yield            dashboardYield
	Prospects        dashboardProspectSummary
}

// dashboardAttentionItem is one queued piece of operator work. Tone selects a
// semantic state token in the stylesheet; Value is the count that justifies
// the row, and an item with a zero Value is never rendered because an empty
// backlog is not an action.
type dashboardAttentionItem struct {
	Tone   string
	Label  string
	Value  int
	Detail string
	Action string
	URL    string
}

// dashboardYield summarises collection efficiency across the same recent jobs
// the activity table already lists. It is composed entirely from the per-job
// runtime and result stats the dashboard has already loaded, so it costs no
// extra repository call and never invents a number.
type dashboardYield struct {
	// Jobs is the number of recent jobs that contributed at least one raw
	// record; zero means the card renders its empty state.
	Jobs             int
	RawRecords       int64
	UniqueRecords    int64
	Duplicates       int64
	Emails           int64
	UniqueYield      string
	DuplicateShare   string
	EmailShare       string
	UniquePercent    int
	DuplicatePercent int
	EmailPercent     int
	// BestJob/WorstJob name the recent jobs with the highest and lowest
	// unique-per-raw share so an operator can see where coverage is thin.
	BestJobID     string
	BestJobName   string
	BestJobYield  string
	WorstJobID    string
	WorstJobName  string
	WorstJobYield string
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
// CSS-safe badge state. Percent is the share of scored businesses, used to
// draw the funnel meter; it stays zero when nothing has been scored.
type dashboardProspectPoint struct {
	Label   string
	State   string
	Value   int
	Percent int
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

	// Backlog counters. Each one is the size of a queue of operator work and
	// feeds exactly one row of the attention region.
	NeedsReview         int
	DuplicateCandidates int
	UncheckedWebsites   int
	MissingEmail        int
	UnscoredProspects   int
}

// dashboardChartPoint is one labelled aggregate. Percent is the value's share
// of the largest point in the same series, so a template can draw an inline
// bar without a chart library; it stays zero until the series is scaled.
type dashboardChartPoint struct {
	Label   string
	Value   int
	Percent int
}

// dashboardAvailability is one contact-field availability row. Percent is the
// share of unique businesses carrying the field and Count is the number of
// businesses behind it, so the meter always states the evidence as well as
// the ratio.
type dashboardAvailability struct {
	Label   string
	Percent int
	Count   int
	Total   int
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
	Duplicates    int
	Emails        int
	// UniqueYield is the unique-per-raw share for this job ("62.0%"), or
	// "not recorded" before any raw record exists. DuplicateShare and
	// EmailShare use the same convention so the campaign table never prints a
	// percentage it cannot substantiate.
	UniqueYield    string
	DuplicateShare string
	EmailShare     string
	Runtime        string
	// Finished is the formatted completion time, empty while the job is still
	// live. Active marks the rows that belong in the "running now" region.
	Finished     string
	Active       bool
	HasBenchmark bool
	CanPause     bool
	CanResume    bool
	CanCancel    bool
	CanRetry     bool
	// CanDuplicate offers "run this configuration again" from the command
	// centre. It follows the lifecycle repository rather than the job state,
	// because copying a configuration is safe at any point in a run.
	CanDuplicate bool
	HasResults   bool
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

// Dashboard row budgets. The command centre answers "what is happening now"
// and "what did the last runs yield"; the Jobs page owns the full history, so
// these caps keep the page dense instead of growing with the workspace.
const (
	dashboardRunningJobLimit = 5
	dashboardCampaignLimit   = 6
	dashboardYieldWindow     = 10
	dashboardAttentionLimit  = 6
)

// dashboardJobIsLive reports whether a job still has work an operator can act
// on. Drafts are excluded deliberately: they have never been queued, so they
// belong to the Jobs page rather than to the "running now" region.
func dashboardJobIsLive(state jobruntime.State) bool {
	switch state {
	case jobruntime.StateQueued, jobruntime.StateStarting, jobruntime.StateRunning,
		jobruntime.StateCancelling, jobruntime.StatePaused:
		return true
	default:
		return false
	}
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
	benchmarks := s.svc.SupportsJobBenchmarks()
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

		// Live runs always surface, however far down the list they sit: an
		// operator who cannot see the job that is running right now has no
		// command centre. Terminal runs are capped because their value is
		// recency, not completeness — the Jobs page holds the full history.
		switch {
		case dashboardJobIsLive(runtime.State):
			if len(page.RunningJobs) < dashboardRunningJobLimit {
				page.RunningJobs = append(page.RunningJobs, s.dashboardJobRow(r, job, runtime, stats, now, benchmarks))
			}
		case runtime.State.Terminal():
			if len(page.Campaigns) < dashboardCampaignLimit {
				page.Campaigns = append(page.Campaigns, s.dashboardJobRow(r, job, runtime, stats, now, benchmarks))
			}
		}

		if index < dashboardYieldWindow {
			accumulateDashboardYield(&page.Yield, job, runtime.RawRecords, stats)
		}
	}
	finaliseDashboardYield(&page.Yield)

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
		page.Metrics.NeedsReview = int(overview.NeedsReview)
		page.Metrics.DuplicateCandidates = int(overview.DuplicateGroups)
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
		// A business with a website URL that has neither an active nor an
		// inactive verdict has never been fetched, so its status is genuinely
		// unknown rather than "no website".
		page.Metrics.UncheckedWebsites = max(0, int(analytics.Availability.Websites-
			analytics.Availability.WebsiteActive-analytics.Availability.WebsiteInactive))
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
			page.Prospects.ByStatus = dashboardProspectPoints(summary.ByStatus, page.Prospects.Scored)
			page.Prospects.ByTier = dashboardProspectPoints(summary.ByTier, page.Prospects.Scored)
		}
	}

	if page.Metrics.UniqueBusinesses > 0 {
		page.Metrics.EmailCoverage = percentage(page.Metrics.Emails, page.Metrics.UniqueBusinesses)
		page.Metrics.WebsiteCoverage = percentage(page.Metrics.Websites, page.Metrics.UniqueBusinesses)
		page.Metrics.PhoneCoverage = percentage(page.Metrics.Phones, page.Metrics.UniqueBusinesses)
		page.Metrics.SocialCoverage = percentage(page.Metrics.SocialProfiles, page.Metrics.UniqueBusinesses)
	}

	total := page.Metrics.UniqueBusinesses
	page.Availability = []dashboardAvailability{
		{Label: "Website", Percent: page.Metrics.WebsiteCoverage, Count: page.Metrics.Websites, Total: total},
		{Label: "Email", Percent: page.Metrics.EmailCoverage, Count: page.Metrics.Emails, Total: total},
		{Label: "Phone", Percent: page.Metrics.PhoneCoverage, Count: page.Metrics.Phones, Total: total},
		{Label: "Social profile", Percent: page.Metrics.SocialCoverage, Count: page.Metrics.SocialProfiles, Total: total},
	}
	// Website reachability is a different question from website discovery, so
	// the active/inactive split gets its own pair of rows rather than being
	// folded into the discovery meter above.
	page.WebsiteStatus = []dashboardAvailability{
		{
			Label:   "Website reachable",
			Percent: percentage(page.Metrics.ActiveWebsites, page.Metrics.Websites),
			Count:   page.Metrics.ActiveWebsites,
			Total:   page.Metrics.Websites,
		},
		{
			Label:   "Website unreachable",
			Percent: percentage(page.Metrics.InactiveWebsites, page.Metrics.Websites),
			Count:   page.Metrics.InactiveWebsites,
			Total:   page.Metrics.Websites,
		},
		{
			Label:   "Website never checked",
			Percent: percentage(page.Metrics.UncheckedWebsites, page.Metrics.Websites),
			Count:   page.Metrics.UncheckedWebsites,
			Total:   page.Metrics.Websites,
		},
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
	// The legacy CSV fallback above builds its points by hand, so the relative
	// bar share is applied here for whichever source produced the series.
	for index := range page.CollectionByDate {
		page.CollectionByDate[index].Percent = percentage(page.CollectionByDate[index].Value, page.CollectionMax)
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

	page.Metrics.MissingEmail = max(0, page.Metrics.UniqueBusinesses-page.Metrics.Emails)
	if page.Prospects.Supported {
		page.Metrics.UnscoredProspects = max(0, page.Metrics.UniqueBusinesses-page.Prospects.Scored)
	}
	page.Attention = dashboardAttention(page)

	return page, activity, nil
}

// dashboardJobRow renders one job for the command centre. Both the live and
// the finished tables use it so a job reads the same either side of the
// terminal boundary, and every ratio comes from counters this row already has.
func (s *Server) dashboardJobRow(
	r *http.Request,
	job Job,
	runtime JobRuntime,
	stats ResultStats,
	now time.Time,
	benchmarks bool,
) dashboardJob {
	state := string(runtime.State)
	percent := roundedPercent(runtime.Progress, runtime.State)
	stage := humanStage(runtime.Stage)
	eta := ""
	live := dashboardJobIsLive(runtime.State)

	// The execution snapshot carries the stage and ETA a running worker has
	// published. It costs one extra read, so it is only fetched for the live
	// rows that can still change, and an ETA is only shown while a worker is
	// actually advancing: a paused or queued job has no arrival time, and
	// printing the last one recorded would be a guess presented as fact.
	if live {
		advancing := runtime.State.Active()
		if execution, executionErr := s.svc.GetJobExecution(r.Context(), job.ID); executionErr == nil {
			if execution.Progress.Stage != jobruntime.StageNone {
				stage = humanStage(execution.Progress.Stage)
			}
			if advancing && execution.Progress.ETASeconds != nil {
				eta = humanDuration(time.Duration(*execution.Progress.ETASeconds) * time.Second)
			}
		}
		if advancing && eta == "" && runtime.StartedAt != nil && percent > 0 && percent < 100 {
			if elapsed := now.Sub(*runtime.StartedAt); elapsed > 0 {
				eta = humanDuration(time.Duration(float64(elapsed) * float64(100-percent) / float64(percent)))
			}
		}
	}

	raw := runtime.RawRecords
	if raw <= 0 {
		raw = int64(stats.Rows)
	}

	finished := ""
	if runtime.FinishedAt != nil {
		finished = compactTimestamp(*runtime.FinishedAt)
	}

	return dashboardJob{
		ID:             job.ID,
		Name:           job.Name,
		State:          state,
		Stage:          stage,
		Percent:        percent,
		ETA:            eta,
		RawRecords:     raw,
		UniqueRecords:  stats.UniqueBusinesses,
		Duplicates:     stats.Duplicates,
		Emails:         stats.WithEmail,
		UniqueYield:    ratioLabel(int64(stats.UniqueBusinesses), raw),
		DuplicateShare: ratioLabel(int64(stats.Duplicates), raw),
		EmailShare:     ratioLabel(int64(stats.WithEmail), int64(stats.UniqueBusinesses)),
		Runtime:        runtimeLabel(runtime),
		Finished:       finished,
		Active:         live,
		HasBenchmark:   benchmarks && runtime.State.Terminal(),
		CanPause:       lifecycleControlAllowed(runtime, jobruntime.ControlPause),
		CanResume:      lifecycleControlAllowed(runtime, jobruntime.ControlResume),
		CanCancel:      lifecycleControlAllowed(runtime, jobruntime.ControlCancel),
		CanRetry: lifecycleControlAllowed(runtime, jobruntime.ControlRestart) &&
			(state == "partial" || state == "failed" || state == "cancelled"),
		CanDuplicate: s.lifecycleAvailable(),
		HasResults:   stats.Rows > 0,
	}
}

// dashboardAttention turns the counters the dashboard already loaded into an
// ordered backlog. Rows with a zero count are dropped rather than rendered as
// a reassuring "0", and every destination is a route registered in web.go, so
// an operator can always act on what they are shown.
func dashboardAttention(page dashboardPageData) []dashboardAttentionItem {
	// A filter row is three parallel query parameters; parseResultSearch
	// zips them back together, so two rows means two of each.
	const uncheckedWebsitesURL = "/app/results" +
		"?filter_field=website&filter_operator=not_empty&filter_value=" +
		"&filter_field=last_checked_at&filter_operator=empty&filter_value="

	candidates := []dashboardAttentionItem{{
		Tone:   "danger",
		Label:  "Jobs need attention",
		Value:  page.Metrics.FailedJobs + page.Metrics.PartialJobs,
		Detail: "Partial or failed runs. Retrying resumes from the last checkpoint and keeps committed rows.",
		Action: "Review runs",
		URL:    "/app/jobs?state=partial,failed",
	}, {
		Tone:   "warning",
		Label:  "Jobs paused",
		Value:  page.Metrics.PausedJobs,
		Detail: "A paused run holds its checkpoint until you resume or cancel it.",
		Action: "Open paused",
		URL:    "/app/jobs?state=paused",
	}, {
		Tone:   "warning",
		Label:  "Duplicate pairs unresolved",
		Value:  page.Metrics.DuplicateCandidates,
		Detail: "Candidate pairs still waiting for a merge or keep-both decision.",
		Action: "Resolve duplicates",
		URL:    "/app/results?include_duplicates=true",
	}, {
		Tone:   "info",
		Label:  "Websites never checked",
		Value:  page.Metrics.UncheckedWebsites,
		Detail: "Businesses whose website URL has never been fetched, so the GBP signal is unknown.",
		Action: "Open backlog",
		URL:    uncheckedWebsitesURL,
	}, {
		Tone:   "info",
		Label:  "No email address yet",
		Value:  page.Metrics.MissingEmail,
		Detail: "Unique businesses with no stored address. Website enrichment is the usual next step.",
		Action: "Open gap list",
		URL:    "/app/results?filter_field=email&filter_operator=empty&filter_value=",
	}, {
		Tone:   "neutral",
		Label:  "Low-confidence records",
		Value:  page.Metrics.NeedsReview,
		Detail: "Unreviewed businesses, or records the quality rules scored below 60% confidence.",
		Action: "Review quality",
		URL:    "/app/results?filter_field=reviewed&filter_operator=eq&filter_value=false",
	}}

	if page.Prospects.Supported {
		candidates = append(candidates, dashboardAttentionItem{
			Tone:   "special",
			Label:  "Prospects not scored",
			Value:  page.Metrics.UnscoredProspects,
			Detail: "Businesses with no worth-calling tier. Select them in Results and recompute the score.",
			Action: "Score prospects",
			URL:    "/app/results?filter_field=prospect_tier&filter_operator=empty&filter_value=",
		})
	}

	items := make([]dashboardAttentionItem, 0, len(candidates))
	for _, item := range candidates {
		if item.Value > 0 {
			items = append(items, item)
		}
	}

	sort.SliceStable(items, func(first, second int) bool {
		return dashboardToneRank(items[first].Tone) < dashboardToneRank(items[second].Tone)
	})

	return items[:min(len(items), dashboardAttentionLimit)]
}

// dashboardToneRank orders the backlog by urgency rather than by count: a
// single failed job outranks ten thousand unscored prospects.
func dashboardToneRank(tone string) int {
	switch tone {
	case "danger":
		return 0
	case "warning":
		return 1
	case "info":
		return 2
	case "special":
		return 3
	default:
		return 4
	}
}

// accumulateDashboardYield folds one recent job into the workspace yield
// summary. Only jobs that produced raw records count, so a queued or empty
// job cannot drag the reported share toward zero.
func accumulateDashboardYield(yield *dashboardYield, job Job, rawRecords int64, stats ResultStats) {
	raw := rawRecords
	if raw <= 0 {
		raw = int64(stats.Rows)
	}
	if raw <= 0 {
		return
	}

	yield.Jobs++
	yield.RawRecords += raw
	yield.UniqueRecords += int64(stats.UniqueBusinesses)
	yield.Duplicates += int64(stats.Duplicates)
	yield.Emails += int64(stats.WithEmail)

	share := float64(stats.UniqueBusinesses) / float64(raw)
	if yield.BestJobName == "" || share > yield.bestShare() {
		yield.BestJobID, yield.BestJobName = job.ID, job.Name
		yield.BestJobYield = ratioLabel(int64(stats.UniqueBusinesses), raw)
	}
	if yield.WorstJobName == "" || share < yield.worstShare() {
		yield.WorstJobID, yield.WorstJobName = job.ID, job.Name
		yield.WorstJobYield = ratioLabel(int64(stats.UniqueBusinesses), raw)
	}
}

// bestShare and worstShare re-read the stored percentage labels so the
// comparison uses exactly the number the operator sees.
func (y dashboardYield) bestShare() float64  { return parsePercentLabel(y.BestJobYield) }
func (y dashboardYield) worstShare() float64 { return parsePercentLabel(y.WorstJobYield) }

func parsePercentLabel(label string) float64 {
	trimmed := strings.TrimSuffix(strings.TrimSpace(label), "%")
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0
	}

	return value / 100
}

// finaliseDashboardYield turns the accumulated counters into the labels and
// meter percentages the card renders.
func finaliseDashboardYield(yield *dashboardYield) {
	if yield.Jobs == 0 || yield.RawRecords <= 0 {
		*yield = dashboardYield{}

		return
	}

	yield.UniqueYield = ratioLabel(yield.UniqueRecords, yield.RawRecords)
	yield.DuplicateShare = ratioLabel(yield.Duplicates, yield.RawRecords)
	yield.EmailShare = ratioLabel(yield.Emails, yield.UniqueRecords)
	yield.UniquePercent = percentage(int(yield.UniqueRecords), int(yield.RawRecords))
	yield.DuplicatePercent = percentage(int(yield.Duplicates), int(yield.RawRecords))
	yield.EmailPercent = percentage(int(yield.Emails), int(yield.UniqueRecords))
	if yield.BestJobID == yield.WorstJobID {
		yield.WorstJobID, yield.WorstJobName, yield.WorstJobYield = "", "", ""
	}
}

// dashboardProspectPoints labels each taxonomy count with a CSS-safe badge
// state for the Prospecting card and, when a scored total is known, the share
// of scored businesses each bucket represents.
func dashboardProspectPoints(points []DashboardCountPoint, scored int) []dashboardProspectPoint {
	converted := make([]dashboardProspectPoint, 0, len(points))
	for _, point := range points {
		converted = append(converted, dashboardProspectPoint{
			Label:   point.Label,
			State:   prospectStateClass(point.Label),
			Value:   int(point.Value),
			Percent: percentage(int(point.Value), scored),
		})
	}

	return converted
}

// dashboardPoints converts a repository series and scales every point against
// the largest value in that series so the template can draw a proportional bar
// beside the printed number.
func dashboardPoints(points []DashboardCountPoint) []dashboardChartPoint {
	converted := make([]dashboardChartPoint, 0, len(points))
	largest := 0
	for _, point := range points {
		if int(point.Value) > largest {
			largest = int(point.Value)
		}
	}

	for _, point := range points {
		converted = append(converted, dashboardChartPoint{
			Label:   point.Label,
			Value:   int(point.Value),
			Percent: percentage(int(point.Value), largest),
		})
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

// stageLabels names every pipeline stage in the words an operator would use.
// Title-casing the identifier produced run-ons such as "Saving Exporting", so
// the wording is stated explicitly instead of derived.
var stageLabels = map[jobruntime.Stage]string{
	jobruntime.StageNone:               "Waiting to start",
	jobruntime.StagePreparingQueries:   "Preparing queries",
	jobruntime.StageGeneratingGrid:     "Generating grid",
	jobruntime.StageSearchingMaps:      "Searching Maps",
	jobruntime.StageExtractingDetails:  "Extracting details",
	jobruntime.StageCrawlingWebsites:   "Crawling websites",
	jobruntime.StageExtractingContacts: "Extracting contacts",
	jobruntime.StageDeduplicating:      "Deduplicating",
	jobruntime.StageSavingExporting:    "Saving and exporting",
}

func humanStage(stage jobruntime.Stage) string {
	if label, known := stageLabels[stage]; known {
		return label
	}

	// An unrecognised stage still has to read as a sentence rather than as a
	// database value, so the identifier is only the fallback.
	words := strings.Fields(strings.ReplaceAll(string(stage), "_", " "))
	if len(words) == 0 {
		return "Waiting to start"
	}
	words[0] = strings.ToUpper(words[0][:1]) + words[0][1:]

	return strings.Join(words, " ")
}

// runtimeLabel states elapsed run time. A job that reached a terminal state
// without a recorded start says so plainly: "not started" next to COMPLETED
// reads as a contradiction, when the truth is only that the timestamp was
// never written.
func runtimeLabel(runtime JobRuntime) string {
	if runtime.StartedAt == nil {
		if runtime.State.Terminal() {
			return "not recorded"
		}

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
