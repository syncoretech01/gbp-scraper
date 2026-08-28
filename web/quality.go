package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrQualityScoringUnsupported indicates that the configured repository
	// cannot persist explainable quality scores.
	ErrQualityScoringUnsupported = errors.New("quality scoring is unavailable")
	// ErrInvalidQualityRules identifies an invalid local scoring configuration.
	ErrInvalidQualityRules = errors.New("invalid quality scoring rules")
)

// QualityRuleSet defines editable local weights and thresholds. A repository
// assigns an immutable content-derived Version whenever rules are saved.
type QualityRuleSet struct {
	Version              string  `json:"version"`
	Name                 string  `json:"name"`
	OpenWeight           float64 `json:"open_weight"`
	ActiveWebsiteWeight  float64 `json:"active_website_weight"`
	HTTPSWeight          float64 `json:"https_weight"`
	PhoneWeight          float64 `json:"phone_weight"`
	EmailWeight          float64 `json:"email_weight"`
	SocialWeight         float64 `json:"social_weight"`
	RatingWeight         float64 `json:"rating_weight"`
	ReviewCountWeight    float64 `json:"review_count_weight"`
	CompletenessWeight   float64 `json:"completeness_weight"`
	FreshnessWeight      float64 `json:"freshness_weight"`
	WebsiteQualityWeight float64 `json:"website_quality_weight"`
	ResponseTimeWeight   float64 `json:"response_time_weight"`
	RatingThreshold      float64 `json:"rating_threshold"`
	ReviewCountThreshold int64   `json:"review_count_threshold"`
	FreshnessDays        int     `json:"freshness_days"`
	ResponseTimeMS       int64   `json:"response_time_ms"`
	ExcludeClosed        bool    `json:"exclude_closed"`
}

// QualityContribution explains one positive, zero, or negative component.
type QualityContribution struct {
	Component    string  `json:"component"`
	Contribution float64 `json:"contribution"`
	Maximum      float64 `json:"maximum"`
	Passed       bool    `json:"passed"`
	Reason       string  `json:"reason"`
}

// BusinessQualityReport is the reproducible score and its rule breakdown.
type BusinessQualityReport struct {
	BusinessID    string                `json:"business_id"`
	Score         float64               `json:"score"`
	Confidence    float64               `json:"confidence"`
	RuleVersion   string                `json:"rule_version"`
	RuleName      string                `json:"rule_name"`
	Contributions []QualityContribution `json:"contributions"`
	EvaluatedAt   time.Time             `json:"evaluated_at"`
}

// DefaultQualityRuleSet returns the built-in, dependency-free 100-point model.
func DefaultQualityRuleSet() QualityRuleSet {
	return QualityRuleSet{
		Version:              "builtin-v1",
		Name:                 "Balanced local quality",
		OpenWeight:           10,
		ActiveWebsiteWeight:  15,
		HTTPSWeight:          5,
		PhoneWeight:          10,
		EmailWeight:          12,
		SocialWeight:         5,
		RatingWeight:         8,
		ReviewCountWeight:    8,
		CompletenessWeight:   12,
		FreshnessWeight:      5,
		WebsiteQualityWeight: 5,
		ResponseTimeWeight:   5,
		RatingThreshold:      4,
		ReviewCountThreshold: 50,
		FreshnessDays:        30,
		ResponseTimeMS:       2000,
	}
}

// ValidateQualityRuleSet bounds user-editable scoring rules before storage.
func ValidateQualityRuleSet(rules QualityRuleSet) error {
	if strings.TrimSpace(rules.Name) == "" || len(rules.Name) > 120 {
		return fmt.Errorf("%w: name is required and must be at most 120 characters", ErrInvalidQualityRules)
	}
	weights := []float64{
		rules.OpenWeight, rules.ActiveWebsiteWeight, rules.HTTPSWeight, rules.PhoneWeight,
		rules.EmailWeight, rules.SocialWeight, rules.RatingWeight, rules.ReviewCountWeight,
		rules.CompletenessWeight, rules.FreshnessWeight, rules.WebsiteQualityWeight,
		rules.ResponseTimeWeight,
	}
	total := 0.0
	for _, weight := range weights {
		if weight < 0 || weight > 100 {
			return fmt.Errorf("%w: every weight must be between 0 and 100", ErrInvalidQualityRules)
		}
		total += weight
	}
	if total <= 0 || total > 200 {
		return fmt.Errorf("%w: total component weight must be greater than 0 and at most 200", ErrInvalidQualityRules)
	}
	if rules.RatingThreshold <= 0 || rules.RatingThreshold > 5 {
		return fmt.Errorf("%w: rating threshold must be between 0 and 5", ErrInvalidQualityRules)
	}
	if rules.ReviewCountThreshold < 1 || rules.ReviewCountThreshold > 10_000_000 {
		return fmt.Errorf("%w: review threshold is outside the supported range", ErrInvalidQualityRules)
	}
	if rules.FreshnessDays < 1 || rules.FreshnessDays > 3650 {
		return fmt.Errorf("%w: freshness must be between 1 and 3650 days", ErrInvalidQualityRules)
	}
	if rules.ResponseTimeMS < 100 || rules.ResponseTimeMS > 120_000 {
		return fmt.Errorf("%w: response threshold must be between 100 and 120000 ms", ErrInvalidQualityRules)
	}

	return nil
}

// QualityScoreDriftSample is one business whose stored quality score does not
// match its own stored breakdown.
type QualityScoreDriftSample struct {
	BusinessID    string  `json:"business_id"`
	Name          string  `json:"name"`
	StoredScore   float64 `json:"stored_score"`
	ExplainedSum  float64 `json:"explained_sum"`
	RuleVersion   string  `json:"rule_version"`
	ComponentRows int64   `json:"component_rows"`
}

// QualityScoreDriftReport is the result of auditing the record-quality column
// against the explanation stored beside it.
//
// The two must agree: a score is only trustworthy if the breakdown adds up to
// it. They can come apart when a value written by a different producer - for
// example an import-time record-completeness number - is merged into the same
// column, which is exactly the conflation the three separate scores exist to
// prevent.
type QualityScoreDriftReport struct {
	Applied  bool                      `json:"applied"`
	Checked  int64                     `json:"checked"`
	Drifted  int64                     `json:"drifted"`
	Repaired int64                     `json:"repaired"`
	Samples  []QualityScoreDriftSample `json:"samples"`
}

type qualityRepository interface {
	ActiveQualityRules(context.Context) (QualityRuleSet, error)
	SaveQualityRules(context.Context, QualityRuleSet) (QualityRuleSet, error)
	BusinessQuality(context.Context, string) (BusinessQualityReport, error)
	RecalculateQuality(context.Context, []string) (int64, error)
	QualityScoreDrift(ctx context.Context, repair bool) (QualityScoreDriftReport, error)
}

// ActiveQualityRules returns the current local scoring configuration.
func (s *Service) ActiveQualityRules(ctx context.Context) (QualityRuleSet, error) {
	repository, ok := s.repo.(qualityRepository)
	if !ok {
		return QualityRuleSet{}, ErrQualityScoringUnsupported
	}
	return repository.ActiveQualityRules(ctx)
}

// SaveQualityRules validates and activates an immutable scoring-rule version.
func (s *Service) SaveQualityRules(ctx context.Context, rules QualityRuleSet) (QualityRuleSet, error) {
	if err := ValidateQualityRuleSet(rules); err != nil {
		return QualityRuleSet{}, err
	}
	repository, ok := s.repo.(qualityRepository)
	if !ok {
		return QualityRuleSet{}, ErrQualityScoringUnsupported
	}
	return repository.SaveQualityRules(ctx, rules)
}

// BusinessQuality returns the stored explainable score for one business.
func (s *Service) BusinessQuality(ctx context.Context, id string) (BusinessQualityReport, error) {
	repository, ok := s.repo.(qualityRepository)
	if !ok {
		return BusinessQualityReport{}, ErrQualityScoringUnsupported
	}
	return repository.BusinessQuality(ctx, id)
}

// QualityRecalculationResult is one recalculation plus the website audits it
// had to queue first. Scored keeps its historical meaning.
type QualityRecalculationResult struct {
	Scored int64 `json:"scored"`
	// Prerequisite reports the website audits queued because some of the
	// requested businesses had never been checked. It is nil when none were
	// needed.
	Prerequisite *WebsiteScoringPrerequisite `json:"website_audit_prerequisite,omitempty"`
}

// RecalculateQualityWithAudit is the explicit "score these records" entry
// point. Website-dependent components are unknown until an audit runs, so
// when auditFirst is set the prerequisite audits are queued durably before
// the pass and the caller is told how many rows are still waiting.
func (s *Service) RecalculateQualityWithAudit(
	ctx context.Context,
	ids []string,
	auditFirst bool,
) (QualityRecalculationResult, error) {
	result := QualityRecalculationResult{}
	if auditFirst {
		prerequisite, err := s.EnsureWebsiteAuditPrerequisite(ctx, ids, "quality_recalculate")
		if err != nil && !errors.Is(err, ErrWebsiteStateUnsupported) {
			return QualityRecalculationResult{}, err
		}
		if prerequisite.Unaudited > 0 {
			result.Prerequisite = &prerequisite
		}
	}
	scored, err := s.RecalculateQuality(ctx, ids)
	if err != nil {
		return QualityRecalculationResult{}, err
	}
	result.Scored = scored

	return result, nil
}

// QualityScoreDrift audits every stored record-quality score against its own
// stored breakdown, and repairs the rows that disagree when repair is set.
func (s *Service) QualityScoreDrift(ctx context.Context, repair bool) (QualityScoreDriftReport, error) {
	repository, ok := s.repo.(qualityRepository)
	if !ok {
		return QualityScoreDriftReport{}, ErrQualityScoringUnsupported
	}

	return repository.QualityScoreDrift(ctx, repair)
}

// RecalculateQuality rescans selected businesses, or every business for an
// empty ID list, using the currently active rules.
func (s *Service) RecalculateQuality(ctx context.Context, ids []string) (int64, error) {
	repository, ok := s.repo.(qualityRepository)
	if !ok {
		return 0, ErrQualityScoringUnsupported
	}
	return repository.RecalculateQuality(ctx, ids)
}
