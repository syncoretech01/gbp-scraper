package web

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func jsonLogger(buffer *bytes.Buffer) *slog.Logger {
	handler := slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug})

	return slog.New(NewRedactingHandler(handler))
}

func decodeRecord(t *testing.T, buffer *bytes.Buffer) map[string]any {
	t.Helper()

	line := strings.TrimSpace(buffer.String())
	if line == "" {
		t.Fatal("no log record was written")
	}

	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("log record is not JSON: %v (%q)", err, line)
	}

	return record
}

func TestStructuredRecordsCarryTheirAttributes(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := jsonLogger(&buffer)

	logger.LogAttrs(context.Background(), slog.LevelInfo, "task finished",
		slog.String("job_id", "job-1"),
		slog.String("task_key", "t-9"),
		slog.Int("rows_added", 18),
	)

	record := decodeRecord(t, &buffer)
	if record["msg"] != "task finished" {
		t.Fatalf("msg = %v", record["msg"])
	}

	if record["job_id"] != "job-1" || record["task_key"] != "t-9" {
		t.Fatalf("record lost its identity attributes: %#v", record)
	}

	if record["rows_added"] != float64(18) {
		t.Fatalf("rows_added = %v, want a real number rather than a formatted sentence", record["rows_added"])
	}
}

// TestSensitiveAttributesAreRedactedByTheHandler is the invariant that keeps a
// future caller from leaking a credential by adding an attribute.
func TestSensitiveAttributesAreRedactedByTheHandler(t *testing.T) {
	t.Parallel()

	secret := "http://user:hunter2@proxy.internal:8080"

	for _, key := range []string{"proxy", "proxy_url", "password", "token", "api_key", "dsn"} {
		var buffer bytes.Buffer
		logger := jsonLogger(&buffer)

		logger.LogAttrs(context.Background(), slog.LevelWarn, "proxy failed", slog.String(key, secret))

		record := decodeRecord(t, &buffer)
		if value, _ := record[key].(string); strings.Contains(value, "hunter2") {
			t.Fatalf("attribute %q leaked a credential: %q", key, value)
		}
	}
}

func TestCredentialsInMessagesAndValuesAreRedacted(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := jsonLogger(&buffer)

	logger.LogAttrs(context.Background(), slog.LevelError,
		"dial http://user:hunter2@proxy.internal:8080 failed",
		slog.String("endpoint", "http://user:hunter2@proxy.internal:8080"),
	)

	record := decodeRecord(t, &buffer)
	if strings.Contains(record["msg"].(string), "hunter2") {
		t.Fatalf("message leaked a credential: %v", record["msg"])
	}

	if strings.Contains(record["endpoint"].(string), "hunter2") {
		t.Fatalf("string attribute leaked a credential: %v", record["endpoint"])
	}
}

func TestRedactionSurvivesGroupsAndWithAttrs(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := jsonLogger(&buffer).With(slog.String("proxy", "http://user:hunter2@host:1"))

	logger.LogAttrs(context.Background(), slog.LevelInfo, "grouped",
		slog.Group("network", slog.String("password", "hunter2")),
	)

	if strings.Contains(buffer.String(), "hunter2") {
		t.Fatalf("redaction did not survive WithAttrs/Group: %s", buffer.String())
	}
}

func TestProcessLoggerLevelAndFormatAreConfigurable(t *testing.T) {
	t.Setenv(logLevelEnv, "error")
	t.Setenv(logFormatEnv, "json")

	var buffer bytes.Buffer
	logger := NewProcessLogger(&buffer)

	logger.Info("suppressed")

	if buffer.Len() != 0 {
		t.Fatalf("info record was emitted at error level: %s", buffer.String())
	}

	logger.Error("kept")

	record := decodeRecord(t, &buffer)
	if record["msg"] != "kept" {
		t.Fatalf("record = %#v", record)
	}
}

func TestParseLogLevelDefaultsToInfo(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]slog.Level{
		"debug": slog.LevelDebug,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
		"":      slog.LevelInfo,
		"nope":  slog.LevelInfo,
	} {
		if got := parseLogLevel(name); got != want {
			t.Fatalf("parseLogLevel(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestJobLoggerBindsTheJobIdentity(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := JobLogger(jsonLogger(&buffer), "job-42")

	logger.Info("stage complete")

	record := decodeRecord(t, &buffer)
	if record["job_id"] != "job-42" {
		t.Fatalf("job identity missing: %#v", record)
	}
}

// TestLogJobEventWritesTheStructuredRecordWithoutAStore proves the durable
// write is best-effort: a workspace that cannot store events still logs.
func TestLogJobEventWritesTheStructuredRecordWithoutAStore(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := jsonLogger(&buffer)

	var service *Service
	service.LogJobEvent(context.Background(), logger, "job-7", "task-complete", "information",
		"finished a task", slog.Int("rows_added", 3))

	record := decodeRecord(t, &buffer)
	if record["job_id"] != "job-7" || record["event"] != "task-complete" {
		t.Fatalf("record = %#v", record)
	}

	if record["rows_added"] != float64(3) {
		t.Fatalf("attribute lost: %#v", record)
	}
}
