package web

import "strings"

// Field provenance answers one question for every stored value: where did this
// come from, and how sure are we? The repository writes machine tokens
// (google_maps_csv, website_contact, manual_edit, structured_data, …) so the
// data stays stable across releases; this file is the single place that turns
// those tokens into the vocabulary the specification and the operator use.

// ProvenanceSourceTypes lists the recognised source types in the order the
// specification names them. A stored value outside this list is still rendered,
// humanised rather than hidden, so an unexpected token never becomes a blank
// cell in the drawer.
func ProvenanceSourceTypes() []string {
	return []string{
		"google_maps",
		"website_homepage",
		"website_contact",
		"website_about",
		"website_footer",
		"structured_data",
		"manual_edit",
	}
}

// provenanceSourceTypeLabels maps every stored source-type token, including the
// historical spellings, onto its display name.
//
//nolint:gochecknoglobals // Immutable lookup table, safe to share.
var provenanceSourceTypeLabels = map[string]string{
	"google_maps":      "Google Maps",
	"google_maps_csv":  "Google Maps",
	"maps":             "Google Maps",
	"website":          "Website",
	"website_homepage": "Website homepage",
	"website_contact":  "Contact page",
	"website_about":    "About page",
	"website_footer":   "Website footer",
	"structured_data":  "Structured data",
	"manual_edit":      "Manual edit",
	"duplicate_merge":  "Duplicate merge",
}

// provenanceMethodLabels names the extraction methods the local extractors
// record alongside a value.
//
//nolint:gochecknoglobals // Immutable lookup table, safe to share.
var provenanceMethodLabels = map[string]string{
	"legacy_csv_import": "Google Maps CSV import",
	"mailto":            "mailto link",
	"visible_text":      "visible page text",
	"deobfuscated_text": "de-obfuscated page text",
	"structured_data":   "structured data",
	"telephone_link":    "telephone link",
	"social_link":       "social profile link",
	"manual_edit":       "operator correction",
}

// ProvenanceSourceTypeLabel renders a stored source type for display.
func ProvenanceSourceTypeLabel(value string) string {
	return provenanceLabel(value, provenanceSourceTypeLabels, "Not recorded")
}

// ProvenanceMethodLabel renders a stored extraction method for display.
func ProvenanceMethodLabel(value string) string {
	return provenanceLabel(value, provenanceMethodLabels, "not recorded")
}

// provenanceLabel looks a token up, falling back to a humanised form of the
// token itself so a source or method added elsewhere still reads sensibly.
func provenanceLabel(value string, labels map[string]string, missing string) string {
	token := strings.ToLower(strings.TrimSpace(value))
	if token == "" {
		return missing
	}

	if label, ok := labels[token]; ok {
		return label
	}

	// A website_<page> token that the crawler learns later still reads as a
	// page kind rather than as a raw identifier.
	if page, ok := strings.CutPrefix(token, "website_"); ok {
		return "Website " + strings.ReplaceAll(page, "_", " ")
	}

	return strings.ReplaceAll(token, "_", " ")
}
