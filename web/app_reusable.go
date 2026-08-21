package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maximumReusableImportBytes = 1 << 20

type reusablePageData struct {
	ActiveTab string
	Query     string
	Templates []templateCardView
	Views     []savedViewCard
	Notice    string
}

type templateCardView struct {
	ID          string
	Name        string
	Description string
	Pinned      bool
	UseCount    int64
	Keywords    string
	Geography   string
	Performance string
	// History is the template's derived run history: how many jobs ran from
	// it, their average result count and average duration. It reads "never
	// run" until a job created from the template finishes.
	History   string
	UpdatedAt string
}

type savedViewCard struct {
	ID        string
	Name      string
	Summary   string
	ResultURL string
	UpdatedAt string
}

// templateHistoryLabel renders one template's derived run history for the
// card. A repository that cannot derive it, or a template nothing has run yet,
// reports the honest "used N times; no completed run yet" rather than an
// invented average.
func (s *Server) templateHistoryLabel(r *http.Request, template ScrapeTemplate) string {
	used := fmt.Sprintf("used %d×", template.UseCount)
	metrics, err := s.svc.ScrapeTemplateMetricsFor(r.Context(), template.ID)
	if err != nil || metrics.RunCount == 0 {
		return used + "; no recorded run yet"
	}
	label := fmt.Sprintf("%s; %d run(s)", used, metrics.RunCount)
	label += fmt.Sprintf("; avg %.0f results", metrics.AverageResults)
	if metrics.TimedRunCount > 0 {
		label += fmt.Sprintf("; avg %s", metrics.AverageDuration.Round(time.Second))
	} else {
		label += "; no completed run timed yet"
	}
	if parameters := template.Configuration.Parameters; parameters != nil && !parameters.Empty() {
		label += fmt.Sprintf("; parameterised (%d × %d)",
			len(parameters.Categories), len(parameters.Locations))
	}

	return label
}

func (s *Server) reusablePage(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	tab := strings.TrimSpace(r.URL.Query().Get("tab"))
	if tab != "views" {
		tab = "templates"
	}
	templates, err := s.svc.ListScrapeTemplates(r.Context(), query)
	if err != nil {
		http.Error(w, "could not load scrape templates", http.StatusInternalServerError)
		return
	}
	views, err := s.svc.ListSavedResultViews(r.Context(), query)
	if err != nil {
		http.Error(w, "could not load saved result views", http.StatusInternalServerError)
		return
	}
	page := reusablePageData{
		ActiveTab: tab,
		Query:     query,
		Notice:    strings.TrimSpace(r.URL.Query().Get("notice")),
	}
	for _, template := range templates {
		lastRun := "never"
		if template.LastRunAt != nil {
			lastRun = template.LastRunAt.Format(time.RFC3339)
		}
		page.Templates = append(page.Templates, templateCardView{
			ID:          template.ID,
			Name:        template.Name,
			Description: template.Description,
			Pinned:      template.Pinned,
			UseCount:    template.UseCount,
			Keywords:    strings.Join(template.Configuration.Keywords, ", "),
			Geography:   templateGeography(template.Configuration),
			Performance: fmt.Sprintf("depth %d; %s; concurrency %d; last run %s",
				template.Configuration.Depth, template.Configuration.MaxTime,
				template.Configuration.Concurrency, lastRun),
			History:   s.templateHistoryLabel(r, template),
			UpdatedAt: template.UpdatedAt.Format(time.RFC3339),
		})
	}
	for _, view := range views {
		page.Views = append(page.Views, savedViewCard{
			ID:        view.ID,
			Name:      view.Name,
			Summary:   savedViewSummary(view.Search),
			ResultURL: savedViewURL(view.Search),
			UpdatedAt: view.UpdatedAt.Format(time.RFC3339),
		})
	}
	activity, _ := s.appActivity(r)
	s.renderAppPage(w, "saved_searches", appPageData{
		Title:     "Saved views and templates",
		Subtitle:  "Reuse complete scrape configurations and indexed Results queries.",
		ActiveNav: "saved-searches",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page:      page,
	})
}

func templateGeography(data JobData) string {
	if data.GridBBox != "" {
		return "grid " + data.GridBBox + fmt.Sprintf(" at %.2f km", data.GridCellKM)
	}
	if data.FastMode {
		return fmt.Sprintf("strict radius %d m around %s, %s", data.Radius, data.Lat, data.Lon)
	}
	return "location inherited from query"
}

func savedViewSummary(search ResultSearch) string {
	parts := make([]string, 0, 3)
	if search.Query != "" {
		parts = append(parts, "search: "+search.Query)
	}
	if search.JobID != "" {
		parts = append(parts, "job: "+search.JobID)
	}
	if len(search.Filters) > 0 {
		parts = append(parts, fmt.Sprintf("%d field filters", len(search.Filters)))
	}
	if len(parts) == 0 {
		return "all normalized businesses"
	}
	return strings.Join(parts, "; ")
}

func savedViewURL(search ResultSearch) string {
	values := url.Values{}
	if search.Query != "" {
		values.Set("q", search.Query)
	}
	if search.JobID != "" {
		values.Set("job_id", search.JobID)
	}
	if search.Sort != "" {
		values.Set("sort", search.Sort)
	}
	if search.IncludeDuplicates {
		values.Set("include_duplicates", "true")
	}
	for _, filter := range search.Filters {
		values.Add("filter_field", filter.Field)
		values.Add("filter_operator", filter.Operator)
		values.Add("filter_value", filter.Value)
	}
	if filterJSON := resultFilterGroupJSON(search.FilterGroup); filterJSON != "" {
		values.Set("filter_json", filterJSON)
	}
	return "/app/results?" + values.Encode()
}

func (s *Server) saveResultView(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 120 {
		http.Error(w, "saved view name is required and must be at most 120 characters", http.StatusUnprocessableEntity)
		return
	}
	search, err := resultSearchFromForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	now := time.Now().UTC()
	view := SavedResultView{ID: uuid.NewString(), Name: name, Search: search, CreatedAt: now, UpdatedAt: now}
	if err := s.svc.SaveResultView(r.Context(), view); err != nil {
		http.Error(w, "could not save result view", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app/saved-searches?tab=views&notice=View+saved", http.StatusSeeOther)
}

func resultSearchFromForm(r *http.Request) (ResultSearch, error) {
	search := ResultSearch{
		Query:             strings.TrimSpace(r.FormValue("q")),
		JobID:             strings.TrimSpace(r.FormValue("job_id")),
		Sort:              strings.TrimSpace(r.FormValue("sort")),
		IncludeDuplicates: r.FormValue("include_duplicates") == "true",
		Limit:             25,
	}
	fields := r.Form["filter_field"]
	operators := r.Form["filter_operator"]
	values := r.Form["filter_value"]
	if len(fields) != len(operators) || len(fields) != len(values) || len(fields) > 25 {
		return ResultSearch{}, fmt.Errorf("saved view filters are incomplete")
	}
	for index := range fields {
		if strings.TrimSpace(fields[index]) == "" || strings.TrimSpace(operators[index]) == "" {
			continue
		}
		search.Filters = append(search.Filters, ResultFilter{
			Field: strings.TrimSpace(fields[index]), Operator: strings.TrimSpace(operators[index]),
			Value: strings.TrimSpace(values[index]),
		})
	}
	group, err := decodeResultFilterGroup(r.FormValue("filter_json"))
	if err != nil {
		return ResultSearch{}, err
	}
	search.FilterGroup = group
	logic := strings.ToLower(strings.TrimSpace(r.FormValue("filter_logic")))
	if logic != "" && logic != "and" && logic != "or" {
		return ResultSearch{}, fmt.Errorf("saved view filter logic must be 'and' or 'or'")
	}
	if logic == "or" && len(search.Filters) > 0 {
		flatGroup := ResultFilterGroup{Logic: "or", Filters: search.Filters}
		search.FilterGroup = combineResultFilterGroups(search.FilterGroup, &flatGroup)
		search.Filters = nil
	}
	if len(search.Query) > maximumResultQueryLength || len(search.JobID) > 128 || len(search.Sort) > 64 {
		return ResultSearch{}, fmt.Errorf("saved view value is too long")
	}
	return search, nil
}

func (s *Server) deleteSavedResultView(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.svc.DeleteSavedResultView(r.Context(), id); err != nil {
		if errors.Is(err, ErrReusableNotFound) {
			http.Error(w, "saved view not found", http.StatusNotFound)
		} else {
			http.Error(w, "could not delete saved view", http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, "/app/saved-searches?tab=views&notice=View+deleted", http.StatusSeeOther)
}

func (s *Server) pinScrapeTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	template, err := s.svc.GetScrapeTemplate(r.Context(), id)
	if err != nil {
		http.Error(w, "template not found", http.StatusNotFound)
		return
	}
	if err := s.svc.SetScrapeTemplatePinned(r.Context(), id, !template.Pinned); err != nil {
		http.Error(w, "could not update template", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app/saved-searches?tab=templates&notice=Template+updated", http.StatusSeeOther)
}

func (s *Server) duplicateScrapeTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	template, err := s.svc.GetScrapeTemplate(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		http.Error(w, "template not found", http.StatusNotFound)
		return
	}
	now := time.Now().UTC()
	template.ID = uuid.NewString()
	template.Name += " (copy)"
	template.Pinned = false
	template.UseCount = 0
	template.LastRunAt = nil
	template.CreatedAt = now
	template.UpdatedAt = now
	if err := s.svc.SaveScrapeTemplate(r.Context(), template); err != nil {
		http.Error(w, "could not duplicate template", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app/saved-searches?tab=templates&notice=Template+duplicated", http.StatusSeeOther)
}

// registerTemplateRenameRoutes exposes the dedicated template rename action.
// The other template mutations are registered directly in web.go; this
// register function keeps the new route in a file owned by the reusable
// feature so web.go only ever needs the one-line
// `ans.registerTemplateRenameRoutes(mux)` call.
func (s *Server) registerTemplateRenameRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/templates/{id}/rename", s.renameScrapeTemplate)
}

func (s *Server) renameScrapeTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	name := strings.TrimSpace(r.FormValue("name"))
	if err := s.svc.RenameScrapeTemplate(r.Context(), id, name); err != nil {
		switch {
		case errors.Is(err, ErrInvalidTemplateRename):
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		case errors.Is(err, ErrReusableNotFound):
			http.Error(w, "template not found", http.StatusNotFound)
		default:
			http.Error(w, "could not rename template", http.StatusInternalServerError)
		}
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]string{"id": id, "name": name}})
		return
	}
	http.Redirect(w, r, "/app/saved-searches?tab=templates&notice=Template+renamed", http.StatusSeeOther)
}

func (s *Server) deleteScrapeTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	if err := s.svc.DeleteScrapeTemplate(r.Context(), strings.TrimSpace(r.PathValue("id"))); err != nil {
		if errors.Is(err, ErrReusableNotFound) {
			http.Error(w, "template not found", http.StatusNotFound)
		} else {
			http.Error(w, "could not delete template", http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, "/app/saved-searches?tab=templates&notice=Template+deleted", http.StatusSeeOther)
}

func (s *Server) exportScrapeTemplate(w http.ResponseWriter, r *http.Request) {
	template, err := s.svc.GetScrapeTemplate(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		http.Error(w, "template not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=\"scrape-template-"+template.ID+".json\"")
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(template)
}

func (s *Server) importScrapeTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maximumReusableImportBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "template import is too large", http.StatusUnprocessableEntity)
		return
	}
	payload := strings.TrimSpace(r.FormValue("template_json"))
	if payload == "" {
		http.Error(w, "template JSON is required", http.StatusUnprocessableEntity)
		return
	}
	var imported ScrapeTemplate
	if err := json.NewDecoder(io.LimitReader(strings.NewReader(payload), maximumReusableImportBytes)).Decode(&imported); err != nil {
		http.Error(w, "invalid template JSON", http.StatusUnprocessableEntity)
		return
	}
	if err := imported.Configuration.Validate(); err != nil {
		http.Error(w, "invalid template configuration: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if len(imported.Configuration.Proxies) > 0 {
		http.Error(w, "template imports cannot contain inline proxy credentials", http.StatusUnprocessableEntity)
		return
	}
	imported.Name = strings.TrimSpace(imported.Name)
	if imported.Name == "" || len(imported.Name) > 120 {
		http.Error(w, "template name is required and must be at most 120 characters", http.StatusUnprocessableEntity)
		return
	}
	now := time.Now().UTC()
	imported.ID = uuid.NewString()
	imported.CreatedAt = now
	imported.UpdatedAt = now
	imported.UseCount = 0
	imported.LastRunAt = nil
	if err := s.svc.SaveScrapeTemplate(r.Context(), imported); err != nil {
		http.Error(w, "could not import template", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app/saved-searches?tab=templates&notice=Template+imported", http.StatusSeeOther)
}
