package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

var (
	ErrIntegrationStoreUnsupported = errors.New("local integration storage is unavailable")
	ErrIntegrationNotFound         = errors.New("integration not found")

	// errIntegrationNotApplicable marks an event a destination cannot carry —
	// a watch folder has nothing to copy for a job event, for example. It is
	// not a delivery failure and is never recorded as one.
	errIntegrationNotApplicable = errors.New("integration does not handle this event")
)

// Local delivery destinations. Command/script execution is deliberately absent:
// a locally reachable web UI must not become a process-execution surface. Local
// automation runs the other way round — this workspace posts a signed event to
// a local listener (n8n, Activepieces, any HTTP endpoint) which then calls back
// into the local REST API.
const (
	IntegrationWebhook     = "webhook"
	IntegrationWatchFolder = "watch_folder"
	IntegrationDatabase    = "database"

	maximumIntegrations       = 100
	maximumIntegrationPayload = 64 << 10
	maximumWebhookResponse    = 64 << 10
)

// Event names an integration can subscribe to. The set is closed so a stored
// subscription can always be validated against the events this build emits.
const (
	IntegrationEventExportCompleted = "export.completed"
	IntegrationEventJobCompleted    = "job.completed"
	IntegrationEventJobFailed       = "job.failed"
	IntegrationEventTest            = "integration.test"
)

// integrationEventNames lists every deliverable event in presentation order.
func integrationEventNames() []string {
	return []string{
		IntegrationEventExportCompleted,
		IntegrationEventJobCompleted,
		IntegrationEventJobFailed,
		IntegrationEventTest,
	}
}

// IntegrationRecord is a safe-to-display local delivery configuration. The
// executable configuration is stored separately and encrypted by the bundled
// SQLite repository.
type IntegrationRecord struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Kind          string     `json:"kind"`
	Enabled       bool       `json:"enabled"`
	Configuration string     `json:"configuration"`
	LastRunAt     *time.Time `json:"last_run_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// Events returns the subscribed event names stored in the public
// configuration, so templates can list them without decrypting anything.
func (record IntegrationRecord) Events() []string {
	var configuration integrationConfiguration
	if json.Unmarshal([]byte(record.Configuration), &configuration) != nil {
		return nil
	}

	return configuration.Events
}

// Signed reports whether a shared secret is configured, without revealing it.
func (record IntegrationRecord) Signed() bool {
	var configuration integrationConfiguration
	if json.Unmarshal([]byte(record.Configuration), &configuration) != nil {
		return false
	}

	return configuration.SecretConfigured
}

// IntegrationSecret is returned only to the delivery engine. It must never be
// serialized by an API handler or written to request/audit logs.
type IntegrationSecret struct {
	Record IntegrationRecord
	Secret string
}

type integrationRepository interface {
	SaveIntegration(context.Context, IntegrationRecord, string) error
	ListIntegrations(context.Context, bool, int) ([]IntegrationRecord, error)
	GetIntegrationSecret(context.Context, string) (IntegrationSecret, error)
	DeleteIntegration(context.Context, string) error
	RecordIntegrationRun(context.Context, string, time.Time, string) error
}

// integrationConfiguration is the stored shape for every destination. The
// public copy keeps only fields that are safe to render; secrets and database
// credentials live exclusively in the encrypted copy.
type integrationConfiguration struct {
	URL              string   `json:"url,omitempty"`
	Secret           string   `json:"secret,omitempty"`
	SecretConfigured bool     `json:"secret_configured,omitempty"`
	Events           []string `json:"events,omitempty"`
	Folder           string   `json:"folder,omitempty"`
	Driver           string   `json:"driver,omitempty"`
	Target           string   `json:"target,omitempty"`
	Table            string   `json:"table,omitempty"`
	DSN              string   `json:"dsn,omitempty"`
}

func (s *Service) integrationRepository() (integrationRepository, error) {
	repository, ok := s.repo.(integrationRepository)
	if !ok {
		return nil, ErrIntegrationStoreUnsupported
	}

	return repository, nil
}

func (s *Service) SaveIntegration(ctx context.Context, record IntegrationRecord, secret string) error {
	repository, err := s.integrationRepository()
	if err != nil {
		return err
	}

	return repository.SaveIntegration(ctx, record, secret)
}

func (s *Service) ListIntegrations(ctx context.Context, enabledOnly bool, limit int) ([]IntegrationRecord, error) {
	repository, err := s.integrationRepository()
	if err != nil {
		return nil, err
	}

	return repository.ListIntegrations(ctx, enabledOnly, limit)
}

func (s *Service) GetIntegrationSecret(ctx context.Context, id string) (IntegrationSecret, error) {
	repository, err := s.integrationRepository()
	if err != nil {
		return IntegrationSecret{}, err
	}

	return repository.GetIntegrationSecret(ctx, id)
}

func (s *Service) DeleteIntegration(ctx context.Context, id string) error {
	repository, err := s.integrationRepository()
	if err != nil {
		return err
	}

	return repository.DeleteIntegration(ctx, id)
}

func validateIntegrationConfiguration(kind string, configuration integrationConfiguration) (string, string, error) {
	events, err := validateIntegrationEvents(kind, configuration.Events)
	if err != nil {
		return "", "", err
	}
	configuration.Events = events
	publicConfiguration := integrationConfiguration{Events: events}
	switch kind {
	case IntegrationWebhook:
		parsed, parseErr := validateLocalWebhookURL(configuration.URL)
		if parseErr != nil {
			return "", "", parseErr
		}
		configuration.URL = parsed.String()
		if err := validateWebhookSecret(configuration.Secret); err != nil {
			return "", "", err
		}
		publicConfiguration.URL = jobruntime.RedactURL(configuration.URL)
		publicConfiguration.SecretConfigured = configuration.Secret != ""
		configuration.SecretConfigured = publicConfiguration.SecretConfigured
	case IntegrationWatchFolder:
		folder, folderErr := validateWatchFolderName(configuration.Folder)
		if folderErr != nil {
			return "", "", folderErr
		}
		configuration.Folder = folder
		publicConfiguration.Folder = filepath.ToSlash(filepath.Join(integrationOutboxDirectory, folder))
	case IntegrationDatabase:
		normalized, publicView, databaseErr := validateDatabaseDestination(configuration)
		if databaseErr != nil {
			return "", "", databaseErr
		}
		configuration.Driver, configuration.Target = normalized.Driver, normalized.Target
		configuration.Table, configuration.DSN = normalized.Table, normalized.DSN
		publicConfiguration.Driver = publicView.Driver
		publicConfiguration.Target = publicView.Target
		publicConfiguration.Table = publicView.Table
	default:
		return "", "", fmt.Errorf("integration kind must be webhook, watch_folder, or database")
	}
	publicJSON, err := json.Marshal(publicConfiguration)
	if err != nil {
		return "", "", err
	}
	secretJSON, err := json.Marshal(configuration)
	if err != nil {
		return "", "", err
	}

	return string(publicJSON), string(secretJSON), nil
}

// validateIntegrationEvents normalizes a subscription. An empty selection keeps
// the historical behaviour of the only event that existed before subscriptions,
// so upgrading a workspace never silently changes what a receiver is sent.
func validateIntegrationEvents(kind string, values []string) ([]string, error) {
	if len(values) > len(integrationEventNames()) {
		return nil, fmt.Errorf("too many integration events selected")
	}
	events := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if !slices.Contains(integrationEventNames(), value) {
			return nil, fmt.Errorf("unsupported integration event %q", value)
		}
		if value == IntegrationEventTest {
			continue
		}
		if !slices.Contains(events, value) {
			events = append(events, value)
		}
	}
	if len(events) == 0 {
		events = []string{IntegrationEventExportCompleted}
	}
	if kind != IntegrationWebhook {
		// Only a webhook can carry a job event; the other destinations act on
		// the file a completed export produced.
		return []string{IntegrationEventExportCompleted}, nil
	}

	return events, nil
}

// validateWebhookSecret bounds the optional shared secret used to sign
// deliveries. A short secret is worse than none because it invites a false
// sense of authentication, so it is rejected rather than padded.
func validateWebhookSecret(secret string) error {
	const (
		minimumWebhookSecret = 16
		maximumWebhookSecret = 256
	)
	if secret == "" {
		return nil
	}
	if len(secret) < minimumWebhookSecret || len(secret) > maximumWebhookSecret {
		return fmt.Errorf("webhook shared secret must contain %d to %d characters", minimumWebhookSecret, maximumWebhookSecret)
	}
	for _, character := range secret {
		if unicode.IsControl(character) {
			return fmt.Errorf("webhook shared secret cannot contain control characters")
		}
	}

	return nil
}

func validateIntegrationName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return fmt.Errorf("integration name must contain 1 to 120 characters")
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return fmt.Errorf("integration name cannot contain control characters")
		}
	}

	return nil
}

func validateLocalWebhookURL(raw string) (*url.URL, error) {
	if len(raw) == 0 || len(raw) > 2048 {
		return nil, fmt.Errorf("webhook URL must contain 1 to 2048 characters")
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Hostname() == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("webhook URL must be an absolute HTTP or HTTPS URL")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return nil, fmt.Errorf("webhook URL cannot contain user information or a fragment")
	}
	if strings.ContainsAny(parsed.Hostname(), "\x00\r\n") {
		return nil, fmt.Errorf("webhook host is invalid")
	}
	if address := net.ParseIP(parsed.Hostname()); address != nil && !permittedLocalIntegrationIP(address) {
		return nil, fmt.Errorf("webhook IP address must be private or loopback")
	}
	if port := parsed.Port(); port != "" {
		if value, parseErr := net.LookupPort("tcp", port); parseErr != nil || value < 1 || value > 65535 {
			return nil, fmt.Errorf("webhook port is invalid")
		}
	}

	return parsed, nil
}

func validateWatchFolderName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 || value == "." || value == ".." {
		return "", fmt.Errorf("watch folder name must contain 1 to 64 safe characters")
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.') {
			return "", fmt.Errorf("watch folder name may only contain letters, numbers, dots, dashes, and underscores")
		}
	}

	return value, nil
}

// deliverCompletedExport starts bounded best-effort deliveries after the
// export itself is durably registered. Delivery failures never invalidate the
// verified local artifact and remain visible in the delivery history.
func (s *Server) deliverCompletedExport(record ExportRecord, path string) {
	if s == nil || s.svc == nil {
		return
	}
	event := integrationEvent{
		Name:       IntegrationEventExportCompleted,
		SubjectID:  record.ID,
		OccurredAt: time.Now().UTC(),
		Data:       exportEventData(record),
		Export:     &record,
		ExportPath: path,
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), integrationDeliveryBudget)
		defer cancel()
		s.broadcastIntegrationEvent(ctx, event)
	}()
}

func exportEventData(record ExportRecord) map[string]any {
	completedAt := time.Now().UTC()
	if record.FinishedAt != nil {
		completedAt = record.FinishedAt.UTC()
	}

	return map[string]any{
		"export_id":    record.ID,
		"name":         record.Name,
		"format":       record.Format,
		"record_count": record.RecordCount,
		"file_size":    record.FileSize,
		"sha256":       record.Checksum,
		"source_type":  record.SourceType,
		"created_at":   record.CreatedAt.UTC(),
		"completed_at": completedAt,
		"download_url": "/api/v1/exports/" + record.ID + "/download",
	}
}

func (s *Server) deliverExportIntegration(
	ctx context.Context,
	id string,
	record ExportRecord,
	path string,
) error {
	integration, err := s.svc.GetIntegrationSecret(ctx, id)
	if err != nil {
		return err
	}

	return s.deliverIntegrationEvent(ctx, integration, integrationEvent{
		Name:       IntegrationEventExportCompleted,
		OccurredAt: time.Now().UTC(),
		Data:       exportEventData(record),
		Export:     &record,
		ExportPath: path,
	})
}

// deliverIntegrationEvent routes one event to one destination. The caller owns
// history and error reporting; this function only performs the delivery.
func (s *Server) deliverIntegrationEvent(
	ctx context.Context,
	integration IntegrationSecret,
	event integrationEvent,
) error {
	if !integration.Record.Enabled {
		return fmt.Errorf("integration is disabled")
	}
	var configuration integrationConfiguration
	if err := json.Unmarshal([]byte(integration.Secret), &configuration); err != nil {
		return fmt.Errorf("decode encrypted integration configuration: %w", err)
	}
	switch integration.Record.Kind {
	case IntegrationWebhook:
		return deliverLocalWebhook(ctx, configuration, event)
	case IntegrationWatchFolder:
		if event.Export == nil || event.ExportPath == "" {
			return errIntegrationNotApplicable
		}

		return s.deliverWatchFolder(configuration.Folder, event.ExportPath)
	case IntegrationDatabase:
		if event.Export == nil || event.ExportPath == "" {
			return errIntegrationNotApplicable
		}

		return s.deliverDatabaseDestination(ctx, configuration, *event.Export, event.ExportPath)
	default:
		return fmt.Errorf("unsupported integration kind")
	}
}

func dialLocalIntegration(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid webhook address: %w", err)
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, fmt.Errorf("resolve local webhook host: %w", err)
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 10 * time.Second}
	var failures []error
	for _, candidate := range addresses {
		if !permittedLocalIntegrationIP(candidate.IP) {
			failures = append(failures, fmt.Errorf("resolved address %s is not private or loopback", candidate.IP))
			continue
		}
		connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		failures = append(failures, dialErr)
	}

	return nil, errors.Join(failures...)
}

func permittedLocalIntegrationIP(address net.IP) bool {
	return address != nil && (address.IsLoopback() || address.IsPrivate()) &&
		!address.IsUnspecified() && !address.IsMulticast() && !address.IsLinkLocalUnicast()
}

// integrationOutboxDirectory is the single contained parent of every
// watch-folder destination.
const integrationOutboxDirectory = "integrations-outbox"

func (s *Server) deliverWatchFolder(folder, sourcePath string) error {
	folder, err := validateWatchFolderName(folder)
	if err != nil {
		return err
	}
	directory, err := safeDataPath(s.svc.dataFolder, filepath.ToSlash(filepath.Join(integrationOutboxDirectory, folder)))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create watch folder: %w", err)
	}
	if err := ensureDirectoryWithinDataFolder(s.svc.dataFolder, directory); err != nil {
		return err
	}
	destination := filepath.Join(directory, filepath.Base(sourcePath))

	return copyFileAtomically(sourcePath, destination)
}

func ensureDirectoryWithinDataFolder(dataFolder, directory string) error {
	base, err := filepath.Abs(dataFolder)
	if err != nil {
		return err
	}
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return fmt.Errorf("resolve data directory: %w", err)
	}
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return fmt.Errorf("resolve watch folder: %w", err)
	}
	relative, err := filepath.Rel(resolvedBase, resolvedDirectory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("watch folder escapes the local data directory")
	}

	return nil
}

// copyFileAtomically writes source to destination through a temporary file so
// a watching automation tool never observes a partially written artifact.
func copyFileAtomically(sourcePath, destination string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open export for local delivery: %w", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("export is not a regular file")
	}
	temporary := destination + ".partial"
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create local delivery: %w", err)
	}
	copyErr := error(nil)
	if _, err := io.Copy(output, source); err != nil {
		copyErr = err
	} else if err := output.Sync(); err != nil {
		copyErr = err
	}
	if err := output.Close(); copyErr == nil {
		copyErr = err
	}
	if copyErr != nil {
		_ = os.Remove(temporary)

		return fmt.Errorf("write local delivery: %w", copyErr)
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)

		return fmt.Errorf("publish local delivery: %w", err)
	}

	return nil
}
