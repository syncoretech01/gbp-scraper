package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type onboardingPageData struct {
	Complete bool
	Checks   []onboardingCheck
	Notice   string
}

type onboardingCheck struct {
	Label   string
	State   string
	Message string
}

func (s *Server) onboardingPage(w http.ResponseWriter, r *http.Request) {
	settings, err := s.svc.LoadSettings(r.Context())
	if err != nil {
		http.Error(w, "could not load onboarding state", http.StatusInternalServerError)
		return
	}
	snapshot, err := s.svc.MaintenanceSnapshot(r.Context())
	if err != nil {
		http.Error(w, "could not inspect local workspace", http.StatusInternalServerError)
		return
	}
	checks := []onboardingCheck{
		{Label: "SQLite database", State: healthState(snapshot.Integrity == "ok"), Message: "integrity: " + snapshot.Integrity},
		{Label: "Data directory", State: healthState(directoryExists(s.svc.dataFolder)), Message: s.svc.dataFolder},
		{Label: "HTTP binding", State: healthState(!wildcardBind(s.srv.Addr)), Message: s.srv.Addr},
		{Label: "Docker browser", State: "info", Message: "Chromium and Playwright are installed and checked by the Docker image build; use Run live self-test to verify Maps access."},
	}
	if summary := settings["onboarding.self_test"]; summary != "" {
		checks = append(checks, onboardingCheck{Label: "Last live self-test", State: settings["onboarding.self_test_state"], Message: summary})
	}
	activity, _ := s.appActivity(r)
	s.renderAppPage(w, "onboarding", appPageData{
		Title:     "Setup and help",
		Subtitle:  "Verify the local data path, database, binding, and optional live Maps connectivity.",
		ActiveNav: "onboarding",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page: onboardingPageData{
			Complete: settings["onboarding.completed"] == "true",
			Checks:   checks,
			Notice:   strings.TrimSpace(r.URL.Query().Get("notice")),
		},
	})
}

func healthState(ok bool) string {
	if ok {
		return "success"
	}
	return "error"
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func (s *Server) completeOnboarding(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	if err := s.svc.SaveSettings(r.Context(), map[string]string{
		"onboarding.completed":    "true",
		"onboarding.completed_at": time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		http.Error(w, "could not save onboarding state", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app/dashboard", http.StatusSeeOther)
}

func (s *Server) runOnboardingSelfTest(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	messages := make([]string, 0, 3)
	state := "success"
	if result, err := s.svc.RunIntegrityCheck(r.Context()); err != nil || result != "ok" {
		state = "error"
		messages = append(messages, "database integrity failed")
	} else {
		messages = append(messages, "database integrity ok")
	}
	tempPath := filepath.Join(s.svc.dataFolder, ".self-test-"+time.Now().UTC().Format("20060102150405.000000000"))
	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		state = "error"
		messages = append(messages, "data directory is not writable")
	} else {
		_, writeErr := file.WriteString("local self-test\n")
		closeErr := file.Close()
		removeErr := os.Remove(tempPath)
		if writeErr != nil || closeErr != nil || removeErr != nil {
			state = "error"
			messages = append(messages, "data directory write cleanup failed")
		} else {
			messages = append(messages, "data directory writable")
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodHead, "https://www.google.com/maps?hl=en", http.NoBody)
	response, requestErr := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if requestErr != nil {
		state = "error"
		messages = append(messages, "Maps connectivity failed")
	} else {
		_ = response.Body.Close()
		if response.StatusCode >= 200 && response.StatusCode < 500 {
			messages = append(messages, fmt.Sprintf("Maps reachable (HTTP %d)", response.StatusCode))
		} else {
			state = "error"
			messages = append(messages, fmt.Sprintf("Maps returned HTTP %d", response.StatusCode))
		}
	}
	if err := s.svc.SaveSettings(r.Context(), map[string]string{
		"onboarding.self_test":       strings.Join(messages, "; "),
		"onboarding.self_test_state": state,
		"onboarding.self_test_at":    time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		http.Error(w, "could not save self-test result", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app/onboarding?notice=Self-test+completed", http.StatusSeeOther)
}
