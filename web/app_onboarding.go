package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
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
		onboardingDiskCheck(r.Context(), s.svc.dataFolder),
		{Label: "HTTP binding", State: healthState(!wildcardBind(s.srv.Addr)), Message: s.srv.Addr},
		onboardingBrowserCheck(),
		s.onboardingProxyCheck(r.Context()),
		{Label: "Internet access", State: "info", Message: "Checked on demand so the first-run page never reaches the network by itself; use Run live self-test."},
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

// onboardingBrowserCheck reuses the System self-test's Playwright probe so the
// first-run checklist reports the same driver state the diagnostics page does,
// rather than an unconditional "installed by Docker" claim.
func onboardingBrowserCheck() onboardingCheck {
	check := browserRuntimeCheck(time.Now())
	state := "warning"
	if check.State == "passed" {
		state = "success"
	}

	return onboardingCheck{Label: "Browser runtime", State: state, Message: check.Message}
}

// onboardingProxyCheck reports the optional proxy configuration without
// touching the network: a first-run page must never dial out by itself.
func (s *Server) onboardingProxyCheck(ctx context.Context) onboardingCheck {
	proxies, err := s.svc.ListProxies(ctx, "")
	if err != nil {
		return onboardingCheck{
			Label:   "Proxies (optional)",
			State:   "info",
			Message: "No local proxy storage is configured; scrapes use the direct connection",
		}
	}
	enabled := 0
	for _, proxy := range proxies {
		if proxy.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return onboardingCheck{
			Label:   "Proxies (optional)",
			State:   "info",
			Message: "No enabled proxies; scrapes use the direct connection. Add a pool later from the Proxies page.",
		}
	}

	return onboardingCheck{
		Label: "Proxies (optional)",
		State: "success",
		Message: fmt.Sprintf("%d of %d stored proxies enabled; credentials are tested by the live self-test",
			enabled, len(proxies)),
	}
}

func healthState(ok bool) string {
	if ok {
		return "success"
	}
	return "error"
}

// onboardingMinimumFreeDiskBytes is the free-space level (2 GB) below which
// the setup checklist warns that scrape results, exports, and backups may not
// fit in the data folder.
const onboardingMinimumFreeDiskBytes uint64 = 2 << 30

// onboardingDiskCheck reports free disk capacity for the data folder's volume
// using the same gopsutil probe as the System diagnostics page.
func onboardingDiskCheck(ctx context.Context, dataFolder string) onboardingCheck {
	usage, err := disk.UsageWithContext(ctx, dataFolder)
	if err != nil {
		return onboardingCheck{
			Label:   "Disk capacity",
			State:   "error",
			Message: "free disk space could not be read for " + dataFolder,
		}
	}
	return diskCapacityCheck(usage.Free, usage.Total)
}

// diskCapacityCheck classifies the data volume's free space: below 2 GB it
// warns rather than fails, because scraping still works until the disk is
// actually full.
func diskCapacityCheck(freeBytes, totalBytes uint64) onboardingCheck {
	message := fmt.Sprintf("%s free of %s (%d bytes free)",
		humanBytes(int64(freeBytes)), humanBytes(int64(totalBytes)), freeBytes)
	if freeBytes < onboardingMinimumFreeDiskBytes {
		return onboardingCheck{
			Label: "Disk capacity",
			State: "warning",
			Message: message + "; below the recommended 2 GB minimum for scrape results, " +
				"exports, and backups",
		}
	}
	return onboardingCheck{Label: "Disk capacity", State: "success", Message: message}
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
	diskCheck := onboardingDiskCheck(r.Context(), s.svc.dataFolder)
	switch diskCheck.State {
	case "error":
		state = "error"
	case "warning":
		if state == "success" {
			state = "warning"
		}
	}
	messages = append(messages, "disk: "+diskCheck.Message)

	// The browser driver is reported from the same probe the System self-test
	// uses; a missing driver is a warning because it is installed on the first
	// scrape rather than a hard failure now.
	browser := browserRuntimeCheck(time.Now())
	if browser.State != "passed" && state == "success" {
		state = "warning"
	}
	messages = append(messages, "browser: "+browser.Message)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Internet access is checked separately from Maps so a blocked Maps
	// endpoint is not reported as a missing internet connection.
	internet := s.runReachabilityCheck(ctx, "internet_reachable", internetReachabilityTarget)
	if internet.State != "passed" {
		state = "error"
	}
	messages = append(messages, "internet: "+internet.Message)
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
