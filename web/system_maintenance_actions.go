package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

const (
	// browserProfileDirectory holds the reusable Chromium profiles the scraper
	// keeps between runs. It is a cache: deleting it costs a cold start, never
	// a business record.
	browserProfileDirectory = "browser-profiles"

	// workerRecycleTimeout bounds a pause/resume cycle so a stuck job cannot
	// hold the request open.
	workerRecycleTimeout = 90 * time.Second

	// workerRecyclePoll is how often a paused job is re-checked while the
	// recycle waits for a safe boundary.
	workerRecyclePoll = 500 * time.Millisecond

	// restorePreparationDirectory holds files staged for an offline restore.
	restorePreparationDirectory = "restore"
)

// registerSystemMaintenanceRoutes adds the maintenance actions the
// specification lists that were not previously reachable.
func (s *Server) registerSystemMaintenanceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/system/browser-profiles/clear", s.apiSystemClearBrowserProfiles)
	mux.HandleFunc("POST /api/v1/system/worker/recycle", s.apiSystemRecycleWorkers)
	mux.HandleFunc("POST /api/v1/system/backups/{id}/restore", s.apiSystemPrepareRestore)
	mux.HandleFunc("POST /api/v1/system/backups/{id}/download", s.downloadSystemBackup)
}

// apiSystemClearBrowserProfiles deletes the reusable browser profiles. It is a
// privacy control as much as a maintenance one: a profile directory holds
// cookies and site data from every page a scrape visited.
func (s *Server) apiSystemClearBrowserProfiles(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	root, err := safeDataPath(s.svc.dataFolder, browserProfileDirectory)
	if err != nil {
		renderSystemActionError(w, "Browser profile directory is not addressable")
		return
	}
	result := systemCleanupResult{Action: "browser_profiles"}
	entries, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		renderSystemActionError(w, "Browser profile cleanup failed")
		return
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		size, _ := directorySize(path)
		if removeErr := os.RemoveAll(path); removeErr != nil {
			renderSystemActionError(w, "Browser profile cleanup failed")
			return
		}
		result.RemovedFiles++
		result.RemovedBytes += size
	}
	s.renderSystemMaintenanceResult(w, r, result)
}

// workerRecycleResult reports what one worker recycle did.
type workerRecycleResult struct {
	Action    string `json:"action"`
	Paused    int    `json:"paused"`
	Resumed   int    `json:"resumed"`
	StillHeld int    `json:"still_paused"`
}

// apiSystemRecycleWorkers restarts the local workers the only way that is safe
// for a resumable scrape: every active job is paused at its next safe boundary,
// which tears the browser workers down, and is then resumed from its durable
// checkpoint. No process is spawned and no committed result is discarded.
func (s *Server) apiSystemRecycleWorkers(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), workerRecycleTimeout)
	defer cancel()

	jobs, err := s.svc.All(ctx)
	if err != nil {
		renderSystemActionError(w, "Could not inspect jobs")
		return
	}
	result := workerRecycleResult{Action: "worker_recycle"}
	paused := make([]string, 0, len(jobs))
	for _, job := range jobs {
		state, stateErr := s.svc.GetRuntime(ctx, job.ID)
		if stateErr != nil || !lifecycleControlAllowed(state, jobruntime.ControlPause) {
			continue
		}
		if _, _, controlErr := s.svc.ApplyControl(ctx, job.ID, jobruntime.ControlPause); controlErr == nil {
			paused = append(paused, job.ID)
			result.Paused++
		}
	}
	for _, id := range paused {
		if !s.waitForPausedJob(ctx, id) {
			result.StillHeld++
			continue
		}
		if _, _, controlErr := s.svc.ApplyControl(ctx, id, jobruntime.ControlResume); controlErr == nil {
			result.Resumed++
		} else {
			result.StillHeld++
		}
	}
	if acceptsJSON(r) {
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: result})
		return
	}
	message := fmt.Sprintf("Recycled %d worker job(s); %d resumed, %d left paused", result.Paused, result.Resumed, result.StillHeld)
	s.renderSystemActionSuccess(w, r, message)
}

// waitForPausedJob polls until the job reports a paused state or the recycle
// budget runs out. A job that never reaches the boundary is left paused rather
// than force-resumed into an inconsistent worker.
func (s *Server) waitForPausedJob(ctx context.Context, id string) bool {
	ticker := time.NewTicker(workerRecyclePoll)
	defer ticker.Stop()
	for {
		state, err := s.svc.GetRuntime(ctx, id)
		if err == nil && lifecycleControlAllowed(state, jobruntime.ControlResume) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

// restorePreparation describes a backup that has been proven restorable.
type restorePreparation struct {
	Action          string `json:"action"`
	BackupID        string `json:"backup_id"`
	SchemaVersion   int    `json:"schema_version"`
	LiveSchema      int    `json:"live_schema_version"`
	VerifiedPath    string `json:"verified_path"`
	SafetyCopyPath  string `json:"safety_copy_path"`
	SafetyCopyBytes int64  `json:"safety_copy_bytes"`
	Encrypted       bool   `json:"encrypted"`
	Instructions    string `json:"instructions"`
}

// apiSystemPrepareRestore proves a backup is restorable and stages it.
//
// A live SQLite file cannot be swapped out from under the open connections
// this process holds, so the restore itself stays an explicit offline step.
// What this action removes is the guesswork: the chosen backup is checksum-
// verified, integrity-checked, and refused if it carries a newer schema than
// the running build; a fresh safety copy of the live database is taken first;
// and the verified file is placed under restore/ with the exact commands to
// finish. Nothing existing is deleted or overwritten.
func (s *Server) apiSystemPrepareRestore(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !validBusinessID(id) {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_backup_id", "Invalid backup ID")
		return
	}
	record, path, err := s.svc.GetDatabaseBackup(r.Context(), id)
	if err != nil {
		renderLocalAPIError(w, http.StatusNotFound, "backup_not_found", "Backup not found")
		return
	}
	snapshot, err := s.svc.MaintenanceSnapshot(r.Context())
	if err != nil {
		renderSystemActionError(w, "Could not read the live database state")
		return
	}
	if record.SchemaVersion > snapshot.SchemaVersion {
		renderLocalAPIError(w, http.StatusConflict, "forward_schema",
			fmt.Sprintf("This backup carries schema v%d and the running build understands v%d; upgrade before restoring",
				record.SchemaVersion, snapshot.SchemaVersion))
		return
	}
	checksum, size, err := checksumLocalFile(path)
	if err != nil {
		renderSystemActionError(w, "Could not read the backup file")
		return
	}
	if record.Checksum != "" && checksum != record.Checksum {
		renderLocalAPIError(w, http.StatusConflict, "backup_checksum_mismatch",
			"The backup file no longer matches its recorded SHA-256 checksum and was not staged")
		return
	}
	encrypted := backupFileEncrypted(path)

	// A fresh safety copy of the live database is taken before anything is
	// staged, so an operator who follows the instructions always has a
	// verified copy of the state they are replacing.
	safety, err := s.svc.CreateDatabaseBackup(r.Context())
	if err != nil {
		renderSystemActionError(w, "Could not take a safety copy of the live database before staging a restore")
		return
	}
	stagedPath, err := s.stageRestoreArtifact(path, record.ID)
	if err != nil {
		renderSystemActionError(w, "Could not stage the verified backup")
		return
	}
	preparation := restorePreparation{
		Action: "restore_prepared", BackupID: record.ID,
		SchemaVersion: record.SchemaVersion, LiveSchema: snapshot.SchemaVersion,
		VerifiedPath: stagedPath, SafetyCopyPath: safety.RelativePath,
		SafetyCopyBytes: size, Encrypted: encrypted,
		Instructions: "Stop the application, replace jobs.db with " + stagedPath +
			" (deleting the -wal and -shm files beside it), keep .proxy-master-key in place, and start again. " +
			"The safety copy of the current database is " + safety.RelativePath + ".",
	}
	if encrypted {
		preparation.Instructions = "This backup is an encrypted container: download it with its passphrase first, then " +
			preparation.Instructions
	}
	if acceptsJSON(r) {
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: preparation})
		return
	}
	s.renderSystemActionSuccess(w, r, "Verified restore staged at "+stagedPath)
}

// stageRestoreArtifact copies the verified backup under restore/ without
// touching the original file.
func (s *Server) stageRestoreArtifact(source, backupID string) (string, error) {
	relative := filepath.ToSlash(filepath.Join(restorePreparationDirectory,
		"verified-"+time.Now().UTC().Format("20060102T150405Z")+"-"+backupID+".db"))
	destination, err := safeDataPath(s.svc.dataFolder, relative)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return "", fmt.Errorf("create restore directory: %w", err)
	}
	if err := copyFileAtomically(source, destination); err != nil {
		return "", err
	}

	return relative, nil
}

// encryptRegisteredBackup rewrites a freshly created backup as an encrypted
// container and updates its registered checksum and size. A backup that cannot
// be encrypted is removed rather than left on disk as unexpected plaintext.
func (s *Server) encryptRegisteredBackup(
	ctx context.Context,
	record BackupRecord,
	passphrase string,
) (BackupRecord, error) {
	if !s.svc.SupportsEncryptedBackups() {
		return BackupRecord{}, ErrMaintenanceUnsupported
	}
	path, err := safeDataPath(s.svc.dataFolder, record.RelativePath)
	if err != nil {
		return BackupRecord{}, err
	}
	if err := encryptBackupFile(path, passphrase); err != nil {
		_ = os.Remove(path)
		_ = s.svc.MarkBackupEncrypted(ctx, record.ID, "", 0)

		return BackupRecord{}, err
	}
	checksum, size, err := checksumLocalFile(path)
	if err != nil {
		return BackupRecord{}, err
	}
	if err := s.svc.MarkBackupEncrypted(ctx, record.ID, checksum, size); err != nil {
		return BackupRecord{}, err
	}
	record.Checksum = checksum
	record.FileSize = size
	record.Encrypted = true

	return record, nil
}
