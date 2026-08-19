package web

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
)

const (
	systemMetricsTimeout       = 5 * time.Second
	systemSelfTestTimeout      = 12 * time.Second
	systemNetworkCheckTimeout  = 4 * time.Second
	systemProxyCheckTimeout    = 10 * time.Second
	schedulerHeartbeatMaxAge   = 5 * time.Second
	minimumAvailableMemory     = 256 << 20
	minimumAvailableDisk       = 512 << 20
	maximumStorageScanEntries  = 100_000
	internetReachabilityTarget = "https://www.google.com/generate_204"
	mapsReachabilityTarget     = "https://www.google.com/maps?hl=en"

	// playwrightModulePath is the browser-automation driver module compiled
	// into release binaries; matches the require line in go.mod.
	playwrightModulePath = "github.com/mxschmitt/playwright-go"

	// playwrightDriverPathVariable mirrors the override the playwright-go
	// runtime itself honours when locating its driver installation.
	playwrightDriverPathVariable = "PLAYWRIGHT_DRIVER_PATH"
)

// ErrSystemDiagnosticsUnsupported indicates that a repository cannot provide
// the lightweight local system probes used by the versioned diagnostics API.
var ErrSystemDiagnosticsUnsupported = errors.New("local system diagnostics are unavailable")

// SystemDatabaseSnapshot is a lightweight database and queue projection. It
// deliberately does not run PRAGMA integrity_check; deep integrity remains an
// explicit maintenance action.
type SystemDatabaseSnapshot struct {
	SchemaVersion  int        `json:"schema_version"`
	SQLiteVersion  string     `json:"sqlite_version"`
	DatabaseBytes  int64      `json:"database_bytes"`
	JobCount       int64      `json:"job_count"`
	BusinessCount  int64      `json:"business_count"`
	SourceCount    int64      `json:"source_count"`
	ExportCount    int64      `json:"export_count"`
	BackupCount    int64      `json:"backup_count"`
	QueuedJobs     int64      `json:"queued_jobs"`
	RunningJobs    int64      `json:"running_jobs"`
	ActiveBrowsers int64      `json:"active_browsers"`
	ActivePages    int64      `json:"active_pages"`
	WebsiteQueue   int64      `json:"website_queue"`
	ProxyTotal     int64      `json:"proxy_total"`
	ProxyHealthy   int64      `json:"proxy_healthy"`
	ProxyBlocked   int64      `json:"proxy_blocked"`
	LastBrowserAt  *time.Time `json:"last_browser_at,omitempty"`
	LastWriteAt    *time.Time `json:"last_write_at,omitempty"`
}

type systemDiagnosticsRepository interface {
	SystemDatabaseSnapshot(context.Context) (SystemDatabaseSnapshot, error)
	CheckDatabaseWritable(context.Context) error
}

// SystemDatabaseSnapshot returns cheap database, storage, and queue metrics.
func (s *Service) SystemDatabaseSnapshot(ctx context.Context) (SystemDatabaseSnapshot, error) {
	repository, ok := s.repo.(systemDiagnosticsRepository)
	if !ok {
		return SystemDatabaseSnapshot{}, ErrSystemDiagnosticsUnsupported
	}

	return repository.SystemDatabaseSnapshot(ctx)
}

// CheckDatabaseWritable verifies that a transaction can write and roll back.
func (s *Service) CheckDatabaseWritable(ctx context.Context) error {
	repository, ok := s.repo.(systemDiagnosticsRepository)
	if !ok {
		return ErrSystemDiagnosticsUnsupported
	}

	return repository.CheckDatabaseWritable(ctx)
}

// RecordSchedulerHeartbeat records the most recent embedded worker poll. It is
// process-local by design and therefore immediately reflects a restarted or
// stalled local worker without adding high-frequency database writes.
func (s *Service) RecordSchedulerHeartbeat(value time.Time) {
	if s == nil {
		return
	}
	if value.IsZero() {
		s.schedulerHeartbeat.Store(0)
		return
	}

	s.schedulerHeartbeat.Store(value.UTC().UnixNano())
}

func (s *Service) schedulerHeartbeatAt() *time.Time {
	if s == nil {
		return nil
	}
	value := s.schedulerHeartbeat.Load()
	if value <= 0 {
		return nil
	}
	heartbeat := time.Unix(0, value).UTC()

	return &heartbeat
}

func (s *Service) uptime(now time.Time) time.Duration {
	if s == nil || s.startedAt.IsZero() || now.Before(s.startedAt) {
		return 0
	}

	return now.Sub(s.startedAt)
}

type localResourceSnapshot struct {
	CPUPercent           float64 `json:"cpu_percent"`
	LogicalCPUs          int     `json:"logical_cpus"`
	MemoryTotalBytes     uint64  `json:"memory_total_bytes"`
	MemoryAvailableBytes uint64  `json:"memory_available_bytes"`
	MemoryUsedBytes      uint64  `json:"memory_used_bytes"`
	MemoryUsedPercent    float64 `json:"memory_used_percent"`
	DiskTotalBytes       uint64  `json:"disk_total_bytes"`
	DiskFreeBytes        uint64  `json:"disk_free_bytes"`
	DiskUsedBytes        uint64  `json:"disk_used_bytes"`
	DiskUsedPercent      float64 `json:"disk_used_percent"`
}

type localSystemProbe interface {
	Resources(context.Context, string) (localResourceSnapshot, error)
	Reach(context.Context, string) (int, error)
}

type defaultLocalSystemProbe struct {
	client *http.Client
}

func newDefaultLocalSystemProbe() localSystemProbe {
	return &defaultLocalSystemProbe{client: &http.Client{
		Timeout: systemNetworkCheckTimeout,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}

			return nil
		},
	}}
}

func (probe *defaultLocalSystemProbe) Resources(ctx context.Context, dataFolder string) (localResourceSnapshot, error) {
	percentages, err := cpu.PercentWithContext(ctx, 0, false)
	if err != nil {
		return localResourceSnapshot{}, fmt.Errorf("read CPU usage: %w", err)
	}
	logicalCPUs, err := cpu.CountsWithContext(ctx, true)
	if err != nil {
		return localResourceSnapshot{}, fmt.Errorf("read CPU count: %w", err)
	}
	memory, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return localResourceSnapshot{}, fmt.Errorf("read memory usage: %w", err)
	}
	diskUsage, err := disk.UsageWithContext(ctx, dataFolder)
	if err != nil {
		return localResourceSnapshot{}, fmt.Errorf("read disk usage: %w", err)
	}
	cpuPercent := 0.0
	if len(percentages) > 0 {
		cpuPercent = percentages[0]
	}

	return localResourceSnapshot{
		CPUPercent:           cpuPercent,
		LogicalCPUs:          logicalCPUs,
		MemoryTotalBytes:     memory.Total,
		MemoryAvailableBytes: memory.Available,
		MemoryUsedBytes:      memory.Used,
		MemoryUsedPercent:    memory.UsedPercent,
		DiskTotalBytes:       diskUsage.Total,
		DiskFreeBytes:        diskUsage.Free,
		DiskUsedBytes:        diskUsage.Used,
		DiskUsedPercent:      diskUsage.UsedPercent,
	}, nil
}

func (probe *defaultLocalSystemProbe) Reach(ctx context.Context, target string) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, target, http.NoBody)
	if err != nil {
		return 0, err
	}
	request.Header.Set("User-Agent", "GoogleMapsScraperLocal/1.0")
	response, err := probe.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()

	return response.StatusCode, nil
}

type schedulerHeartbeatView struct {
	Status          string     `json:"status"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
	AgeSeconds      *int64     `json:"age_seconds,omitempty"`
}

type systemHealthResponse struct {
	// These fields preserve the existing health response keys. Integrity is
	// explicitly not checked by this lightweight endpoint.
	SchemaVersion int
	SQLiteVersion string
	Integrity     string
	DatabaseBytes int64
	JobCount      int64
	BusinessCount int64
	SourceCount   int64
	ExportCount   int64
	BackupCount   int64

	Status        string                 `json:"status"`
	CheckedAt     time.Time              `json:"checked_at"`
	UptimeSeconds int64                  `json:"uptime_seconds"`
	QueuedJobs    int64                  `json:"queued_jobs"`
	RunningJobs   int64                  `json:"running_jobs"`
	Scheduler     schedulerHeartbeatView `json:"scheduler"`
}

type systemMetricsResponse struct {
	Status        string                 `json:"status"`
	CollectedAt   time.Time              `json:"collected_at"`
	UptimeSeconds int64                  `json:"uptime_seconds"`
	GoVersion     string                 `json:"go_version"`
	OperatingSys  string                 `json:"operating_system"`
	Architecture  string                 `json:"architecture"`
	Resources     localResourceSnapshot  `json:"resources"`
	Database      SystemDatabaseSnapshot `json:"database"`
	Storage       localStorageSnapshot   `json:"storage"`
	Scheduler     schedulerHeartbeatView `json:"scheduler"`
}

type localStorageSnapshot struct {
	DataBytes        int64 `json:"data_bytes"`
	ExportsBytes     int64 `json:"exports_bytes"`
	ScreenshotsBytes int64 `json:"screenshots_bytes"`
	LogsBytes        int64 `json:"logs_bytes"`
	BackupsBytes     int64 `json:"backups_bytes"`
	CacheBytes       int64 `json:"cache_bytes"`
	TemporaryBytes   int64 `json:"temporary_bytes"`
	ScannedEntries   int   `json:"scanned_entries"`
	Truncated        bool  `json:"truncated"`
}

func (s *Server) apiSystemHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), systemMetricsTimeout)
	defer cancel()
	database, err := s.svc.SystemDatabaseSnapshot(ctx)
	if err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "health_failed", "Could not inspect local database health")
		return
	}
	now := time.Now().UTC()
	heartbeat := s.schedulerHeartbeatView(now)
	status := "ok"
	if heartbeat.Status == "stale" {
		status = "degraded"
	}
	response := systemHealthResponse{
		SchemaVersion: database.SchemaVersion, SQLiteVersion: database.SQLiteVersion,
		Integrity: "not_checked", DatabaseBytes: database.DatabaseBytes,
		JobCount: database.JobCount, BusinessCount: database.BusinessCount,
		SourceCount: database.SourceCount, ExportCount: database.ExportCount,
		BackupCount: database.BackupCount, Status: status, CheckedAt: now,
		UptimeSeconds: int64(s.svc.uptime(now).Seconds()), QueuedJobs: database.QueuedJobs,
		RunningJobs: database.RunningJobs, Scheduler: heartbeat,
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: response})
}

func (s *Server) apiSystemMetrics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), systemMetricsTimeout)
	defer cancel()
	database, err := s.svc.SystemDatabaseSnapshot(ctx)
	if err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "metrics_failed", "Could not read local database metrics")
		return
	}
	resources, err := s.systemProbe.Resources(ctx, s.svc.dataFolder)
	if err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "metrics_failed", "Could not read local resource metrics")
		return
	}
	storage, err := s.workspaceStorageUsage(ctx)
	if err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "metrics_failed", "Could not inspect local storage")
		return
	}
	now := time.Now().UTC()
	heartbeat := s.schedulerHeartbeatView(now)
	status := "ok"
	if heartbeat.Status == "stale" || storage.Truncated {
		status = "degraded"
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: systemMetricsResponse{
		Status: status, CollectedAt: now, UptimeSeconds: int64(s.svc.uptime(now).Seconds()),
		GoVersion: runtime.Version(), OperatingSys: runtime.GOOS, Architecture: runtime.GOARCH,
		Resources: resources, Database: database,
		Storage: localStorageSnapshot{
			DataBytes: storage.DataBytes, ExportsBytes: storage.ExportsBytes,
			ScreenshotsBytes: storage.ScreenshotsBytes, LogsBytes: storage.LogsBytes,
			BackupsBytes: storage.BackupsBytes, CacheBytes: storage.CacheBytes,
			TemporaryBytes: storage.TemporaryBytes, ScannedEntries: storage.ScannedEntries,
			Truncated: storage.Truncated,
		},
		Scheduler: heartbeat,
	}})
}

type systemSelfTestResponse struct {
	Status           string                `json:"status"`
	NetworkRequested bool                  `json:"network_requested"`
	StartedAt        time.Time             `json:"started_at"`
	DurationMS       int64                 `json:"duration_ms"`
	Checks           []systemSelfTestCheck `json:"checks"`
}

type systemSelfTestCheck struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	Message    string `json:"message"`
	DurationMS int64  `json:"duration_ms"`
}

func (s *Server) apiSystemSelfTest(w http.ResponseWriter, r *http.Request) {
	includeNetwork, err := parseExplicitNetworkCheck(r.URL.Query().Get("include_network"))
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_self_test", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), systemSelfTestTimeout)
	defer cancel()
	startedAt := time.Now().UTC()
	report := systemSelfTestResponse{
		Status: "passed", NetworkRequested: includeNetwork, StartedAt: startedAt,
		Checks: make([]systemSelfTestCheck, 0, 10),
	}
	addCheck := func(check systemSelfTestCheck) {
		report.Checks = append(report.Checks, check)
		switch check.State {
		case "failed":
			report.Status = "failed"
		case "warning":
			if report.Status == "passed" {
				report.Status = "degraded"
			}
		}
	}

	checkStarted := time.Now()
	if _, err := s.svc.SystemDatabaseSnapshot(ctx); err != nil {
		addCheck(newSystemCheck("database_readable", "failed", "Database read failed", checkStarted))
	} else {
		addCheck(newSystemCheck("database_readable", "passed", "Database query succeeded", checkStarted))
	}
	checkStarted = time.Now()
	if err := s.svc.CheckDatabaseWritable(ctx); err != nil {
		addCheck(newSystemCheck("database_writable", "failed", "Rollback-only database write failed", checkStarted))
	} else {
		addCheck(newSystemCheck("database_writable", "passed", "Rollback-only database write succeeded", checkStarted))
	}
	checkStarted = time.Now()
	if err := checkOutputDirectoryWritable(s.svc.dataFolder); err != nil {
		addCheck(newSystemCheck("output_directory", "failed", "Output directory write/cleanup failed", checkStarted))
	} else {
		addCheck(newSystemCheck("output_directory", "passed", "Output directory is writable", checkStarted))
	}

	checkStarted = time.Now()
	resources, resourceErr := s.systemProbe.Resources(ctx, s.svc.dataFolder)
	resourceDuration := time.Since(checkStarted)
	if resourceErr != nil {
		addCheck(systemSelfTestCheck{Name: "memory", State: "failed", Message: "Memory metrics unavailable", DurationMS: resourceDuration.Milliseconds()})
		addCheck(systemSelfTestCheck{Name: "disk", State: "failed", Message: "Disk metrics unavailable", DurationMS: resourceDuration.Milliseconds()})
	} else {
		memoryState := "passed"
		memoryMessage := "Available memory is above the local safety threshold"
		if resources.MemoryAvailableBytes < minimumAvailableMemory {
			memoryState = "warning"
			memoryMessage = "Available memory is below 256 MiB"
		}
		addCheck(systemSelfTestCheck{Name: "memory", State: memoryState, Message: memoryMessage, DurationMS: resourceDuration.Milliseconds()})
		diskState := "passed"
		diskMessage := "Free disk is above the local safety threshold"
		if resources.DiskFreeBytes < minimumAvailableDisk {
			diskState = "warning"
			diskMessage = "Free disk is below 512 MiB"
		}
		addCheck(systemSelfTestCheck{Name: "disk", State: diskState, Message: diskMessage, DurationMS: resourceDuration.Milliseconds()})
	}

	heartbeat := s.schedulerHeartbeatView(time.Now().UTC())
	heartbeatState := "passed"
	if heartbeat.Status != "healthy" {
		heartbeatState = "warning"
	}
	addCheck(systemSelfTestCheck{
		Name: "scheduler_heartbeat", State: heartbeatState,
		Message: "Scheduler heartbeat is " + heartbeat.Status,
	})

	addCheck(browserRuntimeCheck(time.Now()))

	for _, target := range []struct {
		name string
		url  string
	}{
		{name: "internet_reachable", url: internetReachabilityTarget},
		{name: "maps_reachable", url: mapsReachabilityTarget},
	} {
		if !includeNetwork {
			addCheck(systemSelfTestCheck{Name: target.name, State: "skipped", Message: "Network check not requested"})
			continue
		}
		addCheck(s.runReachabilityCheck(ctx, target.name, target.url))
	}

	addCheck(s.proxyCredentialsCheck(ctx, includeNetwork, time.Now()))

	report.DurationMS = time.Since(startedAt).Milliseconds()
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: report})
}

// BrowserAutomation reports the compiled-in Playwright driver module version
// for the versions panel on the system page. The method lives here so the
// version logic stays beside the other diagnostics probes.
func (systemPageData) BrowserAutomation() string {
	return browserAutomationVersion()
}

func browserAutomationVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "not embedded"
	}

	return playwrightModuleVersion(info)
}

// playwrightModuleVersion extracts the playwright-go dependency version from
// build metadata. It reports "not embedded" when the running binary was built
// without the module (for example a trimmed test binary) rather than guessing.
func playwrightModuleVersion(info *debug.BuildInfo) string {
	if info == nil {
		return "not embedded"
	}
	for _, dependency := range info.Deps {
		if dependency == nil || dependency.Path != playwrightModulePath {
			continue
		}
		version := dependency.Version
		if dependency.Replace != nil && dependency.Replace.Version != "" {
			version = dependency.Replace.Version
		}
		if version == "" {
			break
		}

		return "playwright-go " + version
	}

	return "not embedded"
}

// browserRuntimeCheck reports whether a Playwright driver installation looks
// present on disk. It never fails hard: a missing directory is only a warning
// because the driver downloads on first use, and a present directory is still
// no proof a browser launches — only a real scrape demonstrates that.
func browserRuntimeCheck(started time.Time) systemSelfTestCheck {
	location, found := playwrightDriverDirectory()
	if location == "" {
		return newSystemCheck("browser_runtime", "warning",
			"Playwright driver not found: no user cache directory is available; only a real scrape proves a browser launch", started)
	}
	if found {
		return newSystemCheck("browser_runtime", "passed",
			"Playwright driver present at "+location+"; only a real scrape proves a browser actually launches", started)
	}

	return newSystemCheck("browser_runtime", "warning",
		"Playwright driver not found at "+location+"; it is installed on the first scrape, and only a real scrape proves a browser launch", started)
}

// playwrightDriverDirectory returns the most authoritative driver location and
// whether it exists: an explicit PLAYWRIGHT_DRIVER_PATH override, otherwise
// the playwright-go cache directories under the user cache dir.
func playwrightDriverDirectory() (string, bool) {
	if override := strings.TrimSpace(os.Getenv(playwrightDriverPathVariable)); override != "" {
		return override, directoryExists(override)
	}
	cacheDirectory, err := os.UserCacheDir()
	if err != nil {
		return "", false
	}
	candidates := []string{
		filepath.Join(cacheDirectory, "ms-playwright-go"),
		filepath.Join(cacheDirectory, "ms-playwright"),
	}
	for _, candidate := range candidates {
		if directoryExists(candidate) {
			return candidate, true
		}
	}

	return candidates[0], false
}

// proxyCredentialsCheck verifies that at least one enabled proxy still accepts
// its stored credentials. It tests exactly one proxy with a bounded context so
// a large pool cannot stall the self-test, and it respects the offline-first
// contract: with the network not requested the live test is skipped honestly.
func (s *Server) proxyCredentialsCheck(ctx context.Context, includeNetwork bool, started time.Time) systemSelfTestCheck {
	proxies, err := s.svc.ListProxies(ctx, "")
	if errors.Is(err, ErrProxyStoreUnsupported) || (err == nil && len(proxies) == 0) {
		return newSystemCheck("proxy_credentials", "passed",
			"No proxies configured; scrapes use the direct connection", started)
	}
	if err != nil {
		return newSystemCheck("proxy_credentials", "warning",
			"Could not read the local proxy configuration: "+redactedDiagnosticError(err), started)
	}
	var candidate *ProxyRecord
	for index := range proxies {
		if proxies[index].Enabled {
			candidate = &proxies[index]
			break
		}
	}
	if candidate == nil {
		return newSystemCheck("proxy_credentials", "passed",
			"No proxies configured; every stored proxy is disabled, so scrapes use the direct connection", started)
	}
	if !includeNetwork {
		return newSystemCheck("proxy_credentials", "skipped",
			"Enabled proxies exist, but the credential test needs the network self-test; run it with internet checks included", started)
	}
	secret, err := s.svc.GetProxySecret(ctx, candidate.ID)
	if err != nil {
		return newSystemCheck("proxy_credentials", "warning",
			"Could not decrypt the stored proxy URL for "+candidate.MaskedURL+": "+redactedDiagnosticError(err), started)
	}
	proxyContext, cancel := context.WithTimeout(ctx, systemProxyCheckTimeout)
	defer cancel()
	result := checkProxyAccess(proxyContext, secret)
	if result.Status == "healthy" || result.Status == "slow" {
		message := fmt.Sprintf("Proxy %s accepted its credentials (status %s)", candidate.MaskedURL, result.Status)
		if result.LatencyMS != nil {
			message += fmt.Sprintf(" in %d ms", *result.LatencyMS)
		}

		return newSystemCheck("proxy_credentials", "passed",
			message+"; only a real scrape proves end-to-end proxy access", started)
	}
	message := fmt.Sprintf("Proxy %s test returned status %s", candidate.MaskedURL, result.Status)
	if result.Error != "" {
		message += ": " + redactedDiagnosticError(errors.New(result.Error))
	}

	return newSystemCheck("proxy_credentials", "warning", message, started)
}

func (s *Server) schedulerHeartbeatView(now time.Time) schedulerHeartbeatView {
	last := s.svc.schedulerHeartbeatAt()
	if last == nil {
		status := "stale"
		if s.svc.uptime(now) <= schedulerHeartbeatMaxAge {
			status = "starting"
		}

		return schedulerHeartbeatView{Status: status}
	}
	age := max(time.Duration(0), now.Sub(*last))
	ageSeconds := int64(age.Seconds())
	status := "healthy"
	if age > schedulerHeartbeatMaxAge {
		status = "stale"
	}

	return schedulerHeartbeatView{Status: status, LastHeartbeatAt: last, AgeSeconds: &ageSeconds}
}

func (s *Server) runReachabilityCheck(ctx context.Context, name, target string) systemSelfTestCheck {
	started := time.Now()
	requestContext, cancel := context.WithTimeout(ctx, systemNetworkCheckTimeout)
	defer cancel()
	statusCode, err := s.systemProbe.Reach(requestContext, target)
	if err != nil {
		return newSystemCheck(name, "failed", "Request failed: "+redactedDiagnosticError(err), started)
	}
	if statusCode < 200 || statusCode >= 500 {
		return newSystemCheck(name, "failed", fmt.Sprintf("Endpoint returned HTTP %d", statusCode), started)
	}

	return newSystemCheck(name, "passed", fmt.Sprintf("Endpoint reachable (HTTP %d)", statusCode), started)
}

func newSystemCheck(name, state, message string, started time.Time) systemSelfTestCheck {
	return systemSelfTestCheck{
		Name: name, State: state, Message: message, DurationMS: time.Since(started).Milliseconds(),
	}
}

func parseExplicitNetworkCheck(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "false", "0":
		return false, nil
	case "true", "1":
		return true, nil
	default:
		return false, errors.New("include_network must be true or false")
	}
}

func checkOutputDirectoryWritable(root string) error {
	file, err := os.CreateTemp(root, ".system-self-test-*")
	if err != nil {
		return err
	}
	path := file.Name()
	defer func() { _ = os.Remove(path) }()
	if _, err := file.WriteString("local system self-test\n"); err != nil {
		_ = file.Close()
		return err
	}

	return file.Close()
}

func boundedDirectorySize(ctx context.Context, root string, maximumEntries int) (int64, int, bool, error) {
	var total int64
	entries := 0
	truncated := false
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if maximumEntries > 0 && entries > maximumEntries {
			truncated = true
			return fs.SkipAll
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()

		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return 0, entries, truncated, err
	}

	return total, entries, truncated, nil
}

func redactedDiagnosticError(err error) string {
	if err == nil {
		return "unknown error"
	}
	message := jobruntime.RedactString(err.Error())
	message = strings.Map(func(character rune) rune {
		if character == '\r' || character == '\n' || character == '\t' {
			return ' '
		}
		return character
	}, message)
	if len(message) > 240 {
		message = message[:240] + "..."
	}

	return message
}
