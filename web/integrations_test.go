package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestLocalWebhookDeliveryIsBoundedToLocalAddressesAndCarriesTheEventEnvelope(t *testing.T) {
	t.Parallel()

	var envelope webhookEnvelope
	var signature, timestamp string
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request = %s, content-type = %q", r.Method, r.Header.Get("Content-Type"))
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maximumIntegrationPayload))
		if err != nil || json.Unmarshal(body, &envelope) != nil {
			t.Errorf("invalid event body: %s (%v)", body, err)
		}
		signature = r.Header.Get(webhookSignatureHeader)
		timestamp = r.Header.Get(webhookTimestampHeader)
		if r.Header.Get(webhookEventHeader) != IntegrationEventExportCompleted {
			t.Errorf("event header = %q", r.Header.Get(webhookEventHeader))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer webhook.Close()

	secret := strings.Repeat("s", 32)
	record := ExportRecord{
		ID: "export-id", Name: "Dentists", Format: "xlsx", RecordCount: 37,
		FileSize: 2048, Checksum: strings.Repeat("a", 64), CreatedAt: time.Now().UTC(),
	}
	configuration := integrationConfiguration{
		URL: webhook.URL + "/events?access_token=secret", Secret: secret,
		Events: []string{IntegrationEventExportCompleted},
	}
	event := integrationEvent{
		Name: IntegrationEventExportCompleted, SubjectID: record.ID,
		OccurredAt: time.Now().UTC(), Data: exportEventData(record),
	}
	if err := deliverLocalWebhook(context.Background(), configuration, event); err != nil {
		t.Fatal(err)
	}
	if envelope.Event != IntegrationEventExportCompleted || envelope.Delivery != record.ID || envelope.Version != webhookPayloadVersion {
		t.Fatalf("envelope = %+v", envelope)
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok || data["export_id"] != record.ID {
		t.Fatalf("envelope data = %+v", envelope.Data)
	}

	// The signature must cover the timestamp and the exact delivered body, so a
	// receiver can reject a replay that only rewrites the timestamp header.
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	unix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		t.Fatalf("timestamp header = %q", timestamp)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(payload)
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); signature != want {
		t.Fatalf("signature = %q, want %q", signature, want)
	}
	if signWebhookPayload(secret, unix+1, payload) == signature {
		t.Fatal("signature does not depend on the timestamp")
	}

	for _, rawURL := range []string{
		"http://8.8.8.8/hook",
		"http://169.254.169.254/latest/meta-data",
		"file:///tmp/hook",
		"http://user:password@127.0.0.1/hook",
	} {
		if _, err := validateLocalWebhookURL(rawURL); err == nil {
			t.Fatalf("unsafe webhook URL %q was accepted", rawURL)
		}
	}
	if permittedLocalIntegrationIP(net.ParseIP("169.254.169.254")) ||
		!permittedLocalIntegrationIP(net.ParseIP("127.0.0.1")) ||
		!permittedLocalIntegrationIP(net.ParseIP("10.0.0.5")) {
		t.Fatal("local webhook IP policy is incorrect")
	}
}

func TestWebhookRetriesTransientFailuresAndStopsOnPermanentRejection(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int64
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)

			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer flaky.Close()

	configuration := integrationConfiguration{URL: flaky.URL, Events: []string{IntegrationEventExportCompleted}}
	event := integrationEvent{Name: IntegrationEventExportCompleted, OccurredAt: time.Now().UTC()}
	if err := deliverLocalWebhook(context.Background(), configuration, event); err != nil {
		t.Fatalf("retried delivery failed: %v", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}

	var rejected atomic.Int64
	permanent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rejected.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer permanent.Close()

	configuration.URL = permanent.URL
	if err := deliverLocalWebhook(context.Background(), configuration, event); err == nil {
		t.Fatal("a permanently rejected delivery must fail")
	}
	if rejected.Load() != 1 {
		t.Fatalf("permanent rejection attempts = %d, want 1", rejected.Load())
	}
	if webhookBackoff(1) >= webhookBackoff(2) || webhookBackoff(99) != webhookMaximumBackoff {
		t.Fatalf("backoff is not bounded and increasing: %v %v %v",
			webhookBackoff(1), webhookBackoff(2), webhookBackoff(99))
	}
}

func TestWebhookSubscriptionsGateEventDelivery(t *testing.T) {
	t.Parallel()

	var delivered atomic.Int64
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		delivered.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer listener.Close()

	configuration := integrationConfiguration{URL: listener.URL, Events: []string{IntegrationEventJobFailed}}
	err := deliverLocalWebhook(context.Background(), configuration,
		integrationEvent{Name: IntegrationEventJobCompleted, OccurredAt: time.Now().UTC()})
	if err == nil || !strings.Contains(err.Error(), "does not handle") {
		t.Fatalf("unsubscribed event error = %v", err)
	}
	if delivered.Load() != 0 {
		t.Fatalf("unsubscribed event was delivered %d times", delivered.Load())
	}
	if err := deliverLocalWebhook(context.Background(), configuration,
		integrationEvent{Name: IntegrationEventJobFailed, OccurredAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if delivered.Load() != 1 {
		t.Fatalf("subscribed event delivered %d times, want 1", delivered.Load())
	}
}

func TestIntegrationConfigurationValidationHidesSecretsAndRejectsUnsafeTargets(t *testing.T) {
	t.Parallel()

	if _, _, err := validateIntegrationConfiguration(IntegrationWebhook, integrationConfiguration{
		URL: "http://user@127.0.0.1:5678/webhook", Secret: strings.Repeat("k", 24),
		Events: []string{IntegrationEventJobCompleted},
	}); err == nil {
		t.Fatal("a webhook URL carrying user information must be rejected")
	}

	public, secret, err := validateIntegrationConfiguration(IntegrationWebhook, integrationConfiguration{
		URL: "http://127.0.0.1:5678/webhook/gmaps", Secret: strings.Repeat("k", 24),
		Events: []string{IntegrationEventJobCompleted, IntegrationEventJobCompleted},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(public, "kkkk") {
		t.Fatalf("public webhook configuration leaks the shared secret: %s", public)
	}
	if !strings.Contains(secret, "kkkk") {
		t.Fatalf("stored webhook configuration lost the shared secret: %s", secret)
	}
	record := IntegrationRecord{Configuration: public}
	if !record.Signed() {
		t.Fatal("a configured shared secret must be visible as a signed marker")
	}
	if events := record.Events(); len(events) != 1 || events[0] != IntegrationEventJobCompleted {
		t.Fatalf("stored events = %v", events)
	}

	if _, _, err := validateIntegrationConfiguration(IntegrationWebhook, integrationConfiguration{
		URL: "http://127.0.0.1:5678/webhook", Secret: "short",
	}); err == nil {
		t.Fatal("a short shared secret must be rejected")
	}
	if _, _, err := validateIntegrationConfiguration("command", integrationConfiguration{}); err == nil {
		t.Fatal("command execution destinations must not be accepted")
	}
	if _, _, err := validateIntegrationConfiguration(IntegrationWebhook, integrationConfiguration{
		URL: "http://127.0.0.1:5678/webhook", Events: []string{"job.exploded"},
	}); err == nil {
		t.Fatal("an unknown event must be rejected")
	}
}

func TestWatchFolderDeliveryStaysInsideDataDirectory(t *testing.T) {
	t.Parallel()

	dataFolder := t.TempDir()
	server := &Server{svc: NewService(nil, dataFolder)}
	source := filepath.Join(dataFolder, "exports", "result.csv")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("name\nDentist\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := server.deliverWatchFolder("n8n-inbox", source); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dataFolder, integrationOutboxDirectory, "n8n-inbox", "result.csv")
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "name\nDentist\n" {
		t.Fatalf("watch-folder output = %q, %v", contents, err)
	}
	for _, name := range []string{"../outside", "a/b", "", ".."} {
		if _, err := validateWatchFolderName(name); err == nil {
			t.Fatalf("unsafe watch folder %q was accepted", name)
		}
	}
}

func TestSQLiteDatabaseDestinationLoadsACSVExportInsideTheDataDirectory(t *testing.T) {
	t.Parallel()

	dataFolder := t.TempDir()
	server := &Server{svc: NewService(nil, dataFolder)}
	source := filepath.Join(dataFolder, "exports", "leads.csv")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("Name,City\nDentist A,San Francisco\nDentist B,Oakland\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	configuration := integrationConfiguration{
		Driver: databaseDriverSQLite, Target: "destinations/leads.sqlite", Table: "leads",
	}
	record := ExportRecord{ID: "export-id", Format: "csv"}
	if err := server.deliverDatabaseDestination(context.Background(), configuration, record, source); err != nil {
		t.Fatal(err)
	}

	database, err := sql.Open("sqlite", filepath.Join(dataFolder, "destinations", "leads.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var loaded int
	if err := database.QueryRow("SELECT COUNT(*) FROM leads").Scan(&loaded); err != nil {
		t.Fatal(err)
	}
	if loaded != 2 {
		t.Fatalf("loaded rows = %d, want 2", loaded)
	}

	// An unsupported source format must be refused with an explanation rather
	// than silently producing an empty destination table.
	err = server.deliverDatabaseDestination(context.Background(), configuration, ExportRecord{Format: "kml"}, source)
	if err == nil || !strings.Contains(err.Error(), "kml") {
		t.Fatalf("unsupported source format error = %v", err)
	}
}

func TestDatabaseDestinationValidationContainsPathsAndHidesCredentials(t *testing.T) {
	t.Parallel()

	// The last three are rejected on every host, not only the one whose
	// filepath rules happen to call them absolute.
	for _, target := range []string{
		"", "../escape.sqlite", "/etc/passwd.db", "notes.txt",
		"C:/leads.sqlite", `C:\leads.sqlite`, "//server/share/leads.sqlite",
	} {
		if _, err := validateSQLiteDestinationPath(target); err == nil {
			t.Fatalf("unsafe SQLite destination %q was accepted", target)
		}
	}
	if _, err := validateDestinationTable("1bad"); err == nil {
		t.Fatal("a table name starting with a digit was accepted")
	}
	if _, err := validateDestinationTable("drop table x; --"); err == nil {
		t.Fatal("a table name containing SQL punctuation was accepted")
	}
	for _, dsn := range []string{
		"postgres://user:pass@203.0.113.9:5432/leads",
		"mysql://user:pass@127.0.0.1:3306/leads",
		"postgres://user:pass@127.0.0.1:5432",
	} {
		if _, _, err := validateLocalPostgresDSN(dsn); err == nil {
			t.Fatalf("unsafe PostgreSQL DSN %q was accepted", dsn)
		}
	}
	public, secret, err := validateIntegrationConfiguration(IntegrationDatabase, integrationConfiguration{
		Driver: databaseDriverPostgres, DSN: "postgres://user:hunter2@127.0.0.1:5432/leads", Table: "leads",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(public, "hunter2") {
		t.Fatalf("public database configuration leaks credentials: %s", public)
	}
	if !strings.Contains(secret, "hunter2") {
		t.Fatalf("stored database configuration lost the DSN: %s", secret)
	}
}
