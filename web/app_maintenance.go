package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

type systemPageData struct {
	Snapshot       MaintenanceSnapshot
	DataFolder     string
	DataBytes      string
	GoVersion      string
	OS             string
	Bind           string
	Notice         string
	Backups        []systemBackupView
	Resources      systemResourceView
	Scheduler      schedulerHeartbeatView
	Uptime         string
	LastWrite      string
	LastBrowser    string
	ScanDetail     string
	Storage        systemStorageView
	ActiveBrowsers int64
	ActivePages    int64
	WebsiteQueue   int64
	ProxyStatus    string
}

type systemStorageView struct {
	Exports     string
	Screenshots string
	Logs        string
	Backups     string
	Cache       string
	Temporary   string
}

type systemResourceView struct {
	Available     bool
	CPU           string
	LogicalCPUs   int
	MemoryUsed    string
	MemoryTotal   string
	MemoryPercent string
	DiskFree      string
	DiskTotal     string
	DiskPercent   string
}

type systemBackupView struct {
	ID            string
	Kind          string
	State         string
	SchemaVersion int
	Size          string
	Checksum      string
	CreatedAt     string
}

func (s *Server) systemPage(w http.ResponseWriter, r *http.Request) {
	page, activity, err := s.buildSystemPage(r)
	if err != nil {
		http.Error(w, "could not load system information", http.StatusInternalServerError)
		return
	}
	page.Notice = strings.TrimSpace(r.URL.Query().Get("notice"))

	s.renderAppPage(w, "system", appPageData{
		Title:     "System",
		Subtitle:  "Inspect local storage, database integrity, versions, and recoverable backups.",
		ActiveNav: "system",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page:      page,
	})
}

func (s *Server) buildSystemPage(r *http.Request) (systemPageData, appActivity, error) {
	snapshot, err := s.svc.MaintenanceSnapshot(r.Context())
	if err != nil {
		return systemPageData{}, appActivity{}, err
	}
	backups, err := s.svc.ListDatabaseBackups(r.Context(), 100)
	if err != nil {
		return systemPageData{}, appActivity{}, err
	}
	activity, err := s.appActivity(r)
	if err != nil {
		return systemPageData{}, appActivity{}, err
	}

	now := time.Now().UTC()
	diagnosticContext, cancel := context.WithTimeout(r.Context(), systemMetricsTimeout)
	defer cancel()
	resources, resourceErr := s.systemProbe.Resources(diagnosticContext, s.svc.dataFolder)
	database, databaseErr := s.svc.SystemDatabaseSnapshot(diagnosticContext)
	storage, storageErr := s.workspaceStorageUsage(diagnosticContext)
	page := systemPageData{
		Snapshot:    snapshot,
		DataFolder:  s.svc.dataFolder,
		DataBytes:   humanBytes(storage.DataBytes),
		GoVersion:   runtime.Version(),
		OS:          runtime.GOOS + "/" + runtime.GOARCH,
		Bind:        s.srv.Addr,
		Scheduler:   s.schedulerHeartbeatView(now),
		Uptime:      humanDuration(s.svc.uptime(now)),
		LastWrite:   "not recorded",
		LastBrowser: "not recorded",
		ScanDetail:  "not available",
		Storage: systemStorageView{
			Exports: humanBytes(storage.ExportsBytes), Screenshots: humanBytes(storage.ScreenshotsBytes),
			Logs: humanBytes(storage.LogsBytes), Backups: humanBytes(storage.BackupsBytes),
			Cache: humanBytes(storage.CacheBytes), Temporary: humanBytes(storage.TemporaryBytes),
		},
	}
	if resourceErr == nil {
		page.Resources = systemResourceView{
			Available: true, CPU: fmt.Sprintf("%.1f%%", resources.CPUPercent), LogicalCPUs: resources.LogicalCPUs,
			MemoryUsed: humanBytes(int64(resources.MemoryUsedBytes)), MemoryTotal: humanBytes(int64(resources.MemoryTotalBytes)),
			MemoryPercent: fmt.Sprintf("%.1f%%", resources.MemoryUsedPercent),
			DiskFree:      humanBytes(int64(resources.DiskFreeBytes)), DiskTotal: humanBytes(int64(resources.DiskTotalBytes)),
			DiskPercent: fmt.Sprintf("%.1f%%", resources.DiskUsedPercent),
		}
	}
	if databaseErr == nil {
		page.ActiveBrowsers = database.ActiveBrowsers
		page.ActivePages = database.ActivePages
		page.WebsiteQueue = database.WebsiteQueue
		page.ProxyStatus = fmt.Sprintf("%d/%d healthy; %d blocked", database.ProxyHealthy, database.ProxyTotal, database.ProxyBlocked)
		if database.LastWriteAt != nil {
			page.LastWrite = database.LastWriteAt.Format(time.RFC3339)
		}
		if database.LastBrowserAt != nil {
			page.LastBrowser = database.LastBrowserAt.Format(time.RFC3339)
		}
	}
	if storageErr == nil {
		page.ScanDetail = fmt.Sprintf("%d entries scanned", storage.ScannedEntries)
		if storage.Truncated {
			page.ScanDetail += " (bounded scan truncated)"
		}
	}
	for _, backup := range backups {
		page.Backups = append(page.Backups, systemBackupView{
			ID:            backup.ID,
			Kind:          backup.Kind,
			State:         backup.State,
			SchemaVersion: backup.SchemaVersion,
			Size:          humanBytes(backup.FileSize),
			Checksum:      shortChecksum(backup.Checksum),
			CreatedAt:     backup.CreatedAt.Format(time.RFC3339),
		})
	}

	return page, activity, nil
}

func directorySize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})

	return total, err
}

func shortChecksum(value string) string {
	if len(value) <= 16 {
		return value
	}
	return value[:16] + "..."
}

func (s *Server) apiSystemIntegrity(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	result, err := s.svc.RunIntegrityCheck(r.Context())
	if err != nil {
		renderSystemActionError(w, "Database integrity check failed")
		return
	}
	s.renderSystemActionSuccess(w, r, "Database integrity check completed: "+result)
}

func (s *Server) apiSystemVacuum(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	if err := s.svc.VacuumDatabase(r.Context()); err != nil {
		renderSystemActionError(w, "Database maintenance failed")
		return
	}
	s.renderSystemActionSuccess(w, r, "Database VACUUM completed")
}

func (s *Server) apiSystemBackup(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	record, err := s.svc.CreateDatabaseBackup(r.Context())
	if err != nil {
		renderSystemActionError(w, "Database backup failed")
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		renderJSON(w, http.StatusCreated, localAPIEnvelope{Data: record})
		return
	}
	http.Redirect(w, r, "/app/system?notice=Backup+created+and+verified", http.StatusSeeOther)
}

type systemCleanupResult struct {
	Action        string `json:"action"`
	RemovedFiles  int    `json:"removed_files"`
	RemovedBytes  int64  `json:"removed_bytes"`
	RetentionDays int    `json:"retention_days,omitempty"`
}

func (s *Server) apiSystemClearCache(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	result, err := s.cleanContainedArtifacts(r.Context(), "cache", 0)
	if err != nil {
		renderSystemActionError(w, "Cache cleanup failed")
		return
	}
	s.renderSystemMaintenanceResult(w, r, result)
}

func (s *Server) apiSystemCleanArtifacts(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_cleanup", "Could not read cleanup options")
		return
	}
	kind := strings.ToLower(strings.TrimSpace(r.FormValue("kind")))
	values, _ := s.svc.LoadSettings(r.Context())
	retentionDays := storagePreferencesFromMap(values).AutomaticCleanupDays
	if raw := strings.TrimSpace(r.FormValue("retention_days")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 3650 {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_cleanup", "Retention days must be between 1 and 3650")
			return
		}
		retentionDays = parsed
	}
	if retentionDays <= 0 {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_cleanup", "Automatic cleanup is disabled; enter a positive retention period")
		return
	}
	result, err := s.cleanContainedArtifacts(r.Context(), kind, retentionDays)
	if err != nil {
		if errors.Is(err, errInvalidSystemCleanup) {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_cleanup", err.Error())
			return
		}
		renderSystemActionError(w, "Old artifact cleanup failed")
		return
	}
	s.renderSystemMaintenanceResult(w, r, result)
}

func (s *Server) apiSystemStopAll(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	jobs, err := s.svc.All(r.Context())
	if err != nil {
		renderSystemActionError(w, "Could not inspect jobs")
		return
	}
	stopped := 0
	for _, job := range jobs {
		runtimeState, runtimeErr := s.svc.GetRuntime(r.Context(), job.ID)
		if runtimeErr != nil || !lifecycleControlAllowed(runtimeState, jobruntime.ControlCancel) {
			continue
		}
		if _, _, controlErr := s.svc.ApplyControl(r.Context(), job.ID, jobruntime.ControlCancel); controlErr == nil {
			stopped++
		}
	}
	s.renderSystemActionSuccess(w, r, fmt.Sprintf("Stop requested for %d job(s)", stopped))
}

func (s *Server) downloadSystemDiagnostics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), systemMetricsTimeout)
	defer cancel()
	database, _ := s.svc.SystemDatabaseSnapshot(ctx)
	resources, _ := s.systemProbe.Resources(ctx, s.svc.dataFolder)
	storage, _ := s.workspaceStorageUsage(ctx)
	settings, _ := s.svc.LoadSettings(ctx)
	settings = redactDiagnosticSettings(settings)
	payload := map[string]any{
		"generated_at": time.Now().UTC(), "application": "google-maps-scraper-local",
		"go_version": runtime.Version(), "operating_system": runtime.GOOS,
		"architecture": runtime.GOARCH, "binding": s.srv.Addr,
		"database": database, "resources": resources, "storage": storage,
		"scheduler": s.schedulerHeartbeatView(time.Now().UTC()), "settings": settings,
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		renderSystemActionError(w, "Could not create diagnostics")
		return
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	report, err := archive.Create("diagnostics.json")
	if err == nil {
		_, err = report.Write(encoded)
	}
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		renderSystemActionError(w, "Could not create diagnostics archive")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=gmaps-diagnostics-"+time.Now().UTC().Format("20060102T150405Z")+".zip")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(buffer.Bytes())
}

func (s *Server) apiSystemUpdateInfo(w http.ResponseWriter, r *http.Request) {
	moduleVersion := "development build"
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		moduleVersion = info.Main.Version
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]any{
		"installed_version": moduleVersion,
		"latest_version":    "not checked",
		"offline_safe":      true,
		"message":           "No network request was made. Use the repository release page when you choose to check for updates.",
	}})
}

func (s *Server) renderSystemMaintenanceResult(w http.ResponseWriter, r *http.Request, result systemCleanupResult) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: result})
		return
	}
	message := fmt.Sprintf("Removed %d safe artifact file(s) (%s)", result.RemovedFiles, humanBytes(result.RemovedBytes))
	http.Redirect(w, r, "/app/system?notice="+url.QueryEscape(message), http.StatusSeeOther)
}

var errInvalidSystemCleanup = errors.New("cleanup kind must be cache, screenshots, exports, logs, or temporary")

func (s *Server) cleanContainedArtifacts(ctx context.Context, kind string, retentionDays int) (systemCleanupResult, error) {
	values, _ := s.svc.LoadSettings(ctx)
	preferences := storagePreferencesFromMap(values)
	relative := ""
	switch kind {
	case "cache":
		relative = "map-tiles"
	case "screenshots":
		relative = preferences.ScreenshotsDirectory
	case "exports":
		relative = preferences.ExportsDirectory
	case "logs":
		relative = preferences.LogsDirectory
	case "temporary":
		relative = preferences.TemporaryDirectory
	default:
		return systemCleanupResult{}, errInvalidSystemCleanup
	}
	root, err := safeDataPath(s.svc.dataFolder, relative)
	if err != nil {
		return systemCleanupResult{}, err
	}
	result := systemCleanupResult{Action: kind, RetentionDays: retentionDays}
	cutoff := time.Now().UTC().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if retentionDays > 0 && !info.ModTime().UTC().Before(cutoff) {
			return nil
		}
		if kind == "exports" {
			relativePath, relativeErr := filepath.Rel(s.svc.dataFolder, path)
			if relativeErr != nil || s.exportArtifactRegistered(ctx, filepath.ToSlash(relativePath)) {
				return nil
			}
		}
		if removeErr := os.Remove(path); removeErr != nil {
			return removeErr
		}
		result.RemovedFiles++
		result.RemovedBytes += info.Size()
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return result, err
}

func (s *Server) exportArtifactRegistered(ctx context.Context, relativePath string) bool {
	records, err := s.svc.ListExports(ctx, 500)
	if errors.Is(err, ErrExportStoreUnsupported) {
		return false
	}
	if err != nil {
		return true
	}
	for _, record := range records {
		if record.RelativePath == relativePath {
			return true
		}
	}
	return false
}

func redactDiagnosticSettings(values map[string]string) map[string]string {
	redacted := make(map[string]string, len(values))
	for key, value := range values {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "password") || strings.Contains(lower, "secret") ||
			strings.Contains(lower, "token") || strings.Contains(lower, "key") ||
			strings.Contains(lower, "proxy") || strings.Contains(lower, "webhook") {
			redacted[key] = jobruntime.RedactedValue
			continue
		}
		redacted[key] = jobruntime.RedactString(value)
	}
	return redacted
}

func (s *Server) renderSystemActionSuccess(w http.ResponseWriter, r *http.Request, message string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]string{"message": message}})
		return
	}
	http.Redirect(w, r, "/app/system?notice=Maintenance+completed", http.StatusSeeOther)
}

func renderSystemActionError(w http.ResponseWriter, message string) {
	renderLocalAPIError(w, http.StatusInternalServerError, "maintenance_failed", message)
}

func (s *Server) downloadSystemBackup(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" || strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		http.Error(w, "invalid backup id", http.StatusUnprocessableEntity)
		return
	}
	record, path, err := s.svc.GetDatabaseBackup(r.Context(), id)
	if err != nil {
		http.Error(w, "backup not found", http.StatusNotFound)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.Error(w, "backup file unavailable", http.StatusNotFound)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", "attachment; filename=\"gmaps-backup-"+record.ID+".db\"")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, file)
}
