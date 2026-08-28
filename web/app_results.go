package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web/prospect"
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
	// RowOffset is the zero-based index of the first row on this page inside
	// the whole result set. The virtualised table numbers its rows from it so
	// aria-rowindex keeps reporting a row's true position, not its position
	// inside the rendered window.
	RowOffset    int
	RangeLabel   string
	CurrentURL   string
	ExportURL    string
	MapURL       string
	PreviousURL  string
	NextURL      string
	Capabilities appResultCapabilities
	// QuickFilters are the one-click lead workflows rendered as chips above
	// the table. Each one is a real URL into the existing filter language.
	QuickFilters []resultQuickFilter
	// ActiveChips describe every filter currently narrowing the table, each
	// with the URL that removes just that condition.
	ActiveChips []resultActiveChip
	// ExportPresets hand the current view to the export builder already
	// narrowed to a practical outreach slice.
	ExportPresets []resultExportPreset
	// LayoutColumns and LayoutGroup carry a saved view's stored table layout
	// into the page so reopening a view restores its columns and grouping.
	LayoutColumns string
	LayoutGroup   string
}

// resultQuickFilter is one prominent lead workflow. URL always points back at
// /app/results with the preset's nested filter expression applied; an active
// chip's URL clears it again so the control toggles.
type resultQuickFilter struct {
	Key    string
	Label  string
	Hint   string
	URL    string
	Active bool
}

// resultActiveChip is one removable condition in the filter bar.
type resultActiveChip struct {
	Label     string
	Value     string
	RemoveURL string
}

// resultExportPreset is a practical export starting point built from the
// current search rather than a saved server-side preset.
type resultExportPreset struct {
	Label string
	Hint  string
	URL   string
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
	CanProspect      bool
	CanEditFields    bool
	CanAddToList     bool
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
	// WebsiteStateLabel and WebsiteStateReason are the canonical audit state
	// (NEVER_CHECKED, LIVE, DEAD, ERROR, NO_WEBSITE, SOCIAL_ONLY, ...) in
	// operator words, so the table and the drawer say the same thing the
	// /api/v1/websites/states summary says.
	WebsiteStateLabel  string
	WebsiteStateReason string
	ResponseTime       string
	QualityLabel       string
	ConfidenceLabel    string
	UpdatedLabel       string
	ScrapedLabel       string
	// ProspectState and ProspectTierState are CSS-safe badge suffixes for the
	// stored prospect taxonomy values; ProspectScoreLabel is display-ready.
	ProspectState      string
	ProspectTierState  string
	ProspectScoreLabel string
	// The labels below back the specification's remaining core columns. They
	// are display-ready so the template never has to format a value itself.
	EmailsLabel           string
	EmailCount            int
	SocialLinks           []appSocialLink
	SocialLabel           string
	TechnologiesLabel     string
	LastCheckedLabel      string
	FirstSeenLabel        string
	LastSeenLabel         string
	RatingsBreakdownLabel string
	UserReviewCount       int
	PopularTimesLabel     string
	CoordinatesLabel      string
	ClaimedLabel          string
}

// appSocialLink is one detected social profile rendered as its own chip so the
// Social column stays scannable instead of collapsing into one long URL.
type appSocialLink struct {
	Platform string
	Label    string
	URL      string
}

type appBusinessDetail struct {
	CSRFToken string
	CanMutate bool
	CanEnrich bool
	Business  appResultRow
	MapURL    string
	// MapEmbedURL frames Map Explorer narrowed to this one business, so
	// the drawer shows the record's location instead of only naming its
	// coordinates. It is empty when the record has no coordinates.
	MapEmbedURL      string
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
	Prospect         appProspectDetail
	// Identity explains how this row's identity was established, so the
	// operator can judge whether the record really is one business.
	Identity appIdentityDetail
}

// appIdentityDetail is the drawer view of the stored identity provenance.
type appIdentityDetail struct {
	HasMethod       bool
	Method          string
	MethodLabel     string
	ConfidenceLabel string
	Evidence        []appIdentityEvidence
}

// appIdentityEvidence is one stored {"signal","value","detail"} entry backing
// the identity decision.
type appIdentityEvidence struct {
	Signal string
	Value  string
	Detail string
}

// appProspectDetail is the drawer view of the stored GBP-prospecting signals.
type appProspectDetail struct {
	HasStatus  bool
	Status     string
	State      string
	ScoreLabel string
	Tier       string
	TierState  string
	Reasons    []appProspectReason
	Opener     string
}

// appProspectReason is one explainable score component parsed from the
// stored prospect_reasons JSON array. Weight is the contribution's magnitude
// as a percentage of the largest contribution in the same explanation, so the
// drawer can draw a comparable bar next to every printed number.
type appProspectReason struct {
	Signal            string
	ContributionLabel string
	Detail            string
	Weight            int
	Tone              string
}

// contributionTone maps a signed score component onto a semantic state token
// name so the printed number and its bar agree: a component that added points
// reads as success, one that removed them as danger, and one that scored
// nothing stays neutral instead of implying a failure.
func contributionTone(value float64) string {
	switch {
	case value > 0:
		return "success"
	case value < 0:
		return "danger"
	default:
		return "neutral"
	}
}

type appBusinessSource struct {
	BusinessSourceView
	ExtractedLabel      string
	ConfidenceLabel     string
	RawJSONLabel        string
	NormalizedJSONLabel string
	SourceTypeLabel     string
	MethodLabel         string
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
	// SourceTypeLabel names the stored source type in the specification's
	// vocabulary, and MethodLabel does the same for the extraction method.
	SourceTypeLabel string
	MethodLabel     string
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
	// Weight is the share of this component's available points that were
	// actually awarded, so the drawer can draw a meter beside the number,
	// and Tone keeps that meter's colour honest about the sign.
	Weight int
	Tone   string
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
		http.Redirect(w, r, savedViewLayoutURL(view), http.StatusSeeOther)
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
		RowOffset:         resultPage.Offset,
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
			CanProspect:      s.svc.SupportsProspects(),
			CanEditFields:    s.manualEditAvailable(),
			CanAddToList:     s.resultListAvailable(),
		},
	}
	page.LayoutColumns, page.LayoutGroup = NormalizeSavedViewLayoutQuery(r.URL.Query())

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
		page.Capabilities.CanCheckEmails || page.Capabilities.CanMerge || page.Capabilities.CanDelete ||
		page.Capabilities.CanProspect
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

	page.QuickFilters = resultQuickFilters(r.URL, page.FilterJSON, page.Capabilities)
	page.ActiveChips = resultActiveChips(r.URL, page)
	page.ExportPresets = resultExportPresets(r.URL, page.Capabilities)

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

// resultLeadFilters returns the nested filter expression behind one lead
// workflow. Every field and operator used here is part of the bounded query
// language implemented by web/sqlite/results.go; nothing is invented.
func resultLeadFilters(key string) *ResultFilterGroup {
	eq := func(field string, values ...string) []ResultFilter {
		filters := make([]ResultFilter, 0, len(values))
		for _, value := range values {
			filters = append(filters, ResultFilter{Field: field, Operator: "eq", Value: value})
		}

		return filters
	}
	switch key {
	case "no-website":
		return &ResultFilterGroup{Logic: "and", Filters: []ResultFilter{{Field: "website", Operator: "empty"}}}
	case "weak-website":
		// One definition of "weak": WeakWebsiteStates in web/results.go, which
		// the weak_website export column and the weak_website filter also read.
		return &ResultFilterGroup{Logic: "and", Filters: []ResultFilter{
			{Field: "weak_website", Operator: "eq", Value: "true"},
		}}
	case "contactable":
		return &ResultFilterGroup{Logic: "or", Filters: []ResultFilter{
			{Field: "email", Operator: "not_empty"},
			{Field: "phone", Operator: "not_empty"},
		}}
	case "never-checked":
		return &ResultFilterGroup{Logic: "and", Filters: []ResultFilter{{Field: "last_checked_at", Operator: "empty"}}}
	case "top-tier":
		return &ResultFilterGroup{Logic: "or", Filters: eq("prospect_tier", "A", "B")}
	case "needs-review":
		return &ResultFilterGroup{Logic: "and", Filters: []ResultFilter{{Field: "reviewed", Operator: "eq", Value: "false"}}}
	default:
		return nil
	}
}

// resultQuickFilters builds the lead-workflow chips. A chip is active when the
// canonical filter expression on the request equals the preset, and an active
// chip links back to the unfiltered view so the control toggles.
func resultQuickFilters(source *url.URL, activeJSON string, capabilities appResultCapabilities) []resultQuickFilter {
	definitions := []struct {
		key, label, hint string
		needsProspects   bool
	}{
		{"no-website", "No website", "The listing links no website at all", false},
		{"weak-website", "Weak website", "Dead, parked, SSL-broken, social-only, free-builder, or HTTP-only", true},
		{"contactable", "Contactable", "Has a stored email address or phone number", false},
		{"never-checked", "Never checked", "No website audit has ever run for this business", false},
		{"top-tier", "Top tier A/B", "The highest worth-calling tiers", true},
		{"needs-review", "Needs review", "Not yet marked reviewed by an operator", false},
	}
	quick := make([]resultQuickFilter, 0, len(definitions))
	for _, definition := range definitions {
		if definition.needsProspects && !capabilities.CanProspect {
			continue
		}
		group := resultLeadFilters(definition.key)
		if group == nil {
			continue
		}
		encoded := resultFilterGroupJSON(group)
		active := encoded != "" && encoded == activeJSON
		target := encoded
		if active {
			target = ""
		}
		quick = append(quick, resultQuickFilter{
			Key:    definition.key,
			Label:  definition.label,
			Hint:   definition.hint,
			URL:    resultLeadFilterURL(source, target),
			Active: active,
		})
	}

	return quick
}

// resultLeadFilterURL replaces the whole filter expression while keeping the
// text search, source job, sort, and duplicate scope the operator chose.
func resultLeadFilterURL(source *url.URL, filterJSON string) string {
	values := source.Query()
	for _, key := range []string{"page", "notice", "view", "filter_field", "filter_operator", "filter_value", "filter_logic"} {
		values.Del(key)
	}
	if filterJSON == "" {
		values.Del("filter_json")
	} else {
		values.Set("filter_json", filterJSON)
	}

	return "/app/results?" + values.Encode()
}

// resultActiveChips lists every condition narrowing the current table with the
// URL that removes exactly that condition.
func resultActiveChips(source *url.URL, page resultsPageData) []resultActiveChip {
	chips := make([]resultActiveChip, 0, len(page.Filters)+4)
	if page.Query != "" {
		chips = append(chips, resultActiveChip{
			Label: "Search", Value: page.Query, RemoveURL: resultDropParameterURL(source, "q"),
		})
	}
	if page.JobID != "" {
		name := page.JobID
		for _, option := range page.JobOptions {
			if option.ID == page.JobID {
				name = option.Name
			}
		}
		chips = append(chips, resultActiveChip{
			Label: "Job", Value: name, RemoveURL: resultDropParameterURL(source, "job_id"),
		})
	}
	for index, filter := range page.Filters {
		chips = append(chips, resultActiveChip{
			Label:     filter.FieldLabel,
			Value:     strings.TrimSpace(filter.OperatorLabel + " " + filter.Value),
			RemoveURL: resultDropFilterRowURL(source, page.Filters, index),
		})
	}
	if page.FilterJSON != "" {
		label := "Filter group"
		if quick := resultQuickFilterLabel(page.FilterJSON, page.Capabilities); quick != "" {
			label = "Lead workflow"
		}
		chips = append(chips, resultActiveChip{
			Label:     label,
			Value:     resultFilterGroupSummary(page.FilterJSON, page.Capabilities),
			RemoveURL: resultDropParameterURL(source, "filter_json"),
		})
	}
	if page.IncludeDuplicates {
		chips = append(chips, resultActiveChip{
			Label: "Rows", Value: "duplicate source rows included",
			RemoveURL: resultDropParameterURL(source, "include_duplicates"),
		})
	}

	return chips
}

// resultQuickFilterLabel names the lead workflow behind a filter expression,
// or returns an empty string when the expression was hand written.
func resultQuickFilterLabel(filterJSON string, capabilities appResultCapabilities) string {
	for _, quick := range resultQuickFilters(&url.URL{Path: "/app/results"}, filterJSON, capabilities) {
		if quick.Active {
			return quick.Label
		}
	}

	return ""
}

// resultFilterGroupSummary describes a nested expression in one short phrase.
func resultFilterGroupSummary(filterJSON string, capabilities appResultCapabilities) string {
	if label := resultQuickFilterLabel(filterJSON, capabilities); label != "" {
		return label
	}
	var group ResultFilterGroup
	if err := json.Unmarshal([]byte(filterJSON), &group); err != nil {
		return "advanced expression"
	}
	conditions := 0
	var count func(ResultFilterGroup)
	count = func(current ResultFilterGroup) {
		conditions += len(current.Filters)
		for _, child := range current.Groups {
			count(child)
		}
	}
	count(group)
	if conditions == 1 {
		return "1 nested condition"
	}

	return fmt.Sprintf("%d nested conditions", conditions)
}

// resultDropParameterURL removes one query parameter and returns to page 1.
func resultDropParameterURL(source *url.URL, parameter string) string {
	values := source.Query()
	values.Del(parameter)
	values.Del("page")
	values.Del("notice")
	values.Del("view")

	return "/app/results?" + values.Encode()
}

// resultDropFilterRowURL re-emits the simple filter rows without the one at
// index, so a chip removes a single condition instead of the whole search.
func resultDropFilterRowURL(source *url.URL, filters []appResultFilter, skip int) string {
	values := source.Query()
	for _, key := range []string{"filter_field", "filter_operator", "filter_value", "page", "notice", "view"} {
		values.Del(key)
	}
	for index, filter := range filters {
		if index == skip {
			continue
		}
		values.Add("filter_field", filter.Field)
		values.Add("filter_operator", filter.Operator)
		values.Add("filter_value", filter.Value)
	}

	return "/app/results?" + values.Encode()
}

// resultExportPresets hand the current view to the export builder, optionally
// narrowed to a practical outreach slice. Every URL is the real export route.
func resultExportPresets(source *url.URL, capabilities appResultCapabilities) []resultExportPreset {
	if !capabilities.CanExport {
		return nil
	}
	presets := []resultExportPreset{{
		Label: "Full data from this view",
		Hint:  "Every column the export builder offers, filtered exactly as the table is now",
		URL:   resultExportURL(source),
	}, {
		Label: "Call sheet",
		Hint:  "Only businesses that already have a phone number or an email address",
		URL:   resultPresetExportURL(source, resultLeadFilters("contactable")),
	}, {
		Label: "No-website leads",
		Hint:  "Listings with no website stored at all",
		URL:   resultPresetExportURL(source, resultLeadFilters("no-website")),
	}}
	if capabilities.CanProspect {
		presets = append(presets, resultExportPreset{
			Label: "Weak-website leads",
			Hint:  "Dead, parked, SSL-broken, social-only, free-builder, or HTTP-only sites",
			URL:   resultPresetExportURL(source, resultLeadFilters("weak-website")),
		})
	}

	return presets
}

// resultPresetExportURL keeps the operator's text search, source job, and
// duplicate scope while replacing the filter expression with the preset's.
func resultPresetExportURL(source *url.URL, group *ResultFilterGroup) string {
	values := source.Query()
	for _, key := range []string{
		"page", "page_size", "sort", "notice", "view",
		"filter_field", "filter_operator", "filter_value", "filter_logic",
	} {
		values.Del(key)
	}
	if encoded := resultFilterGroupJSON(group); encoded != "" {
		values.Set("filter_json", encoded)
	} else {
		values.Del("filter_json")
	}
	values.Set("source", "results")

	return "/app/exports?" + values.Encode()
}

func resultExportURL(source *url.URL) string {
	values := source.Query()
	values.Del("page")
	values.Del("page_size")
	values.Del("sort")
	values.Set("source", "results")
	return "/app/exports?" + values.Encode()
}

// businessMapEmbedURL narrows Map Explorer to one business using the same
// bounded filter language the Results table uses, so the drawer can frame the
// existing map page instead of shipping a second map implementation.
func businessMapEmbedURL(businessID string) string {
	if !validBusinessID(businessID) {
		return ""
	}

	values := url.Values{}
	values.Set("source", "results")
	values.Add("filter_field", "id")
	values.Add("filter_operator", "eq")
	values.Add("filter_value", businessID)

	return "/app/map?" + values.Encode()
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
		Prospect:       s.newAppProspectDetail(r.Context(), detail),
		Identity:       newAppIdentityDetail(detail),
		Quality: appQualityReport{
			BusinessQualityReport: detail.Quality,
			ScoreLabel:            strconv.FormatFloat(detail.Quality.Score, 'f', 0, 64),
			ConfidenceLabel:       fmt.Sprintf("%.0f%%", detail.Quality.Confidence*100),
			EvaluatedLabel:        appResultTime(detail.Quality.EvaluatedAt),
		},
	}
	for _, item := range detail.Quality.Contributions {
		weight := 0
		if item.Maximum > 0 {
			weight = int(math.Round(math.Max(0, math.Min(1, item.Contribution/item.Maximum)) * 100))
		}
		page.Quality.Contributions = append(page.Quality.Contributions, appQualityContribution{
			QualityContribution: item,
			ContributionLabel:   fmt.Sprintf("%+.2f", item.Contribution),
			MaximumLabel:        fmt.Sprintf("%.2f", item.Maximum),
			Weight:              weight,
			Tone:                contributionTone(item.Contribution),
		})
	}
	if detail.Business.Latitude != nil && detail.Business.Longitude != nil {
		page.MapURL = fmt.Sprintf("https://www.openstreetmap.org/?mlat=%0.6f&mlon=%0.6f#map=17/%0.6f/%0.6f",
			*detail.Business.Latitude, *detail.Business.Longitude, *detail.Business.Latitude, *detail.Business.Longitude)
		page.MapEmbedURL = businessMapEmbedURL(detail.Business.ID)
	}
	for _, source := range detail.Sources {
		page.Sources = append(page.Sources, appBusinessSource{
			BusinessSourceView:  source,
			ExtractedLabel:      appResultTime(source.ExtractedAt),
			ConfidenceLabel:     fmt.Sprintf("%.0f%%", source.Confidence*100),
			RawJSONLabel:        prettyJSON(source.RawJSON),
			NormalizedJSONLabel: prettyJSON(source.NormalizedJSON),
			SourceTypeLabel:     ProvenanceSourceTypeLabel(source.SourceType),
			MethodLabel:         ProvenanceMethodLabel(source.ExtractionMethod),
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
			SourceTypeLabel:     ProvenanceSourceTypeLabel(item.SourceType),
			MethodLabel:         ProvenanceMethodLabel(item.ExtractionMethod),
		})
	}
	for _, item := range detail.Websites {
		page.Websites = append(page.Websites, appWebsiteView{
			WebsiteView:     item,
			HTTPStatusLabel: optionalIntLabel(item.HTTPStatus, "—"),
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
		return "—"
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
	websiteResolution := WebsiteStateForResult(result.Website, result.MapsURL, result.WebsiteStatus)
	websiteState := strings.ToLower(strings.ReplaceAll(websiteResolution.State, "_", "-"))
	prospectScore := ""
	if result.ProspectScore != nil {
		prospectScore = strconv.FormatFloat(*result.ProspectScore, 'f', 0, 64)
	}

	coordinates := ""
	if result.Latitude != nil && result.Longitude != nil {
		coordinates = fmt.Sprintf("%.6f, %.6f", *result.Latitude, *result.Longitude)
	}
	claimed := "—"
	if result.Claimed {
		claimed = "claimed"
	}
	social := appSocialLinks(result.Social)
	socialLabel := make([]string, 0, len(social))
	for _, link := range social {
		socialLabel = append(socialLabel, link.Label)
	}

	return appResultRow{
		BusinessResult:        result,
		RatingLabel:           rating,
		ReviewCountLabel:      reviews,
		WebsiteState:          safeCSSState(websiteState),
		WebsiteStateLabel:     websiteResolution.Label,
		WebsiteStateReason:    websiteResolution.Reason,
		ResponseTime:          responseTime,
		QualityLabel:          strconv.FormatFloat(result.QualityScore, 'f', 0, 64),
		ConfidenceLabel:       fmt.Sprintf("%.0f%%", result.Confidence*100),
		UpdatedLabel:          appResultTime(result.UpdatedAt),
		ScrapedLabel:          appResultTime(result.ScrapedAt),
		ProspectState:         prospectStateClass(result.ProspectStatus),
		ProspectTierState:     prospectStateClass(result.ProspectTier),
		ProspectScoreLabel:    prospectScore,
		EmailsLabel:           strings.Join(result.Emails, ", "),
		EmailCount:            len(result.Emails),
		SocialLinks:           social,
		SocialLabel:           strings.Join(socialLabel, ", "),
		TechnologiesLabel:     strings.Join(result.Technologies, ", "),
		LastCheckedLabel:      resultOptionalTimeLabel(result.LastCheckedAt),
		FirstSeenLabel:        appResultTime(result.FirstSeenAt),
		LastSeenLabel:         appResultTime(result.LastSeenAt),
		RatingsBreakdownLabel: ratingsBreakdownLabel(result.ReviewsPerRating),
		UserReviewCount:       jsonArrayLength(result.UserReviews),
		PopularTimesLabel:     popularTimesLabel(result.PopularTimes),
		CoordinatesLabel:      coordinates,
		ClaimedLabel:          claimed,
	}
}

// socialPlatformLabels names each stored platform key for display.
var socialPlatformLabels = map[string]string{
	"facebook":  "Facebook",
	"instagram": "Instagram",
	"linkedin":  "LinkedIn",
	"x":         "X",
	"youtube":   "YouTube",
	"tiktok":    "TikTok",
	"whatsapp":  "WhatsApp",
}

// appSocialLinks turns the stored per-platform profile URLs into ordered
// display chips, skipping the platforms with no local evidence.
func appSocialLinks(social BusinessSocial) []appSocialLink {
	links := make([]appSocialLink, 0, len(SocialPlatforms()))
	for _, platform := range SocialPlatforms() {
		profileURL := social.URL(platform)
		if profileURL == "" {
			continue
		}
		links = append(links, appSocialLink{
			Platform: platform,
			Label:    socialPlatformLabels[platform],
			URL:      profileURL,
		})
	}

	return links
}

// ratingsBreakdownLabel renders the stored reviews_per_rating object as a
// compact "5★ 120 · 4★ 30" summary. An unparsable or absent value renders as
// an empty string so the column shows the usual missing-value dash.
func ratingsBreakdownLabel(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	counts := map[string]json.Number{}
	if err := json.Unmarshal([]byte(value), &counts); err != nil {
		return ""
	}
	parts := make([]string, 0, len(counts))
	for _, star := range []string{"5", "4", "3", "2", "1"} {
		count, ok := counts[star]
		if !ok {
			continue
		}
		parts = append(parts, star+"★ "+count.String())
	}

	return strings.Join(parts, " · ")
}

// popularTimesLabel summarises the stored popular_times object by naming the
// days it covers rather than printing a wall of numbers into a table cell.
func popularTimesLabel(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	days := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(value), &days); err != nil {
		return ""
	}
	if len(days) == 0 {
		return ""
	}

	return fmt.Sprintf("%d day profile", len(days))
}

// jsonArrayLength counts the elements of a stored JSON array without decoding
// their contents, so a large user-review cell costs one pass.
func jsonArrayLength(value string) int {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(value), &items); err != nil {
		return 0
	}

	return len(items)
}

// prospectStateClass converts a stored prospect taxonomy value (for example
// NO_WEBSITE or tier A) into a CSS-safe badge suffix such as "no-website".
func prospectStateClass(value string) string {
	value = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "_", "-")
	if value == "" {
		return ""
	}

	return safeCSSState(value)
}

// newAppIdentityDetail turns the stored identity provenance into drawer rows.
// A record written before identity provenance existed simply has no method,
// and the drawer says so rather than inventing one.
func newAppIdentityDetail(detail BusinessDetail) appIdentityDetail {
	method := strings.TrimSpace(detail.IdentityMethod)
	if method == "" {
		return appIdentityDetail{}
	}
	confidence := "—"
	if detail.IdentityConfidence != nil {
		confidence = fmt.Sprintf("%.0f%%", *detail.IdentityConfidence*100)
	}

	return appIdentityDetail{
		HasMethod:       true,
		Method:          method,
		MethodLabel:     resultIdentityMethodLabel(method),
		ConfidenceLabel: confidence,
		Evidence:        parseIdentityEvidence(detail.IdentityEvidence),
	}
}

// resultIdentityMethodLabel spells out the stored identity method codes.
func resultIdentityMethodLabel(value string) string {
	labels := map[string]string{
		"exact":                 "Exact identifier match",
		"new":                   "First time this business was seen",
		"phone_corroborated":    "Matched on a corroborating phone number",
		"website_corroborated":  "Matched on a corroborating website",
		"address_corroborated":  "Matched on a corroborating address",
		"name_corroborated":     "Matched on a corroborating name",
		"geo_corroborated":      "Matched on corroborating coordinates",
		"place_id":              "Matched on the Google place ID",
		"cid":                   "Matched on the Google CID",
		"data_id":               "Matched on the Google data ID",
		"merged":                "Merged from a duplicate record",
		"operator":              "Set by an operator decision",
		"fallback":              "Matched by a corroborated fallback signal",
		"normalized_name_geo":   "Matched on normalized name and coordinates",
		"normalized_name_phone": "Matched on normalized name and phone number",
	}
	if label := labels[strings.ToLower(strings.TrimSpace(value))]; label != "" {
		return label
	}

	return value
}

// parseIdentityEvidence tolerantly decodes the stored identity evidence array;
// malformed JSON simply yields no evidence rows.
func parseIdentityEvidence(raw string) []appIdentityEvidence {
	var stored []struct {
		Signal string `json:"signal"`
		Value  string `json:"value"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return nil
	}
	evidence := make([]appIdentityEvidence, 0, len(stored))
	for _, item := range stored {
		if strings.TrimSpace(item.Signal) == "" && strings.TrimSpace(item.Detail) == "" {
			continue
		}
		evidence = append(evidence, appIdentityEvidence(item))
	}

	return evidence
}

// parseProspectReasons tolerantly decodes the stored prospect_reasons JSON
// into displayable rows; malformed JSON simply yields no explanation.
func parseProspectReasons(raw string) []appProspectReason {
	var stored []struct {
		Signal       string  `json:"signal"`
		Contribution float64 `json:"contribution"`
		Detail       string  `json:"detail"`
	}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return nil
	}
	largest := 0.0
	for _, reason := range stored {
		largest = math.Max(largest, math.Abs(reason.Contribution))
	}
	reasons := make([]appProspectReason, 0, len(stored))
	for _, reason := range stored {
		if strings.TrimSpace(reason.Signal) == "" && strings.TrimSpace(reason.Detail) == "" {
			continue
		}
		weight := 0
		if largest > 0 {
			weight = int(math.Round(math.Abs(reason.Contribution) / largest * 100))
		}
		reasons = append(reasons, appProspectReason{
			Signal:            reason.Signal,
			ContributionLabel: fmt.Sprintf("%+.1f", reason.Contribution),
			Detail:            reason.Detail,
			Weight:            weight,
			Tone:              contributionTone(reason.Contribution),
		})
	}

	return reasons
}

// prospectOpener renders the status-matched call opener for one business
// entirely on the server so the drawer needs no additional JavaScript.
func (s *Server) prospectOpener(ctx context.Context, detail BusinessDetail) string {
	if s == nil || s.svc == nil || !s.svc.SupportsProspects() {
		return ""
	}
	templates, err := s.svc.ProspectOpenerTemplates(ctx)
	if err != nil || len(templates) == 0 {
		return ""
	}
	opener := prospect.OpenerTemplateFor(templates, detail.Business.ProspectStatus)
	if strings.TrimSpace(opener) == "" {
		return ""
	}
	rating := ""
	if detail.Business.Rating != nil {
		rating = strconv.FormatFloat(*detail.Business.Rating, 'f', 1, 64)
	}
	reviews := ""
	if detail.Business.ReviewCount != nil {
		reviews = strconv.FormatInt(*detail.Business.ReviewCount, 10)
	}

	return strings.TrimSpace(prospect.RenderOpener(opener, map[string]string{
		"name":     detail.Business.Name,
		"category": detail.Business.PrimaryCategory,
		"city":     detail.Business.City,
		"status":   detail.Business.ProspectStatus,
		"tier":     detail.Business.ProspectTier,
		"rating":   rating,
		"reviews":  reviews,
	}))
}

// newAppProspectDetail assembles the drawer's Prospecting section data.
func (s *Server) newAppProspectDetail(ctx context.Context, detail BusinessDetail) appProspectDetail {
	status := strings.TrimSpace(detail.Business.ProspectStatus)
	if status == "" {
		return appProspectDetail{}
	}
	scoreLabel := ""
	if detail.Business.ProspectScore != nil {
		scoreLabel = strconv.FormatFloat(*detail.Business.ProspectScore, 'f', 0, 64)
	}

	return appProspectDetail{
		HasStatus:  true,
		Status:     status,
		State:      prospectStateClass(status),
		ScoreLabel: scoreLabel,
		Tier:       detail.Business.ProspectTier,
		TierState:  prospectStateClass(detail.Business.ProspectTier),
		Reasons:    parseProspectReasons(detail.ProspectReasons),
		Opener:     s.prospectOpener(ctx, detail),
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
		"prospect_status": "Prospect status", "prospect_tier": "Prospect tier",
		"prospect_score": "Prospect score",
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
		return "—"
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
