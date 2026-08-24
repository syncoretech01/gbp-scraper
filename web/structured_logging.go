package web

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// Structured logging for the workspace's own code.
//
// The specification asks for structured logs with efficient local storage. Go's
// log/slog provides the structure with no third-party dependency, and the
// storage already exists: records that belong to a job are written to the
// durable, redacted job event log that the Job Monitor reads, while
// process-level records stay on the standard writer. There is deliberately no
// second log store.
//
// Redaction is enforced by the handler rather than left to each caller. A
// future caller that adds a proxy URL or a password attribute cannot leak it,
// because every value passes through jobruntime redaction on its way out.
const (
	// logFormatEnv selects the process handler: "text" (default) or "json".
	logFormatEnv = "GMAPS_LOG_FORMAT"
	// logLevelEnv sets the process level: debug, info, warn, or error.
	logLevelEnv = "GMAPS_LOG_LEVEL"
)

// sensitiveLogKeys are attribute names whose values are always redacted, even
// when the value itself carries no recognisable URL or credential shape.
var sensitiveLogKeys = map[string]struct{}{
	"proxy":         {},
	"proxy_url":     {},
	"proxies":       {},
	"password":      {},
	"secret":        {},
	"token":         {},
	"api_key":       {},
	"authorization": {},
	"dsn":           {},
	"credentials":   {},
}

// redactingHandler wraps a slog.Handler and redacts every string value it
// emits. It is the single choke point that keeps secrets out of the log.
type redactingHandler struct {
	inner slog.Handler
}

// NewRedactingHandler wraps h so that string attributes and messages are
// redacted before they reach the writer.
func NewRedactingHandler(h slog.Handler) slog.Handler {
	return &redactingHandler{inner: h}
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	safe := slog.NewRecord(record.Time, record.Level, jobruntime.RedactString(record.Message), record.PC)

	record.Attrs(func(attr slog.Attr) bool {
		safe.AddAttrs(redactAttr(attr))

		return true
	})

	return h.inner.Handle(ctx, safe)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		redacted = append(redacted, redactAttr(attr))
	}

	return &redactingHandler{inner: h.inner.WithAttrs(redacted)}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name)}
}

// redactAttr redacts one attribute, recursing into groups.
func redactAttr(attr slog.Attr) slog.Attr {
	if attr.Value.Kind() == slog.KindGroup {
		members := attr.Value.Group()
		redacted := make([]slog.Attr, 0, len(members))

		for _, member := range members {
			redacted = append(redacted, redactAttr(member))
		}

		return slog.Attr{Key: attr.Key, Value: slog.GroupValue(redacted...)}
	}

	if _, sensitive := sensitiveLogKeys[strings.ToLower(attr.Key)]; sensitive {
		return slog.String(attr.Key, "[redacted]")
	}

	if attr.Value.Kind() == slog.KindString {
		return slog.String(attr.Key, jobruntime.RedactString(attr.Value.String()))
	}

	return attr
}

// NewProcessLogger builds the workspace's process logger from the operator's
// environment. The default is a text handler at info level on stderr, which
// keeps existing console output readable.
func NewProcessLogger(writer io.Writer) *slog.Logger {
	if writer == nil {
		writer = os.Stderr
	}

	options := &slog.HandlerOptions{Level: parseLogLevel(os.Getenv(logLevelEnv))}

	var handler slog.Handler
	if strings.EqualFold(strings.TrimSpace(os.Getenv(logFormatEnv)), "json") {
		handler = slog.NewJSONHandler(writer, options)
	} else {
		handler = slog.NewTextHandler(writer, options)
	}

	return slog.New(NewRedactingHandler(handler))
}

// parseLogLevel maps the configured name to a level, defaulting to info so an
// unset or misspelled value never silences the workspace.
func parseLogLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// InstallProcessLogger makes the redacting structured logger the default for
// both slog and the standard log package, so existing log.Printf calls also
// flow through redaction and gain a consistent shape.
func InstallProcessLogger(writer io.Writer) *slog.Logger {
	logger := NewProcessLogger(writer)
	slog.SetDefault(logger)

	return logger
}

// JobLogger returns a logger bound to one job. Records made through it carry
// the job identity, which is what lets a job-scoped record be routed to the
// durable event log alongside everything else that happened to that job.
func JobLogger(logger *slog.Logger, jobID string) *slog.Logger {
	if logger == nil {
		logger = slog.Default()
	}

	return logger.With(slog.String("job_id", jobID))
}

// LogJobEvent writes one structured record and, when the workspace can store
// it, persists the same facts to the durable job event log the Job Monitor
// reads. The durable write is best-effort: observability must never fail a
// scrape.
func (s *Service) LogJobEvent(
	ctx context.Context,
	logger *slog.Logger,
	jobID, eventType, severity, message string,
	attrs ...slog.Attr,
) {
	if logger == nil {
		logger = slog.Default()
	}

	level := slog.LevelInfo

	switch strings.ToLower(severity) {
	case "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	record := append([]slog.Attr{
		slog.String("job_id", jobID),
		slog.String("event", eventType),
	}, attrs...)

	logger.LogAttrs(ctx, level, message, record...)

	if s == nil {
		return
	}

	fields := make(map[string]any, len(attrs))
	for _, attr := range attrs {
		safe := redactAttr(attr)
		fields[safe.Key] = safe.Value.Any()
	}

	_ = s.RecordJobWorkerEvent(ctx, jobID, eventType, severity, message, fields)
}
