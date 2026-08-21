package web

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
)

// Business-status values the wizard's step-5 status filter accepts. They are
// the values the importer normalizes Maps' status text into.
const (
	JobStatusOperational        = "operational"
	JobStatusTemporarilyClosed  = "temporarily_closed"
	JobStatusPermanentlyClosed  = "permanently_closed"
	maximumJobFilterCategories  = 50
	maximumJobFilterNameRunes   = 200
	maximumJobFilterCategoryLen = 120
)

// maximumJobFilterReviews bounds the review-count range so a stored job can
// never carry an absurd value into a numeric SQL filter.
const maximumJobFilterReviews = 10_000_000

// JobResultFilters are the wizard's step-5 collection filters.
//
// Google Maps applies none of them: the scraper always receives whatever the
// listing pages return and the per-job CSV always keeps every collected row.
// These rules are applied to the workspace's stored results AFTER collection,
// which is why the wizard and every surface built from them must say so.
type JobResultFilters struct {
	// RatingMin and RatingMax bound the star rating. A listing with no
	// rating at all is excluded once either bound is set.
	RatingMin *float64 `json:"rating_min,omitempty"`
	RatingMax *float64 `json:"rating_max,omitempty"`
	// ReviewsMin and ReviewsMax bound the review count.
	ReviewsMin *int64 `json:"reviews_min,omitempty"`
	ReviewsMax *int64 `json:"reviews_max,omitempty"`
	// IncludeCategories keeps only businesses carrying one of these
	// categories; ExcludeCategories drops businesses carrying any of them.
	IncludeCategories []string `json:"include_categories,omitempty"`
	ExcludeCategories []string `json:"exclude_categories,omitempty"`
	// Statuses keeps only the listed business statuses.
	Statuses []string `json:"statuses,omitempty"`
	// Claimed keeps only claimed (true) or only unclaimed (false) listings.
	// Maps does not expose ownership for every listing, so a nil value — the
	// default — never filters.
	Claimed *bool `json:"claimed,omitempty"`
	// NameContains and NameExcludes are case-insensitive substring rules on
	// the business name.
	NameContains string `json:"name_contains,omitempty"`
	NameExcludes string `json:"name_excludes,omitempty"`
}

// JobResultFilterNotice is the exact sentence every surface must show next to
// these filters. Presenting them as anything Google applied would be a lie.
const JobResultFilterNotice = "Applied to stored results after collection. " +
	"Google Maps returned every listing the plan reached and the per-job CSV keeps them all."

// Empty reports whether the filter set would narrow nothing.
func (f *JobResultFilters) Empty() bool {
	if f == nil {
		return true
	}

	return f.RatingMin == nil && f.RatingMax == nil &&
		f.ReviewsMin == nil && f.ReviewsMax == nil &&
		len(f.IncludeCategories) == 0 && len(f.ExcludeCategories) == 0 &&
		len(f.Statuses) == 0 && f.Claimed == nil &&
		strings.TrimSpace(f.NameContains) == "" && strings.TrimSpace(f.NameExcludes) == ""
}

// Validate bounds every stored value. It is called from JobData.Validate, so
// an invalid filter can never be persisted or reach a SQL builder.
func (f *JobResultFilters) Validate() error {
	if f == nil {
		return nil
	}
	if err := validateJobRatingBounds(f.RatingMin, f.RatingMax); err != nil {
		return err
	}
	if err := validateJobReviewBounds(f.ReviewsMin, f.ReviewsMax); err != nil {
		return err
	}
	for _, group := range [][]string{f.IncludeCategories, f.ExcludeCategories} {
		if len(group) > maximumJobFilterCategories {
			return fmt.Errorf("at most %d categories may be listed per filter", maximumJobFilterCategories)
		}
		for _, category := range group {
			trimmed := strings.TrimSpace(category)
			if trimmed == "" {
				return fmt.Errorf("category filters must not contain empty entries")
			}
			if len([]rune(trimmed)) > maximumJobFilterCategoryLen {
				return fmt.Errorf("a category filter must be at most %d characters", maximumJobFilterCategoryLen)
			}
		}
	}
	for _, status := range f.Statuses {
		switch status {
		case JobStatusOperational, JobStatusTemporarilyClosed, JobStatusPermanentlyClosed:
		default:
			return fmt.Errorf("business status filter %q is not one of %q, %q, %q",
				status, JobStatusOperational, JobStatusTemporarilyClosed, JobStatusPermanentlyClosed)
		}
	}
	for _, value := range []string{f.NameContains, f.NameExcludes} {
		if len([]rune(value)) > maximumJobFilterNameRunes {
			return fmt.Errorf("a name filter must be at most %d characters", maximumJobFilterNameRunes)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("a name filter must stay on one line")
		}
	}

	return nil
}

func validateJobRatingBounds(minimum, maximum *float64) error {
	for _, bound := range []*float64{minimum, maximum} {
		if bound == nil {
			continue
		}
		if math.IsNaN(*bound) || math.IsInf(*bound, 0) || *bound < 0 || *bound > 5 {
			return fmt.Errorf("rating filters must be between 0 and 5")
		}
	}
	if minimum != nil && maximum != nil && *minimum > *maximum {
		return fmt.Errorf("the minimum rating must not exceed the maximum rating")
	}

	return nil
}

func validateJobReviewBounds(minimum, maximum *int64) error {
	for _, bound := range []*int64{minimum, maximum} {
		if bound == nil {
			continue
		}
		if *bound < 0 || *bound > maximumJobFilterReviews {
			return fmt.Errorf("review-count filters must be between 0 and %d", maximumJobFilterReviews)
		}
	}
	if minimum != nil && maximum != nil && *minimum > *maximum {
		return fmt.Errorf("the minimum review count must not exceed the maximum review count")
	}

	return nil
}

// Normalized returns a copy with trimmed, de-duplicated, ordered values, or
// nil when nothing would be filtered.
func (f *JobResultFilters) Normalized() *JobResultFilters {
	if f.Empty() {
		return nil
	}

	normalized := JobResultFilters{
		RatingMin: f.RatingMin, RatingMax: f.RatingMax,
		ReviewsMin: f.ReviewsMin, ReviewsMax: f.ReviewsMax,
		Claimed:      f.Claimed,
		NameContains: strings.TrimSpace(f.NameContains),
		NameExcludes: strings.TrimSpace(f.NameExcludes),
	}
	normalized.IncludeCategories = normalizeJobFilterList(f.IncludeCategories)
	normalized.ExcludeCategories = normalizeJobFilterList(f.ExcludeCategories)
	normalized.Statuses = normalizeJobFilterList(f.Statuses)

	if normalized.Empty() {
		return nil
	}

	return &normalized
}

func normalizeJobFilterList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, value)
	}
	if len(unique) == 0 {
		return nil
	}

	return unique
}

// FilterGroup renders the filter set as one bounded expression for the
// existing Results, Map, saved-view, and export filter language. Anything
// that lists alternatives (categories, statuses) becomes a nested OR group so
// the whole expression stays a single AND of independent rules.
func (f *JobResultFilters) FilterGroup() *ResultFilterGroup {
	normalized := f.Normalized()
	if normalized == nil {
		return nil
	}

	group := ResultFilterGroup{Logic: "and"}
	appendJobNumericFilter(&group, "rating", normalized.RatingMin, normalized.RatingMax)
	appendJobReviewFilter(&group, normalized.ReviewsMin, normalized.ReviewsMax)

	if len(normalized.IncludeCategories) > 0 {
		group.Groups = append(group.Groups, jobCategoryGroup(normalized.IncludeCategories, false))
	}
	if len(normalized.ExcludeCategories) > 0 {
		group.Groups = append(group.Groups, jobCategoryGroup(normalized.ExcludeCategories, true))
	}
	if len(normalized.Statuses) > 0 {
		statuses := ResultFilterGroup{Logic: "or"}
		for _, status := range normalized.Statuses {
			statuses.Filters = append(statuses.Filters,
				ResultFilter{Field: "business_status", Operator: "eq", Value: status})
		}
		group.Groups = append(group.Groups, statuses)
	}
	if normalized.Claimed != nil {
		group.Filters = append(group.Filters, ResultFilter{
			Field: "claimed", Operator: "eq", Value: strconv.FormatBool(*normalized.Claimed),
		})
	}
	if normalized.NameContains != "" {
		group.Filters = append(group.Filters, ResultFilter{
			Field: "name", Operator: "contains", Value: normalized.NameContains,
		})
	}
	if normalized.NameExcludes != "" {
		group.Filters = append(group.Filters, ResultFilter{
			Field: "name", Operator: "not_contains", Value: normalized.NameExcludes,
		})
	}
	if len(group.Filters) == 0 && len(group.Groups) == 0 {
		return nil
	}

	return &group
}

func appendJobNumericFilter(group *ResultFilterGroup, field string, minimum, maximum *float64) {
	if minimum != nil {
		group.Filters = append(group.Filters, ResultFilter{
			Field: field, Operator: "gte", Value: strconv.FormatFloat(*minimum, 'f', -1, 64),
		})
	}
	if maximum != nil {
		group.Filters = append(group.Filters, ResultFilter{
			Field: field, Operator: "lte", Value: strconv.FormatFloat(*maximum, 'f', -1, 64),
		})
	}
}

func appendJobReviewFilter(group *ResultFilterGroup, minimum, maximum *int64) {
	if minimum != nil {
		group.Filters = append(group.Filters, ResultFilter{
			Field: "review_count", Operator: "gte", Value: strconv.FormatInt(*minimum, 10),
		})
	}
	if maximum != nil {
		group.Filters = append(group.Filters, ResultFilter{
			Field: "review_count", Operator: "lte", Value: strconv.FormatInt(*maximum, 10),
		})
	}
}

// jobCategoryGroup matches the primary category or any additional category.
// Excluding negates the whole group so a business is dropped when ANY of its
// categories matches.
func jobCategoryGroup(categories []string, exclude bool) ResultFilterGroup {
	group := ResultFilterGroup{Logic: "or", Not: exclude}
	for _, category := range categories {
		group.Filters = append(group.Filters,
			ResultFilter{Field: "category", Operator: "eq", Value: category},
			ResultFilter{Field: "category_member", Operator: "eq", Value: category},
		)
	}

	return group
}

// ResultsQuery renders the filter set as the query string the Results page
// and the results API already understand, scoped to one job.
func (f *JobResultFilters) ResultsQuery(jobID string) string {
	values := url.Values{}
	if trimmed := strings.TrimSpace(jobID); trimmed != "" {
		values.Set("job_id", trimmed)
	}
	normalized := f.Normalized()
	if normalized == nil {
		return values.Encode()
	}

	add := func(field, operator, value string) {
		values.Add("filter_field", field)
		values.Add("filter_operator", operator)
		values.Add("filter_value", value)
	}
	if normalized.RatingMin != nil {
		add("rating", "gte", strconv.FormatFloat(*normalized.RatingMin, 'f', -1, 64))
	}
	if normalized.RatingMax != nil {
		add("rating", "lte", strconv.FormatFloat(*normalized.RatingMax, 'f', -1, 64))
	}
	if normalized.ReviewsMin != nil {
		add("review_count", "gte", strconv.FormatInt(*normalized.ReviewsMin, 10))
	}
	if normalized.ReviewsMax != nil {
		add("review_count", "lte", strconv.FormatInt(*normalized.ReviewsMax, 10))
	}
	for _, category := range normalized.ExcludeCategories {
		add("category", "not_contains", category)
	}
	if normalized.Claimed != nil {
		add("claimed", "eq", strconv.FormatBool(*normalized.Claimed))
	}
	if normalized.NameContains != "" {
		add("name", "contains", normalized.NameContains)
	}
	if normalized.NameExcludes != "" {
		add("name", "not_contains", normalized.NameExcludes)
	}
	// Alternatives (included categories, statuses) cannot be expressed as
	// independent AND rows, so they travel as the structured expression the
	// same endpoints accept.
	if len(normalized.IncludeCategories) > 0 || len(normalized.Statuses) > 0 {
		if group := normalized.alternativesGroup(); group != nil {
			if encoded, err := json.Marshal(group); err == nil {
				values.Set("filter_json", string(encoded))
			}
		}
	}

	return values.Encode()
}

// alternativesGroup isolates the rules that need OR semantics.
func (f *JobResultFilters) alternativesGroup() *ResultFilterGroup {
	group := ResultFilterGroup{Logic: "and"}
	if len(f.IncludeCategories) > 0 {
		group.Groups = append(group.Groups, jobCategoryGroup(f.IncludeCategories, false))
	}
	if len(f.Statuses) > 0 {
		statuses := ResultFilterGroup{Logic: "or"}
		for _, status := range f.Statuses {
			statuses.Filters = append(statuses.Filters,
				ResultFilter{Field: "business_status", Operator: "eq", Value: status})
		}
		group.Groups = append(group.Groups, statuses)
	}
	if len(group.Groups) == 0 {
		return nil
	}

	return &group
}
