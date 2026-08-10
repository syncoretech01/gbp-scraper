package web

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type systemPageData struct {
	Snapshot   MaintenanceSnapshot
	DataFolder string
	DataBytes  string
	GoVersion  string
	OS         string
	Bind       string
	Notice     string
	Backups    []systemBackupView
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

	dataBytes, _ := directorySize(s.svc.dataFolder)
	page := systemPageData{
		Snapshot:   snapshot,
		DataFolder: s.svc.dataFolder,
		DataBytes:  humanBytes(dataBytes),
		GoVersion:  runtime.Version(),
		OS:         runtime.GOOS + "/" + runtime.GOARCH,
		Bind:       s.srv.Addr,
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

func (s *Server) apiSystemHealth(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.svc.MaintenanceSnapshot(r.Context())
	if err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "health_failed", "Could not inspect local health")
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: snapshot})
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
