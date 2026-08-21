package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maximumScrapePlanRequestBytes bounds every request body this file accepts.
const maximumScrapePlanRequestBytes = 64 << 10

// registerScrapePlanRoutes exposes the wizard's data-field catalogue, the
// bundled business-category vocabulary, reusable category groups, and the
// per-job collection plan that resolves a saved job's field selection,
// post-collection filters, and rescan mode into one honest answer.
func (s *Server) registerScrapePlanRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/scrape-fields", s.apiScrapeFields)
	mux.HandleFunc("GET /api/v1/business-categories", s.apiBusinessCategories)
	mux.HandleFunc("GET /api/v1/category-groups", s.apiListCategoryGroups)
	mux.HandleFunc("POST /api/v1/category-groups", s.apiSaveCategoryGroup)
	mux.HandleFunc("POST /api/v1/category-groups/{id}/use", s.apiUseCategoryGroup)
	mux.HandleFunc("POST /api/v1/category-groups/{id}/delete", s.apiDeleteCategoryGroup)
	mux.HandleFunc("DELETE /api/v1/category-groups/{id}", s.apiDeleteCategoryGroup)
	mux.HandleFunc("GET /api/v1/jobs/{id}/collection-plan", s.apiJobCollectionPlan)
}

func (s *Server) apiScrapeFields(w http.ResponseWriter, r *http.Request) {
	catalogue := JobFieldCatalogue()
	groups := []map[string]any{
		{"key": JobFieldGroupCore, "label": "Core details"},
		{"key": JobFieldGroupIdentifiers, "label": "Identifiers"},
		{"key": JobFieldGroupExtended, "label": "Extended details"},
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{
		Data: catalogue,
		Meta: map[string]any{
			"count":    len(catalogue),
			"groups":   groups,
			"required": RequiredJobFieldKeys(),
			"notice": "The engine always collects every field below and the per-job CSV " +
				"keeps its full schema. A selection controls what this workspace displays and exports.",
		},
	})
}

func (s *Server) apiBusinessCategories(w http.ResponseWriter, r *http.Request) {
	categories := BusinessCategories()
	if sector := strings.TrimSpace(r.URL.Query().Get("sector")); sector != "" {
		filtered := make([]BusinessCategory, 0, len(categories))
		for _, category := range categories {
			if strings.EqualFold(category.Sector, sector) {
				filtered = append(filtered, category)
			}
		}
		categories = filtered
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{
		Data: categories,
		Meta: map[string]any{
			"count":   len(categories),
			"sectors": BusinessCategorySectors(),
			"notice": "A bundled local vocabulary of Maps listing categories. " +
				"Free text is still accepted anywhere a category is asked for.",
		},
	})
}

func (s *Server) apiListCategoryGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.svc.ListCategoryGroups(r.Context())
	if err != nil {
		renderCategoryGroupError(w, err)

		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: groups, Meta: map[string]any{"count": len(groups)}})
}

type categoryGroupInput struct {
	ID         string   `json:"id,omitempty"`
	Name       string   `json:"name"`
	Categories []string `json:"categories"`
}

func (s *Server) apiSaveCategoryGroup(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}

	var input categoryGroupInput
	if err := decodeScrapePlanJSON(w, r, &input); err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_category_group", err.Error())

		return
	}

	group, err := s.svc.SaveCategoryGroup(r.Context(), CategoryGroup{
		ID: input.ID, Name: input.Name, Categories: input.Categories,
	})
	if err != nil {
		renderCategoryGroupError(w, err)

		return
	}

	renderJSON(w, http.StatusCreated, localAPIEnvelope{Data: group})
}

func (s *Server) apiUseCategoryGroup(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}

	group, err := s.svc.TouchCategoryGroupUse(r.Context(), r.PathValue("id"), time.Now().UTC())
	if err != nil {
		renderCategoryGroupError(w, err)

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: group})
}

func (s *Server) apiDeleteCategoryGroup(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}

	if err := s.svc.DeleteCategoryGroup(r.Context(), r.PathValue("id")); err != nil {
		renderCategoryGroupError(w, err)

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]string{"message": "Category group deleted"}})
}

func (s *Server) apiJobCollectionPlan(w http.ResponseWriter, r *http.Request) {
	plan, err := s.svc.JobCollectionPlanFor(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			renderLocalAPIError(w, http.StatusNotFound, "job_not_found", "No such job")

			return
		}
		renderLocalAPIError(w, http.StatusInternalServerError, "collection_plan_failed",
			"Could not resolve the job's collection plan")

		return
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{
		Data: plan,
		Meta: map[string]any{
			"incremental_label": IncrementalModeLabel(plan.IncrementalMode),
			"field_count":       len(plan.Fields),
		},
	})
}

func decodeScrapePlanJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maximumScrapePlanRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("request body must be one JSON object: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain exactly one JSON object")
	}

	return nil
}

func renderCategoryGroupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidCategoryGroup):
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_category_group", err.Error())
	case errors.Is(err, ErrCategoryGroupNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "category_group_not_found", "No such category group")
	case errors.Is(err, ErrCategoryGroupsUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "category_groups_unavailable",
			"Reusable category groups need the upgraded local database")
	default:
		renderLocalAPIError(w, http.StatusInternalServerError, "category_group_failed",
			"Could not read or save reusable category groups")
	}
}
