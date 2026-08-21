package web

import (
	"fmt"
	"strings"
)

// Placeholders a parameterised query pattern may use. They are the only two
// substitutions, which keeps a stored pattern a bounded, non-executable
// template rather than an expression language.
const (
	JobParameterCategoryToken = "{category}"
	JobParameterLocationToken = "{location}"
)

// DefaultJobQueryPattern is the pattern a parameterised template uses when it
// stores none of its own.
const DefaultJobQueryPattern = JobParameterCategoryToken + " in " + JobParameterLocationToken

const (
	// MaximumJobParameterValues bounds each parameter list.
	MaximumJobParameterValues = 500
	// MaximumJobParameterQueries bounds the expanded query set, which is the
	// product of the two lists.
	MaximumJobParameterQueries = 5000
	// maximumJobParameterValueRunes bounds one category or location.
	maximumJobParameterValueRunes = 120
	// maximumJobQueryPatternRunes bounds the stored pattern.
	maximumJobQueryPatternRunes = 200
)

// JobParameters make one saved configuration reusable across many inputs: a
// set of categories applied to a set of locations. The query lines are
// regenerated from them every time the configuration runs, so adding a city to
// a template updates every future scheduled run without editing query text.
type JobParameters struct {
	// Categories are the business categories to search for.
	Categories []string `json:"categories,omitempty"`
	// Locations are the places to search each category in.
	Locations []string `json:"locations,omitempty"`
	// Pattern shapes one generated query line. An empty pattern uses
	// DefaultJobQueryPattern.
	Pattern string `json:"query_pattern,omitempty"`
	// Replace makes the expansion the complete query list instead of adding
	// to the lines already stored on the job.
	Replace bool `json:"replace,omitempty"`
}

// Empty reports whether the parameters would generate nothing.
func (p *JobParameters) Empty() bool {
	if p == nil {
		return true
	}

	return len(normalizeJobFilterList(p.Categories)) == 0 || len(normalizeJobFilterList(p.Locations)) == 0
}

// Validate bounds every stored value.
func (p *JobParameters) Validate() error {
	if p == nil {
		return nil
	}
	for label, values := range map[string][]string{
		"categories": p.Categories,
		"locations":  p.Locations,
	} {
		if len(values) > MaximumJobParameterValues {
			return fmt.Errorf("a template may list at most %d %s", MaximumJobParameterValues, label)
		}
		for _, value := range values {
			trimmed := strings.TrimSpace(value)
			if trimmed == "" {
				continue
			}
			if len([]rune(trimmed)) > maximumJobParameterValueRunes {
				return fmt.Errorf("a template %s value must be at most %d characters",
					label, maximumJobParameterValueRunes)
			}
			if strings.ContainsAny(trimmed, "\r\n") {
				return fmt.Errorf("a template %s value must stay on one line", label)
			}
		}
	}

	pattern := strings.TrimSpace(p.Pattern)
	if pattern == "" {
		return nil
	}
	if len([]rune(pattern)) > maximumJobQueryPatternRunes {
		return fmt.Errorf("a query pattern must be at most %d characters", maximumJobQueryPatternRunes)
	}
	if strings.ContainsAny(pattern, "\r\n") {
		return fmt.Errorf("a query pattern must stay on one line")
	}
	if !strings.Contains(pattern, JobParameterCategoryToken) && !strings.Contains(pattern, JobParameterLocationToken) {
		return fmt.Errorf("a query pattern must use %s, %s, or both",
			JobParameterCategoryToken, JobParameterLocationToken)
	}
	if count := expandedJobQueryCount(p); count > MaximumJobParameterQueries {
		return fmt.Errorf("this template expands to %d queries; the maximum is %d",
			count, MaximumJobParameterQueries)
	}

	return nil
}

func expandedJobQueryCount(parameters *JobParameters) int {
	categories := len(normalizeJobFilterList(parameters.Categories))
	locations := len(normalizeJobFilterList(parameters.Locations))
	if categories == 0 || locations == 0 {
		return 0
	}

	return categories * locations
}

// Normalized returns a copy with trimmed, de-duplicated values, or nil when
// the parameters would generate nothing.
func (p *JobParameters) Normalized() *JobParameters {
	if p.Empty() {
		return nil
	}

	return &JobParameters{
		Categories: normalizeJobFilterList(p.Categories),
		Locations:  normalizeJobFilterList(p.Locations),
		Pattern:    strings.TrimSpace(p.Pattern),
		Replace:    p.Replace,
	}
}

// ExpandQueries renders the parameter grid into query lines, in a stable
// category-major order so the same template always produces the same plan.
func (p *JobParameters) ExpandQueries() ([]string, error) {
	normalized := p.Normalized()
	if normalized == nil {
		return nil, nil
	}
	if err := normalized.Validate(); err != nil {
		return nil, err
	}

	pattern := normalized.Pattern
	if pattern == "" {
		pattern = DefaultJobQueryPattern
	}

	queries := make([]string, 0, len(normalized.Categories)*len(normalized.Locations))
	seen := make(map[string]struct{}, cap(queries))
	for _, category := range normalized.Categories {
		for _, location := range normalized.Locations {
			line := strings.ReplaceAll(pattern, JobParameterCategoryToken, category)
			line = strings.TrimSpace(strings.ReplaceAll(line, JobParameterLocationToken, location))
			if line == "" {
				continue
			}
			key := strings.ToLower(line)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			queries = append(queries, line)
		}
	}
	if len(queries) > MaximumJobParameterQueries {
		return nil, fmt.Errorf("this template expands to %d queries; the maximum is %d",
			len(queries), MaximumJobParameterQueries)
	}

	return queries, nil
}

// ApplyJobParameters regenerates a configuration's query lines from its stored
// parameters. A configuration without parameters is returned untouched, which
// is what every saved job and template did before parameters existed.
//
// This runs at job-creation time rather than at save time, so editing a
// template's location list changes every future run without rewriting queries.
func ApplyJobParameters(data JobData) (JobData, error) {
	parameters := data.Parameters.Normalized()
	if parameters == nil {
		return data, nil
	}

	generated, err := parameters.ExpandQueries()
	if err != nil {
		return JobData{}, err
	}
	if len(generated) == 0 {
		return data, nil
	}

	existing := data.Keywords
	if parameters.Replace {
		existing = nil
	}

	seen := make(map[string]struct{}, len(existing)+len(generated))
	merged := make([]string, 0, len(existing)+len(generated))
	for _, group := range [][]string{existing, generated} {
		for _, keyword := range group {
			trimmed := strings.TrimSpace(keyword)
			if trimmed == "" {
				continue
			}
			key := strings.ToLower(trimmed)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, trimmed)
		}
	}

	data.Keywords = merged
	data.Parameters = parameters

	return data, nil
}
