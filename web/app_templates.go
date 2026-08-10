package web

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const appTemplateRoot = "static/templates/app"

func (s *Server) loadAppTemplates() error {
	partials, err := fs.Glob(static, appTemplateRoot+"/partials/*.html")
	if err != nil {
		return fmt.Errorf("find app template partials: %w", err)
	}

	pages, err := fs.Glob(static, appTemplateRoot+"/pages/*.html")
	if err != nil {
		return fmt.Errorf("find app page templates: %w", err)
	}

	common := append([]string{appTemplateRoot + "/layout.html"}, partials...)

	for _, pageFile := range pages {
		files := append(append([]string{}, common...), pageFile)
		name := strings.TrimSuffix(path.Base(pageFile), path.Ext(pageFile))

		parsed, err := template.New(name).ParseFS(static, files...)
		if err != nil {
			return fmt.Errorf("parse app page %s: %w", name, err)
		}

		s.tmpl["app/"+name] = parsed
	}

	return nil
}

func (s *Server) renderAppPage(w http.ResponseWriter, key string, data appPageData) {
	tmpl, ok := s.tmpl["app/"+key]
	if !ok {
		http.Error(w, "page is not available", http.StatusNotFound)

		return
	}

	entry := "app/page/" + strings.ReplaceAll(key, "_", "-")
	data.Features = s.appFeatures()
	if s.svc != nil {
		values, err := s.svc.LoadSettings(context.Background())
		if err == nil {
			if theme := values["appearance.theme"]; theme == "system" || theme == "light" || theme == "dark" {
				data.Theme = theme
			}
		}
	}

	var rendered bytes.Buffer
	if err := tmpl.ExecuteTemplate(&rendered, entry, data); err != nil {
		http.Error(w, "could not render page", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = rendered.WriteTo(w)
}

func (s *Server) appFeatures() appFeatureFlags {
	features := appFeatureFlags{API: true}
	if s == nil || s.svc == nil || s.svc.repo == nil {
		return features
	}
	_, features.SavedSearches = s.svc.repo.(reusableRepository)
	_, features.Schedules = s.svc.repo.(scheduleRepository)
	_, features.Proxies = s.svc.repo.(proxyRepository)
	_, features.Exports = s.svc.repo.(exportRepository)
	_, features.System = s.svc.repo.(maintenanceRepository)
	_, features.Settings = s.svc.repo.(settingsRepository)
	features.Onboarding = features.System && features.Settings
	return features
}
