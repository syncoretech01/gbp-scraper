package web

import (
	"net/http"
)

type apiWorkspacePageData struct {
	BaseURL               string
	AuthenticationSummary string
	ExposedBeyondLoopback bool
	Endpoints             []apiEndpointView
}

type apiEndpointView struct {
	Method      string
	Path        string
	Description string
}

func (s *Server) apiWorkspacePage(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if host == "" {
		host = s.srv.Addr
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	activity, _ := s.appActivity(r)
	page := apiWorkspacePageData{
		BaseURL:               scheme + "://" + host,
		AuthenticationSummary: "Loopback-only trust; no API-key middleware is enabled",
		ExposedBeyondLoopback: wildcardBind(s.srv.Addr),
		Endpoints: []apiEndpointView{
			{Method: "GET", Path: "/api/v1/jobs", Description: "List jobs"},
			{Method: "POST", Path: "/api/v1/jobs", Description: "Create a validated job"},
			{Method: "GET", Path: "/api/v1/jobs/{id}", Description: "Read a job and its configuration"},
			{Method: "POST", Path: "/api/v1/jobs/{id}/pause", Description: "Pause at a safe worker boundary"},
			{Method: "POST", Path: "/api/v1/jobs/{id}/resume", Description: "Resume a paused job"},
			{Method: "POST", Path: "/api/v1/jobs/{id}/cancel", Description: "Cancel and retain committed results"},
			{Method: "GET", Path: "/api/v1/jobs/{id}/events", Description: "Stream lifecycle events with SSE"},
			{Method: "GET", Path: "/api/v1/jobs/{id}/download", Description: "Download current compatible CSV"},
			{Method: "GET", Path: "/api/v1/results", Description: "Search and filter normalized businesses"},
			{Method: "GET", Path: "/api/v1/results/{id}", Description: "Read details, provenance and versions"},
			{Method: "GET", Path: "/api/v1/system/health", Description: "Read database and workspace health"},
			{Method: "POST", Path: "/api/v1/system/backups", Description: "Create a verified local SQLite backup"},
		},
	}

	s.renderAppPage(w, "api", appPageData{
		Title:     "Local API",
		Subtitle:  "Use the versioned interface from scripts running on this computer.",
		ActiveNav: "api",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page:      page,
	})
}

func (s *Server) apiOpenAPI(w http.ResponseWriter, _ *http.Request) {
	paths := map[string]any{
		"/api/v1/jobs": map[string]any{
			"get":  map[string]any{"summary": "List jobs", "responses": map[string]any{"200": map[string]string{"description": "Jobs"}}},
			"post": map[string]any{"summary": "Create job", "responses": map[string]any{"201": map[string]string{"description": "Created"}}},
		},
		"/api/v1/jobs/{id}": map[string]any{
			"get": map[string]any{"summary": "Get job", "responses": map[string]any{"200": map[string]string{"description": "Job"}}},
		},
		"/api/v1/jobs/{id}/events": map[string]any{
			"get": map[string]any{"summary": "Stream job events", "responses": map[string]any{"200": map[string]string{"description": "text/event-stream"}}},
		},
		"/api/v1/results": map[string]any{
			"get": map[string]any{"summary": "Search normalized results", "responses": map[string]any{"200": map[string]string{"description": "Results"}}},
		},
		"/api/v1/system/health": map[string]any{
			"get": map[string]any{"summary": "Read local health", "responses": map[string]any{"200": map[string]string{"description": "Health"}}},
		},
	}
	renderJSON(w, http.StatusOK, map[string]any{
		"openapi": "3.1.0",
		"info": map[string]string{
			"title":       "Google Maps Scraper Local API",
			"version":     "1.0.0",
			"description": "Loopback-first API. Mutating browser requests require the local CSRF token.",
		},
		"servers": []map[string]string{{"url": "/"}},
		"paths":   paths,
	})
}
