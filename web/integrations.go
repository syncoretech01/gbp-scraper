package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

var (
	ErrIntegrationStoreUnsupported = errors.New("local integration storage is unavailable")
	ErrIntegrationNotFound         = errors.New("integration not found")
)

const (
	IntegrationWebhook     = "webhook"
	IntegrationWatchFolder = "watch_folder"
	IntegrationCommand     = "command"

	maximumIntegrations       = 100
	maximumIntegrationPayload = 64 << 10
	maximumHookOutput         = 64 << 10
	commandHooksEnableEnv     = "GMS_ENABLE_LOCAL_COMMAND_HOOKS"
	commandHooksAllowlistEnv  = "GMS_LOCAL_COMMAND_ALLOWLIST"
)

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

type integrationConfiguration struct {
	URL        string   `json:"url,omitempty"`
	Folder     string   `json:"folder,omitempty"`
	Executable string   `json:"executable,omitempty"`
	Arguments  []string `json:"arguments,omitempty"`
}

type integrationDeliveryEvent struct {
	Event       string    `json:"event"`
	ExportID    string    `json:"export_id"`
	Name        string    `json:"name"`
	Format      string    `json:"format"`
	RecordCount int64     `json:"record_count"`
	FileSize    int64     `json:"file_size"`
	Checksum    string    `json:"sha256"`
	CreatedAt   time.Time `json:"created_at"`
	CompletedAt time.Time `json:"completed_at"`
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
	var publicConfiguration integrationConfiguration
	switch kind {
	case IntegrationWebhook:
		parsed, err := validateLocalWebhookURL(configuration.URL)
		if err != nil {
			return "", "", err
		}
		configuration.URL = parsed.String()
		publicConfiguration.URL = jobruntime.RedactURL(configuration.URL)
	case IntegrationWatchFolder:
		folder, err := validateWatchFolderName(configuration.Folder)
		if err != nil {
			return "", "", err
		}
		configuration.Folder = folder
		publicConfiguration.Folder = filepath.ToSlash(filepath.Join("integrations-outbox", folder))
	case IntegrationCommand:
		if err := validateCommandConfiguration(configuration); err != nil {
			return "", "", err
		}
		publicConfiguration.Executable = configuration.Executable
		publicConfiguration.Arguments = append([]string(nil), configuration.Arguments...)
	default:
		return "", "", fmt.Errorf("integration kind must be webhook, watch_folder, or command")
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

func validateCommandConfiguration(configuration integrationConfiguration) error {
	if os.Getenv(commandHooksEnableEnv) != "1" {
		return fmt.Errorf("command hooks require %s=1", commandHooksEnableEnv)
	}
	executable := filepath.Clean(strings.TrimSpace(configuration.Executable))
	if executable == "." || !filepath.IsAbs(executable) || len(executable) > 1024 {
		return fmt.Errorf("command executable must be an absolute path")
	}
	allowed, err := commandExecutableAllowed(executable)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("command executable is not listed in %s", commandHooksAllowlistEnv)
	}
	base := strings.ToLower(filepath.Base(executable))
	if slices.Contains([]string{"sh", "bash", "dash", "zsh", "fish", "cmd", "cmd.exe", "powershell", "powershell.exe", "pwsh", "pwsh.exe"}, base) {
		return fmt.Errorf("shell interpreters are not accepted; configure an executable or Python script directly")
	}
	if len(configuration.Arguments) > 32 {
		return fmt.Errorf("command hooks accept at most 32 arguments")
	}
	for _, argument := range configuration.Arguments {
		if len(argument) > 1024 || strings.ContainsRune(argument, '\x00') {
			return fmt.Errorf("command hook arguments must be at most 1024 characters and contain no NUL bytes")
		}
	}
	return nil
}

func commandExecutableAllowed(executable string) (bool, error) {
	allowlist := filepath.SplitList(os.Getenv(commandHooksAllowlistEnv))
	if len(allowlist) == 0 {
		return false, fmt.Errorf("%s must list one or more absolute executables", commandHooksAllowlistEnv)
	}
	resolvedExecutable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return false, fmt.Errorf("resolve command executable: %w", err)
	}
	info, err := os.Stat(resolvedExecutable)
	if err != nil || !info.Mode().IsRegular() {
		return false, fmt.Errorf("command executable is not a regular file")
	}
	for _, allowed := range allowlist {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" || !filepath.IsAbs(allowed) {
			continue
		}
		resolvedAllowed, resolveErr := filepath.EvalSymlinks(filepath.Clean(allowed))
		if resolveErr == nil && sameFilesystemPath(resolvedAllowed, resolvedExecutable) {
			return true, nil
		}
	}
	return false, nil
}

func sameFilesystemPath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

// deliverCompletedExport starts bounded best-effort deliveries after the
// export itself is durably registered. Delivery failures never invalidate the
// verified local artifact and remain visible on the integration record.
func (s *Server) deliverCompletedExport(record ExportRecord, path string) {
	if s == nil || s.svc == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		integrations, err := s.svc.ListIntegrations(ctx, true, maximumIntegrations)
		if err != nil {
			return
		}
		var wait sync.WaitGroup
		for _, integration := range integrations {
			integration := integration
			wait.Add(1)
			go func() {
				defer wait.Done()
				deliveryErr := s.deliverExportIntegration(ctx, integration.ID, record, path)
				message := ""
				if deliveryErr != nil {
					message = jobruntime.RedactString(deliveryErr.Error())
					if len(message) > 2000 {
						message = message[:2000]
					}
				}
				repository, repositoryErr := s.svc.integrationRepository()
				if repositoryErr == nil {
					_ = repository.RecordIntegrationRun(context.Background(), integration.ID, time.Now().UTC(), message)
				}
			}()
		}
		wait.Wait()
	}()
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
	if !integration.Record.Enabled {
		return fmt.Errorf("integration is disabled")
	}
	var configuration integrationConfiguration
	if err := json.Unmarshal([]byte(integration.Secret), &configuration); err != nil {
		return fmt.Errorf("decode encrypted integration configuration: %w", err)
	}
	switch integration.Record.Kind {
	case IntegrationWebhook:
		return deliverLocalWebhook(ctx, configuration.URL, record)
	case IntegrationWatchFolder:
		return s.deliverWatchFolder(configuration.Folder, path)
	case IntegrationCommand:
		return s.deliverCommandHook(ctx, configuration, record, path)
	default:
		return fmt.Errorf("unsupported integration kind")
	}
}

func deliverLocalWebhook(ctx context.Context, rawURL string, record ExportRecord) error {
	target, err := validateLocalWebhookURL(rawURL)
	if err != nil {
		return err
	}
	event := integrationDeliveryEvent{
		Event: "export.completed", ExportID: record.ID, Name: record.Name, Format: record.Format,
		RecordCount: record.RecordCount, FileSize: record.FileSize, Checksum: record.Checksum,
		CreatedAt: record.CreatedAt, CompletedAt: time.Now().UTC(),
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		MaxIdleConns:          2,
		IdleConnTimeout:       10 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		DialContext:           dialLocalIntegration,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("webhook redirected too many times")
			}
			if _, err := validateLocalWebhookURL(request.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "google-maps-scraper-local-integration/1")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("deliver local webhook: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumHookOutput))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("local webhook returned HTTP %d", response.StatusCode)
	}
	return nil
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

func (s *Server) deliverWatchFolder(folder, sourcePath string) error {
	folder, err := validateWatchFolderName(folder)
	if err != nil {
		return err
	}
	directory, err := safeDataPath(s.svc.dataFolder, filepath.ToSlash(filepath.Join("integrations-outbox", folder)))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create watch folder: %w", err)
	}
	if err := ensureDirectoryWithinDataFolder(s.svc.dataFolder, directory); err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open export for watch-folder delivery: %w", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("export is not a regular file")
	}
	destination := filepath.Join(directory, filepath.Base(sourcePath))
	temporary := destination + ".partial"
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create watch-folder delivery: %w", err)
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
		return fmt.Errorf("write watch-folder delivery: %w", copyErr)
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("publish watch-folder delivery: %w", err)
	}
	return nil
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

func (s *Server) deliverCommandHook(
	ctx context.Context,
	configuration integrationConfiguration,
	record ExportRecord,
	path string,
) error {
	if err := validateCommandConfiguration(configuration); err != nil {
		return err
	}
	arguments := make([]string, len(configuration.Arguments))
	for index, argument := range configuration.Arguments {
		replacer := strings.NewReplacer(
			"{export_path}", path,
			"{export_id}", record.ID,
			"{format}", record.Format,
		)
		arguments[index] = replacer.Replace(argument)
	}
	commandContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(commandContext, configuration.Executable, arguments...)
	command.Dir = s.svc.dataFolder
	command.Env = []string{
		"GMS_EXPORT_PATH=" + path,
		"GMS_EXPORT_ID=" + record.ID,
		"GMS_EXPORT_FORMAT=" + record.Format,
	}
	if runtime.GOOS == "windows" {
		if systemRoot := os.Getenv("SystemRoot"); systemRoot != "" {
			command.Env = append(command.Env, "SystemRoot="+systemRoot)
		}
	}
	output := &boundedHookWriter{remaining: maximumHookOutput}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(jobruntime.RedactString(output.String()))
		if message != "" {
			return fmt.Errorf("command hook failed: %w: %s", err, message)
		}
		return fmt.Errorf("command hook failed: %w", err)
	}
	return nil
}

type boundedHookWriter struct {
	buffer    bytes.Buffer
	remaining int
}

func (writer *boundedHookWriter) Write(value []byte) (int, error) {
	originalLength := len(value)
	if writer.remaining > 0 {
		if len(value) > writer.remaining {
			value = value[:writer.remaining]
		}
		_, _ = writer.buffer.Write(value)
		writer.remaining -= len(value)
	}
	return originalLength, nil
}

func (writer *boundedHookWriter) String() string {
	return writer.buffer.String()
}
