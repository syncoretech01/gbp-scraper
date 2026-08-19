package web

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestValidateSettingsFormPersistsAppearanceAndContainedStorage(t *testing.T) {
	form := completeSettingsForm()
	form.Set("theme", "dark")
	form.Set("sidebar_default", "collapsed")
	form.Set("date_time_format", "iso")
	form.Set("number_format", "plain")
	form.Set("appearance_locale", "en-US")
	form.Set("font_size", "large")
	form.Set("compact_tables", "on")
	form.Set("reduced_motion", "on")
	form.Set("high_contrast", "on")
	form.Set("exports_directory", "artifacts/exports")
	form.Set("automatic_cleanup_days", "45")
	form.Set("backup_count", "12")

	request := httptest.NewRequest("POST", "/api/v1/settings", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	values, err := validateSettingsForm(request)
	if err != nil {
		t.Fatalf("validateSettingsForm() error = %v", err)
	}
	for key, expected := range map[string]string{
		"appearance.theme":            "dark",
		"appearance.sidebar_default":  "collapsed",
		"appearance.date_time_format": "iso",
		"appearance.number_format":    "plain",
		"appearance.locale":           "en-US",
		"appearance.font_size":        "large",
		"appearance.compact_tables":   "true",
		"appearance.reduced_motion":   "true",
		"appearance.high_contrast":    "true",
		"storage.exports_directory":   "artifacts/exports",
		"storage.cleanup_days":        "45",
		"storage.backup_count":        "12",
	} {
		if values[key] != expected {
			t.Fatalf("%s = %q, want %q", key, values[key], expected)
		}
	}
}

func TestValidateSettingsFormRejectsEscapingStoragePaths(t *testing.T) {
	for _, candidate := range []string{"../outside", "exports/../../outside", `C:\\outside`} {
		form := completeSettingsForm()
		form.Set("exports_directory", candidate)
		request := httptest.NewRequest("POST", "/api/v1/settings", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if _, err := validateSettingsForm(request); err == nil {
			t.Fatalf("validateSettingsForm() accepted %q", candidate)
		}
	}
}

func completeSettingsForm() url.Values {
	return url.Values{
		"language":               {"en"},
		"zoom":                   {"12"},
		"depth":                  {"10"},
		"max_runtime":            {"60m"},
		"concurrency":            {"4"},
		"browser_pool_size":      {"2"},
		"pages_per_browser":      {"2"},
		"max_records":            {"0"},
		"retry_count":            {"3"},
		"retry_delay":            {"2s"},
		"page_timeout":           {"45s"},
		"random_delay_min":       {"0s"},
		"random_delay_max":       {"0s"},
		"low_disk_mb":            {"2048"},
		"radius":                 {"10000"},
		"grid_cell_km":           {"2.5"},
		"theme":                  {"system"},
		"sidebar_default":        {"expanded"},
		"date_time_format":       {"local"},
		"number_format":          {"locale"},
		"appearance_locale":      {"en"},
		"font_size":              {"medium"},
		"exports_directory":      {"exports"},
		"screenshots_directory":  {"screenshots"},
		"logs_directory":         {"logs"},
		"backups_directory":      {"backups"},
		"temporary_directory":    {"temp"},
		"maximum_storage_gb":     {"0"},
		"automatic_cleanup_days": {"30"},
		"backup_count":           {"10"},
		"version_retention_days": {"365"},
		"ai_endpoint":            {"http://127.0.0.1:11434"},
		"ai_model":               {""},
		"ai_timeout_seconds":     {"60"},
	}
}
