package web

import "testing"

// Provenance answers "where did this value come from" in the specification's
// words, not in storage tokens, and never leaves a stored token unrendered.

func TestProvenanceSourceTypeLabelCoversTheSpecificationVocabulary(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"google_maps":      "Google Maps",
		"google_maps_csv":  "Google Maps",
		"website_homepage": "Website homepage",
		"website_contact":  "Contact page",
		"website_about":    "About page",
		"website_footer":   "Website footer",
		"structured_data":  "Structured data",
		"manual_edit":      "Manual edit",
	}

	for token, want := range tests {
		if got := ProvenanceSourceTypeLabel(token); got != want {
			t.Errorf("ProvenanceSourceTypeLabel(%q) = %q, want %q", token, got, want)
		}
	}

	for _, source := range ProvenanceSourceTypes() {
		if ProvenanceSourceTypeLabel(source) == "" {
			t.Errorf("source type %q has no label", source)
		}
	}
}

func TestProvenanceLabelsDegradeGracefully(t *testing.T) {
	t.Parallel()

	if got := ProvenanceSourceTypeLabel(""); got != "Not recorded" {
		t.Errorf("empty source type = %q, want Not recorded", got)
	}

	// A page kind the crawler learns later still reads as a page, not a token.
	if got := ProvenanceSourceTypeLabel("website_pricing"); got != "Website pricing" {
		t.Errorf("unknown website source = %q, want Website pricing", got)
	}

	if got := ProvenanceSourceTypeLabel("partner_feed"); got != "partner feed" {
		t.Errorf("unknown source = %q, want a humanised token", got)
	}

	if got := ProvenanceMethodLabel("structured_data"); got != "structured data" {
		t.Errorf("structured data method = %q", got)
	}

	if got := ProvenanceMethodLabel(""); got != "not recorded" {
		t.Errorf("empty method = %q, want not recorded", got)
	}
}
