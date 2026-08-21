package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// maximumExportProfileRequestBytes bounds a profile definition; it holds a
// column list and one stored filter expression.
const maximumExportProfileRequestBytes = 1 << 20

// exportProfileAPIInput is the JSON body of POST /api/v1/exports/profiles.
// A profile is the reusable half of an export request — the named column
// set, the delivery format, and the query it runs against — with no
// generated file paths, credentials, or run state.
type exportProfileAPIInput struct {
	// ID replaces an existing profile in place; empty creates a new one.
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	Format string `json:"format"`
	// Columns is the ordered column set. An empty list keeps the default
	// export shape.
	Columns []ExportColumnSelection `json:"columns,omitempty"`
	// Search is the stored result query the profile exports.
	Search *ResultSearch `json:"search,omitempty"`
	// Options carries the delivery shape: splitting, ZIP packaging and the
	// optional extra sheets.
	Options *ExportBuildOptions `json:"options,omitempty"`
}

// registerExportProfileRoutes exposes durable export profiles: a named
// column set plus format plus filter, reusable across exports. They are the
// same durable records the existing `save_preset` flag creates, given a
// first-class lifecycle of their own so a profile can be defined, read and
// removed without running an export first.
//
// The routes sit at /api/v1/export-profiles rather than under
// /api/v1/exports/ because the latter already owns a {id} wildcard at that
// depth, and a literal segment beside it would be an ambiguous pattern.
// POST /api/v1/exports keeps accepting preset_id exactly as before.
func (s *Server) registerExportProfileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/export-profiles", s.listExportProfiles)
	mux.HandleFunc("POST /api/v1/export-profiles", s.saveExportProfile)
	mux.HandleFunc("GET /api/v1/export-profiles/{id}", s.getExportProfile)
	mux.HandleFunc("DELETE /api/v1/export-profiles/{id}", s.deleteExportProfile)
	mux.HandleFunc("POST /api/v1/export-profiles/{id}/delete", s.deleteExportProfile)
}

func (s *Server) listExportProfiles(w http.ResponseWriter, r *http.Request) {
	presets, err := s.svc.ListExportPresets(r.Context(), 200)
	if err != nil {
		s.renderExportProfileError(w, err)

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: presets})
}

func (s *Server) getExportProfile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !validBusinessID(id) {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_profile_id", "Invalid export profile ID")

		return
	}

	preset, err := s.svc.GetExportPreset(r.Context(), id)
	if err != nil {
		s.renderExportProfileError(w, err)

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: preset})
}

func (s *Server) saveExportProfile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maximumExportProfileRequestBytes)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var input exportProfileAPIInput
	if err := decoder.Decode(&input); err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_profile", "Invalid export profile")

		return
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_profile",
			"Request must contain exactly one JSON object")

		return
	}

	if !s.requireCSRF(w, r) {
		return
	}

	preset, err := exportProfileFromInput(input)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_profile", err.Error())

		return
	}

	if !s.allowExportFormat(w, r, preset.Format) {
		return
	}

	saved, err := s.svc.SaveExportPreset(r.Context(), preset)
	if err != nil {
		s.renderExportProfileError(w, err)

		return
	}

	renderJSON(w, http.StatusCreated, localAPIEnvelope{Data: saved})
}

func (s *Server) deleteExportProfile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !validBusinessID(id) {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_profile_id", "Invalid export profile ID")

		return
	}

	if !s.requireCSRF(w, r) {
		return
	}

	if err := s.svc.DeleteExportPreset(r.Context(), id); err != nil {
		s.renderExportProfileError(w, err)

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]string{"id": id, "state": "deleted"}})
}

// exportProfileFromInput validates a profile definition and encodes it into
// the durable preset record. Every value is run through the same validators
// the export request path uses, so a stored profile can never describe an
// export the builder would refuse.
func exportProfileFromInput(input exportProfileAPIInput) (ExportPreset, error) {
	name, err := validExportName(input.Name, "")
	if err != nil {
		return ExportPreset{}, err
	}

	format := strings.ToLower(strings.TrimSpace(input.Format))
	if _, ok := exportExtension(format); !ok {
		return ExportPreset{}, errors.New("unsupported export format")
	}

	options := ExportBuildOptions{SplitBy: "none", Deduplicate: true}
	if input.Options != nil {
		options = *input.Options
	}

	columns := input.Columns
	if len(columns) == 0 {
		columns = defaultExportColumns()
		options.LegacyShape = true
	}

	columnJSON, optionJSON, err := encodeExportConfiguration(columns, options)
	if err != nil {
		return ExportPreset{}, err
	}

	// Round-tripping through the stored decoders is the validation: a
	// profile that cannot be read back is never written.
	if _, err := decodeExportColumns(columnJSON); err != nil {
		return ExportPreset{}, err
	}

	if _, err := decodeExportOptions(optionJSON); err != nil {
		return ExportPreset{}, err
	}

	search := ResultSearch{Sort: "updated_desc"}
	if input.Search != nil {
		search = *input.Search
	}

	search.Limit = 250
	search.Offset = 0

	if search.Sort == "" {
		search.Sort = "updated_desc"
	}

	if len(search.Query) > maximumResultQueryLength || len(search.JobID) > 128 {
		return ExportPreset{}, errors.New("export profile filter is too long")
	}

	filterJSON, err := json.Marshal(search)
	if err != nil || len(filterJSON) > maximumExportFilterBytes {
		return ExportPreset{}, errors.New("export profile filter is too large")
	}

	id := strings.TrimSpace(input.ID)
	if id == "" {
		id = uuid.NewString()
	} else if !validBusinessID(id) {
		return ExportPreset{}, errors.New("invalid export profile ID")
	}

	now := time.Now().UTC()

	return ExportPreset{
		ID: id, Name: name, Format: format,
		Columns: columnJSON, Filters: string(filterJSON), Sort: search.Sort, Options: optionJSON,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (s *Server) renderExportProfileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrExportStoreUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "export_profiles_unavailable",
			"Export profiles require the upgraded local database")
	case errors.Is(err, ErrExportNotFound), errors.Is(err, ErrNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "export_profile_not_found", "Export profile not found")
	default:
		renderLocalAPIError(w, http.StatusInternalServerError, "export_profile_failed",
			"Could not complete the export profile action")
	}
}
