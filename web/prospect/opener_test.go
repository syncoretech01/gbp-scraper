package prospect

import (
	"strings"
	"testing"
)

func TestRenderOpener(t *testing.T) {
	fields := map[string]string{
		"name":          "Maria's Bakery",
		"category":      "bakery",
		"city":          "Austin",
		"status":        StatusNoWebsite,
		"status_reason": "no website listed",
		"rating":        "4.8",
		"reviews":       "132",
		"tier":          "A",
	}

	cases := []struct {
		name     string
		template string
		want     string
	}{
		{
			name:     "all canonical placeholders",
			template: "{name}|{category}|{city}|{status}|{status_reason}|{rating}|{reviews}|{tier}",
			want:     "Maria's Bakery|bakery|Austin|NO_WEBSITE|no website listed|4.8|132|A",
		},
		{
			name:     "unknown placeholder renders empty",
			template: "Hi {name}, about {nonexistent_field}your site",
			want:     "Hi Maria's Bakery, about your site",
		},
		{
			name:     "unclosed brace stays literal",
			template: "curly { stays literal",
			want:     "curly { stays literal",
		},
		{
			name:     "non-token braces stay literal",
			template: "keep {Not A Token} and {UPPER} as-is",
			want:     "keep {Not A Token} and {UPPER} as-is",
		},
		{
			name:     "empty template",
			template: "",
			want:     "",
		},
		{
			name:     "adjacent placeholders",
			template: "{name}{tier}",
			want:     "Maria's BakeryA",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RenderOpener(tc.template, fields); got != tc.want {
				t.Fatalf("RenderOpener(%q) = %q, want %q", tc.template, got, tc.want)
			}
		})
	}
}

func TestRenderOpenerNilFields(t *testing.T) {
	if got := RenderOpener("Hi {name}!", nil); got != "Hi !" {
		t.Fatalf("RenderOpener with nil fields = %q, want %q", got, "Hi !")
	}
}

func TestDefaultOpenerTemplatesCoverEveryStatus(t *testing.T) {
	templates := DefaultOpenerTemplates()

	statuses := []string{
		StatusNoWebsite, StatusSocialOnly, StatusDead, StatusParked,
		StatusSSLBroken, StatusFreeBuilder, StatusNoHTTPS, StatusLive,
	}

	for _, status := range statuses {
		tmpl, ok := templates[status]
		if !ok || strings.TrimSpace(tmpl) == "" {
			t.Errorf("no template for status %s", status)

			continue
		}

		if !strings.Contains(tmpl, "{name}") {
			t.Errorf("template for %s should address the owner via {name}: %q", status, tmpl)
		}
	}

	if strings.TrimSpace(templates["default"]) == "" {
		t.Error("missing default fallback template")
	}
}

func TestOpenerTemplateForFallsBackToDefault(t *testing.T) {
	templates := DefaultOpenerTemplates()

	if got := OpenerTemplateFor(templates, StatusDead); got != templates[StatusDead] {
		t.Fatalf("OpenerTemplateFor(DEAD) = %q, want the DEAD template", got)
	}

	// Statuses without a template of their own fall back to "default".
	if got := OpenerTemplateFor(templates, "SOME_FUTURE_STATUS"); got != templates["default"] {
		t.Fatalf("OpenerTemplateFor(unknown) = %q, want default", got)
	}

	trimmed := map[string]string{
		StatusDead: "   ",
		"default":  "fallback text",
	}

	if got := OpenerTemplateFor(trimmed, StatusDead); got != "fallback text" {
		t.Fatalf("OpenerTemplateFor(blank template) = %q, want the default fallback", got)
	}
}

func TestDefaultOpenerRendersNaturally(t *testing.T) {
	templates := DefaultOpenerTemplates()
	got := RenderOpener(templates[StatusNoWebsite], map[string]string{
		"name":     "Joe",
		"category": "plumbers",
		"city":     "Denver",
	})

	want := "Hi Joe, I searched for plumbers in Denver and found you on Google — but couldn't find a website to send people to. Do you have one I missed?"
	if got != want {
		t.Fatalf("rendered opener = %q, want %q", got, want)
	}
}
