package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The privacy panel must report what is true in this process. Restating the
// shipped Compose configuration would reassure an operator who started the
// workspace by hand without the telemetry switch.

func TestPrivacyStatusReportsTheLiveTelemetrySwitch(t *testing.T) {
	server := newMaintenanceActionServer(t, t.TempDir())

	t.Setenv(telemetryEnvironmentVariable, "1")
	status := server.privacyStatus(t.Context())
	if !status.TelemetryDisabled || !strings.Contains(status.TelemetrySource, "=1 is set") {
		t.Fatalf("telemetry status with the switch set = %+v", status)
	}

	t.Setenv(telemetryEnvironmentVariable, "")
	status = server.privacyStatus(t.Context())
	if status.TelemetryDisabled {
		t.Fatalf("telemetry reported as disabled without the switch: %+v", status)
	}
	if !strings.Contains(status.TelemetrySource, "not set to 1") {
		t.Fatalf("telemetry source does not explain the live state: %q", status.TelemetrySource)
	}
	if len(status.EncryptedSecrets) < 2 {
		t.Fatalf("encrypted-secret inventory = %v", status.EncryptedSecrets)
	}
	if status.BrowserProfiles == "" {
		t.Fatal("the privacy panel does not report the browser profile size")
	}
}

// The Settings page must show the configured directories with their measured
// size and retention rule, and must render the live privacy inventory.
func TestSettingsPageShowsDirectoryUsageAndLivePrivacyState(t *testing.T) {
	t.Parallel()

	server := newLocalAIHandlersServer(t)

	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app/settings", http.NoBody))

	if recorder.Code != http.StatusOK {
		t.Fatalf("settings page = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	if !strings.Contains(body, "directory-usage") {
		t.Fatal("the settings page does not show per-directory usage")
	}
	for _, field := range []string{
		"exports_directory", "screenshots_directory", "logs_directory",
		"backups_directory", "temporary_directory",
	} {
		if !strings.Contains(body, `name="`+field+`"`) {
			t.Fatalf("the settings page does not let an operator configure %s", field)
		}
	}
	for _, phrase := range []string{"Encrypted at rest", "Browser profiles", "Secrets are redacted", "Telemetry"} {
		if !strings.Contains(body, phrase) {
			t.Fatalf("the privacy panel does not mention %q", phrase)
		}
	}
	if !strings.Contains(body, "-data-folder") {
		t.Fatal("the settings page does not explain where the database directory comes from")
	}
}
