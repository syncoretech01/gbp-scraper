package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"
)

type exportAPIInput struct {
	Name              string                  `json:"name"`
	Format            string                  `json:"format"`
	SourceScope       string                  `json:"source_scope,omitempty"`
	Query             string                  `json:"query,omitempty"`
	JobID             string                  `json:"job_id,omitempty"`
	SavedViewID       string                  `json:"saved_view_id,omitempty"`
	SelectedIDs       []string                `json:"selected_ids,omitempty"`
	Sort              string                  `json:"sort,omitempty"`
	IncludeDuplicates bool                    `json:"include_duplicates,omitempty"`
	Filters           []ResultFilter          `json:"filters,omitempty"`
	FilterGroup       *ResultFilterGroup      `json:"filter_group,omitempty"`
	Columns           []ExportColumnSelection `json:"columns,omitempty"`
	SplitBy           string                  `json:"split_by,omitempty"`
	MaxRowsPerFile    int64                   `json:"max_rows_per_file,omitempty"`
	ZIP               bool                    `json:"zip,omitempty"`
	IncludeRaw        bool                    `json:"include_raw,omitempty"`
	IncludeSources    bool                    `json:"include_sources,omitempty"`
	IncludeProvenance bool                    `json:"include_provenance,omitempty"`
	IncludeChanges    bool                    `json:"include_changes,omitempty"`
	Deduplicate       *bool                   `json:"deduplicate,omitempty"`
	SavePreset        bool                    `json:"save_preset,omitempty"`
	PresetName        string                  `json:"preset_name,omitempty"`
	PresetID          string                  `json:"preset_id,omitempty"`
	RepeatID          string                  `json:"repeat_id,omitempty"`
}

func prepareExportCreationForm(w http.ResponseWriter, r *http.Request) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return parseBoundedRequestForm(w, r, maximumExportFilterBytes)
	}
	r.Body = http.MaxBytesReader(w, r.Body, maximumExportFilterBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input exportAPIInput
	if err := decoder.Decode(&input); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request must contain exactly one JSON object")
	}
	values := make(url.Values)
	values.Set("name", input.Name)
	values.Set("format", input.Format)
	values.Set("source_scope", input.SourceScope)
	values.Set("q", input.Query)
	values.Set("job_id", input.JobID)
	values.Set("saved_view_id", input.SavedViewID)
	values.Set("selected_ids", strings.Join(input.SelectedIDs, ","))
	values.Set("sort", input.Sort)
	values.Set("include_duplicates", strconv.FormatBool(input.IncludeDuplicates))
	values.Set("split_by", input.SplitBy)
	if input.MaxRowsPerFile > 0 {
		values.Set("max_rows_per_file", strconv.FormatInt(input.MaxRowsPerFile, 10))
	}
	values.Set("zip", strconv.FormatBool(input.ZIP))
	values.Set("include_raw", strconv.FormatBool(input.IncludeRaw))
	values.Set("include_sources", strconv.FormatBool(input.IncludeSources))
	values.Set("include_provenance", strconv.FormatBool(input.IncludeProvenance))
	values.Set("include_changes", strconv.FormatBool(input.IncludeChanges))
	if input.Deduplicate != nil {
		values.Set("deduplicate", strconv.FormatBool(*input.Deduplicate))
	}
	values.Set("save_preset", strconv.FormatBool(input.SavePreset))
	values.Set("preset_name", input.PresetName)
	values.Set("preset_id", input.PresetID)
	values.Set("repeat_id", input.RepeatID)
	for _, filter := range input.Filters {
		values.Add("filter_field", filter.Field)
		values.Add("filter_operator", filter.Operator)
		values.Add("filter_value", filter.Value)
	}
	if input.FilterGroup != nil {
		encoded, err := json.Marshal(input.FilterGroup)
		if err != nil {
			return err
		}
		values.Set("filter_json", string(encoded))
	}
	if len(input.Columns) > 0 {
		lines := make([]string, 0, len(input.Columns))
		for _, column := range input.Columns {
			lines = append(lines, column.Key+"="+column.Label)
		}
		values.Set("columns_spec", strings.Join(lines, "\n"))
	}
	r.Form = values
	r.PostForm = values
	return nil
}

type exportCreationRequest struct {
	Name           string
	Format         string
	SourceType     string
	Search         ResultSearch
	Columns        []ExportColumnSelection
	Options        ExportBuildOptions
	PresetID       string
	SavedViewID    string
	SavePresetName string
}

func (s *Server) registerExportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/exports", s.listExportsAPI)
	mux.HandleFunc("POST /api/v1/exports", s.createResultsExport)
	mux.HandleFunc("GET /api/v1/exports/presets", s.listExportPresetsAPI)
	mux.HandleFunc("GET /api/v1/exports/{id}", s.exportStatusAPI)
	mux.HandleFunc("DELETE /api/v1/exports/{id}", s.deleteExport)
	mux.HandleFunc("POST /api/v1/exports/{id}/delete", s.deleteExport)
	mux.HandleFunc("POST /api/v1/exports/{id}/repeat", s.repeatExport)
	mux.HandleFunc("GET /api/v1/exports/{id}/download", s.downloadExport)
}

func (s *Server) resolveExportCreation(r *http.Request) (exportCreationRequest, error) {
	if repeatID := strings.TrimSpace(r.FormValue("repeat_id")); repeatID != "" {
		return s.repeatedExportCreation(r, repeatID)
	}
	if presetID := strings.TrimSpace(r.FormValue("preset_id")); presetID != "" {
		return s.presetExportCreation(r, presetID)
	}
	format := strings.ToLower(strings.TrimSpace(r.FormValue("format")))
	if _, ok := exportExtension(format); !ok {
		return exportCreationRequest{}, fmt.Errorf("unsupported export format")
	}
	name, err := validExportName(r.FormValue("name"), "business-results")
	if err != nil {
		return exportCreationRequest{}, err
	}
	options, err := parseExportBuildOptions(r)
	if err != nil {
		return exportCreationRequest{}, err
	}
	if format == "xlsx" && options.SplitBy == "max_rows" && options.MaxRowsPerFile > maximumXLSXDataRows {
		return exportCreationRequest{}, fmt.Errorf("XLSX parts may contain at most %d data rows", maximumXLSXDataRows)
	}
	columns, legacyShape, err := parseExportColumnSpec(r.FormValue("columns_spec"), options)
	if err != nil {
		return exportCreationRequest{}, err
	}
	options.LegacyShape = legacyShape

	scope := strings.ToLower(strings.TrimSpace(r.FormValue("source_scope")))
	if scope == "" {
		scope = "filtered"
	}
	creation := exportCreationRequest{
		Name: name, Format: format, SourceType: "results_" + scope,
		Columns: columns, Options: options,
	}
	switch scope {
	case "all":
		creation.Search = ResultSearch{Sort: "updated_desc"}
	case "filtered":
		search, err := resultSearchFromForm(r)
		if err != nil {
			return exportCreationRequest{}, err
		}
		creation.Search = search
	case "saved_view":
		viewID := strings.TrimSpace(r.FormValue("saved_view_id"))
		if !validBusinessID(viewID) {
			return exportCreationRequest{}, fmt.Errorf("a valid saved view is required")
		}
		view, err := s.svc.GetSavedResultView(r.Context(), viewID)
		if err != nil {
			return exportCreationRequest{}, fmt.Errorf("saved view was not found")
		}
		creation.Search = view.Search
		creation.SavedViewID = view.ID
	case "selected":
		ids, err := parseSelectedExportIDs(r.FormValue("selected_ids"))
		if err != nil {
			return exportCreationRequest{}, err
		}
		creation.Search = ResultSearch{
			Sort: "updated_desc",
			Filters: []ResultFilter{{
				Field: "id", Operator: "in", Value: strings.Join(ids, ","),
			}},
		}
	default:
		return exportCreationRequest{}, fmt.Errorf("unsupported export source scope")
	}
	creation.Search.IncludeDuplicates = !options.Deduplicate
	creation.Search.Limit = 250
	creation.Search.Offset = 0
	if creation.Search.Sort == "" {
		creation.Search.Sort = "updated_desc"
	}
	if len(creation.Search.Query) > maximumResultQueryLength || len(creation.Search.JobID) > 128 {
		return exportCreationRequest{}, fmt.Errorf("export filter is too long")
	}
	encodedSearch, err := json.Marshal(creation.Search)
	if err != nil || len(encodedSearch) > maximumExportFilterBytes {
		return exportCreationRequest{}, fmt.Errorf("export filter is too large")
	}
	if formBoolean(r, "save_preset", false) {
		presetName := strings.TrimSpace(r.FormValue("preset_name"))
		if presetName == "" {
			presetName = name
		}
		if _, err := validExportName(presetName, ""); err != nil {
			return exportCreationRequest{}, fmt.Errorf("invalid export preset name: %w", err)
		}
		creation.SavePresetName = presetName
	}
	return creation, nil
}

const maximumExportFilterBytes = 1 << 20

func (s *Server) repeatedExportCreation(r *http.Request, id string) (exportCreationRequest, error) {
	if !validBusinessID(id) {
		return exportCreationRequest{}, fmt.Errorf("invalid export to repeat")
	}
	repository, err := s.svc.exportRepository()
	if err != nil {
		return exportCreationRequest{}, err
	}
	record, err := repository.GetExport(r.Context(), id)
	if err != nil {
		return exportCreationRequest{}, fmt.Errorf("export to repeat was not found")
	}
	creation, err := storedExportCreation(record.Name+" repeat", record.Format, record.Filters, record.Columns, record.Options)
	if err != nil {
		return exportCreationRequest{}, err
	}
	creation.SourceType = record.SourceType
	creation.PresetID = record.PresetID
	creation.SavedViewID = record.SavedViewID
	if override := strings.TrimSpace(r.FormValue("name")); override != "" {
		creation.Name, err = validExportName(override, "")
		if err != nil {
			return exportCreationRequest{}, err
		}
	}
	return creation, nil
}

func (s *Server) presetExportCreation(r *http.Request, id string) (exportCreationRequest, error) {
	if !validBusinessID(id) {
		return exportCreationRequest{}, fmt.Errorf("invalid export preset")
	}
	preset, err := s.svc.GetExportPreset(r.Context(), id)
	if err != nil {
		return exportCreationRequest{}, fmt.Errorf("export preset was not found")
	}
	creation, err := storedExportCreation(preset.Name, preset.Format, preset.Filters, preset.Columns, preset.Options)
	if err != nil {
		return exportCreationRequest{}, err
	}
	creation.SourceType = "results_preset"
	creation.PresetID = preset.ID
	if override := strings.TrimSpace(r.FormValue("name")); override != "" {
		creation.Name, err = validExportName(override, "")
		if err != nil {
			return exportCreationRequest{}, err
		}
	}
	return creation, nil
}

func storedExportCreation(name, format, filters, columnsJSON, optionsJSON string) (exportCreationRequest, error) {
	if _, ok := exportExtension(format); !ok {
		return exportCreationRequest{}, fmt.Errorf("stored export format is unsupported")
	}
	name, err := validExportName(name, "business-results")
	if err != nil {
		return exportCreationRequest{}, err
	}
	var search ResultSearch
	if len(filters) > maximumExportFilterBytes || json.Unmarshal([]byte(filters), &search) != nil {
		return exportCreationRequest{}, fmt.Errorf("stored export filter is invalid")
	}
	columns, err := decodeExportColumns(columnsJSON)
	if err != nil {
		return exportCreationRequest{}, err
	}
	options, err := decodeExportOptions(optionsJSON)
	if err != nil {
		return exportCreationRequest{}, err
	}
	if strings.TrimSpace(optionsJSON) == "" || strings.TrimSpace(optionsJSON) == "{}" {
		options.LegacyShape = equalExportColumns(columns, defaultExportColumns())
	}
	search.Limit = 250
	search.Offset = 0
	if search.Sort == "" {
		search.Sort = "updated_desc"
	}
	return exportCreationRequest{
		Name: name, Format: format, SourceType: "results", Search: search,
		Columns: columns, Options: options,
	}, nil
}

func validExportName(value, fallback string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	if value == "" || len(value) > 120 {
		return "", fmt.Errorf("export name must contain 1 to 120 characters")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("export name cannot contain control characters")
		}
	}
	return value, nil
}

func parseSelectedExportIDs(raw string) ([]string, error) {
	values := strings.FieldsFunc(raw, func(character rune) bool {
		return unicode.IsSpace(character) || character == ',' || character == ';'
	})
	if len(values) == 0 {
		return nil, fmt.Errorf("at least one selected business ID is required")
	}
	if len(values) > maximumSelectedExportIDs {
		return nil, fmt.Errorf("at most %d selected businesses can be exported at once", maximumSelectedExportIDs)
	}
	seen := make(map[string]struct{}, len(values))
	ids := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !validBusinessID(value) {
			return nil, fmt.Errorf("selected business ID %q is invalid", value)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		ids = append(ids, value)
	}
	return ids, nil
}

func (s *Server) deleteExportPresetFromBuilder(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.FormValue("preset_id"))
	if !validBusinessID(id) {
		http.Error(w, "invalid export preset", http.StatusUnprocessableEntity)
		return
	}
	if err := s.svc.DeleteExportPreset(r.Context(), id); err != nil {
		if errors.Is(err, ErrExportNotFound) {
			http.Error(w, "export preset not found", http.StatusNotFound)
		} else {
			http.Error(w, "could not delete export preset", http.StatusInternalServerError)
		}
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]string{"message": "Export preset deleted"}})
		return
	}
	http.Redirect(w, r, "/app/exports?notice="+url.QueryEscape("Export preset deleted"), http.StatusSeeOther)
}

func exportRecordSource(record ExportRecord) string {
	switch {
	case record.SavedViewID != "":
		return "saved view " + record.SavedViewID
	case record.SourceID != "":
		return "job " + record.SourceID
	case record.SourceType != "":
		return strings.ReplaceAll(record.SourceType, "_", " ")
	default:
		return "results"
	}
}

func exportOptionsSummary(options ExportBuildOptions) string {
	parts := make([]string, 0, 4)
	if options.SplitBy != "" && options.SplitBy != "none" {
		parts = append(parts, "split by "+strings.ReplaceAll(options.SplitBy, "_", " "))
	}
	if options.ZIP {
		parts = append(parts, "ZIP")
	}
	details := 0
	for _, enabled := range []bool{options.IncludeRaw, options.IncludeSources, options.IncludeProvenance, options.IncludeChanges} {
		if enabled {
			details++
		}
	}
	if details > 0 {
		parts = append(parts, fmt.Sprintf("%d detail sets", details))
	}
	if options.Deduplicate {
		parts = append(parts, "deduplicated")
	}
	if len(parts) == 0 {
		return "standard"
	}
	return strings.Join(parts, "; ")
}

func removeExportArtifact(dataFolder string, artifact exportArtifact) {
	paths := make(map[string]struct{}, len(artifact.Parts)+1)
	paths[artifact.RelativePath] = struct{}{}
	for _, part := range artifact.Parts {
		paths[part.RelativePath] = struct{}{}
	}
	for relativePath := range paths {
		path, err := safeDataPath(dataFolder, relativePath)
		if err == nil {
			_ = os.Remove(path)
		}
	}
}

// listExportsAPI, exportStatusAPI, repeatExport, and listExportPresetsAPI are
// ready for the Local API router. The page uses the same persistence paths.
func (s *Server) listExportsAPI(w http.ResponseWriter, r *http.Request) {
	records, err := s.svc.ListExports(r.Context(), 200)
	if err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "export_list_failed", "Could not list exports")
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: records})
}

func (s *Server) exportStatusAPI(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !validBusinessID(id) {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_export_id", "Invalid export ID")
		return
	}
	repository, err := s.svc.exportRepository()
	if err != nil {
		renderLocalAPIError(w, http.StatusNotImplemented, "exports_unavailable", "Exports are unavailable")
		return
	}
	record, err := repository.GetExport(r.Context(), id)
	if err != nil {
		renderLocalAPIError(w, http.StatusNotFound, "export_not_found", "Export not found")
		return
	}
	parts, _ := s.svc.ListExportParts(r.Context(), id)
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]any{"export": record, "parts": parts}})
}

func (s *Server) repeatExport(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid export form", http.StatusBadRequest)
		return
	}
	r.Form.Set("repeat_id", strings.TrimSpace(r.PathValue("id")))
	s.createResultsExport(w, r)
}

func (s *Server) listExportPresetsAPI(w http.ResponseWriter, r *http.Request) {
	presets, err := s.svc.ListExportPresets(r.Context(), 200)
	if err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "export_preset_list_failed", "Could not list export presets")
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: presets})
}
