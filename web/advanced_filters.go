package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// Export scope keys. A scope names exactly which businesses an export
// contains. It is deliberately explicit: an operator can always answer "which
// rows are in this file?" from the scope alone, and no scope silently widens
// into another.
const (
	// ExportScopeFiltered exports the rows the Results table is showing right
	// now. It requires at least one narrowing condition, because a filtered
	// export with no filter is the whole workspace wearing a narrower label.
	ExportScopeFiltered = "filtered"
	// ExportScopeSelected exports only the business IDs the operator ticked.
	ExportScopeSelected = "selected"
	// ExportScopeJob exports every business one source job discovered.
	ExportScopeJob = "job"
	// ExportScopeWorkspace exports every normalized business on this machine.
	ExportScopeWorkspace = "all"
	// ExportScopeSavedView exports a stored saved view's own query.
	ExportScopeSavedView = "saved_view"
)

// ExportScopeOption describes one selectable scope for the interface. Count is
// how many businesses the scope currently contains, so the operator sees the
// size of a delivery before creating it.
type ExportScopeOption struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Hint      string `json:"hint"`
	Count     int64  `json:"count"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// exportScopeDefinitions is the single source of truth for scope keys and the
// words the interface uses for them. Every surface (the builder, the Results
// export buttons, the export history, the API) reads these labels, so a scope
// cannot be described one way in one place and another way somewhere else.
var exportScopeDefinitions = []ExportScopeOption{
	{Key: ExportScopeFiltered, Label: "Current filtered view", Hint: "Exactly the rows the Results table is showing now"},
	{Key: ExportScopeSelected, Label: "Selected businesses", Hint: "Only the businesses that were ticked"},
	{Key: ExportScopeJob, Label: "Current source job", Hint: "Every business one scrape job discovered"},
	{Key: ExportScopeWorkspace, Label: "Entire workspace", Hint: "Every normalized business stored on this machine"},
	{Key: ExportScopeSavedView, Label: "Saved view", Hint: "The stored query behind a saved view"},
}

// ExportScopes returns the scope catalogue in presentation order.
func ExportScopes() []ExportScopeOption {
	scopes := make([]ExportScopeOption, len(exportScopeDefinitions))
	copy(scopes, exportScopeDefinitions)

	return scopes
}

// exportScopeLabel names one scope for a human. Unknown keys fall back to the
// raw key so a stored historical value is still readable.
func exportScopeLabel(key string) string {
	for _, scope := range exportScopeDefinitions {
		if scope.Key == key {
			return scope.Label
		}
	}

	return strings.ReplaceAll(strings.TrimSpace(key), "_", " ")
}

// normalizeExportScope validates a submitted scope key.
func normalizeExportScope(value string) (string, error) {
	scope := strings.ToLower(strings.TrimSpace(value))
	if scope == "" {
		return ExportScopeFiltered, nil
	}
	for _, known := range exportScopeDefinitions {
		if known.Key == scope {
			return scope, nil
		}
	}

	return "", fmt.Errorf("unsupported export source scope")
}

// searchIsNarrowed reports whether a result query actually restricts the
// workspace. Duplicate inclusion widens a query rather than narrowing it, so
// it deliberately does not count.
func searchIsNarrowed(search ResultSearch) bool {
	return strings.TrimSpace(search.Query) != "" ||
		strings.TrimSpace(search.JobID) != "" ||
		len(search.Filters) > 0 ||
		search.FilterGroup != nil
}

// exportScopeInputs are the request inputs each scope consumes. Anything a
// scope does not consume must not be quietly discarded, which is how an export
// labelled with one job ends up carrying another job's rows.
var exportScopeInputs = map[string][]string{
	ExportScopeFiltered: {
		"q", "job_id", "filter_field", "filter_operator", "filter_value",
		"filter_json", "filter_logic", "sort", "include_duplicates",
	},
	ExportScopeSelected:  {"selected_ids"},
	ExportScopeJob:       {"job_id"},
	ExportScopeWorkspace: {},
	ExportScopeSavedView: {"saved_view_id"},
}

// scopeSensitiveExportInputs are the inputs that change which businesses an
// export contains. They are checked against the chosen scope; formatting,
// column, and delivery inputs are never scope sensitive.
var scopeSensitiveExportInputs = []string{
	"q", "job_id", "filter_field", "filter_operator", "filter_value",
	"filter_json", "selected_ids", "saved_view_id",
}

// exportScopeInputLabels name the ignored inputs in the words the operator saw
// on screen.
var exportScopeInputLabels = map[string]string{
	"q":               "a search text",
	"job_id":          "a source job",
	"filter_field":    "table filters",
	"filter_operator": "table filters",
	"filter_value":    "table filters",
	"filter_json":     "an advanced filter expression",
	"selected_ids":    "selected business IDs",
	"saved_view_id":   "a saved view",
}

// conflictingExportScopeInputs lists, in operator language, the submitted
// inputs the chosen scope would silently ignore. An export must never quietly
// widen past what its own form is showing, so a non-empty result is refused
// rather than dropped.
func conflictingExportScopeInputs(scope string, reader formValueReader) []string {
	consumed := make(map[string]struct{})
	for _, name := range exportScopeInputs[scope] {
		consumed[name] = struct{}{}
	}
	conflicts := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, name := range scopeSensitiveExportInputs {
		if _, ok := consumed[name]; ok {
			continue
		}
		if strings.TrimSpace(reader.FormValue(name)) == "" {
			continue
		}
		label := exportScopeInputLabels[name]
		if label == "" {
			label = name
		}
		if _, repeated := seen[label]; repeated {
			continue
		}
		seen[label] = struct{}{}
		conflicts = append(conflicts, label)
	}

	return conflicts
}

// exportScopeSearch turns one scope plus the request into the canonical
// ResultSearch behind it. It is the only place a scope becomes a query, so the
// Results count, the scope preview, and the generated file cannot disagree.
func (s *Server) exportScopeSearch(r *http.Request, scope string, filtered ResultSearch) (ResultSearch, string, error) {
	switch scope {
	case ExportScopeWorkspace:
		return ResultSearch{Sort: "updated_desc"}, "", nil
	case ExportScopeFiltered:
		return filtered, "", nil
	case ExportScopeJob:
		jobID := strings.TrimSpace(r.FormValue("job_id"))
		if jobID == "" {
			return ResultSearch{}, "", fmt.Errorf("choose the source job to export")
		}
		if len(jobID) > 128 {
			return ResultSearch{}, "", fmt.Errorf("source job ID is too long")
		}

		return ResultSearch{Sort: "updated_desc", JobID: jobID}, "", nil
	case ExportScopeSelected:
		ids, err := parseSelectedExportIDs(r.FormValue("selected_ids"))
		if err != nil {
			return ResultSearch{}, "", err
		}

		return ResultSearch{
			Sort: "updated_desc",
			Filters: []ResultFilter{{
				Field: "id", Operator: "in", Value: strings.Join(ids, ","),
			}},
		}, "", nil
	case ExportScopeSavedView:
		viewID := strings.TrimSpace(r.FormValue("saved_view_id"))
		if !validBusinessID(viewID) {
			return ResultSearch{}, "", fmt.Errorf("a valid saved view is required")
		}
		view, err := s.svc.GetSavedResultView(r.Context(), viewID)
		if err != nil {
			return ResultSearch{}, "", fmt.Errorf("saved view was not found")
		}

		return view.Search, view.ID, nil
	}

	return ResultSearch{}, "", fmt.Errorf("unsupported export source scope")
}

// countResultSearch returns how many businesses one query matches without
// reading a single row of it.
func (s *Server) countResultSearch(ctx context.Context, search ResultSearch) (int64, error) {
	search.Limit = 1
	search.Offset = 0
	page, err := s.svc.SearchBusinesses(ctx, search)
	if err != nil {
		return 0, err
	}

	return page.Total, nil
}
