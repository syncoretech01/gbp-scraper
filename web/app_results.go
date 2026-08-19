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
	FilterLogic       string
	FilterJSON        string
	JobOptions        []resultJobOption
	SavedViews        []namedAppOption
	Filters           []appResultFilter
	IncludeDuplicates bool
	Results           []appResultRow
	Total             int64
	RangeLabel        string
	CurrentURL        string
	ExportURL         string
	MapURL            string
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
	CSRFToken        string
	CanMutate        bool
	CanEnrich        bool
	Business         appResultRow
	MapURL           string
	RawJSON          string
	Sources          []appBusinessSource
	Provenance       []appFieldProvenance
	Websites         []appWebsiteView
	Emails           []appEmailView
	Phones           []PhoneView
	SocialProfiles   []SocialProfileView
	Versions         []appBusinessVersion
	Changes          []appBusinessChange
	Duplicates       []string
	DuplicateMatches []appDuplicateMatch
	Quality          appQualityReport
}

type appBusinessSource struct {
	BusinessSourceView
	ExtractedLabel      string
	ConfidenceLabel     string
	RawJSONLabel        string
	NormalizedJSONLabel string
}

type appBusinessVersion struct {
	BusinessVersionView
	ObservedLabel string
	FieldsLabel   string
	SnapshotLabel string
}

type appFieldProvenance struct {
	FieldProvenanceView
	ExtractedLabel  string
	SupersededLabel string
	ConfidenceLabel string
}

type appWebsiteView struct {
	WebsiteView
	HTTPStatusLabel string
	HTTPSLabel      string
	TLSLabel        string
	ResponseLabel   string
	CheckedLabel    string
}

type appEmailView struct {
	EmailView
	MXLabel         string
	CheckedLabel    string
	ConfidenceLabel string
	RelevanceLabel  string
}

type appBusinessChange struct {
	BusinessChangeView
	DetectedLabel string
	BeforeLabel   string
	AfterLabel    string
}

type appDuplicateMatch struct {
	DuplicateMatchView
	ScoreLabel   string
	SignalsLabel string
	CanResolve   bool
	KeepID       string
	OtherID      string
	ThisName     string
	ThisAddress  string
}

type appQualityReport struct {
	BusinessQualityReport
	ScoreLabel      string
	ConfidenceLabel string
	EvaluatedLabel  string
	Contributions   []appQualityContribution
}

type appQualityContribution struct {
	QualityContribution
	ContributionLabel string
	MaximumLabel      string
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
		FilterLogic:       "and",
		IncludeDuplicates: search.IncludeDuplicates,
		Total:             resultPage.Total,
		CurrentURL:        r.URL.RequestURI(),
		ExportURL:         resultExportURL(r.URL),
		MapURL:            resultMapURL(r.URL),
		Capabilities: appResultCapabilities{
			CanMap:           true,
			CanSavedViews:    s.reusableAvailable(),
			CanExport:        s.exportAvailable(),
			CanTag:           s.resultMutationAvailable(),
			CanMarkReviewed:  s.resultMutationAvailable(),
			CanEnrich:        s.enrichmentAvailable(),
			CanCheckWebsites: s.enrichmentAvailable(),
			CanCheckEmails:   s.enrichmentAvailable(),
			CanMerge:         s.duplicateReviewAvailable(),
			CanDelete:        s.resultMutationAvailable(),
		},
	}
	flatFilters := search.Filters
	requestedFilterLogic := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("filter_logic")))
	requestedFilterJSON := strings.TrimSpace(r.URL.Query().Get("filter_json"))
	if requestedFilterLogic == "or" && requestedFilterJSON == "" && search.FilterGroup != nil &&
		strings.EqualFold(search.FilterGroup.Logic, "or") && !search.FilterGroup.Not && len(search.FilterGroup.Groups) == 0 {
		page.FilterLogic = "or"
		flatFilters = search.FilterGroup.Filters
	} else if search.FilterGroup != nil {
		page.FilterJSON = resultFilterGroupJSON(search.FilterGroup)
	}
	page.Capabilities.CanSelect = page.Capabilities.CanTag || page.Capabilities.CanMarkReviewed ||
		page.Capabilities.CanEnrich || page.Capabilities.CanCheckWebsites ||
		page.Capabilities.CanCheckEmails || page.Capabilities.CanMerge || page.Capabilities.CanDelete
	for _, filter := range flatFilters {
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

func resultMapURL(source *url.URL) string {
	values := source.Query()
	values.Del("page")
	values.Del("page_size")
	values.Set("source", "results")

	return "/app/map?" + values.Encode()
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
		CSRFToken:      s.csrfToken,
		CanMutate:      s.resultMutationAvailable(),
		CanEnrich:      s.enrichmentAvailable(),
		Business:       newAppResultRow(detail.Business),
		RawJSON:        prettyJSON(detail.RawJSON),
		Duplicates:     detail.Duplicates,
		Phones:         detail.Phones,
		SocialProfiles: detail.SocialProfiles,
		Quality: appQualityReport{
			BusinessQualityReport: detail.Quality,
			ScoreLabel:            strconv.FormatFloat(detail.Quality.Score, 'f', 0, 64),
			ConfidenceLabel:       fmt.Sprintf("%.0f%%", detail.Quality.Confidence*100),
			EvaluatedLabel:        appResultTime(detail.Quality.EvaluatedAt),
		},
	}
	for _, item := range detail.Quality.Contributions {
		page.Quality.Contributions = append(page.Quality.Contributions, appQualityContribution{
			QualityContribution: item,
			ContributionLabel:   fmt.Sprintf("%+.2f", item.Contribution),
			MaximumLabel:        fmt.Sprintf("%.2f", item.Maximum),
		})
	}
	if detail.Business.Latitude != nil && detail.Business.Longitude != nil {
		page.MapURL = fmt.Sprintf("https://www.openstreetmap.org/?mlat=%0.6f&mlon=%0.6f#map=17/%0.6f/%0.6f",
			*detail.Business.Latitude, *detail.Business.Longitude, *detail.Business.Latitude, *detail.Business.Longitude)
	}
	for _, source := range detail.Sources {
		page.Sources = append(page.Sources, appBusinessSource{
			BusinessSourceView:  source,
			ExtractedLabel:      appResultTime(source.ExtractedAt),
			ConfidenceLabel:     fmt.Sprintf("%.0f%%", source.Confidence*100),
			RawJSONLabel:        prettyJSON(source.RawJSON),
			NormalizedJSONLabel: prettyJSON(source.NormalizedJSON),
		})
	}
	for _, item := range detail.Provenance {
		superseded := "current"
		if item.SupersededAt != nil {
			superseded = appResultTime(*item.SupersededAt)
		}
		page.Provenance = append(page.Provenance, appFieldProvenance{
			FieldProvenanceView: item,
			ExtractedLabel:      appResultTime(item.ExtractedAt),
			SupersededLabel:     superseded,
			ConfidenceLabel:     fmt.Sprintf("%.0f%%", item.Confidence*100),
		})
	}
	for _, item := range detail.Websites {
		page.Websites = append(page.Websites, appWebsiteView{
			WebsiteView:     item,
			HTTPStatusLabel: optionalIntLabel(item.HTTPStatus, "not recorded"),
			HTTPSLabel:      optionalBoolLabel(item.HTTPS, "not checked"),
			TLSLabel:        optionalBoolLabel(item.TLSValid, "not checked"),
			ResponseLabel:   optionalDurationLabel(item.ResponseTimeMS),
			CheckedLabel:    resultOptionalTimeLabel(item.LastCheckedAt),
		})
	}
	for _, item := range detail.Emails {
		page.Emails = append(page.Emails, appEmailView{
			EmailView:       item,
			MXLabel:         optionalBoolLabel(item.DomainHasMX, "not checked"),
			CheckedLabel:    resultOptionalTimeLabel(item.LastCheckedAt),
			ConfidenceLabel: fmt.Sprintf("%.0f%%", item.Confidence*100),
			RelevanceLabel:  fmt.Sprintf("%d/100", item.Relevance),
		})
	}
	for _, version := range detail.Versions {
		page.Versions = append(page.Versions, appBusinessVersion{
			BusinessVersionView: version,
			ObservedLabel:       appResultTime(version.ObservedAt),
			FieldsLabel:         strings.Join(version.ChangedFields, ", "),
			SnapshotLabel:       prettyJSON(version.Snapshot),
		})
	}
	for _, item := range detail.Changes {
		page.Changes = append(page.Changes, appBusinessChange{
			BusinessChangeView: item,
			DetectedLabel:      appResultTime(item.DetectedAt),
			BeforeLabel:        prettyJSON(item.BeforeValue),
			AfterLabel:         prettyJSON(item.AfterValue),
		})
	}
	canResolve := s.duplicateReviewAvailable()

	for _, item := range detail.DuplicateMatches {
		page.DuplicateMatches = append(page.DuplicateMatches, appDuplicateMatch{
			DuplicateMatchView: item,
			ScoreLabel:         fmt.Sprintf("%.0f%%", item.Score*100),
			SignalsLabel:       prettyJSON(item.Signals),
			// Only a pending pair may still be decided, and only when the
			// repository can record the decision.
			CanResolve:  canResolve && item.State == "pending" && item.CandidateID > 0,
			KeepID:      detail.Business.ID,
			OtherID:     item.BusinessID,
			ThisName:    detail.Business.Name,
			ThisAddress: detail.Business.Address,
		})
	}

	return page, http.StatusOK, nil
}

func optionalIntLabel(value *int64, fallback string) string {
	if value == nil {
		return fallback
	}
	return strconv.FormatInt(*value, 10)
}

func optionalBoolLabel(value *bool, fallback string) string {
	if value == nil {
		return fallback
	}
	if *value {
		return "yes"
	}
	return "no"
}

func optionalDurationLabel(value *int64) string {
	if value == nil {
		return "not checked"
	}
	return fmt.Sprintf("%d ms", *value)
}

func resultOptionalTimeLabel(value *time.Time) string {
	if value == nil {
		return "not recorded"
	}
	return appResultTime(*value)
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
		"id": "ID", "name": "Name", "address": "Address", "city": "City", "state": "State",
		"postal_code": "Postal code", "country": "Country", "category": "Primary category",
		"category_member": "Any category", "status": "Business status", "business_status": "Business status",
		"website_status": "Website status", "domain": "Domain", "rating": "Rating",
		"reviews": "Review count", "review_count": "Review count", "quality_score": "Quality score",
		"confidence": "Quality confidence", "website_response_ms": "Website response time",
		"website": "Website", "email": "Email", "email_status": "Email status", "email_kind": "Email kind",
		"phone": "Phone", "social": "Social platform", "technology": "Technology",
		"reviewed": "Reviewed", "claimed": "Claimed", "tags": "Tags", "change_status": "Change status",
		"place_id": "Place ID", "cid": "CID", "data_id": "Data ID", "maps_url": "Maps URL",
		"updated_at": "Updated date", "first_seen_at": "First seen date", "last_seen_at": "Last seen date",
		"scraped_at": "Scraped date", "last_checked_at": "Website checked date",
		"distance": "Distance from point", "bbox": "Bounding box", "polygon": "GeoJSON polygon",
	}
	if label := labels[strings.ToLower(strings.TrimSpace(value))]; label != "" {
		return label
	}

	return value
}

func resultOperatorLabel(value string) string {
	labels := map[string]string{
		"eq": "equals", "neq": "does not equal", "contains": "contains", "not_contains": "does not contain",
		"starts_with": "starts with", "ends_with": "ends with", "gte": "at least", "lte": "at most",
		"gt": "greater than", "lt": "less than", "between": "between", "within": "is within",
		"empty": "is empty", "not_empty": "is not empty",
	}
	if label := labels[strings.ToLower(strings.TrimSpace(value))]; label != "" {
		return label
	}

	return value
}

func resultFilterGroupJSON(group *ResultFilterGroup) string {
	if group == nil {
		return ""
	}
	encoded, err := json.Marshal(group)
	if err != nil {
		return ""
	}

	return string(encoded)
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
