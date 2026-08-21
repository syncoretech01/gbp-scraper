package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type scrapeDefaults struct {
	Language     string
	Zoom         int
	Depth        int
	MaxTime      string
	Concurrency  int
	TaskWorkers  int
	BrowserPool  int
	PagesBrowser int
	MaxRecords   int
	RetryCount   int
	RetryDelay   string
	PageTimeout  string
	RandomMin    string
	RandomMax    string
	Headfull     bool
	LoadImages   bool
	Adaptive     bool
	LowDiskMB    int
	Radius       int
	GridCellKM   string
	Email        bool
	ExtraReviews bool
	// LocationLabel, Lat, and Lon prefill the wizard's location step. All
	// three stay empty until an operator saves their own defaults, keeping
	// the built-in San Francisco example untouched.
	LocationLabel string
	Lat           string
	Lon           string
	// ProxyPoolID preselects a proxy pool in the wizard when it still exists.
	ProxyPoolID string
}

type settingsPageData struct {
	Defaults    scrapeDefaults
	Quality     QualityRuleSet
	Preferences appPreferences
	Storage     storagePreferences
	Directories []systemDirectoryView
	Privacy     privacyStatus
	LocalAI     localAISettings
	ProxyPools  []proxyPoolOption
	DataFolder  string
	Theme       string
	Notice      string
}

// privacyStatus reports the live state of the privacy guarantees rather than
// restating them, so an operator can tell whether telemetry is actually off in
// this process and which secrets are actually encrypted at rest.
type privacyStatus struct {
	TelemetryDisabled bool
	TelemetrySource   string
	EncryptedSecrets  []string
	BrowserProfiles   string
}

type appPreferences struct {
	CompactTables  bool
	SidebarDefault string
	DateTimeFormat string
	NumberFormat   string
	Locale         string
	ReducedMotion  bool
	FontSize       string
	HighContrast   bool
}

type storagePreferences struct {
	ExportsDirectory     string
	ScreenshotsDirectory string
	LogsDirectory        string
	BackupsDirectory     string
	TemporaryDirectory   string
	MaximumStorageGB     int
	AutomaticCleanupDays int
	BackupCount          int
	VersionRetentionDays int
}

func defaultAppPreferences() appPreferences {
	return appPreferences{
		SidebarDefault: "expanded",
		DateTimeFormat: "local",
		NumberFormat:   "locale",
		Locale:         "en",
		FontSize:       "medium",
	}
}

func appPreferencesFromMap(values map[string]string) appPreferences {
	preferences := defaultAppPreferences()
	preferences.CompactTables = values["appearance.compact_tables"] == "true"
	preferences.SidebarDefault = settingEnum(values, "appearance.sidebar_default", preferences.SidebarDefault, "expanded", "collapsed")
	preferences.DateTimeFormat = settingEnum(values, "appearance.date_time_format", preferences.DateTimeFormat, "local", "iso", "us", "eu")
	preferences.NumberFormat = settingEnum(values, "appearance.number_format", preferences.NumberFormat, "locale", "plain")
	preferences.Locale = normalizeLocale(settingString(values, "appearance.locale", preferences.Locale))
	preferences.ReducedMotion = values["appearance.reduced_motion"] == "true"
	preferences.FontSize = settingEnum(values, "appearance.font_size", preferences.FontSize, "small", "medium", "large")
	preferences.HighContrast = values["appearance.high_contrast"] == "true"

	return preferences
}

func defaultStoragePreferences() storagePreferences {
	return storagePreferences{
		ExportsDirectory:     "exports",
		ScreenshotsDirectory: "screenshots",
		LogsDirectory:        "logs",
		BackupsDirectory:     "backups",
		TemporaryDirectory:   "temp",
		AutomaticCleanupDays: 30,
		BackupCount:          10,
		VersionRetentionDays: 365,
	}
}

func storagePreferencesFromMap(values map[string]string) storagePreferences {
	preferences := defaultStoragePreferences()
	preferences.ExportsDirectory = settingString(values, "storage.exports_directory", preferences.ExportsDirectory)
	preferences.ScreenshotsDirectory = settingString(values, "storage.screenshots_directory", preferences.ScreenshotsDirectory)
	preferences.LogsDirectory = settingString(values, "storage.logs_directory", preferences.LogsDirectory)
	preferences.BackupsDirectory = settingString(values, "storage.backups_directory", preferences.BackupsDirectory)
	preferences.TemporaryDirectory = settingString(values, "storage.temporary_directory", preferences.TemporaryDirectory)
	preferences.MaximumStorageGB = settingInt(values, "storage.maximum_gb", preferences.MaximumStorageGB)
	preferences.AutomaticCleanupDays = settingInt(values, "storage.cleanup_days", preferences.AutomaticCleanupDays)
	preferences.BackupCount = settingInt(values, "storage.backup_count", preferences.BackupCount)
	preferences.VersionRetentionDays = settingInt(values, "storage.version_retention_days", preferences.VersionRetentionDays)

	return preferences
}

func settingEnum(values map[string]string, key, fallback string, allowed ...string) string {
	value := strings.TrimSpace(values[key])
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}

	return fallback
}

func normalizeLocale(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 2 || len(value) > 16 {
		return "en"
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && character != '-' {
			return "en"
		}
	}

	return value
}

func defaultScrapeSettings() scrapeDefaults {
	return scrapeDefaults{
		Language:     "en",
		Zoom:         12,
		Depth:        10,
		MaxTime:      "60m",
		Concurrency:  4,
		TaskWorkers:  4,
		BrowserPool:  2,
		PagesBrowser: 2,
		MaxRecords:   0,
		RetryCount:   3,
		RetryDelay:   "2s",
		PageTimeout:  "45s",
		RandomMin:    "0s",
		RandomMax:    "0s",
		Adaptive:     true,
		LowDiskMB:    2048,
		Radius:       10000,
		GridCellKM:   "2.5",
	}
}

func scrapeSettingsFromMap(values map[string]string) scrapeDefaults {
	defaults := defaultScrapeSettings()
	defaults.Language = settingString(values, "scrape.language", defaults.Language)
	defaults.Zoom = settingInt(values, "scrape.zoom", defaults.Zoom)
	defaults.Depth = settingInt(values, "scrape.depth", defaults.Depth)
	defaults.MaxTime = settingString(values, "scrape.max_runtime", defaults.MaxTime)
	defaults.Concurrency = settingInt(values, "scrape.concurrency", defaults.Concurrency)
	defaults.TaskWorkers = settingInt(values, "scrape.task_workers", defaults.TaskWorkers)
	defaults.BrowserPool = settingInt(values, "scrape.browser_pool", defaults.BrowserPool)
	defaults.PagesBrowser = settingInt(values, "scrape.pages_per_browser", defaults.PagesBrowser)
	defaults.MaxRecords = settingInt(values, "scrape.max_records", defaults.MaxRecords)
	defaults.RetryCount = settingInt(values, "scrape.retry_count", defaults.RetryCount)
	defaults.RetryDelay = settingString(values, "scrape.retry_delay", defaults.RetryDelay)
	defaults.PageTimeout = settingString(values, "scrape.page_timeout", defaults.PageTimeout)
	defaults.RandomMin = settingString(values, "scrape.random_delay_min", defaults.RandomMin)
	defaults.RandomMax = settingString(values, "scrape.random_delay_max", defaults.RandomMax)
	defaults.Headfull = values["scrape.headfull"] == "true"
	defaults.LoadImages = values["scrape.load_images"] == "true"
	if value, exists := values["scrape.adaptive_performance"]; exists {
		defaults.Adaptive = value == "true"
	}
	defaults.LowDiskMB = settingInt(values, "scrape.low_disk_mb", defaults.LowDiskMB)
	defaults.Radius = settingInt(values, "scrape.radius", defaults.Radius)
	defaults.GridCellKM = settingString(values, "scrape.grid_cell_km", defaults.GridCellKM)
	defaults.Email = values["scrape.email"] == "true"
	defaults.ExtraReviews = values["scrape.extra_reviews"] == "true"
	defaults.LocationLabel = settingString(values, "scrape.location_label", defaults.LocationLabel)
	defaults.Lat = settingString(values, "scrape.latitude", defaults.Lat)
	defaults.Lon = settingString(values, "scrape.longitude", defaults.Lon)
	defaults.ProxyPoolID = settingString(values, "scrape.default_proxy_pool", defaults.ProxyPoolID)
	return defaults
}

func settingString(values map[string]string, key, fallback string) string {
	if value := strings.TrimSpace(values[key]); value != "" {
		return value
	}
	return fallback
}

func settingInt(values map[string]string, key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(values[key]))
	if err != nil {
		return fallback
	}
	return value
}

func (s *Server) loadScrapeDefaults(r *http.Request) scrapeDefaults {
	values, err := s.svc.LoadSettings(r.Context())
	if err != nil {
		return defaultScrapeSettings()
	}
	return scrapeSettingsFromMap(values)
}

// telemetryEnvironmentVariable is the upstream switch the scraper reads once,
// at start-up, to decide whether any usage event is ever sent.
const telemetryEnvironmentVariable = "DISABLE_TELEMETRY"

// privacyStatus reports what is actually true in this process rather than what
// the shipped Compose file happens to set: a workspace started by hand without
// the variable is told so instead of being reassured.
func (s *Server) privacyStatus(ctx context.Context) privacyStatus {
	status := privacyStatus{
		TelemetryDisabled: os.Getenv(telemetryEnvironmentVariable) == "1",
		BrowserProfiles:   s.browserProfileSize(),
	}
	if status.TelemetryDisabled {
		status.TelemetrySource = telemetryEnvironmentVariable + "=1 is set for this process"
	} else {
		status.TelemetrySource = telemetryEnvironmentVariable +
			" is not set to 1 for this process; set it in the environment to disable usage events"
	}

	status.EncryptedSecrets = []string{
		"Proxy URLs and passwords (AES-256-GCM under .proxy-master-key)",
		"Integration webhook secrets and database credentials (same local key)",
	}
	if s.svc.SupportsEncryptedBackups() {
		status.EncryptedSecrets = append(status.EncryptedSecrets,
			"Database backups, when a passphrase is given (AES-256-GCM, scrypt-derived)")
	}
	if settings, err := s.svc.LoadSettings(ctx); err == nil && settings[authPasswordSettingKey] != "" {
		status.EncryptedSecrets = append(status.EncryptedSecrets,
			"The local access password (stored only as a salted hash, never recoverable)")
	}

	return status
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	values, err := s.svc.LoadSettings(r.Context())
	if err != nil {
		http.Error(w, "could not load settings", http.StatusInternalServerError)
		return
	}
	activity, _ := s.appActivity(r)
	qualityRules, qualityErr := s.svc.ActiveQualityRules(r.Context())
	if qualityErr != nil {
		qualityRules = DefaultQualityRuleSet()
	}
	// Mirrors the wizard's pool filter so a stored default always names a
	// pool the wizard can actually offer. A repository without proxy support
	// simply leaves the list empty and the select hidden.
	proxyOptions := make([]proxyPoolOption, 0)
	if pools, poolsErr := s.svc.ListProxyPools(r.Context()); poolsErr == nil {
		for _, pool := range pools {
			if pool.EnabledCount > 0 {
				proxyOptions = append(proxyOptions, proxyPoolOption{
					ID: pool.ID, Name: pool.Name, HealthyCount: int(pool.HealthyCount),
				})
			}
		}
	}
	storage, storageErr := s.workspaceStorageUsage(r.Context())
	if storageErr != nil {
		storage = workspaceStorageSnapshot{}
	}
	s.renderAppPage(w, "settings", appPageData{
		Title:     "Settings",
		Subtitle:  "Choose defaults applied to every newly configured local scrape.",
		ActiveNav: "settings",
		Theme:     settingString(values, "appearance.theme", "system"),
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page: settingsPageData{
			Defaults:    scrapeSettingsFromMap(values),
			Quality:     qualityRules,
			Preferences: appPreferencesFromMap(values),
			Storage:     storagePreferencesFromMap(values),
			Directories: s.systemDirectoryViews(r.Context(), storage),
			Privacy:     s.privacyStatus(r.Context()),
			LocalAI:     localAISettingsFromMap(values),
			ProxyPools:  proxyOptions,
			DataFolder:  s.svc.dataFolder,
			Theme:       settingString(values, "appearance.theme", "system"),
			Notice:      strings.TrimSpace(r.URL.Query().Get("notice")),
		},
	})
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	values, err := validateSettingsForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if err := s.ensureConfiguredStorageDirectories(values); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if err := s.svc.SaveSettings(r.Context(), values); err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "settings_failed", "Could not save local settings")
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]string{"message": "Settings saved"}})
		return
	}
	http.Redirect(w, r, "/app/settings?notice=Settings+saved", http.StatusSeeOther)
}

func validateSettingsForm(r *http.Request) (map[string]string, error) {
	language := strings.ToLower(strings.TrimSpace(r.FormValue("language")))
	if len(language) != 2 {
		return nil, fmt.Errorf("language must be a two-letter code")
	}
	zoom, err := boundedFormInt(r, "zoom", 1, 21)
	if err != nil {
		return nil, err
	}
	depth, err := boundedFormInt(r, "depth", 1, 100)
	if err != nil {
		return nil, err
	}
	// Older clients and partial forms predate this field, so an absent value
	// keeps the existing default rather than failing the whole save.
	taskWorkers := defaultScrapeSettings().TaskWorkers

	if strings.TrimSpace(r.FormValue("task_workers")) != "" {
		taskWorkers, err = boundedFormInt(r, "task_workers", 1, MaximumJobTaskWorkers)
		if err != nil {
			return nil, err
		}
	}

	concurrency, err := boundedFormInt(r, "concurrency", 1, 64)
	if err != nil {
		return nil, err
	}
	browserPool, err := boundedFormInt(r, "browser_pool_size", 1, 32)
	if err != nil {
		return nil, err
	}
	pagesBrowser, err := boundedFormInt(r, "pages_per_browser", 1, 16)
	if err != nil {
		return nil, err
	}
	maxRecords, err := boundedFormInt(r, "max_records", 0, 10_000_000)
	if err != nil {
		return nil, err
	}
	retryCount, err := boundedFormInt(r, "retry_count", 0, 20)
	if err != nil {
		return nil, err
	}
	retryDelay, err := boundedFormDuration(r, "retry_delay", 0, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	pageTimeout, err := boundedFormDuration(r, "page_timeout", time.Second, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	randomMin, err := boundedFormDuration(r, "random_delay_min", 0, time.Minute)
	if err != nil {
		return nil, err
	}
	randomMax, err := boundedFormDuration(r, "random_delay_max", 0, time.Minute)
	if err != nil {
		return nil, err
	}
	if randomMax < randomMin {
		return nil, fmt.Errorf("random delay maximum must be at least the minimum")
	}
	lowDiskMB, err := boundedFormInt(r, "low_disk_mb", 128, 1_048_576)
	if err != nil {
		return nil, err
	}
	radius, err := boundedFormInt(r, "radius", 100, 100000)
	if err != nil {
		return nil, err
	}
	gridCell, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("grid_cell_km")), 64)
	if err != nil || gridCell < 0.1 || gridCell > 50 {
		return nil, fmt.Errorf("grid cell size must be between 0.1 and 50 km")
	}
	maxRuntime := strings.TrimSpace(r.FormValue("max_runtime"))
	duration, err := time.ParseDuration(maxRuntime)
	if err != nil || duration < 3*time.Minute {
		return nil, fmt.Errorf("maximum runtime must be a duration of at least 3m")
	}
	theme := strings.TrimSpace(r.FormValue("theme"))
	if theme != "system" && theme != "light" && theme != "dark" {
		return nil, fmt.Errorf("invalid appearance theme")
	}
	// Like task_workers, these fields postdate older forms: absent or empty
	// values keep the stored defaults empty rather than failing the save.
	locationLabel, latitude, longitude, defaultProxyPool, err := validateScrapeLocationDefaults(r)
	if err != nil {
		return nil, err
	}
	preferences, err := validateAppearancePreferences(r)
	if err != nil {
		return nil, err
	}
	storage, err := validateStoragePreferences(r)
	if err != nil {
		return nil, err
	}
	localAI, err := validateLocalAISettings(r)
	if err != nil {
		return nil, err
	}

	values := map[string]string{
		"scrape.language":                language,
		"scrape.zoom":                    strconv.Itoa(zoom),
		"scrape.depth":                   strconv.Itoa(depth),
		"scrape.max_runtime":             maxRuntime,
		"scrape.concurrency":             strconv.Itoa(concurrency),
		"scrape.task_workers":            strconv.Itoa(taskWorkers),
		"scrape.browser_pool":            strconv.Itoa(browserPool),
		"scrape.pages_per_browser":       strconv.Itoa(pagesBrowser),
		"scrape.max_records":             strconv.Itoa(maxRecords),
		"scrape.retry_count":             strconv.Itoa(retryCount),
		"scrape.retry_delay":             retryDelay.String(),
		"scrape.page_timeout":            pageTimeout.String(),
		"scrape.random_delay_min":        randomMin.String(),
		"scrape.random_delay_max":        randomMax.String(),
		"scrape.headfull":                strconv.FormatBool(r.FormValue("headfull") == "on"),
		"scrape.load_images":             strconv.FormatBool(r.FormValue("load_images") == "on"),
		"scrape.adaptive_performance":    strconv.FormatBool(r.FormValue("adaptive_performance") == "on"),
		"scrape.low_disk_mb":             strconv.Itoa(lowDiskMB),
		"scrape.radius":                  strconv.Itoa(radius),
		"scrape.grid_cell_km":            strconv.FormatFloat(gridCell, 'f', -1, 64),
		"scrape.email":                   strconv.FormatBool(r.FormValue("email") == "on"),
		"scrape.extra_reviews":           strconv.FormatBool(r.FormValue("extra_reviews") == "on"),
		"scrape.location_label":          locationLabel,
		"scrape.latitude":                latitude,
		"scrape.longitude":               longitude,
		"scrape.default_proxy_pool":      defaultProxyPool,
		"appearance.theme":               theme,
		"appearance.compact_tables":      strconv.FormatBool(preferences.CompactTables),
		"appearance.sidebar_default":     preferences.SidebarDefault,
		"appearance.date_time_format":    preferences.DateTimeFormat,
		"appearance.number_format":       preferences.NumberFormat,
		"appearance.locale":              preferences.Locale,
		"appearance.reduced_motion":      strconv.FormatBool(preferences.ReducedMotion),
		"appearance.font_size":           preferences.FontSize,
		"appearance.high_contrast":       strconv.FormatBool(preferences.HighContrast),
		"storage.exports_directory":      storage.ExportsDirectory,
		"storage.screenshots_directory":  storage.ScreenshotsDirectory,
		"storage.logs_directory":         storage.LogsDirectory,
		"storage.backups_directory":      storage.BackupsDirectory,
		"storage.temporary_directory":    storage.TemporaryDirectory,
		"storage.maximum_gb":             strconv.Itoa(storage.MaximumStorageGB),
		"storage.cleanup_days":           strconv.Itoa(storage.AutomaticCleanupDays),
		"storage.backup_count":           strconv.Itoa(storage.BackupCount),
		"storage.version_retention_days": strconv.Itoa(storage.VersionRetentionDays),
		"ai.enabled":                     strconv.FormatBool(localAI.Enabled),
		"ai.endpoint":                    localAI.Endpoint,
		"ai.model":                       localAI.Model,
		"ai.timeout_seconds":             strconv.Itoa(localAI.TimeoutSeconds),
		"privacy.telemetry":              "disabled",
	}

	return values, nil
}

// validateScrapeLocationDefaults reads the optional default location and proxy
// pool for new scrapes. Every field may be empty: the wizard then keeps its
// built-in example instead of a stored operator preference.
func validateScrapeLocationDefaults(r *http.Request) (label, latitude, longitude, proxyPool string, err error) {
	label = strings.TrimSpace(r.FormValue("location_label"))
	if len(label) > 120 {
		return "", "", "", "", fmt.Errorf("default location label must be at most 120 characters")
	}
	if strings.ContainsAny(label, "\x00\r\n") {
		return "", "", "", "", fmt.Errorf("default location label contains control characters")
	}

	latitude = strings.TrimSpace(r.FormValue("latitude"))
	if latitude != "" {
		value, parseErr := strconv.ParseFloat(latitude, 64)
		if parseErr != nil || value < -90 || value > 90 {
			return "", "", "", "", fmt.Errorf("default latitude must be between -90 and 90")
		}
	}

	longitude = strings.TrimSpace(r.FormValue("longitude"))
	if longitude != "" {
		value, parseErr := strconv.ParseFloat(longitude, 64)
		if parseErr != nil || value < -180 || value > 180 {
			return "", "", "", "", fmt.Errorf("default longitude must be between -180 and 180")
		}
	}

	proxyPool = strings.TrimSpace(r.FormValue("default_proxy_pool"))
	if len(proxyPool) > 64 {
		return "", "", "", "", fmt.Errorf("default proxy pool ID must be at most 64 characters")
	}

	return label, latitude, longitude, proxyPool, nil
}

func validateAppearancePreferences(r *http.Request) (appPreferences, error) {
	preferences := defaultAppPreferences()
	preferences.CompactTables = r.FormValue("compact_tables") == "on"
	preferences.ReducedMotion = r.FormValue("reduced_motion") == "on"
	preferences.HighContrast = r.FormValue("high_contrast") == "on"
	preferences.SidebarDefault = strings.TrimSpace(r.FormValue("sidebar_default"))
	preferences.DateTimeFormat = strings.TrimSpace(r.FormValue("date_time_format"))
	preferences.NumberFormat = strings.TrimSpace(r.FormValue("number_format"))
	preferences.Locale = strings.TrimSpace(r.FormValue("appearance_locale"))
	preferences.FontSize = strings.TrimSpace(r.FormValue("font_size"))

	if preferences.SidebarDefault == "" {
		preferences.SidebarDefault = "expanded"
	}
	if preferences.DateTimeFormat == "" {
		preferences.DateTimeFormat = "local"
	}
	if preferences.NumberFormat == "" {
		preferences.NumberFormat = "locale"
	}
	if preferences.Locale == "" {
		preferences.Locale = "en"
	}
	if preferences.FontSize == "" {
		preferences.FontSize = "medium"
	}
	if settingEnum(map[string]string{"value": preferences.SidebarDefault}, "value", "", "expanded", "collapsed") == "" {
		return appPreferences{}, fmt.Errorf("invalid sidebar default")
	}
	if settingEnum(map[string]string{"value": preferences.DateTimeFormat}, "value", "", "local", "iso", "us", "eu") == "" {
		return appPreferences{}, fmt.Errorf("invalid date and time format")
	}
	if settingEnum(map[string]string{"value": preferences.NumberFormat}, "value", "", "locale", "plain") == "" {
		return appPreferences{}, fmt.Errorf("invalid number format")
	}
	if normalized := normalizeLocale(preferences.Locale); normalized != preferences.Locale {
		return appPreferences{}, fmt.Errorf("locale must be a simple language tag such as en or en-US")
	}
	if settingEnum(map[string]string{"value": preferences.FontSize}, "value", "", "small", "medium", "large") == "" {
		return appPreferences{}, fmt.Errorf("invalid font size")
	}

	return preferences, nil
}

func validateStoragePreferences(r *http.Request) (storagePreferences, error) {
	preferences := defaultStoragePreferences()
	fields := []struct {
		name        string
		destination *string
	}{
		{name: "exports_directory", destination: &preferences.ExportsDirectory},
		{name: "screenshots_directory", destination: &preferences.ScreenshotsDirectory},
		{name: "logs_directory", destination: &preferences.LogsDirectory},
		{name: "backups_directory", destination: &preferences.BackupsDirectory},
		{name: "temporary_directory", destination: &preferences.TemporaryDirectory},
	}
	for _, field := range fields {
		value := strings.TrimSpace(r.FormValue(field.name))
		if value == "" {
			continue
		}
		validated, err := validateRelativeStorageDirectory(value)
		if err != nil {
			return storagePreferences{}, fmt.Errorf("%s: %w", strings.ReplaceAll(field.name, "_", " "), err)
		}
		*field.destination = validated
	}

	var err error
	if preferences.MaximumStorageGB, err = optionalBoundedFormInt(r, "maximum_storage_gb", 0, 100000, 0); err != nil {
		return storagePreferences{}, err
	}
	if preferences.AutomaticCleanupDays, err = optionalBoundedFormInt(r, "automatic_cleanup_days", 0, 3650, 30); err != nil {
		return storagePreferences{}, err
	}
	if preferences.BackupCount, err = optionalBoundedFormInt(r, "backup_count", 1, 1000, 10); err != nil {
		return storagePreferences{}, err
	}
	if preferences.VersionRetentionDays, err = optionalBoundedFormInt(r, "version_retention_days", 0, 36500, 365); err != nil {
		return storagePreferences{}, err
	}

	return preferences, nil
}

func validateRelativeStorageDirectory(value string) (string, error) {
	if filepath.IsAbs(value) || filepath.VolumeName(value) != "" || strings.ContainsAny(value, `\\:`) {
		return "", fmt.Errorf("must stay inside the local data directory")
	}
	cleaned := filepath.Clean(filepath.FromSlash(value))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("must be a non-parent relative directory")
	}
	for _, segment := range strings.Split(cleaned, string(filepath.Separator)) {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("contains an invalid path segment")
		}
	}

	return filepath.ToSlash(cleaned), nil
}

func optionalBoundedFormInt(r *http.Request, name string, minimum, maximum, fallback int) (int, error) {
	raw := strings.TrimSpace(r.FormValue(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", strings.ReplaceAll(name, "_", " "), minimum, maximum)
	}

	return value, nil
}

func (s *Server) ensureConfiguredStorageDirectories(values map[string]string) error {
	if s == nil || s.svc == nil {
		return nil
	}
	for _, key := range []string{
		"storage.exports_directory", "storage.screenshots_directory", "storage.logs_directory",
		"storage.backups_directory", "storage.temporary_directory",
	} {
		path, err := safeDataPath(s.svc.dataFolder, values[key])
		if err != nil {
			return fmt.Errorf("invalid configured storage directory: %w", err)
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create configured storage directory: %w", err)
		}
	}

	return nil
}

func boundedFormDuration(r *http.Request, name string, minimum, maximum time.Duration) (time.Duration, error) {
	value, err := time.ParseDuration(strings.TrimSpace(r.FormValue(name)))
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be a duration between %s and %s", strings.ReplaceAll(name, "_", " "), minimum, maximum)
	}
	return value, nil
}

func boundedFormInt(r *http.Request, name string, minimum, maximum int) (int, error) {
	value, err := requiredFormInt(r, name)
	if err != nil {
		return 0, err
	}
	if value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", strings.ReplaceAll(name, "_", " "), minimum, maximum)
	}
	return value, nil
}
