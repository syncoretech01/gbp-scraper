package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Manual field edits let an operator correct a stored business value without
// losing history: the previous value stays behind as superseded provenance and
// the edit itself is recorded with a reason, an optional operator name, a
// field-level change row, and an audit entry.

const maximumManualEditRequestBytes = 16 << 10

var (
	// ErrManualEditUnsupported indicates the repository cannot store manual
	// field edits with provenance.
	ErrManualEditUnsupported = errors.New("manual field edits are unavailable")
	// ErrInvalidManualEdit identifies a malformed manual field edit.
	ErrInvalidManualEdit = errors.New("invalid manual field edit")
)

// ManualFieldEdit is one operator correction to a stored business field.
type ManualFieldEdit struct {
	BusinessID string `json:"business_id,omitempty"`
	Field      string `json:"field"`
	Value      string `json:"value"`
	Reason     string `json:"reason"`
	Operator   string `json:"operator,omitempty"`
}

// ManualFieldEditResult reports the durably stored outcome of one edit.
type ManualFieldEditResult struct {
	BusinessID    string `json:"business_id"`
	Field         string `json:"field"`
	Value         string `json:"value"`
	PreviousValue string `json:"previous_value"`
}

type manualEditRepository interface {
	ApplyManualFieldEdit(context.Context, ManualFieldEdit) (ManualFieldEditResult, error)
}

// SupportsManualFieldEdits reports whether operator field corrections can be
// recorded durably with provenance.
func (s *Service) SupportsManualFieldEdits() bool {
	_, ok := s.repo.(manualEditRepository)

	return ok
}

// manualEditAvailable is the Server capability helper for manual field edits.
func (s *Server) manualEditAvailable() bool {
	return s != nil && s.svc != nil && s.svc.SupportsManualFieldEdits()
}

// CanEditFields reports whether the result drawer should render the manual
// field edit form. The drawer view model is assembled by loadAppBusinessDetail,
// which this change set must not modify, so no dedicated flag can be threaded
// through; CanMutate is populated from the workflow-mutation capability of the
// same repository that implements manual edits, and the endpoint itself stays
// authoritatively gated by Server.manualEditAvailable, which answers 501 when
// the repository cannot store edits.
func (d appBusinessDetail) CanEditFields() bool {
	return d.CanMutate
}

// ManualEditFields lists the business fields an operator may correct.
func ManualEditFields() []string {
	return []string{"name", "phone", "website", "category"}
}

// ApplyManualFieldEdit validates one operator correction and stores it in a
// single transaction together with provenance, change, and audit records.
func (s *Service) ApplyManualFieldEdit(
	ctx context.Context,
	edit ManualFieldEdit,
) (ManualFieldEditResult, error) {
	repository, ok := s.repo.(manualEditRepository)
	if !ok {
		return ManualFieldEditResult{}, ErrManualEditUnsupported
	}

	edit.BusinessID = strings.TrimSpace(edit.BusinessID)
	edit.Field = strings.ToLower(strings.TrimSpace(edit.Field))
	edit.Value = strings.TrimSpace(edit.Value)
	edit.Reason = strings.TrimSpace(edit.Reason)
	edit.Operator = strings.TrimSpace(edit.Operator)

	if !validBusinessID(edit.BusinessID) {
		return ManualFieldEditResult{}, fmt.Errorf("%w: invalid business ID", ErrInvalidManualEdit)
	}
	if len(edit.Reason) < 3 || len(edit.Reason) > 300 {
		return ManualFieldEditResult{}, fmt.Errorf(
			"%w: a reason between 3 and 300 characters is required", ErrInvalidManualEdit,
		)
	}
	if len(edit.Operator) > 64 {
		return ManualFieldEditResult{}, fmt.Errorf("%w: operator must be at most 64 characters", ErrInvalidManualEdit)
	}

	switch edit.Field {
	case "name":
		if edit.Value == "" || len(edit.Value) > 200 {
			return ManualFieldEditResult{}, fmt.Errorf(
				"%w: name must be between 1 and 200 characters", ErrInvalidManualEdit,
			)
		}
	case "phone":
		if len(edit.Value) > 40 {
			return ManualFieldEditResult{}, fmt.Errorf("%w: phone must be at most 40 characters", ErrInvalidManualEdit)
		}
	case "website":
		if err := validateManualWebsite(edit.Value); err != nil {
			return ManualFieldEditResult{}, err
		}
	case "category":
		if len(edit.Value) > 100 {
			return ManualFieldEditResult{}, fmt.Errorf(
				"%w: category must be at most 100 characters", ErrInvalidManualEdit,
			)
		}
	default:
		return ManualFieldEditResult{}, fmt.Errorf(
			"%w: field must be one of %s", ErrInvalidManualEdit, strings.Join(ManualEditFields(), ", "),
		)
	}

	return repository.ApplyManualFieldEdit(ctx, edit)
}

// validateManualWebsite accepts an empty value (clearing the field) or an
// absolute HTTP(S) URL.
func validateManualWebsite(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 2048 {
		return fmt.Errorf("%w: website must be at most 2048 characters", ErrInvalidManualEdit)
	}

	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%w: website must be an absolute http(s) URL or empty", ErrInvalidManualEdit)
	}

	return nil
}

// registerManualEditRoutes exposes manual field edits on the local API.
func (s *Server) registerManualEditRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/results/{id}/fields", s.apiApplyManualFieldEdit)
}

func (s *Server) apiApplyManualFieldEdit(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	if !s.manualEditAvailable() {
		renderLocalAPIError(w, http.StatusNotImplemented, "manual_edits_unavailable", "Manual field edits are unavailable")

		return
	}

	edit, err := decodeManualFieldEdit(w, r)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_manual_edit", err.Error())

		return
	}

	edit.BusinessID = strings.TrimSpace(r.PathValue("id"))

	result, err := s.svc.ApplyManualFieldEdit(r.Context(), edit)
	if err != nil {
		renderManualEditAPIError(w, err)

		return
	}

	if !strings.Contains(r.Header.Get("Accept"), "application/json") &&
		!strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		http.Redirect(
			w, r,
			"/app/results/"+url.PathEscape(result.BusinessID)+"?notice=Field+updated",
			http.StatusSeeOther,
		)

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: result})
}

func decodeManualFieldEdit(w http.ResponseWriter, r *http.Request) (ManualFieldEdit, error) {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		r.Body = http.MaxBytesReader(w, r.Body, maximumManualEditRequestBytes)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()

		var edit ManualFieldEdit
		if err := decoder.Decode(&edit); err != nil {
			return ManualFieldEdit{}, fmt.Errorf("invalid JSON")
		}

		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return ManualFieldEdit{}, fmt.Errorf("request must contain exactly one JSON object")
		}

		return edit, nil
	}

	if err := parseBoundedRequestForm(w, r, maximumManualEditRequestBytes); err != nil {
		return ManualFieldEdit{}, fmt.Errorf("invalid form")
	}

	return ManualFieldEdit{
		Field:    strings.TrimSpace(r.FormValue("field")),
		Value:    strings.TrimSpace(r.FormValue("value")),
		Reason:   strings.TrimSpace(r.FormValue("reason")),
		Operator: strings.TrimSpace(r.FormValue("operator")),
	}, nil
}

func renderManualEditAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrManualEditUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "manual_edits_unavailable", "Manual field edits are unavailable")
	case errors.Is(err, ErrBusinessNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "business_not_found", "Business was not found")
	case errors.Is(err, ErrInvalidManualEdit):
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_manual_edit", err.Error())
	default:
		renderLocalAPIError(w, http.StatusInternalServerError, "manual_edit_failed", "Could not save the field edit")
	}
}
