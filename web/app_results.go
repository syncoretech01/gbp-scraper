package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type resultsPageData struct {
	Notice            string
	Stats             appResultStats
	Query             string
	JobID             string
	Sort              string
	PageSize          string
	JobOptions        []resultJobOption
	SavedViews        []namedAppOption
	Filters           []appResultFilter
	IncludeDuplicates bool
	Results           []appResultRow
	Total             int64
	RangeLabel        string
	CurrentURL        string
	ExportURL         string
	PreviousURL       string
	NextURL           string
	Capabilities      appResultCapabilities
}

type appResultStats struct {
	UniqueBusinesses int64
	RawRecords       int64
	DuplicateGroups  int64
	DuplicatesMerged int64
	Websites         int64
	Emails           int64
	NeedsReview      int64
	WebsitePercent   int
	EmailPercent     int
}

type appResultCapabilities struct {
	CanSelect        bool
	CanMap           bool
	CanSavedViews    bool
	CanExport        bool
	CanTag           bool
	CanMarkReviewed  bool
	CanEnrich        bool
	CanCheckWebsites bool
	CanCheckEmails   bool
	CanMerge         bool
	CanDelete        bool
}

type resultJobOption struct {
	ID       string
	Name     string
	Selected bool
}

type appResultFilter struct {
	Field         string
	FieldLabel    string
	Operator      string
	OperatorLabel string
	Value         string
}

type appResultRow struct {
	BusinessResult
	RatingLabel      string
	ReviewCountLabel string
	WebsiteState     string
	ResponseTime     string
	QualityLabel     string
	ConfidenceLabel  string
	UpdatedLabel     string
	ScrapedLabel     string
}

type appBusinessDetail struct {
	CSRFToken  string
	CanMutate  bool
	Business   appResultRow
	RawJSON    string
	Sources    []appBusinessSource
	Versions   []appBusinessVersion
	Duplicates []string
}

type appBusinessSource struct {
	BusinessSourceView
	ExtractedLabel string
}

type appBusinessVersion struct {
	BusinessVersionView
	ObservedLabel string
	FieldsLabel   string
}

func (s *Server) resultsPage(w http.ResponseWriter, r *http.Request) {
	if viewID := strings.TrimSpace(r.URL.Query().Get("view")); viewID != "" {
		view, err := s.svc.GetSavedResultView(r.Context(), viewID)
		if err != nil {
			if errors.Is(err, ErrReusableNotFound) {
				http.Error(w, "saved result view not found", http.StatusNotFound)
			} else {
				http.Error(w, "could not load saved result view", http.StatusInternalServerError)
			}
			return
		}
		http.Redirect(w, r, savedViewURL(view.Search), http.StatusSeeOther)
		return
	}
	search, err := parseResultSearch(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)

		return
	}
	if search.Sort == "" {
		search.Sort = "updated_desc"
	}

	page, activity, err := s.buildResultsPage(r, search)
	if err != nil {
		http.Error(w, "could not load normalized results", http.StatusInternalServerError)

		return
	}

	s.renderAppPage(w, "results", appPageData{
		Title:     "Results",
		Subtitle:  "Search, filter, audit, and export normalized businesses stored on this computer.",
		ActiveNav: "results",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page:      page,
	})
}

func (s *Server) buildResultsPage(r *http.Request, search ResultSearch) (resultsPageData, appActivity, error) {
	activity, err := s.appActivity(r)
	if err != nil {
		return resultsPageData{}, appActivity{}, err
	}

	overview, err := s.svc.ResultOverview(r.Context())
	if err != nil {
		return resultsPageData{}, appActivity{}, err
	}
	resultPage, err := s.svc.SearchBusinesses(r.Context(), search)
	if err != nil {
		return resultsPageData{}, appActivity{}, err
	}

	page := resultsPageData{
		Notice: strings.TrimSpace(r.URL.Query().Get("notice")),
		Stats: appResultStats{
			UniqueBusinesses: overview.UniqueBusinesses,
			RawRecords:       overview.RawRecords,
			DuplicateGroups:  overview.DuplicateGroups,
			DuplicatesMerged: overview.DuplicatesMerged,
			Websites:         overview.Websites,
			Emails:           overview.Emails,
			NeedsReview:      overview.NeedsReview,
			WebsitePercent:   intPercentage(overview.Websites, overview.UniqueBusinesses),
			EmailPercent:     intPercentage(overview.Emails, overview.UniqueBusinesses),
		},
		Query:             search.Query,
		JobID:             search.JobID,
		Sort:              search.Sort,
		PageSize:          strconv.Itoa(resultPage.Limit),
		IncludeDuplicates: search.IncludeDuplicates,
		Total:             resultPage.Total,
		CurrentURL:        r.URL.RequestURI(),
		ExportURL:         resultExportURL(r.URL),
		Capabilities: appResultCapabilities{
			CanMap:          true,
			CanSavedViews:   s.reusableAvailable(),
			CanExport:       s.exportAvailable(),
			CanTag:          s.resultMutationAvailable(),
			CanMarkReviewed: s.resultMutationAvailable(),
		},
	}
	page.Capabilities.CanSelect = page.Capabilities.CanTag || page.Capabilities.CanMarkReviewed ||
		page.Capabilities.CanEnrich || page.Capabilities.CanCheckWebsites ||
		page.Capabilities.CanCheckEmails || page.Capabilities.CanMerge || page.Capabilities.CanDelete
	for _, filter := range search.Filters {
		page.Filters = append(page.Filters, appResultFilter{
			Field:         filter.Field,
			FieldLabel:    resultFieldLabel(filter.Field),
			Operator:      filter.Operator,
			OperatorLabel: resultOperatorLabel(filter.Operator),
			Value:         filter.Value,
		})
	}
	for _, result := range resultPage.Results {
		page.Results = append(page.Results, newAppResultRow(result))
	}
	if page.Capabilities.CanSavedViews {
		views, err := s.svc.ListSavedResultViews(r.Context(), "")
		if err != nil {
			return resultsPageData{}, appActivity{}, err
		}
		for _, view := range views {
			page.SavedViews = append(page.SavedViews, namedAppOption{ID: view.ID, Name: view.Name})
		}
	}

	jobs, err := s.svc.All(r.Context())
	if err != nil {
		return resultsPageData{}, appActivity{}, err
	}
	for _, job := range jobs {
		page.JobOptions = append(page.JobOptions, resultJobOption{
			ID:       job.ID,
			Name:     job.Name,
			Selected: job.ID == search.JobID,
		})
	}

	if len(page.Results) > 0 {
		first := resultPage.Offset + 1
		last := resultPage.Offset + len(page.Results)
		page.RangeLabel = fmt.Sprintf("%d-%d", first, last)
	}
	currentPage := resultPageNumber(search)
	if currentPage > 1 {
		page.PreviousURL = resultPaginationURL(r.URL, currentPage-1)
	}
	if resultPage.Offset+len(resultPage.Results) < int(resultPage.Total) {
		page.NextURL = resultPaginationURL(r.URL, currentPage+1)
	}

	return page, activity, nil
}

func resultExportURL(source *url.URL) string {
	values := source.Query()
	values.Del("page")
	values.Del("page_size")
	values.Del("sort")
	values.Set("source", "results")
	return "/app/exports?" + values.Encode()
}

func (s *Server) businessDetailPage(w http.ResponseWriter, r *http.Request) {
	detail, status, err := s.loadAppBusinessDetail(r)
	if err != nil {
		http.Error(w, http.StatusText(status), status)

		return
	}
	activity, activityErr := s.appActivity(r)
	if activityErr != nil {
		http.Error(w, "could not load activity", http.StatusInternalServerError)

		return
	}

	s.renderAppPage(w, "result_detail", appPageData{
		Title:     detail.Business.Name,
		Subtitle:  "Normalized record, source history, changes, and raw local data.",
		ActiveNav: "results",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page:      detail,
	})
}

func (s *Server) businessDetailDrawer(w http.ResponseWriter, r *http.Request) {
	detail, status, err := s.loadAppBusinessDetail(r)
	if err != nil {
		http.Error(w, http.StatusText(status), status)

		return
	}

	tmpl, ok := s.tmpl["app/results"]
	if !ok {
		http.Error(w, "result template is unavailable", http.StatusInternalServerError)

		return
	}
	var rendered bytes.Buffer
	if err := tmpl.ExecuteTemplate(&rendered, "app/result-drawer", detail); err != nil {
		http.Error(w, "could not render business details", http.StatusInternalServerError)

		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = rendered.WriteTo(w)
}

func (s *Server) loadAppBusinessDetail(r *http.Request) (appBusinessDetail, int, error) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !validBusinessID(id) {
		return appBusinessDetail{}, http.StatusUnprocessableEntity, errors.New("invalid business ID")
	}
	detail, err := s.svc.GetBusiness(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrBusinessNotFound) {
			return appBusinessDetail{}, http.StatusNotFound, err
		}

		return appBusinessDetail{}, http.StatusInternalServerError, err
	}

	page := appBusinessDetail{
		CSRFToken:  s.csrfToken,
		CanMutate:  s.resultMutationAvailable(),
		Business:   newAppResultRow(detail.Business),
		RawJSON:    prettyJSON(detail.RawJSON),
		Duplicates: detail.Duplicates,
	}
	for _, source := range detail.Sources {
		page.Sources = append(page.Sources, appBusinessSource{
			BusinessSourceView: source,
			ExtractedLabel:     appResultTime(source.ExtractedAt),
		})
	}
	for _, version := range detail.Versions {
		page.Versions = append(page.Versions, appBusinessVersion{
			BusinessVersionView: version,
			ObservedLabel:       appResultTime(version.ObservedAt),
			FieldsLabel:         strings.Join(version.ChangedFields, ", "),
		})
	}

	return page, http.StatusOK, nil
}

func newAppResultRow(result BusinessResult) appResultRow {
	rating := "not available"
	if result.Rating != nil {
		rating = strconv.FormatFloat(*result.Rating, 'f', 1, 64)
	}
	reviews := "0"
	if result.ReviewCount != nil {
		reviews = strconv.FormatInt(*result.ReviewCount, 10)
	}
	responseTime := "not checked"
	if result.WebsiteResponseMS != nil {
		responseTime = fmt.Sprintf("%d ms", *result.WebsiteResponseMS)
	}
	websiteState := strings.ToLower(strings.TrimSpace(result.WebsiteStatus))
	if websiteState == "" {
		websiteState = "unknown"
	}

	return appResultRow{
		BusinessResult:   result,
		RatingLabel:      rating,
		ReviewCountLabel: reviews,
		WebsiteState:     safeCSSState(websiteState),
		ResponseTime:     responseTime,
		QualityLabel:     strconv.FormatFloat(result.QualityScore, 'f', 0, 64),
		ConfidenceLabel:  fmt.Sprintf("%.0f%%", result.Confidence*100),
		UpdatedLabel:     appResultTime(result.UpdatedAt),
		ScrapedLabel:     appResultTime(result.ScrapedAt),
	}
}

func resultPaginationURL(source *url.URL, page int) string {
	values := source.Query()
	values.Set("page", strconv.Itoa(max(page, 1)))

	return "/app/results?" + values.Encode()
}

func resultFieldLabel(value string) string {
	labels := map[string]string{
		"id": "ID", "name": "Name", "city": "City", "state": "State",
		"country": "Country", "category": "Category", "status": "Business status",
		"website_status": "Website status", "domain": "Domain", "rating": "Rating",
		"reviews": "Review count", "review_count": "Review count", "quality_score": "Quality score",
		"website": "Website", "email": "Email", "phone": "Phone", "reviewed": "Reviewed", "tags": "Tags",
	}
	if label := labels[strings.ToLower(strings.TrimSpace(value))]; label != "" {
		return label
	}

	return value
}

func resultOperatorLabel(value string) string {
	labels := map[string]string{
		"eq": "equals", "contains": "contains", "starts_with": "starts with",
		"gte": "at least", "lte": "at most", "empty": "is empty", "not_empty": "is not empty",
	}
	if label := labels[strings.ToLower(strings.TrimSpace(value))]; label != "" {
		return label
	}

	return value
}

func intPercentage(value, total int64) int {
	if value <= 0 || total <= 0 {
		return 0
	}

	return int(min(int64(100), (value*100+total/2)/total))
}

func appResultTime(value time.Time) string {
	if value.IsZero() || value.Unix() <= 0 {
		return "not recorded"
	}

	return value.Local().Format("Jan 2, 2006 15:04")
}

func safeCSSState(value string) string {
	var state strings.Builder
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
			state.WriteRune(character)
		}
	}
	if state.Len() == 0 {
		return "unknown"
	}

	return state.String()
}

func prettyJSON(value string) string {
	var output bytes.Buffer
	if json.Indent(&output, []byte(value), "", "  ") == nil {
		return output.String()
	}

	return value
}
