package web

import (
	"fmt"
	"net/http"
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
	BrowserPool  int
	PagesBrowser int
	Radius       int
	GridCellKM   string
	Email        bool
	ExtraReviews bool
}

type settingsPageData struct {
	Defaults scrapeDefaults
	Theme    string
	Notice   string
}

func defaultScrapeSettings() scrapeDefaults {
	return scrapeDefaults{
		Language:     "en",
		Zoom:         12,
		Depth:        10,
		MaxTime:      "60m",
		Concurrency:  4,
		BrowserPool:  2,
		PagesBrowser: 2,
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
	defaults.BrowserPool = settingInt(values, "scrape.browser_pool", defaults.BrowserPool)
	defaults.PagesBrowser = settingInt(values, "scrape.pages_per_browser", defaults.PagesBrowser)
	defaults.Radius = settingInt(values, "scrape.radius", defaults.Radius)
	defaults.GridCellKM = settingString(values, "scrape.grid_cell_km", defaults.GridCellKM)
	defaults.Email = values["scrape.email"] == "true"
	defaults.ExtraReviews = values["scrape.extra_reviews"] == "true"
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

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	values, err := s.svc.LoadSettings(r.Context())
	if err != nil {
		http.Error(w, "could not load settings", http.StatusInternalServerError)
		return
	}
	activity, _ := s.appActivity(r)
	s.renderAppPage(w, "settings", appPageData{
		Title:     "Settings",
		Subtitle:  "Choose defaults applied to every newly configured local scrape.",
		ActiveNav: "settings",
		Theme:     settingString(values, "appearance.theme", "system"),
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page: settingsPageData{
			Defaults: scrapeSettingsFromMap(values),
			Theme:    settingString(values, "appearance.theme", "system"),
			Notice:   strings.TrimSpace(r.URL.Query().Get("notice")),
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

	return map[string]string{
		"scrape.language":          language,
		"scrape.zoom":              strconv.Itoa(zoom),
		"scrape.depth":             strconv.Itoa(depth),
		"scrape.max_runtime":       maxRuntime,
		"scrape.concurrency":       strconv.Itoa(concurrency),
		"scrape.browser_pool":      strconv.Itoa(browserPool),
		"scrape.pages_per_browser": strconv.Itoa(pagesBrowser),
		"scrape.radius":            strconv.Itoa(radius),
		"scrape.grid_cell_km":      strconv.FormatFloat(gridCell, 'f', -1, 64),
		"scrape.email":             strconv.FormatBool(r.FormValue("email") == "on"),
		"scrape.extra_reviews":     strconv.FormatBool(r.FormValue("extra_reviews") == "on"),
		"appearance.theme":         theme,
		"privacy.telemetry":        "disabled",
	}, nil
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
