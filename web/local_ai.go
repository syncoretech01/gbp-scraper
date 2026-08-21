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
	"strconv"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

const (
	maximumLocalAIInputBytes    = 16 << 10
	maximumLocalAIContextBytes  = 64 << 10
	maximumLocalAIResponseBytes = 2 << 20
)

type localAISettings struct {
	Enabled        bool
	Endpoint       string
	Model          string
	TimeoutSeconds int
}

type localAIAssistRequest struct {
	Task    string          `json:"task"`
	Input   string          `json:"input"`
	Context json.RawMessage `json:"context,omitempty"`
}

type localAIAssistResponse struct {
	Task       string `json:"task"`
	Model      string `json:"model"`
	Result     any    `json:"result"`
	DurationMS int64  `json:"duration_ms"`
}

type ollamaGenerateRequest struct {
	Model   string         `json:"model"`
	Prompt  string         `json:"prompt"`
	Stream  bool           `json:"stream"`
	Format  string         `json:"format"`
	Options map[string]any `json:"options"`
}

type ollamaGenerateResponse struct {
	Model    string `json:"model"`
	Response string `json:"response"`
	Error    string `json:"error"`
}

func defaultLocalAISettings() localAISettings {
	return localAISettings{Endpoint: "http://127.0.0.1:11434", TimeoutSeconds: 60}
}

func localAISettingsFromMap(values map[string]string) localAISettings {
	settings := defaultLocalAISettings()
	settings.Enabled = values["ai.enabled"] == "true"
	settings.Endpoint = settingString(values, "ai.endpoint", settings.Endpoint)
	settings.Model = strings.TrimSpace(values["ai.model"])
	settings.TimeoutSeconds = settingInt(values, "ai.timeout_seconds", settings.TimeoutSeconds)
	if settings.TimeoutSeconds < 5 || settings.TimeoutSeconds > 300 {
		settings.TimeoutSeconds = 60
	}

	return settings
}

func validateLocalAISettings(r formValueReader) (localAISettings, error) {
	settings := defaultLocalAISettings()
	settings.Enabled = formBoolean(r, "ai_enabled", false)
	settings.Endpoint = strings.TrimSpace(r.FormValue("ai_endpoint"))
	if settings.Endpoint == "" {
		settings.Endpoint = defaultLocalAISettings().Endpoint
	}
	endpoint, err := validateLocalAIEndpoint(settings.Endpoint)
	if err != nil {
		return localAISettings{}, err
	}
	settings.Endpoint = endpoint
	settings.Model = strings.TrimSpace(r.FormValue("ai_model"))
	if len(settings.Model) > 128 || strings.ContainsAny(settings.Model, "\x00\r\n") {
		return localAISettings{}, fmt.Errorf("local AI model name must be at most 128 characters")
	}
	settings.TimeoutSeconds = 60
	if raw := strings.TrimSpace(r.FormValue("ai_timeout_seconds")); raw != "" {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || value < 5 || value > 300 {
			return localAISettings{}, fmt.Errorf("local AI timeout must be between 5 and 300 seconds")
		}
		settings.TimeoutSeconds = value
	}
	if settings.Enabled && settings.Model == "" {
		return localAISettings{}, fmt.Errorf("choose an installed Ollama model before enabling local AI")
	}

	return settings, nil
}

func validateLocalAIEndpoint(raw string) (string, error) {
	parsed, err := validateLocalWebhookURL(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil {
		return "", fmt.Errorf("local AI endpoint: %w", err)
	}
	if parsed.RawQuery != "" {
		return "", fmt.Errorf("local AI endpoint cannot contain a query")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("local AI endpoint must be the Ollama server root")
	}
	// Ollama is a local process. Unlike an outbound webhook, which may legitimately
	// address a local service by name, the AI endpoint is only ever reached over the
	// loopback or a private address, so reject public hosts before any request is
	// built rather than relying solely on the dial-time guard.
	if !localAIHostIsLocal(parsed.Hostname()) {
		return "", fmt.Errorf("local AI endpoint must be a loopback or private address")
	}
	parsed.Path = ""

	return strings.TrimRight(parsed.String(), "/"), nil
}

// localAIHostIsLocal reports whether host is a loopback or private target that a
// local Ollama server can plausibly occupy. Named hosts are rejected unless they
// are the reserved localhost name, because a name can resolve anywhere.
func localAIHostIsLocal(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return false
	}
	if address := net.ParseIP(host); address != nil {
		return permittedLocalIntegrationIP(address)
	}

	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

func (s *Server) registerLocalAIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/ai/status", s.apiLocalAIStatus)
	mux.HandleFunc("POST /api/v1/ai/assist", s.apiLocalAIAssist)
}

func (s *Server) apiLocalAIStatus(w http.ResponseWriter, r *http.Request) {
	settings, err := s.loadLocalAISettings(r.Context())
	if err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "ai_settings_failed", "Could not load local AI settings")
		return
	}
	if !settings.Enabled {
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]any{
			"enabled": false, "reachable": false, "endpoint": settings.Endpoint,
		}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), min(time.Duration(settings.TimeoutSeconds)*time.Second, 10*time.Second))
	defer cancel()
	payload, status, err := callLocalAI(ctx, settings.Endpoint, http.MethodGet, "/api/tags", nil)
	if err != nil {
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]any{
			"enabled": true, "reachable": false, "endpoint": settings.Endpoint,
			"model": settings.Model, "error": publicLocalAIError(err),
		}})
		return
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(payload, &tags); err != nil {
		renderLocalAPIError(w, http.StatusBadGateway, "ai_invalid_response", "Ollama returned invalid model metadata")
		return
	}
	models := make([]string, 0, len(tags.Models))
	for _, model := range tags.Models {
		if len(models) >= 100 {
			break
		}
		models = append(models, model.Name)
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]any{
		"enabled": true, "reachable": true, "http_status": status,
		"endpoint": settings.Endpoint, "model": settings.Model, "models": models,
	}})
}

func (s *Server) apiLocalAIAssist(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	settings, err := s.loadLocalAISettings(r.Context())
	if err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "ai_settings_failed", "Could not load local AI settings")
		return
	}
	if !settings.Enabled {
		renderLocalAPIError(w, http.StatusConflict, "ai_disabled", "Local AI is disabled in Settings")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maximumLocalAIInputBytes+maximumLocalAIContextBytes+4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input localAIAssistRequest
	if err := decoder.Decode(&input); err != nil {
		renderLocalAPIError(w, http.StatusBadRequest, "invalid_ai_request", "Request must be one bounded JSON object")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		renderLocalAPIError(w, http.StatusBadRequest, "invalid_ai_request", "Request must contain exactly one JSON object")
		return
	}
	input.Input = strings.TrimSpace(input.Input)
	if input.Input == "" || len(input.Input) > maximumLocalAIInputBytes || len(input.Context) > maximumLocalAIContextBytes {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_ai_input", "Input must contain 1 to 16384 characters and context must be at most 65536 bytes")
		return
	}
	prompt, err := localAIPrompt(input)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_ai_task", err.Error())
		return
	}
	requestBody, err := json.Marshal(ollamaGenerateRequest{
		Model: settings.Model, Prompt: prompt, Stream: false, Format: "json",
		Options: map[string]any{"temperature": 0.2, "num_predict": 1200},
	})
	if err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "ai_request_failed", "Could not prepare local AI request")
		return
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(settings.TimeoutSeconds)*time.Second)
	defer cancel()
	payload, _, err := callLocalAI(ctx, settings.Endpoint, http.MethodPost, "/api/generate", requestBody)
	if err != nil {
		renderLocalAPIError(w, http.StatusBadGateway, "ai_unavailable", publicLocalAIError(err))
		return
	}
	var generated ollamaGenerateResponse
	if err := json.Unmarshal(payload, &generated); err != nil || generated.Error != "" {
		message := generated.Error
		if message == "" {
			message = "Ollama returned an invalid response"
		}
		renderLocalAPIError(w, http.StatusBadGateway, "ai_generation_failed", publicLocalAIError(errors.New(message)))
		return
	}
	var result any
	if err := json.Unmarshal([]byte(generated.Response), &result); err != nil {
		renderLocalAPIError(w, http.StatusBadGateway, "ai_invalid_json", "The local model did not return the required JSON object")
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: localAIAssistResponse{
		Task: input.Task, Model: generated.Model, Result: result, DurationMS: time.Since(started).Milliseconds(),
	}})
}

func (s *Server) loadLocalAISettings(ctx context.Context) (localAISettings, error) {
	values, err := s.svc.LoadSettings(ctx)
	if err != nil {
		return localAISettings{}, err
	}

	return localAISettingsFromMap(values), nil
}

func localAIPrompt(request localAIAssistRequest) (string, error) {
	instructions := map[string]string{
		"keyword_variations":   `Return {"keywords":[string],"exclude_keywords":[string],"category_groups":[string]}. Provide at most 30 specific, non-duplicate Maps search queries.`,
		"scrape_configuration": `Return {"name":string,"keywords":[string],"location":string,"latitude":number|null,"longitude":number|null,"radius_metres":number,"grid_cell_km":number,"depth":number,"warnings":[string]}. Prefer conservative local settings and never invent coordinates when unknown.`,
		"result_filters":       `Return a Results Explorer expression as {"logic":"and|or","filters":[{"field":string,"operator":string,"value":string}],"groups":[]}. Use only documented local fields and operators.`,
		"classify_business":    `Return {"categories":[string],"quality_band":"low|medium|high","confidence":number,"reasoning":[string]}. Base every statement only on supplied data.`,
		"explain_quality":      `Return {"summary":string,"positive_factors":[string],"negative_factors":[string],"next_actions":[string]}. Explain supplied score evidence without adding facts.`,
		"explain_duplicate":    `Return {"summary":string,"matching_evidence":[string],"conflicting_evidence":[string],"recommendation":"merge|keep_both|needs_review","confidence":number}. Compare only the supplied candidate records and never assert that either record is authoritative.`,
		"summarize_changes":    `Return {"summary":string,"important_changes":[string],"follow_up":[string]}. Describe only supplied before/after evidence.`,
		"summarize_business":   `Return {"summary":string,"services":[string],"audience":[string],"caveats":[string]}. Summarize only the supplied description, categories, and website text; never invent services or claims the source does not state.`,
		"suggest_coverage":     `Return {"cities":[string],"categories":[string],"exclude_keywords":[string],"warnings":[string]}. Suggestions must be clearly labelled and not treated as verified facts.`,
	}
	instruction, ok := instructions[strings.TrimSpace(request.Task)]
	if !ok {
		return "", fmt.Errorf("task must be one of keyword_variations, scrape_configuration, result_filters, " +
			"classify_business, explain_quality, explain_duplicate, summarize_changes, summarize_business, or suggest_coverage")
	}
	contextValue := "null"
	if len(request.Context) > 0 {
		if !json.Valid(request.Context) {
			return "", fmt.Errorf("context must be valid JSON")
		}
		contextValue = string(request.Context)
	}

	return "You are an optional, local-only assistant for a Google Maps research workspace. " +
		"Return exactly one valid JSON object, no markdown or prose outside JSON. Do not claim verification, legal compliance, or mailbox validity. " +
		instruction + "\nUser input:\n" + request.Input + "\nStructured context:\n" + contextValue, nil
}

func callLocalAI(ctx context.Context, endpoint, method, path string, body []byte) ([]byte, int, error) {
	base, err := validateLocalAIEndpoint(endpoint)
	if err != nil {
		return nil, 0, err
	}
	target, err := url.JoinPath(base, path)
	if err != nil {
		return nil, 0, fmt.Errorf("build Ollama URL: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "google-maps-scraper-local-ai/1")
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	transport := &http.Transport{
		Proxy: nil, DisableCompression: true, DialContext: dialLocalIntegration,
		MaxIdleConns: 2, IdleConnTimeout: 10 * time.Second, TLSHandshakeTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) >= 2 {
				return fmt.Errorf("Ollama redirected too many times")
			}
			_, err := validateLocalAIEndpoint(next.URL.Scheme + "://" + next.URL.Host)
			return err
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("connect to local Ollama: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maximumLocalAIResponseBytes+1))
	if err != nil || len(payload) > maximumLocalAIResponseBytes {
		return nil, response.StatusCode, fmt.Errorf("read bounded Ollama response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, response.StatusCode, fmt.Errorf("Ollama returned HTTP %d", response.StatusCode)
	}

	return payload, response.StatusCode, nil
}

func publicLocalAIError(err error) string {
	if err == nil {
		return "Local AI request failed"
	}
	message := jobruntime.RedactString(err.Error())
	if len(message) > 500 {
		message = message[:500]
	}

	return message
}
