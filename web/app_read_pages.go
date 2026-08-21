package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/grid"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

const (
	appDefaultPageSize = 25
	appMaximumPageSize = 250
	appMaximumMapCells = 2500
)

type jobsPageData struct {
	Query       string
	ActiveState string
	Sort        string
	Folders     []appSelectOption
	Counts      jobsPageCounts
	Jobs        []jobsPageJob
	RangeLabel  string
	Total       int
	PreviousURL string
	NextURL     string
	// Filtered reports whether any search, state, or folder filter is
	// narrowing the list. An empty table means something different with a
	// filter applied ("nothing matches") than without one ("no jobs yet").
	Filtered bool
	// Sparse marks an unfiltered workspace with only a handful of jobs, where
	// a bare table reads as a broken page. The template pairs the table with
	// a short "what to do next" panel instead of leaving the space blank.
	Sparse          bool
	CanViewArchived bool
	ShowArchived    bool
	// ActiveFolder is the folder filter in force; CanApplyLabels gates the
	// folder picker and the tag column so neither renders without storage
	// behind it.
	ActiveFolder   string
	CanApplyLabels bool
}

// jobFolderOptions turns the folders in use into a sorted picker. A folder
// that no job carries is never offered, so the control can never filter the
// table down to nothing.
func jobFolderOptions(folders map[string]struct{}) []appSelectOption {
	names := make([]string, 0, len(folders))
	for name := range folders {
		names = append(names, name)
	}
	sort.SliceStable(names, func(left, right int) bool {
		return strings.ToLower(names[left]) < strings.ToLower(names[right])
	})

	options := make([]appSelectOption, 0, len(names))
	for _, name := range names {
		options = append(options, appSelectOption{Value: name, Label: name})
	}

	return options
}

// jobLabelsMatchQuery lets the jobs search find a job by a tag, a folder, or an
// owner, which is what the search field has always promised.
func jobLabelsMatchQuery(labels JobLabels, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return false
	}

	if strings.Contains(strings.ToLower(labels.Folder), query) ||
		strings.Contains(strings.ToLower(labels.Owner), query) {
		return true
	}

	for _, tag := range labels.Tags {
		if strings.Contains(strings.ToLower(tag), query) {
			return true
		}
	}

	return false
}

type appSelectOption struct {
	Value string
	Label string
}

type jobsPageCounts struct {
	All       int
	Active    int
	Queued    int
	Paused    int
	Attention int
}

type jobsPageJob struct {
	ID            string
	Name          string
	State         string
	Stage         string
	Percent       int
	ETA           string
	UniqueRecords int
	RawRecords    int
	Emails        int
	UpdatedAt     string
	Runtime       string
	// Finished is the formatted completion timestamp, empty while the job has
	// not stopped. The timing cell leads with it for terminal jobs so an
	// operator reads "finished" rather than "last touched".
	Finished        string
	Terminal        bool
	QuerySummary    string
	LocationSummary string
	Tags            []string
	Folder          string
	Owner           string
	HasResults      bool
	CanStart        bool
	CanPause        bool
	CanResume       bool
	CanRetry        bool
	CanCancel       bool
	CanDuplicate    bool
	HasMoreActions  bool
	CanArchive      bool
	CanRename       bool
	CanDelete       bool
	Archived        bool
	updatedAt       time.Time
	createdAt       time.Time
}

type jobMonitorPageData struct {
	Job         jobMonitorJob
	QueueNotice string
	Pipeline    []jobPipelineStep
	Checkpoint  jobCheckpointView
	ProxyPools  []jobProxyPoolView
	Logs        []jobLogView
	LogQuery    string
	// LogLevels is generated from web.JobLogLevels, so the severity filter can
	// never offer a class that classifyJobLogLevel does not produce.
	LogLevels   []appSelectOption
	LogSeverity string
	// LogAutoscroll keeps the "follow live" preference across a filter submit,
	// which is a form round trip and would otherwise reset it every time.
	LogAutoscroll   bool
	CanDownloadLogs bool
	HasResources    bool
	// Labels and Notes are the operator-applied organisation for this job.
	// CanApplyLabels gates the whole editor: without label storage there is no
	// form, rather than a form that cannot save.
	Labels         JobLabels
	TagsValue      string
	Notes          string
	CanApplyLabels bool
	CanEditNotes   bool
}

// jobLogLevelOptions renders the severity filter, marking the active level.
func jobLogLevelOptions() []appSelectOption {
	options := make([]appSelectOption, 0, len(JobLogLevels))
	for _, level := range JobLogLevels {
		label := strings.ReplaceAll(level, "-", " ")
		options = append(options, appSelectOption{
			Value: level,
			Label: strings.ToUpper(label[:1]) + label[1:],
		})
	}

	return options
}

type jobMonitorJob struct {
	ID        string
	Name      string
	CreatedAt string
	// ScraperVersion is the build identity recorded when a worker started this
	// job, or "not recorded" for a run that predates version stamping. It is
	// never back-filled from the binary running today, because that build did
	// not necessarily produce the committed rows. LocalBuild states the
	// version of the binary serving the page, so both facts stay separable.
	ScraperVersion    string
	LocalBuild        string
	State             string
	Stage             string
	Percent           int
	CurrentTask       string
	ETA               string
	RawRecords        int64
	CommittedRows     int
	UniqueRecords     int64
	Duplicates        int
	Emails            int64
	Websites          int
	PlacesPerMinute   string
	TasksComplete     int64
	TasksTotal        int64
	TasksFailed       int64
	TasksRemaining    int64
	Runtime           string
	MaxRuntime        string
	CurrentKeyword    string
	CurrentLocation   string
	CurrentCell       string
	WebsiteQueue      string
	ActiveProxy       string
	LastCheckpoint    string
	Concurrency       int
	CPUPercent        string
	Memory            string
	DiskFree          string
	Browsers          string
	Pages             string
	WorkerConcurrency string
	DatabaseWrites    string
	// ProxyPoolLabel names the pool this job routes through together with its
	// stored health counts, and BlockRate is this job's own refusal rate,
	// measured from its durable proxy-failure, rate-limit, and captcha events
	// against the tasks that finished.
	ProxyPoolLabel string
	BlockRate      string
	// Queries and Cells describe the plan this run executed: how many distinct
	// searches were generated and how many grid cells they covered. Cells is a
	// string because "no grid cells recorded" is a real answer for a radius
	// search, and a zero there would read as a failure.
	Queries        int64
	Cells          string
	SocialProfiles int64
	// Retries, Warnings, and Errors are run-wide totals for the job-detail
	// readout, taken from whichever of the runtime row, the task plan, and the
	// event log has the most evidence.
	Retries              int64
	Warnings             int64
	Errors               int64
	QuerySummary         string
	LocationSummary      string
	Depth                int
	Zoom                 int
	Radius               string
	EnrichmentSummary    string
	Owner                string
	ConfigJSON           string
	HasResults           bool
	CanStart             bool
	CanPause             bool
	CanResume            bool
	CanCancel            bool
	CanDuplicate         bool
	CanRetry             bool
	CanAddRuntime        bool
	CanChangeConcurrency bool
	CanChangeProxyPool   bool
	CanRetryCurrent      bool
	CanRestartCheckpoint bool
	HasRuntimeControls   bool
}

type jobPipelineStep struct {
	Order  int
	Label  string
	Detail string
	State  string
	// Metrics are the named per-stage measurements the specification lists for
	// this stage. They are built in web/job_pipeline_view.go from durable
	// evidence only, so a stage that has not run says so instead of showing a
	// zero that reads as a result.
	Metrics []jobPipelineMetric
}

type jobCheckpointView struct {
	Available        bool
	CreatedAt        string
	CompletedTasks   int64
	RemainingTasks   int64
	RunningTasks     int64
	FailedTasks      int64
	Version          string
	RecoveryRequired bool
	RecoveryMessage  string
}

// RecoveryStatusMessage states plainly what a resume would do, so an operator
// does not have to infer it from the task counters.
func RecoveryStatusMessage(execution JobExecutionSnapshot) string {
	if execution.RecoveryRequired {
		return fmt.Sprintf(
			"This job was interrupted. Resuming runs the %d remaining task(s) and keeps the %d already committed.",
			execution.Tasks.Remaining(), execution.Tasks.Completed,
		)
	}

	if execution.Tasks.Remaining() > 0 {
		return fmt.Sprintf(
			"%d task(s) remain; %d have committed their rows.",
			execution.Tasks.Remaining(), execution.Tasks.Completed,
		)
	}

	return "Every planned task has committed its rows."
}

type jobProxyPoolView struct {
	ID      string
	Name    string
	Healthy int
}

type jobLogView struct {
	OccurredAtISO string
	OccurredAt    string
	Severity      string
	Message       string
	TargetURL     string
}

type mapPageData struct {
	Mode            string
	AreaID          string
	AreaName        string
	AreaGeoJSON     string
	GeometryType    string
	Place           string
	Latitude        string
	Longitude       string
	RadiusKM        string
	GridCellKM      string
	Query           string
	SelectedJobID   string
	Jobs            []mapJobOption
	Areas           []mapAreaOption
	KeywordGroups   []namedAppOption
	Estimate        mapEstimateView
	Cells           []mapCellView
	Markers         []mapMarkerView
	SelectedCells   int
	SelectedCellIDs string
	CanExportArea   bool
	CanSaveArea     bool
	CanUseArea      bool
	CanMutateCells  bool
}

type mapAreaOption struct {
	ID       string
	Name     string
	Selected bool
}

type mapJobOption struct {
	ID       string
	Name     string
	State    string
	Selected bool
}

type mapEstimateView struct {
	Cells   string
	Queries string
	Tasks   string
	Runtime string
}

type mapCellView struct {
	ID          string
	Number      int
	X           float64
	Y           float64
	Width       float64
	Height      float64
	State       string
	ResultCount int
}

type mapMarkerView struct {
	X        float64
	Y        float64
	Name     string
	Category string
	Rating   string
	Email    string
	Phone    string
}

// compactTimestamp is the queue-table date format: short, tabular, and
// unambiguous. formatDate spells the time down to the second, which is
// precision a list view cannot use and which costs a column of width.
func compactTimestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}

	return value.Format("2006-01-02 15:04")
}

func (s *Server) jobsPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	page, activity, err := s.buildJobsPage(r)
	if err != nil {
		http.Error(w, "could not load jobs", http.StatusInternalServerError)

		return
	}

	s.renderAppPage(w, "jobs", appPageData{
		Title:     "Jobs",
		Subtitle:  "Inspect durable lifecycle state, committed results, and safe actions.",
		ActiveNav: "jobs",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page:      page,
	})
}

func (s *Server) buildJobsPage(r *http.Request) (jobsPageData, appActivity, error) {
	page := jobsPageData{
		Query:       strings.TrimSpace(r.URL.Query().Get("q")),
		ActiveState: strings.TrimSpace(r.URL.Query().Get("state")),
		Sort:        strings.TrimSpace(r.URL.Query().Get("sort")),
	}
	if page.Sort == "" {
		page.Sort = "updated_desc"
	}

	if s.svc == nil || s.svc.repo == nil {
		return page, appActivity{}, nil
	}

	jobs, err := s.svc.All(r.Context())
	if err != nil {
		return jobsPageData{}, appActivity{}, err
	}

	lifecycleAvailable := s.lifecycleAvailable()
	organisationAvailable := s.jobOrganisationAvailable()
	rows := make([]jobsPageJob, 0, len(jobs))
	activity := appActivity{}
	states := parseStateFilter(page.ActiveState)

	// Labels are read once for the whole page rather than per row, and a
	// repository without label support simply renders no tags and no folder
	// filter instead of an empty control.
	labels := map[string]JobLabels{}
	page.CanApplyLabels = s.svc.SupportsJobLabels()
	if page.CanApplyLabels {
		loaded, labelErr := s.svc.AllJobLabels(r.Context())
		if labelErr != nil && !errors.Is(labelErr, ErrJobLabelsUnsupported) {
			return jobsPageData{}, appActivity{}, labelErr
		}
		if loaded != nil {
			labels = loaded
		}
	}
	page.ActiveFolder = strings.TrimSpace(r.URL.Query().Get("folder"))
	folders := map[string]struct{}{}

	// Archived jobs stay out of the default queue view but remain one query
	// parameter away, so nothing is hidden permanently.
	archived := map[string]struct{}{}
	page.CanViewArchived = organisationAvailable
	page.ShowArchived = strings.TrimSpace(r.URL.Query().Get("archived")) == "1"

	if organisationAvailable {
		known, archivedErr := s.svc.ArchivedJobIDs(r.Context())
		if archivedErr != nil && !errors.Is(archivedErr, ErrJobOrganisationUnsupported) {
			return jobsPageData{}, appActivity{}, archivedErr
		}

		if known != nil {
			archived = known
		}
	}

	for _, job := range jobs {
		runtime, runtimeErr := s.runtimeForJob(r.Context(), job)
		if runtimeErr != nil {
			return jobsPageData{}, appActivity{}, runtimeErr
		}

		state := string(runtime.State)
		page.Counts.All++
		switch runtime.State {
		case jobruntime.StateQueued:
			page.Counts.Queued++
			activity.Queued++
		case jobruntime.StateStarting, jobruntime.StateRunning, jobruntime.StateCancelling:
			page.Counts.Active++
			activity.Running++
		case jobruntime.StatePaused:
			page.Counts.Paused++
		case jobruntime.StatePartial, jobruntime.StateFailed:
			page.Counts.Attention++
		}

		// Folder options are collected from every job, not only the visible
		// ones, so choosing a folder never empties the picker that produced it.
		jobLabels := labels[job.ID]
		if jobLabels.Folder != "" {
			folders[jobLabels.Folder] = struct{}{}
		}

		if len(states) > 0 {
			if _, included := states[state]; !included {
				continue
			}
		}

		if page.ActiveFolder != "" && jobLabels.Folder != page.ActiveFolder {
			continue
		}

		if !jobMatchesQuery(job, page.Query) && !jobLabelsMatchQuery(jobLabels, page.Query) {
			continue
		}

		stats, statsErr := s.svc.GetResultStats(r.Context(), job.ID)
		if statsErr != nil && !errors.Is(statsErr, ErrPlacesNotFound) {
			return jobsPageData{}, appActivity{}, statsErr
		}

		updatedAt := runtime.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = job.Date
		}

		row := jobsPageJob{
			ID:              job.ID,
			Name:            job.Name,
			State:           state,
			Stage:           humanStage(runtime.Stage),
			Percent:         roundedPercent(runtime.Progress, runtime.State),
			ETA:             jobsRowETA(runtime),
			UniqueRecords:   stats.UniqueBusinesses,
			RawRecords:      stats.Rows,
			Emails:          stats.WithEmail,
			UpdatedAt:       compactTimestamp(updatedAt),
			Runtime:         runtimeLabel(runtime),
			Terminal:        runtime.State.Terminal(),
			QuerySummary:    querySummary(job.Data.Keywords),
			LocationSummary: locationSummary(job.Data),
			HasResults:      stats.Rows > 0,
			CanStart:        lifecycleAvailable && canApplyControl(runtime, jobruntime.ControlStart),
			CanPause:        lifecycleAvailable && canApplyControl(runtime, jobruntime.ControlPause),
			CanResume:       lifecycleAvailable && canApplyControl(runtime, jobruntime.ControlResume),
			CanCancel:       lifecycleAvailable && canApplyControl(runtime, jobruntime.ControlCancel),
			CanDuplicate:    lifecycleAvailable,
			CanRetry: lifecycleAvailable &&
				(runtime.State == jobruntime.StatePartial || runtime.State == jobruntime.StateFailed) &&
				canApplyControl(runtime, jobruntime.ControlRestart),
			Tags:      jobLabels.Tags,
			Folder:    jobLabels.Folder,
			Owner:     jobLabels.Owner,
			CanRename: organisationAvailable,
			// Archiving is only offered once a job has stopped, so an operator
			// cannot hide work that is still running.
			CanArchive: organisationAvailable && runtime.State.Terminal(),
			updatedAt:  updatedAt,
			createdAt:  job.Date,
		}
		if runtime.FinishedAt != nil {
			row.Finished = compactTimestamp(*runtime.FinishedAt)
		}
		if _, isArchived := archived[job.ID]; isArchived {
			row.Archived = true
			if !page.ShowArchived {
				continue
			}
		}
		row.HasMoreActions = row.CanDuplicate || row.CanRetry || row.CanCancel ||
			row.CanArchive || row.CanRename || row.CanDelete
		rows = append(rows, row)
	}

	sortJobRows(rows, page.Sort)
	page.Folders = jobFolderOptions(folders)
	start, end, previousURL, nextURL := paginationWindow(r, len(rows))
	page.Filtered = page.Query != "" || page.ActiveState != "" || page.ActiveFolder != ""
	// Three or fewer jobs is a workspace that is still being set up, not a
	// queue an operator is managing.
	page.Sparse = !page.Filtered && len(rows) > 0 && len(rows) <= 3
	page.Total = len(rows)
	page.RangeLabel = rangeLabel(start, end, len(rows))
	page.PreviousURL = previousURL
	page.NextURL = nextURL
	page.Jobs = rows[start:end]

	return page, activity, nil
}

func (s *Server) jobMonitorPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	id, ok := getIDFromRequest(r)
	if !ok {
		http.Error(w, "invalid job ID", http.StatusUnprocessableEntity)

		return
	}

	page, err := s.buildJobMonitorPage(r, id.String())
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrLifecycleNotFound) {
			http.Error(w, "job not found", http.StatusNotFound)

			return
		}

		http.Error(w, "could not load job", http.StatusInternalServerError)

		return
	}

	activity, _ := s.appActivity(r)
	if page.Job.State == string(jobruntime.StateQueued) {
		if activity.Running > 0 {
			page.QueueNotice = fmt.Sprintf(
				"This job is safely queued behind %d active local job(s). The worker processes queued jobs in FIFO order.",
				activity.Running,
			)
		} else {
			page.QueueNotice = "The local worker polls the durable queue every second. If this message remains for more than a few seconds, open System health and check the Docker logs; restarting the container is safe because the queue and committed rows are durable."
		}
	}
	s.renderAppPage(w, "job_monitor", appPageData{
		Title:     page.Job.Name,
		Subtitle:  "Live state, committed counters, configuration, and redacted events.",
		ActiveNav: "jobs",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page:      page,
	})
}

func (s *Server) buildJobMonitorPage(r *http.Request, id string) (jobMonitorPageData, error) {
	job, err := s.svc.Get(r.Context(), id)
	if err != nil {
		return jobMonitorPageData{}, err
	}
	if job.ID == "" {
		return jobMonitorPageData{}, ErrNotFound
	}

	runtime, err := s.runtimeForJob(r.Context(), job)
	if err != nil {
		return jobMonitorPageData{}, err
	}

	stats, err := s.svc.GetResultStats(r.Context(), job.ID)
	if err != nil && !errors.Is(err, ErrPlacesNotFound) {
		return jobMonitorPageData{}, err
	}

	lifecycleAvailable := s.lifecycleAvailable()
	rawRecords := max(runtime.RawRecords, int64(stats.Rows))
	uniqueRecords := max(runtime.UniqueRecords, int64(stats.UniqueBusinesses))
	emails := max(runtime.Emails, int64(stats.WithEmail))
	remaining := max(int64(0), runtime.TotalTasks-runtime.Completed-runtime.Failed)
	configJSON, err := safeJobConfigJSON(job)
	if err != nil {
		return jobMonitorPageData{}, err
	}

	page := jobMonitorPageData{
		Job: jobMonitorJob{
			ID:                job.ID,
			Name:              job.Name,
			CreatedAt:         formatDate(job.Date),
			ScraperVersion:    "not recorded",
			LocalBuild:        ScraperVersion(),
			State:             string(runtime.State),
			Stage:             humanStage(runtime.Stage),
			Percent:           roundedPercent(runtime.Progress, runtime.State),
			CurrentTask:       currentTaskLabel(runtime),
			ETA:               runtimeETA(runtime),
			RawRecords:        rawRecords,
			CommittedRows:     stats.Rows,
			UniqueRecords:     uniqueRecords,
			Duplicates:        stats.Duplicates,
			Emails:            emails,
			Websites:          stats.WithWebsite,
			PlacesPerMinute:   placesPerMinute(rawRecords, runtime),
			TasksComplete:     runtime.Completed,
			TasksTotal:        runtime.TotalTasks,
			TasksFailed:       runtime.Failed,
			TasksRemaining:    remaining,
			Runtime:           runtimeLabel(runtime),
			MaxRuntime:        humanDuration(job.Data.MaxTime),
			CurrentKeyword:    "not reported by worker",
			CurrentLocation:   locationSummary(job.Data),
			CurrentCell:       "not reported by worker",
			WebsiteQueue:      "not reported by worker",
			ActiveProxy:       proxySummary(job.Data.Proxies),
			LastCheckpoint:    "not reported by worker",
			CPUPercent:        "not reported",
			Memory:            "not reported",
			Browsers:          "not reported",
			Pages:             "not reported",
			DatabaseWrites:    "not reported",
			ProxyPoolLabel:    proxySummary(job.Data.Proxies),
			QuerySummary:      querySummary(job.Data.Keywords),
			LocationSummary:   locationSummary(job.Data),
			Depth:             job.Data.Depth,
			Zoom:              job.Data.Zoom,
			Radius:            radiusSummary(job.Data),
			EnrichmentSummary: enrichmentSummary(job.Data),
			Owner:             "not recorded",
			ConfigJSON:        configJSON,
			HasResults:        stats.Rows > 0,
			CanStart:          lifecycleAvailable && canApplyControl(runtime, jobruntime.ControlStart),
			CanPause:          lifecycleAvailable && canApplyControl(runtime, jobruntime.ControlPause),
			CanResume:         lifecycleAvailable && canApplyControl(runtime, jobruntime.ControlResume),
			CanCancel:         lifecycleAvailable && canApplyControl(runtime, jobruntime.ControlCancel),
			CanDuplicate:      lifecycleAvailable,
			CanRetry: lifecycleAvailable &&
				(runtime.State == jobruntime.StatePartial || runtime.State == jobruntime.StateFailed) &&
				canApplyControl(runtime, jobruntime.ControlRestart),
		},
		CanDownloadLogs: lifecycleAvailable,
		Checkpoint: jobCheckpointView{
			Available:      false,
			CreatedAt:      "not available",
			CompletedTasks: runtime.Completed,
			RemainingTasks: remaining,
			Version:        "not available",
		},
		LogQuery:    strings.TrimSpace(r.URL.Query().Get("log_q")),
		LogLevels:   jobLogLevelOptions(),
		LogSeverity: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("severity"))),
		// Following the tail is the default; only an explicit unchecked box in
		// a submitted filter form turns it off.
		LogAutoscroll: !r.URL.Query().Has("log_q") || r.URL.Query().Get("log_autoscroll") != "",
	}

	if lifecycleAvailable {
		logs, logErr := s.monitorLogs(r.Context(), job.ID, page.LogQuery, page.LogSeverity)
		if logErr != nil && !errors.Is(logErr, ErrLifecycleNotFound) {
			return jobMonitorPageData{}, logErr
		}
		page.Logs = logs
	}

	page.CanApplyLabels = s.svc.SupportsJobLabels()
	if page.CanApplyLabels {
		labels, labelErr := s.svc.JobLabels(r.Context(), job.ID)
		if labelErr != nil && !errors.Is(labelErr, ErrJobLabelsUnsupported) &&
			!errors.Is(labelErr, ErrLifecycleNotFound) {
			return jobMonitorPageData{}, labelErr
		}
		page.Labels = labels
		page.TagsValue = strings.Join(labels.Tags, ", ")
		page.Job.Owner = labels.Owner
	}
	if page.Job.Owner == "" {
		page.Job.Owner = "not recorded"
	}

	page.CanEditNotes = s.jobOrganisationAvailable()
	if page.CanEditNotes {
		organisation, organisationErr := s.svc.GetJobOrganisation(r.Context(), job.ID)
		if organisationErr != nil && !errors.Is(organisationErr, ErrJobOrganisationUnsupported) &&
			!errors.Is(organisationErr, ErrLifecycleNotFound) {
			return jobMonitorPageData{}, organisationErr
		}
		page.Notes = organisation.Notes
	}

	// A recorded build identity is evidence about this run; an unsupported
	// repository or an unstamped legacy job simply keeps "not recorded".
	if recorded, versionErr := s.svc.JobScraperVersion(r.Context(), job.ID); versionErr == nil && recorded != "" {
		page.Job.ScraperVersion = recorded
	} else if versionErr != nil &&
		!errors.Is(versionErr, ErrScraperVersionUnsupported) &&
		!errors.Is(versionErr, ErrLifecycleNotFound) {
		return jobMonitorPageData{}, versionErr
	}

	var execution JobExecutionSnapshot
	if snapshot, executionErr := s.svc.GetJobExecution(r.Context(), job.ID); executionErr == nil {
		execution = snapshot
		page.Job.TasksTotal = execution.Tasks.Total
		page.Job.TasksComplete = execution.Tasks.Completed
		page.Job.TasksFailed = execution.Tasks.Failed
		page.Job.TasksRemaining = execution.Tasks.Remaining()
		if execution.Progress.CurrentQuery != "" {
			page.Job.CurrentKeyword = execution.Progress.CurrentQuery
			page.Job.CurrentTask = execution.Progress.CurrentQuery
		}
		if execution.Progress.CurrentCell != "" {
			page.Job.CurrentCell = execution.Progress.CurrentCell
		}
		page.Job.WebsiteQueue = strconv.FormatInt(execution.Progress.WebsiteQueue, 10)

		liveControlsAvailable := s.svc != nil && s.svc.SupportsLiveControls()
		jobActive := runtime.State == jobruntime.StateRunning || runtime.State == jobruntime.StateStarting
		page.Job.CanAddRuntime = liveControlsAvailable && jobActive
		page.Job.CanChangeConcurrency = liveControlsAvailable && jobActive
		page.Job.CanChangeProxyPool = liveControlsAvailable && jobActive
		page.Job.CanRetryCurrent = liveControlsAvailable && jobActive
		page.Job.HasRuntimeControls = page.Job.CanAddRuntime || page.Job.CanChangeConcurrency ||
			page.Job.CanChangeProxyPool || page.Job.CanRetryCurrent

		if page.Job.CanChangeProxyPool || job.Data.ProxyPoolID != "" {
			if pools, poolsErr := s.svc.ListProxyPools(r.Context()); poolsErr == nil {
				for _, pool := range pools {
					if page.Job.CanChangeProxyPool {
						page.ProxyPools = append(page.ProxyPools, jobProxyPoolView{
							ID: pool.ID, Name: pool.Name, Healthy: int(pool.HealthyCount),
						})
					}
					if pool.ID == job.Data.ProxyPoolID {
						page.Job.ProxyPoolLabel = fmt.Sprintf(
							"%s — %d of %d proxies healthy",
							pool.Name, pool.HealthyCount, pool.TotalCount,
						)
					}
				}
			}
		}

		page.Job.PlacesPerMinute = strconv.FormatFloat(execution.Progress.PlacesPerMinute, 'f', 1, 64)
		if execution.Progress.ETASeconds != nil {
			page.Job.ETA = humanDuration(time.Duration(*execution.Progress.ETASeconds) * time.Second)
		}
		page.HasResources = execution.Progress.CurrentQuery != "" || execution.Progress.DiskFreeBytes > 0 ||
			execution.Progress.MemoryBytes > 0 || execution.Progress.CPUPercent > 0
		if page.HasResources {
			page.Job.CPUPercent = strconv.FormatFloat(execution.Progress.CPUPercent, 'f', 1, 64)
			page.Job.Memory = humanBytes(int64(min(execution.Progress.MemoryBytes, uint64(math.MaxInt64))))
			page.Job.DiskFree = humanBytes(int64(min(execution.Progress.DiskFreeBytes, uint64(math.MaxInt64))))
			page.Job.Browsers = strconv.FormatInt(execution.Progress.BrowserCount, 10)
			page.Job.Pages = strconv.FormatInt(execution.Progress.ActivePages, 10)
			page.Job.DatabaseWrites = strconv.FormatInt(execution.Progress.DatabaseWrites, 10)
			page.Job.WorkerConcurrency = fmt.Sprintf(
				"%d effective / %d requested",
				execution.Progress.EffectiveWorkers,
				execution.Progress.DesiredWorkers,
			)
		}
		if execution.Checkpoint != nil {
			page.Checkpoint = jobCheckpointView{
				Available: true, CreatedAt: formatDate(execution.Checkpoint.CreatedAt),
				CompletedTasks: execution.Tasks.Completed, RemainingTasks: execution.Tasks.Remaining(),
				RunningTasks: execution.Tasks.Running, FailedTasks: execution.Tasks.Failed,
				Version:          fmt.Sprintf("checkpoint %d / %s", execution.Checkpoint.ID, execution.Checkpoint.TaskKey),
				RecoveryRequired: execution.RecoveryRequired,
				RecoveryMessage:  RecoveryStatusMessage(execution),
			}
			page.Job.LastCheckpoint = formatDate(execution.Checkpoint.CreatedAt)
			page.Job.CanRestartCheckpoint = lifecycleAvailable && canApplyControl(runtime, jobruntime.ControlRestart)
		} else if execution.RecoveryRequired || execution.Tasks.Total > 0 {
			// A job interrupted before its first task committed still has to say
			// where it stands, otherwise the card silently claims nothing exists.
			page.Checkpoint = jobCheckpointView{
				Available: true, CreatedAt: "no checkpoint committed yet",
				CompletedTasks: execution.Tasks.Completed, RemainingTasks: execution.Tasks.Remaining(),
				RunningTasks: execution.Tasks.Running, FailedTasks: execution.Tasks.Failed,
				Version:          fmt.Sprintf("%d task(s) planned", execution.Tasks.Total),
				RecoveryRequired: execution.RecoveryRequired,
				RecoveryMessage:  RecoveryStatusMessage(execution),
			}
			page.Job.CanRestartCheckpoint = lifecycleAvailable && canApplyControl(runtime, jobruntime.ControlRestart)
		}
	} else if !errors.Is(executionErr, ErrCheckpointUnsupported) && !errors.Is(executionErr, ErrLifecycleNotFound) {
		return jobMonitorPageData{}, executionErr
	}

	// Per-stage evidence is optional capability: a repository without it keeps
	// the pipeline, which then reports "not reported yet" for the metrics that
	// need durable joins rather than inventing them.
	var facts JobPipelineFacts
	hasFacts := false
	if loaded, factsErr := s.svc.JobPipelineFacts(r.Context(), job.ID); factsErr == nil {
		facts, hasFacts = loaded, true
	} else if !errors.Is(factsErr, ErrJobPipelineFactsUnsupported) {
		return jobMonitorPageData{}, factsErr
	}

	page.Pipeline = buildPipeline(jobPipelineInput{
		Job:        job,
		Runtime:    runtime,
		Stats:      stats,
		Execution:  execution,
		Facts:      facts,
		HasFacts:   hasFacts,
		RawRecords: rawRecords,
	})
	applyJobDetailCounters(&page, runtime, execution, facts, hasFacts)

	return page, nil
}

// applyJobDetailCounters fills the job-detail readouts the specification names
// alongside the pipeline: planned cells, retries, warnings, and errors. Each
// one prefers the durable evidence over the live worker frame, because a
// worker that has stopped no longer republishes its counters.
func applyJobDetailCounters(
	page *jobMonitorPageData,
	runtime JobRuntime,
	execution JobExecutionSnapshot,
	facts JobPipelineFacts,
	hasFacts bool,
) {
	page.Job.Retries = max(execution.Tasks.Retries, execution.Progress.Retries)
	page.Job.Warnings = runtime.Warnings
	page.Job.Errors = runtime.Errors
	page.Job.Cells = notReported

	if !hasFacts {
		return
	}

	page.Job.Retries = max(page.Job.Retries, facts.Retries)
	page.Job.Warnings = max(page.Job.Warnings, facts.Warnings)
	page.Job.Errors = max(page.Job.Errors, facts.Errors)
	page.Job.Queries = facts.QueriesPlanned
	page.Job.SocialProfiles = facts.WithSocial
	page.Job.BlockRate = fmt.Sprintf("%.1f%% (%d blocks)", facts.BlockRatePercent(), facts.BlockEvents())
	if facts.CellsPlanned > 0 {
		page.Job.Cells = strconv.FormatInt(facts.CellsPlanned, 10)
	} else {
		page.Job.Cells = "no grid cells recorded"
	}
}

func (s *Server) jobLogsDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	id, ok := getIDFromRequest(r)
	if !ok {
		http.Error(w, "invalid job ID", http.StatusUnprocessableEntity)

		return
	}

	if _, err := s.svc.Get(r.Context(), id.String()); err != nil {
		http.Error(w, "job not found", http.StatusNotFound)

		return
	}

	logs, err := s.monitorLogs(r.Context(), id.String(), "", "")
	if err != nil {
		if errors.Is(err, ErrLifecycleUnsupported) {
			http.Error(w, "job logs are unavailable", http.StatusNotImplemented)

			return
		}

		http.Error(w, "could not load job logs", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", id.String()+"-logs.txt"))
	for _, entry := range logs {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", entry.OccurredAtISO, entry.Severity, entry.Message)
	}
}

func (s *Server) mapPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	page, activity, err := s.buildMapPage(r)
	if err != nil {
		http.Error(w, "could not load map", http.StatusInternalServerError)

		return
	}

	s.renderAppPage(w, "map", appPageData{
		Title:     "Map Explorer",
		Subtitle:  "Preview real job grids and map committed CSV results without a paid map API.",
		ActiveNav: "map",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page:      page,
	})
}

func (s *Server) buildMapPage(r *http.Request) (mapPageData, appActivity, error) {
	page := mapPageData{
		Mode:          mapMode(firstNonEmpty(r.URL.Query().Get("mode"), r.URL.Query().Get("source"))),
		AreaID:        strings.TrimSpace(r.URL.Query().Get("area_id")),
		GeometryType:  mapGeometry(r.URL.Query().Get("geometry_type")),
		Place:         queryDefault(r, "place", "San Francisco, CA"),
		Latitude:      queryDefault(r, "latitude", "37.7749"),
		Longitude:     queryDefault(r, "longitude", "-122.4194"),
		RadiusKM:      queryDefault(r, "radius_km", "10"),
		GridCellKM:    queryDefault(r, "grid_cell_km", "2.5"),
		Query:         strings.TrimSpace(r.URL.Query().Get("q")),
		SelectedJobID: strings.TrimSpace(r.URL.Query().Get("job_id")),
		Estimate: mapEstimateView{
			Cells:   "0",
			Queries: "not configured",
			Tasks:   "not configured",
			Runtime: "not enough data",
		},
	}

	if s.svc == nil || s.svc.repo == nil {
		return page, appActivity{}, nil
	}

	var geometry MapGeometry
	if _, ok := s.svc.repo.(MapRepository); ok {
		page.CanSaveArea = true
		areas, listErr := s.svc.ListSavedAreas(r.Context(), maximumSavedAreaList)
		if listErr != nil {
			return mapPageData{}, appActivity{}, listErr
		}
		sort.SliceStable(areas, func(left, right int) bool {
			return strings.ToLower(areas[left].Name) < strings.ToLower(areas[right].Name)
		})
		for _, area := range areas {
			page.Areas = append(page.Areas, mapAreaOption{
				ID: area.ID, Name: area.Name, Selected: area.ID == page.AreaID,
			})
		}
		if page.AreaID != "" {
			area, getErr := s.svc.GetSavedArea(r.Context(), page.AreaID)
			if getErr != nil {
				return mapPageData{}, appActivity{}, getErr
			}
			geometry, getErr = ParseMapGeometry(area.GeoJSON)
			if getErr != nil {
				return mapPageData{}, appActivity{}, getErr
			}
			page.AreaName = area.Name
			page.CanExportArea = true
			page.CanUseArea = true
		}
	}
	if templates, templateErr := s.svc.ListScrapeTemplates(r.Context(), ""); templateErr == nil {
		for _, template := range templates {
			if len(template.Configuration.Keywords) == 0 {
				continue
			}
			page.KeywordGroups = append(page.KeywordGroups, namedAppOption{ID: template.ID, Name: template.Name})
		}
	}

	jobs, err := s.svc.All(r.Context())
	if err != nil {
		return mapPageData{}, appActivity{}, err
	}

	activity := appActivity{}
	var selected *Job
	for index := range jobs {
		job := jobs[index]
		runtime, runtimeErr := s.runtimeForJob(r.Context(), job)
		if runtimeErr != nil {
			return mapPageData{}, appActivity{}, runtimeErr
		}

		switch runtime.State {
		case jobruntime.StateQueued:
			activity.Queued++
		case jobruntime.StateStarting, jobruntime.StateRunning, jobruntime.StateCancelling:
			activity.Running++
		}

		option := mapJobOption{
			ID:       job.ID,
			Name:     job.Name,
			State:    string(runtime.State),
			Selected: job.ID == page.SelectedJobID,
		}
		page.Jobs = append(page.Jobs, option)
		if option.Selected {
			selectedJob := job
			selected = &selectedJob
		}
	}

	if page.SelectedJobID != "" && selected == nil {
		return mapPageData{}, appActivity{}, ErrNotFound
	}

	if selected != nil && page.AreaID == "" {
		applyJobMapDefaults(r, &page, *selected)
	}

	if !geometry.Valid() {
		var geometryErr error
		geometry, geometryErr = mapGeometryForPage(page, selected)
		if geometryErr != nil {
			return mapPageData{}, appActivity{}, geometryErr
		}
	} else {
		applyMapGeometryDefaults(r, &page, geometry)
	}
	page.GeometryType = geometry.Kind()
	page.AreaGeoJSON = string(geometry.GeoJSON())
	page.CanMutateCells = true

	cellSizeKM, validCellSize := parseMapFloat(page.GridCellKM, minimumMapCellKM, maximumMapCellKM)
	if !validCellSize {
		return mapPageData{}, appActivity{}, fmt.Errorf("invalid map grid size")
	}
	preview, err := PreviewMapGrid(geometry, cellSizeKM, appMaximumMapCells)
	if err != nil {
		return mapPageData{}, appActivity{}, err
	}
	page.Estimate.Cells = strconv.Itoa(len(preview.Cells))

	if selected != nil {
		queryCount := len(selected.Data.Keywords)
		page.Estimate.Queries = strconv.Itoa(queryCount)
		page.Estimate.Tasks = strconv.Itoa(queryCount * len(preview.Cells))
	}

	return page, activity, nil
}

func mapGeometryForPage(page mapPageData, selected *Job) (MapGeometry, error) {
	if selected != nil && strings.TrimSpace(selected.Data.AreaGeoJSON) != "" {
		return ParseMapGeometry([]byte(selected.Data.AreaGeoJSON))
	}
	if selected != nil && strings.TrimSpace(selected.Data.GridBBox) != "" {
		bounds, err := grid.ParseBoundingBox(selected.Data.GridBBox)
		if err != nil {
			return MapGeometry{}, err
		}
		return mapBBoxGeometry(MapBounds{
			MinLatitude: bounds.MinLat, MinLongitude: bounds.MinLon,
			MaxLatitude: bounds.MaxLat, MaxLongitude: bounds.MaxLon,
		})
	}

	latitude, validLatitude := parseMapFloat(page.Latitude, -90, 90)
	longitude, validLongitude := parseMapFloat(page.Longitude, -180, 180)
	radiusKM, validRadius := parseMapFloat(page.RadiusKM, minimumCircleRadiusM/1000, maximumCircleRadiusM/1000)
	if !validLatitude || !validLongitude || !validRadius {
		return MapGeometry{}, fmt.Errorf("invalid map coordinates or radius")
	}
	payload, err := json.Marshal(map[string]any{
		"type":       "Feature",
		"properties": map[string]any{"shape": "circle", "radius_m": radiusKM * 1000},
		"geometry":   map[string]any{"type": "Point", "coordinates": []float64{longitude, latitude}},
	})
	if err != nil {
		return MapGeometry{}, err
	}

	return ParseMapGeometry(payload)
}

func mapBBoxGeometry(bounds MapBounds) (MapGeometry, error) {
	payload, err := json.Marshal(map[string]any{
		"type": "Feature",
		"properties": map[string]any{
			"shape": "bbox",
			"bbox":  []float64{bounds.MinLongitude, bounds.MinLatitude, bounds.MaxLongitude, bounds.MaxLatitude},
		},
		"geometry": nil,
	})
	if err != nil {
		return MapGeometry{}, err
	}

	return ParseMapGeometry(payload)
}

func applyMapGeometryDefaults(r *http.Request, page *mapPageData, geometry MapGeometry) {
	centre := mapGeometryRepresentativePoint(geometry)
	if _, present := r.URL.Query()["latitude"]; !present {
		page.Latitude = strconv.FormatFloat(centre.Latitude, 'f', -1, 64)
	}
	if _, present := r.URL.Query()["longitude"]; !present {
		page.Longitude = strconv.FormatFloat(centre.Longitude, 'f', -1, 64)
	}
	if geometry.circle != nil {
		if _, present := r.URL.Query()["radius_km"]; !present {
			page.RadiusKM = strconv.FormatFloat(geometry.circle.RadiusM/1000, 'f', -1, 64)
		}
	}
	if page.AreaName != "" && r.URL.Query().Get("place") == "" {
		page.Place = page.AreaName
	}
}

func (s *Server) runtimeForJob(ctx context.Context, job Job) (JobRuntime, error) {
	runtime, err := s.svc.GetRuntime(ctx, job.ID)
	if err == nil {
		return runtime, nil
	}
	if !errors.Is(err, ErrLifecycleNotFound) {
		return JobRuntime{}, err
	}

	state, stateErr := stateFromLegacyStatus(job.Status)
	if stateErr != nil {
		return JobRuntime{}, stateErr
	}

	return JobRuntime{
		JobID:     job.ID,
		State:     state,
		Stage:     stageForState(state),
		Progress:  progressForState(state),
		UpdatedAt: job.Date,
	}, nil
}

func (s *Server) lifecycleAvailable() bool {
	if s.svc == nil || s.svc.repo == nil {
		return false
	}

	_, ok := s.svc.repo.(LifecycleRepository)

	return ok
}

func canApplyControl(runtime JobRuntime, control jobruntime.Control) bool {
	decision, err := jobruntime.DecideControl(runtime.State, runtime.RequestedStop, control)

	return err == nil && decision.Error() == nil && decision.Changed()
}

func parseStateFilter(value string) map[string]struct{} {
	states := make(map[string]struct{})
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if state, err := jobruntime.ParseState(item); err == nil {
			states[string(state)] = struct{}{}
		}
	}

	return states
}

func jobMatchesQuery(job Job, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}

	values := []string{job.ID, job.Name, strings.Join(job.Data.Keywords, " ")}
	return strings.Contains(strings.ToLower(strings.Join(values, "\x00")), query)
}

func sortJobRows(rows []jobsPageJob, order string) {
	sort.SliceStable(rows, func(left, right int) bool {
		switch order {
		case "created_asc":
			return rows[left].createdAt.Before(rows[right].createdAt)
		case "created_desc":
			return rows[left].createdAt.After(rows[right].createdAt)
		case "name_asc":
			return strings.ToLower(rows[left].Name) < strings.ToLower(rows[right].Name)
		default:
			return rows[left].updatedAt.After(rows[right].updatedAt)
		}
	})
}

func roundedPercent(value float64, state jobruntime.State) int {
	if state == jobruntime.StateCompleted {
		return 100
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}

	return int(math.Round(max(0, min(100, value))))
}

// jobsRowETA is the queue-table variant of runtimeETA. The table already
// prints a status badge in the neighbouring cell, so "stopped" beside a
// finished job is noise and a paused or queued job has no arrival time at
// all; both cases return an empty string and the cell says nothing.
func jobsRowETA(runtime JobRuntime) string {
	if !runtime.State.Active() {
		return ""
	}

	return runtimeETA(runtime)
}

func runtimeETA(runtime JobRuntime) string {
	if runtime.State.Terminal() {
		return "stopped"
	}
	if runtime.StartedAt == nil || runtime.TotalTasks <= 0 {
		return "calculating"
	}

	terminal := min(runtime.TotalTasks, runtime.Completed+runtime.Failed)
	eta, ok := jobruntime.EstimateETA(runtime.TotalTasks, terminal, time.Since(*runtime.StartedAt))
	if !ok {
		return "calculating"
	}

	return eta.Round(time.Second).String()
}

func querySummary(keywords []string) string {
	if len(keywords) == 0 {
		return "No queries recorded"
	}
	if len(keywords) <= 2 {
		return strings.Join(keywords, "; ")
	}

	return fmt.Sprintf("%s; %s; +%d more", keywords[0], keywords[1], len(keywords)-2)
}

func locationSummary(data JobData) string {
	if data.GridBBox != "" {
		return fmt.Sprintf("Grid %s (%g km cells)", data.GridBBox, data.GridCellKM)
	}
	if data.FastMode {
		return fmt.Sprintf("Fast Mode within %s of %s, %s", metresLabel(data.Radius), data.Lat, data.Lon)
	}
	if data.Lat != "" || data.Lon != "" {
		return fmt.Sprintf("Maps search near %s, %s", data.Lat, data.Lon)
	}

	return "No geographic constraint recorded"
}

func radiusSummary(data JobData) string {
	if data.FastMode {
		return metresLabel(data.Radius) + " (strict Fast Mode filter)"
	}
	if data.GridBBox != "" {
		return "not applied; bounded by grid"
	}
	if data.Radius > 0 {
		return metresLabel(data.Radius) + " (not strict outside Fast Mode)"
	}

	return "not configured"
}

func metresLabel(metres int) string {
	if metres > 0 && metres%1000 == 0 {
		return fmt.Sprintf("%d km", metres/1000)
	}

	return fmt.Sprintf("%d m", metres)
}

func enrichmentSummary(data JobData) string {
	parts := []string{"Maps business details"}
	if data.Email {
		parts = append(parts, "website/email crawl")
	}
	if data.ExtraReviews {
		parts = append(parts, "extra reviews")
	}

	return strings.Join(parts, ", ")
}

func proxySummary(proxies []string) string {
	if len(proxies) == 0 {
		return "Direct connection"
	}

	return fmt.Sprintf("%d configured (credentials hidden)", len(proxies))
}

func humanDuration(value time.Duration) string {
	if value <= 0 {
		return "not recorded"
	}

	return value.Round(time.Second).String()
}

func currentTaskLabel(runtime JobRuntime) string {
	if strings.TrimSpace(runtime.Message) != "" {
		return runtime.Message
	}
	if runtime.State.Terminal() {
		return "No active task"
	}
	if runtime.State == jobruntime.StateQueued {
		return "Waiting for local worker capacity"
	}
	if runtime.State == jobruntime.StatePaused {
		return "Paused at a safe boundary"
	}

	return "No current worker task reported"
}

func placesPerMinute(records int64, runtime JobRuntime) string {
	if records <= 0 || runtime.StartedAt == nil {
		return "not enough data"
	}
	end := time.Now().UTC()
	if runtime.FinishedAt != nil {
		end = *runtime.FinishedAt
	}
	duration := end.Sub(*runtime.StartedAt)
	if duration <= 0 {
		return "not enough data"
	}

	return strconv.FormatFloat(jobruntime.RatePerMinute(records, duration), 'f', 1, 64)
}

func safeJobConfigJSON(job Job) (string, error) {
	progress := newJobProgressDTO(job, ResultStats{}, JobRuntime{})
	encoded, err := json.MarshalIndent(progress.Config, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render job configuration: %w", err)
	}

	return string(encoded), nil
}

func (s *Server) monitorLogs(
	ctx context.Context,
	jobID string,
	query string,
	severity string,
) ([]jobLogView, error) {
	events, err := s.svc.EventsAfter(ctx, jobID, 0, 1000)
	if err != nil {
		return nil, err
	}

	query = strings.ToLower(strings.TrimSpace(query))
	severity = strings.ToLower(strings.TrimSpace(severity))
	logs := make([]jobLogView, 0, len(events))
	for _, event := range events {
		// The search covers the redacted context too, so an operator can find
		// every line about one query, cell, or task key.
		haystack := strings.ToLower(event.Message + " " + event.Type + " " + event.Context)
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}

		level := classifyJobLogLevel(event)
		if severity != "" && !strings.EqualFold(level, severity) {
			continue
		}

		logs = append(logs, jobLogView{
			OccurredAtISO: event.OccurredAt.UTC().Format(time.RFC3339),
			OccurredAt:    formatDate(event.OccurredAt),
			Severity:      level,
			Message:       event.Message,
			TargetURL:     jobLogTarget(jobID, event),
		})
	}

	return logs, nil
}

func paginationWindow(r *http.Request, total int) (int, int, string, string) {
	page := positiveQueryInt(r.URL.Query().Get("page"), 1)
	pageSize := positiveQueryInt(r.URL.Query().Get("page_size"), appDefaultPageSize)
	pageSize = min(pageSize, appMaximumPageSize)
	maximumPage := max(1, int(math.Ceil(float64(total)/float64(pageSize))))
	page = min(page, maximumPage)
	start := min(total, (page-1)*pageSize)
	end := min(total, start+pageSize)

	previousURL := ""
	if page > 1 {
		previousURL = pageURL(r, page-1)
	}
	nextURL := ""
	if end < total {
		nextURL = pageURL(r, page+1)
	}

	return start, end, previousURL, nextURL
}

func positiveQueryInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return fallback
	}

	return parsed
}

func pageURL(r *http.Request, page int) string {
	query := cloneURLValues(r.URL.Query())
	query.Set("page", strconv.Itoa(page))

	return r.URL.Path + "?" + query.Encode()
}

func cloneURLValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, entries := range values {
		cloned[key] = append([]string(nil), entries...)
	}

	return cloned
}

func rangeLabel(start, end, total int) string {
	if total == 0 {
		return "0"
	}

	return fmt.Sprintf("%d-%d", start+1, end)
}

func queryDefault(r *http.Request, name, fallback string) string {
	if _, present := r.URL.Query()[name]; !present {
		return fallback
	}

	return strings.TrimSpace(r.URL.Query().Get(name))
}

func mapMode(value string) string {
	switch value {
	case "live", "results":
		return value
	default:
		return "planning"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}

	return ""
}

func mapGeometry(value string) string {
	switch value {
	case "polygon", "bbox":
		return value
	default:
		return "circle"
	}
}

func applyJobMapDefaults(r *http.Request, page *mapPageData, job Job) {
	if _, present := r.URL.Query()["place"]; !present {
		page.Place = job.Name
	}
	if _, present := r.URL.Query()["latitude"]; !present && job.Data.Lat != "" {
		page.Latitude = job.Data.Lat
	}
	if _, present := r.URL.Query()["longitude"]; !present && job.Data.Lon != "" {
		page.Longitude = job.Data.Lon
	}
	if _, present := r.URL.Query()["radius_km"]; !present && job.Data.Radius > 0 {
		page.RadiusKM = strconv.FormatFloat(float64(job.Data.Radius)/1000, 'f', -1, 64)
	}
	if _, present := r.URL.Query()["grid_cell_km"]; !present && job.Data.GridCellKM > 0 {
		page.GridCellKM = strconv.FormatFloat(job.Data.GridCellKM, 'f', -1, 64)
	}
}

func mapGrid(page mapPageData, selected *Job) (grid.BoundingBox, []grid.Cell, error) {
	if selected != nil && selected.Data.GridBBox != "" {
		bbox, err := grid.ParseBoundingBox(selected.Data.GridBBox)
		if err != nil {
			return grid.BoundingBox{}, nil, err
		}
		cellKM := selected.Data.GridCellKM
		if value, present := parseMapFloat(page.GridCellKM, 0.1, 50); present {
			cellKM = value
		}

		cells := grid.GenerateCells(bbox, cellKM)
		if len(cells) > appMaximumMapCells {
			return grid.BoundingBox{}, nil, fmt.Errorf("map grid exceeds %d cells", appMaximumMapCells)
		}

		return bbox, cells, nil
	}

	latitude, okLat := parseMapFloat(page.Latitude, -90, 90)
	longitude, okLon := parseMapFloat(page.Longitude, -180, 180)
	radiusKM, okRadius := parseMapFloat(page.RadiusKM, 0.1, 500)
	cellKM, okCell := parseMapFloat(page.GridCellKM, 0.1, 50)
	if !okLat || !okLon || !okRadius || !okCell {
		return grid.BoundingBox{}, nil, fmt.Errorf("invalid map coordinates, radius, or grid size")
	}

	bboxString := radiusBoundingBox(latitude, longitude, radiusKM*1000)
	bbox, err := grid.ParseBoundingBox(bboxString)
	if err != nil {
		return grid.BoundingBox{}, nil, err
	}
	cells := grid.GenerateCells(bbox, cellKM)
	if len(cells) > appMaximumMapCells {
		return grid.BoundingBox{}, nil, fmt.Errorf("map grid exceeds %d cells", appMaximumMapCells)
	}

	return bbox, cells, nil
}

func parseMapFloat(value string, minimum, maximum float64) (float64, bool) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < minimum || parsed > maximum {
		return 0, false
	}

	return parsed, true
}

func renderMapCells(bbox grid.BoundingBox, cells []grid.Cell) []mapCellView {
	if len(cells) == 0 {
		return nil
	}

	columns := 1
	firstLatitude := cells[0].Lat
	for columns < len(cells) && math.Abs(cells[columns].Lat-firstLatitude) < 1e-9 {
		columns++
	}
	rows := int(math.Ceil(float64(len(cells)) / float64(columns)))
	width := 880 / float64(columns)
	height := 500 / float64(rows)
	views := make([]mapCellView, 0, len(cells))
	for index := range cells {
		views = append(views, mapCellView{
			ID:     fmt.Sprintf("planned-%d", index+1),
			Number: index + 1,
			X:      60 + float64(index%columns)*width,
			Y:      60 + float64(index/columns)*height,
			Width:  max(2, width-3),
			Height: max(2, height-3),
			State:  "waiting",
		})
	}

	_ = bbox

	return views
}

func (s *Server) mapPlaces(
	ctx context.Context,
	jobs []Job,
	selectedJobID string,
	query string,
) ([]Place, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	seen := make(map[string]struct{})
	places := []Place{}
	for _, job := range jobs {
		if selectedJobID != "" && job.ID != selectedJobID {
			continue
		}

		jobPlaces, err := s.svc.GetPlaces(ctx, job.ID)
		if errors.Is(err, ErrPlacesNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}

		for _, place := range jobPlaces {
			haystack := strings.ToLower(strings.Join([]string{place.Title, place.Category, place.Address}, " "))
			if query != "" && !strings.Contains(haystack, query) {
				continue
			}
			key := fmt.Sprintf("%.7f\x00%.7f\x00%s", place.Latitude, place.Longitude, strings.ToLower(place.Title))
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			places = append(places, place)
		}
	}

	return places, nil
}

func renderMapMarkers(places []Place, bbox grid.BoundingBox) []mapMarkerView {
	if len(places) == 0 {
		return nil
	}

	minLat, maxLat := bbox.MinLat, bbox.MaxLat
	minLon, maxLon := bbox.MinLon, bbox.MaxLon
	for _, place := range places {
		minLat = min(minLat, place.Latitude)
		maxLat = max(maxLat, place.Latitude)
		minLon = min(minLon, place.Longitude)
		maxLon = max(maxLon, place.Longitude)
	}
	latSpan := max(maxLat-minLat, 1e-9)
	lonSpan := max(maxLon-minLon, 1e-9)

	markers := make([]mapMarkerView, 0, len(places))
	for _, place := range places {
		markers = append(markers, mapMarkerView{
			X:        60 + (place.Longitude-minLon)/lonSpan*880,
			Y:        560 - (place.Latitude-minLat)/latSpan*500,
			Name:     place.Title,
			Category: place.Category,
			Rating:   strconv.FormatFloat(place.ReviewRating, 'f', 1, 64),
			Email:    "not included in map CSV view",
			Phone:    place.Phone,
		})
	}

	return markers
}
