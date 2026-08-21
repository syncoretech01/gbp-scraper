package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The maintenance actions all mutate local state, so every one of them must be
// CSRF-protected, contained inside the data directory, and honest about what it
// actually did.

func newMaintenanceActionServer(t *testing.T, dataFolder string) *Server {
	t.Helper()

	server, err := New(NewService(&openAPIRouteRepository{}, dataFolder), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return server
}

func TestClearBrowserProfilesRemovesOnlyTheContainedCache(t *testing.T) {
	t.Parallel()

	dataFolder := t.TempDir()
	profiles := filepath.Join(dataFolder, browserProfileDirectory, "worker-1")
	if err := os.MkdirAll(profiles, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profiles, "Cookies"), []byte("session"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A business artifact outside the profile directory must survive.
	keep := filepath.Join(dataFolder, "exports", "leads.csv")
	if err := os.MkdirAll(filepath.Dir(keep), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("name\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := newMaintenanceActionServer(t, dataFolder)

	// Without the CSRF token the action must be refused outright.
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder,
		httptest.NewRequest(http.MethodPost, "/api/v1/system/browser-profiles/clear", http.NoBody))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated clear = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if _, err := os.Stat(filepath.Join(profiles, "Cookies")); err != nil {
		t.Fatal("a refused request still deleted browser profile data")
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/system/browser-profiles/clear", http.NoBody)
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	request.Header.Set("Accept", "application/json")

	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("clear browser profiles = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dataFolder, browserProfileDirectory, "worker-1")); !os.IsNotExist(err) {
		t.Fatalf("browser profile directory survived the clear: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("clearing browser profiles removed an unrelated artifact: %v", err)
	}
}

func TestPrepareRestoreRejectsAnUnknownBackupAndRequiresCSRF(t *testing.T) {
	t.Parallel()

	server := newMaintenanceActionServer(t, t.TempDir())

	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder,
		httptest.NewRequest(http.MethodPost, "/api/v1/system/backups/missing-backup/restore", http.NoBody))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated restore = %d, want %d", recorder.Code, http.StatusForbidden)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/system/backups/missing-backup/restore", http.NoBody)
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	request.Header.Set("Accept", "application/json")

	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("restore of an unknown backup = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

// The System page must show every storage directory with a size and the
// retention rule that governs it, which is the specification's requirement for
// separate, configurable, and measurable directories.
func TestSystemPageListsEveryStorageDirectoryWithRetention(t *testing.T) {
	t.Parallel()

	server := newMaintenanceActionServer(t, t.TempDir())

	directories := server.systemDirectoryViews(t.Context(), workspaceStorageSnapshot{})
	if len(directories) < 8 {
		t.Fatalf("storage directory views = %d, want every configurable directory", len(directories))
	}
	for _, directory := range directories {
		if directory.Label == "" || directory.Path == "" || directory.Size == "" || directory.Retention == "" {
			t.Fatalf("storage directory view is incomplete: %+v", directory)
		}
	}

	recorder := httptest.NewRecorder()
	server.renderAppPage(recorder, "system", appPageData{
		Title: "System", ActiveNav: "system", Theme: "system", CSRFToken: server.csrfToken,
		Page: systemPageData{Directories: directories, BrowserProfile: "0 B"},
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("system page = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	for _, label := range []string{
		"Database", "Exports", "Screenshots", "Logs", "Backups",
		"Map tile cache", "Browser profiles", "Temporary files",
	} {
		if !strings.Contains(body, ">"+label+"</td>") {
			t.Fatalf("the storage directory table omits %q", label)
		}
	}
	for _, action := range []string{
		"/api/v1/system/browser-profiles/clear",
		"/api/v1/system/worker/recycle",
		"/api/v1/system/backups",
		"/api/v1/system/vacuum",
		"/api/v1/system/integrity",
		"/api/v1/system/cache/clear",
		"/api/v1/system/artifacts/cleanup",
		"/api/v1/system/jobs/stop-all",
		"/api/v1/system/diagnostics/download",
	} {
		if !strings.Contains(body, action) {
			t.Fatalf("the system page does not expose %s", action)
		}
	}
	if !strings.Contains(body, `name="passphrase"`) {
		t.Fatal("the system page does not offer an encrypted backup")
	}
}
