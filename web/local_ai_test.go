package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidateLocalAIEndpointRejectsPublicAndNestedTargets(t *testing.T) {
	for _, candidate := range []string{
		"https://example.com", "http://127.0.0.1:11434/api/generate", "file:///tmp/model",
	} {
		if _, err := validateLocalAIEndpoint(candidate); err == nil {
			t.Fatalf("validateLocalAIEndpoint(%q) succeeded", candidate)
		}
	}
	if endpoint, err := validateLocalAIEndpoint("http://127.0.0.1:11434/"); err != nil || endpoint != "http://127.0.0.1:11434" {
		t.Fatalf("validated endpoint = %q, %v", endpoint, err)
	}
}

func TestLocalAIPromptIsTaskBoundedAndRequiresJSONContext(t *testing.T) {
	prompt, err := localAIPrompt(localAIAssistRequest{
		Task: "keyword_variations", Input: "dentists in San Francisco", Context: json.RawMessage(`{"language":"en"}`),
	})
	if err != nil || !strings.Contains(prompt, "exactly one valid JSON object") || !strings.Contains(prompt, "at most 30") {
		t.Fatalf("localAIPrompt() = %q, %v", prompt, err)
	}
	if _, err := localAIPrompt(localAIAssistRequest{Task: "shell", Input: "run this"}); err == nil {
		t.Fatal("unsupported task was accepted")
	}
	if _, err := localAIPrompt(localAIAssistRequest{Task: "result_filters", Input: "email", Context: json.RawMessage(`{`)}); err == nil {
		t.Fatal("invalid JSON context was accepted")
	}
}

func TestCallLocalAIUsesBoundedLocalJSONEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"test","response":"{\\"keywords\\":[\\"dentists\\"]}"}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload, status, err := callLocalAI(ctx, server.URL, http.MethodPost, "/api/generate", []byte(`{"model":"test"}`))
	if err != nil || status != http.StatusOK || !strings.Contains(string(payload), `"model":"test"`) {
		t.Fatalf("callLocalAI() = %d %s, %v", status, payload, err)
	}
}

func TestValidateLocalAISettingsRequiresModelOnlyWhenEnabled(t *testing.T) {
	form := completeSettingsForm()
	settings, err := validateLocalAISettings(formReader(form))
	if err != nil || settings.Enabled || settings.Endpoint != "http://127.0.0.1:11434" {
		t.Fatalf("disabled settings = %+v, %v", settings, err)
	}
	form.Set("ai_enabled", "on")
	if _, err := validateLocalAISettings(formReader(form)); err == nil {
		t.Fatal("enabled local AI without a model was accepted")
	}
	form.Set("ai_model", "qwen2.5:3b")
	settings, err = validateLocalAISettings(formReader(form))
	if err != nil || !settings.Enabled || settings.Model != "qwen2.5:3b" {
		t.Fatalf("enabled settings = %+v, %v", settings, err)
	}
}

type formReader map[string][]string

func (form formReader) FormValue(key string) string {
	if values := form[key]; len(values) > 0 {
		return values[0]
	}
	return ""
}
