package web

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalWebhookDeliveryIsBoundedToLocalAddresses(t *testing.T) {
	t.Parallel()

	var event integrationDeliveryEvent
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request = %s, content-type = %q", r.Method, r.Header.Get("Content-Type"))
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maximumIntegrationPayload))
		if err != nil || json.Unmarshal(body, &event) != nil {
			t.Errorf("invalid event body: %s (%v)", body, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer webhook.Close()

	record := ExportRecord{
		ID: "export-id", Name: "Dentists", Format: "xlsx", RecordCount: 37,
		FileSize: 2048, Checksum: strings.Repeat("a", 64), CreatedAt: time.Now().UTC(),
	}
	if err := deliverLocalWebhook(context.Background(), webhook.URL+"/events?access_token=secret", record); err != nil {
		t.Fatal(err)
	}
	if event.Event != "export.completed" || event.ExportID != record.ID || event.RecordCount != 37 {
		t.Fatalf("event = %+v", event)
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
	destination := filepath.Join(dataFolder, "integrations-outbox", "n8n-inbox", "result.csv")
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

func TestCommandHooksRequireExplicitEnablementAndAllowlist(t *testing.T) {
	t.Setenv(commandHooksEnableEnv, "")
	t.Setenv(commandHooksAllowlistEnv, "")
	err := validateCommandConfiguration(integrationConfiguration{Executable: filepath.Join(t.TempDir(), "hook")})
	if err == nil || !strings.Contains(err.Error(), commandHooksEnableEnv) {
		t.Fatalf("disabled command hook error = %v", err)
	}
}

func TestBoundedHookWriterLimitsCapturedOutput(t *testing.T) {
	t.Parallel()

	writer := &boundedHookWriter{remaining: 4}
	value := []byte("abcdefgh")
	written, err := writer.Write(value)
	if err != nil || written != len(value) || writer.String() != "abcd" {
		t.Fatalf("Write() = %d, %v; output = %q", written, err, writer.String())
	}
}
