package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gosom/google-maps-scraper/web/prospect"
)

var (
	// ErrProspectsUnsupported indicates that the configured repository cannot
	// persist GBP prospecting signals.
	ErrProspectsUnsupported = errors.New("prospect scoring is unavailable")
	// ErrInvalidProspectConfig identifies an invalid prospect configuration or
	// request value.
	ErrInvalidProspectConfig = errors.New("invalid prospect configuration")
)

// Settings keys for the durable prospect configuration. The two *_url keys
// are design-only integration boundaries: they are stored and exposed but
// never called from this codebase.
const (
	settingProspectScoreWeights     = "prospect.score_weights"
	settingProspectOpenerTemplates  = "prospect.opener_templates"
	settingProspectEmailVerifierURL = "prospect.email_verifier_url"
	settingProspectAuditServiceURL  = "prospect.audit_service_url"
)

const (
	maximumProspectRecomputeIDs   = 100_000
	minimumOpenerTemplateLength   = 10
	maximumOpenerTemplateLength   = 500
	defaultDiscoveredCompanyLimit = 1000
	maximumDiscoveredCompanyLimit = 5000
	discoveredCompanyPageSize     = 250
	maximumQuerySynonyms          = 50
	maximumQuerySynonymLength     = 80
	defaultQueryTopN              = 20
	maximumQueryTopN              = 200
)

// SupportsProspects reports whether prospect signals can be stored durably.
func (s *Service) SupportsProspects() bool {
	_, ok := s.repo.(prospectRepository)
	return ok
}

// ProspectScoreWeights returns the stored worth-calling weights, falling back
// to the built-in defaults when nothing valid is stored or the repository
// has no settings storage.
func (s *Service) ProspectScoreWeights(ctx context.Context) (prospect.ScoreWeights, error) {
	values, err := s.LoadSettings(ctx)
	if err != nil {
		if errors.Is(err, ErrSettingsUnsupported) {
			return prospect.DefaultScoreWeights(), nil
		}
		return prospect.ScoreWeights{}, err
	}
	raw := strings.TrimSpace(values[settingProspectScoreWeights])
	if raw == "" {
		return prospect.DefaultScoreWeights(), nil
	}
	weights := prospect.DefaultScoreWeights()
	if err := json.Unmarshal([]byte(raw), &weights); err != nil {
		return prospect.DefaultScoreWeights(), nil
	}
	if err := weights.Validate(); err != nil {
		return prospect.DefaultScoreWeights(), nil
	}
	return weights, nil
}

// SaveProspectScoreWeights validates and persists the worth-calling weights.
// The settings repository records the audit trail for every settings write.
func (s *Service) SaveProspectScoreWeights(ctx context.Context, weights prospect.ScoreWeights) error {
	if err := weights.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProspectConfig, err)
	}
	encoded, err := json.Marshal(weights)
	if err != nil {
		return fmt.Errorf("encode prospect score weights: %w", err)
	}
	return s.SaveSettings(ctx, map[string]string{settingProspectScoreWeights: string(encoded)})
}

// ProspectOpenerTemplates returns the stored call-opener templates, falling
// back to the built-in defaults when nothing valid is stored.
func (s *Service) ProspectOpenerTemplates(ctx context.Context) (map[string]string, error) {
	values, err := s.LoadSettings(ctx)
	if err != nil {
		if errors.Is(err, ErrSettingsUnsupported) {
			return prospect.DefaultOpenerTemplates(), nil
		}
		return nil, err
	}
	raw := strings.TrimSpace(values[settingProspectOpenerTemplates])
	if raw == "" {
		return prospect.DefaultOpenerTemplates(), nil
	}
	var templates map[string]string
	if err := json.Unmarshal([]byte(raw), &templates); err != nil {
		return prospect.DefaultOpenerTemplates(), nil
	}
	if err := validateProspectOpenerTemplates(templates); err != nil {
		return prospect.DefaultOpenerTemplates(), nil
	}
	return templates, nil
}

// SaveProspectOpenerTemplates validates and persists call-opener templates.
func (s *Service) SaveProspectOpenerTemplates(ctx context.Context, templates map[string]string) error {
	if err := validateProspectOpenerTemplates(templates); err != nil {
		return err
	}
	encoded, err := json.Marshal(templates)
	if err != nil {
		return fmt.Errorf("encode prospect opener templates: %w", err)
	}
	return s.SaveSettings(ctx, map[string]string{settingProspectOpenerTemplates: string(encoded)})
}

// validateProspectOpenerTemplates bounds template keys to the known status
// taxonomy plus "default", which must always be present.
func validateProspectOpenerTemplates(templates map[string]string) error {
	if len(templates) == 0 {
		return fmt.Errorf("%w: at least the \"default\" opener template is required", ErrInvalidProspectConfig)
	}
	known := prospect.DefaultOpenerTemplates()
	for key, template := range templates {
		if _, ok := known[key]; !ok {
			return fmt.Errorf("%w: %q is not a known website status or \"default\"", ErrInvalidProspectConfig, key)
		}
		length := utf8.RuneCountInString(strings.TrimSpace(template))
		if length < minimumOpenerTemplateLength || length > maximumOpenerTemplateLength {
			return fmt.Errorf("%w: template %q must contain %d to %d characters",
				ErrInvalidProspectConfig, key, minimumOpenerTemplateLength, maximumOpenerTemplateLength)
		}
	}
	if strings.TrimSpace(templates["default"]) == "" {
		return fmt.Errorf("%w: the \"default\" opener template is required", ErrInvalidProspectConfig)
	}
	return nil
}

// ProspectIntegrationSettings exposes the two design-only boundary URLs.
// They are stored for future workstreams and never called from this process.
type ProspectIntegrationSettings struct {
	EmailVerifierURL string `json:"email_verifier_url"`
	AuditServiceURL  string `json:"audit_service_url"`
}

// ProspectIntegrations returns the stored boundary URLs.
func (s *Service) ProspectIntegrations(ctx context.Context) (ProspectIntegrationSettings, error) {
	values, err := s.LoadSettings(ctx)
	if err != nil {
		if errors.Is(err, ErrSettingsUnsupported) {
			return ProspectIntegrationSettings{}, nil
		}
		return ProspectIntegrationSettings{}, err
	}
	return ProspectIntegrationSettings{
		EmailVerifierURL: strings.TrimSpace(values[settingProspectEmailVerifierURL]),
		AuditServiceURL:  strings.TrimSpace(values[settingProspectAuditServiceURL]),
	}, nil
}

// SaveProspectIntegrations validates and persists the boundary URLs. Empty
// values clear a boundary; non-empty values must point at loopback or
// private addresses only.
func (s *Service) SaveProspectIntegrations(ctx context.Context, settings ProspectIntegrationSettings) error {
	settings.EmailVerifierURL = strings.TrimSpace(settings.EmailVerifierURL)
	settings.AuditServiceURL = strings.TrimSpace(settings.AuditServiceURL)
	for name, value := range map[string]string{
		"email verifier URL": settings.EmailVerifierURL,
		"audit service URL":  settings.AuditServiceURL,
	} {
		if value == "" {
			continue
		}
		if err := validateProspectBoundaryURL(value); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalidProspectConfig, name, err)
		}
	}
	return s.SaveSettings(ctx, map[string]string{
		settingProspectEmailVerifierURL: settings.EmailVerifierURL,
		settingProspectAuditServiceURL:  settings.AuditServiceURL,
	})
}

// validateProspectBoundaryURL accepts only URLs that the shared local webhook
// rule permits AND whose host is literally loopback or private: "localhost",
// a *.localhost name, or an IP address already vetted by the webhook rule.
// Public host names such as example.com are rejected because these boundaries
// must never leave the operator's machine or network.
func validateProspectBoundaryURL(raw string) error {
	parsed, err := validateLocalWebhookURL(raw)
	if err != nil {
		return err
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil
	}
	if net.ParseIP(host) != nil {
		// validateLocalWebhookURL already rejected non-private, non-loopback IPs.
		return nil
	}
	return fmt.Errorf("host %q must be localhost or a private/loopback IP address", host)
}

// ProspectRecomputeResult is one recompute pass plus the website audits it
// had to queue first. Processed keeps its historical meaning, so a caller
// that only reads that field is unchanged.
type ProspectRecomputeResult struct {
	Processed int64 `json:"processed"`
	// Prerequisite reports the website audits queued because the requested
	// businesses had never been checked. It is nil when nothing was needed.
	Prerequisite *WebsiteScoringPrerequisite `json:"website_audit_prerequisite,omitempty"`
}

// RecomputeProspectsWithAudit is the explicit "score these businesses"
// entry point.
//
// The worth-calling score leans on website state, so scoring a business whose
// website has never been checked would turn "unknown" into a number that
// reads like a measurement. When auditFirst is set, the prerequisite audits
// are queued durably before the pass runs and the caller is told how many
// rows are still waiting on evidence; the pass itself still runs, and the
// classifier leaves an unaudited business unclassified rather than guessing.
func (s *Service) RecomputeProspectsWithAudit(
	ctx context.Context,
	ids []string,
	auditFirst bool,
) (ProspectRecomputeResult, error) {
	result := ProspectRecomputeResult{}
	if auditFirst {
		prerequisite, err := s.EnsureWebsiteAuditPrerequisite(ctx, ids, "prospect_recompute")
		if err != nil && !errors.Is(err, ErrWebsiteStateUnsupported) {
			return ProspectRecomputeResult{}, err
		}
		if prerequisite.Unaudited > 0 {
			result.Prerequisite = &prerequisite
		}
	}
	processed, err := s.RecomputeProspects(ctx, ids)
	if err != nil {
		return ProspectRecomputeResult{}, err
	}
	result.Processed = processed

	return result, nil
}

// RecomputeProspects rescans the selected businesses, or every business when
// the ID list is empty, using the currently stored score weights.
func (s *Service) RecomputeProspects(ctx context.Context, ids []string) (int64, error) {
	repository, ok := s.repo.(prospectRepository)
	if !ok {
		return 0, ErrProspectsUnsupported
	}
	if len(ids) > maximumProspectRecomputeIDs {
		return 0, fmt.Errorf("%w: too many selected businesses", ErrInvalidProspectConfig)
	}
	cleaned := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			cleaned = append(cleaned, id)
		}
	}
	weights, err := s.ProspectScoreWeights(ctx)
	if err != nil {
		return 0, err
	}
	return repository.RecomputeProspects(ctx, weights, cleaned)
}

// ProspectSummaryData returns stored prospect aggregates for dashboards.
func (s *Service) ProspectSummaryData(ctx context.Context) (ProspectSummary, error) {
	repository, ok := s.repo.(prospectRepository)
	if !ok {
		return ProspectSummary{}, ErrProspectsUnsupported
	}
	return repository.ProspectSummary(ctx)
}

// DiscoveredCompany is the Lead-Engine ingestion contract. Field names are
// part of the boundary and must not change.
type DiscoveredCompany struct {
	ProviderCompanyID string                `json:"providerCompanyId"`
	Name              string                `json:"name"`
	Domain            string                `json:"domain"`
	Website           string                `json:"website"`
	Industry          string                `json:"industry"`
	City              string                `json:"city"`
	State             string                `json:"state"`
	Country           string                `json:"country"`
	Phone             string                `json:"phone"`
	SourceURL         string                `json:"sourceUrl"`
	Confidence        float64               `json:"confidence"`
	Meta              DiscoveredCompanyMeta `json:"meta"`
}

// DiscoveredCompanyMeta wraps the raw prospecting payload.
type DiscoveredCompanyMeta struct {
	RawPayload DiscoveredCompanyPayload `json:"rawPayload"`
}

// DiscoveredCompanyPayload carries the prospecting signals the Engine stores
// verbatim. WebsiteStatus uses the prospect taxonomy (NO_WEBSITE, DEAD, ...;
// empty when the classification is inconclusive). has_ads_tag and
// copyright_year are false/zero until a website audit contributes them; the
// normalized result row does not carry them.
type DiscoveredCompanyPayload struct {
	WebsiteStatus string   `json:"website_status"`
	ProspectScore float64  `json:"prospect_score"`
	ProspectTier  string   `json:"prospect_tier"`
	HasAdsTag     bool     `json:"has_ads_tag"`
	CopyrightYear int      `json:"copyright_year"`
	Emails        []string `json:"emails"`
}

// DiscoveredCompanies returns one job's businesses in the Lead-Engine
// contract shape. limit is bounded to 1..5000 and defaults to 1000.
func (s *Service) DiscoveredCompanies(ctx context.Context, jobID string, limit int) ([]DiscoveredCompany, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, fmt.Errorf("%w: job_id is required", ErrInvalidProspectConfig)
	}
	if limit <= 0 {
		limit = defaultDiscoveredCompanyLimit
	}
	if limit > maximumDiscoveredCompanyLimit {
		limit = maximumDiscoveredCompanyLimit
	}
	weights, templates := s.prospectExportConfiguration(ctx)

	companies := make([]DiscoveredCompany, 0, min(limit, discoveredCompanyPageSize))
	for len(companies) < limit {
		pageSize := min(discoveredCompanyPageSize, limit-len(companies))
		page, err := s.SearchBusinesses(ctx, ResultSearch{
			JobID:  jobID,
			Limit:  pageSize,
			Offset: len(companies),
		})
		if err != nil {
			return nil, err
		}
		for _, business := range page.Results {
			computed := computeProspectExportData(business, weights, templates)
			companies = append(companies, discoveredCompanyFromBusiness(business, computed))
			if len(companies) >= limit {
				break
			}
		}
		if len(page.Results) < pageSize || int64(len(companies)) >= page.Total {
			break
		}
	}
	return companies, nil
}

// prospectExportData carries prospect values computed for one business at
// read time from the currently stored weights and opener templates.
type prospectExportData struct {
	Status  string            `json:"status"`
	Score   float64           `json:"score"`
	Tier    string            `json:"tier"`
	Reasons []prospect.Reason `json:"reasons,omitempty"`
	Opener  string            `json:"opener,omitempty"`
}

// prospectExportConfiguration loads the stored weights and templates, using
// the pure defaults whenever settings storage is unavailable.
func (s *Service) prospectExportConfiguration(ctx context.Context) (prospect.ScoreWeights, map[string]string) {
	weights, err := s.ProspectScoreWeights(ctx)
	if err != nil {
		weights = prospect.DefaultScoreWeights()
	}
	templates, err := s.ProspectOpenerTemplates(ctx)
	if err != nil || len(templates) == 0 {
		templates = prospect.DefaultOpenerTemplates()
	}
	return weights, templates
}

// prospectSignalsFromBusiness derives prospect signals from the fields the
// normalized result row actually carries. Audit-only signals (ads tag,
// copyright year, content size) stay at their zero values.
func prospectSignalsFromBusiness(business BusinessResult) prospect.Signals {
	website := strings.TrimSpace(business.Website)
	status := strings.ToLower(strings.TrimSpace(business.WebsiteStatus))

	// Only genuine audit outcomes count as audit evidence. Un-audited
	// businesses carry the placeholder "unknown" (or nothing), and treating
	// that as a performed audit would misclassify every live-but-unaudited
	// site as DEAD.
	audited := status == "active" || status == "inactive" || status == "error"

	signals := prospect.Signals{
		WebsiteURL:     website,
		MapsURL:        business.MapsURL,
		AuditPerformed: audited,
		Reachable:      status == "active",
		HTTPS:          strings.HasPrefix(strings.ToLower(website), "https://"),
		PhonePresent:   business.Phone != "" || business.NormalizedPhone != "",
		EmailFound:     business.PrimaryEmail != "",
		CurrentYear:    time.Now().UTC().Year(),
	}
	signals.TLSValid = signals.HTTPS
	if signals.Reachable {
		signals.StatusCode = 200
	}
	if business.Rating != nil {
		signals.Rating = *business.Rating
	}
	if business.ReviewCount != nil {
		signals.ReviewCount = *business.ReviewCount
	}
	return signals
}

// computeProspectExportData classifies, scores, and renders the call opener
// for one business with the supplied configuration. An inconclusive
// classification (website present but never audited) keeps the empty status,
// matching the stored prospect_status default.
func computeProspectExportData(business BusinessResult, weights prospect.ScoreWeights, templates map[string]string) prospectExportData {
	signals := prospectSignalsFromBusiness(business)

	// The durable classification (written at import and after every website
	// audit) is authoritative when present: it was computed with audit
	// evidence this read-time view may not carry. Read-time classification is
	// the fallback for rows the recompute pass has not reached yet.
	status := business.ProspectStatus
	var (
		score   float64
		tier    string
		reasons []prospect.Reason
	)

	if status != "" {
		if business.ProspectScore != nil {
			score = *business.ProspectScore
			tier = business.ProspectTier
			_, _, reasons = prospect.Score(status, signals, weights)
		} else {
			score, tier, reasons = prospect.Score(status, signals, weights)
		}
	} else {
		status, _ = prospect.Classify(signals)
		if status != "" {
			score, tier, reasons = prospect.Score(status, signals, weights)
		}
	}

	// An unclassified business carries no score or tier, matching the durable
	// columns: scoring an unknown website status would rank noise.
	if status == "" {
		score, tier, reasons = 0, "", nil
	}

	template := prospect.OpenerTemplateFor(templates, status)
	if strings.TrimSpace(template) == "" {
		template = prospect.DefaultOpenerTemplates()["default"]
	}
	rating := ""
	if business.Rating != nil {
		rating = strconv.FormatFloat(*business.Rating, 'f', 1, 64)
	}
	reviews := ""
	if business.ReviewCount != nil {
		reviews = strconv.FormatInt(*business.ReviewCount, 10)
	}
	statusReason := ""
	if len(reasons) > 0 && reasons[0].Signal == status {
		statusReason = reasons[0].Detail
	}
	opener := prospect.RenderOpener(template, map[string]string{
		"name":          business.Name,
		"city":          business.City,
		"state":         business.State,
		"category":      business.PrimaryCategory,
		"domain":        prospect.DomainFromWebsite(business.Website),
		"status":        status,
		"status_reason": statusReason,
		"rating":        rating,
		"reviews":       reviews,
		"tier":          tier,
	})
	return prospectExportData{Status: status, Score: score, Tier: tier, Reasons: reasons, Opener: opener}
}

// discoveredCompanyFromBusiness maps one normalized business row into the
// Lead-Engine contract shape.
func discoveredCompanyFromBusiness(business BusinessResult, computed prospectExportData) DiscoveredCompany {
	providerID := strings.TrimSpace(business.PlaceID)
	confidence := 1.0
	if providerID == "" {
		providerID = business.ID
		confidence = 0.7
	}
	emails := []string{}
	if business.PrimaryEmail != "" {
		emails = append(emails, business.PrimaryEmail)
	}
	return DiscoveredCompany{
		ProviderCompanyID: providerID,
		Name:              business.Name,
		Domain:            prospect.DomainFromWebsite(business.Website),
		Website:           business.Website,
		Industry:          business.PrimaryCategory,
		City:              business.City,
		State:             business.State,
		Country:           business.Country,
		Phone:             business.Phone,
		SourceURL:         business.MapsURL,
		Confidence:        confidence,
		Meta: DiscoveredCompanyMeta{RawPayload: DiscoveredCompanyPayload{
			WebsiteStatus: computed.Status,
			ProspectScore: computed.Score,
			ProspectTier:  computed.Tier,
			HasAdsTag:     false,
			CopyrightYear: 0,
			Emails:        emails,
		}},
	}
}

// ProspectQueryPlan is the wizard's query-generation result.
type ProspectQueryPlan struct {
	Queries  []string   `json:"queries"`
	Centre   [2]float64 `json:"centre"`
	ZIPCount int        `json:"zip_count"`
	Skipped  int        `json:"skipped"`
	// Targets carries the geography each query must be executed from: one
	// entry per (synonym, ZIP) with that ZIP's own centroid, city, state,
	// population, selection rank and a stable id. Geography must never live
	// only inside the query string, which is what made a 25-ZIP plan execute
	// as one area from a single population-weighted centre.
	Targets []prospect.QueryTarget `json:"targets,omitempty"`
}

// GenerateProspectQueries crosses category synonyms with the most populous
// matching ZIP areas. When zipCSV is nil the embedded US ZIP dataset is used
// (falling back to the built-in sample areas if the embedded data cannot be
// decoded).
func (s *Service) GenerateProspectQueries(state, city string, topN int, synonyms []string, zipCSV io.Reader) (ProspectQueryPlan, error) {
	if topN <= 0 {
		topN = defaultQueryTopN
	}
	if topN > maximumQueryTopN {
		return ProspectQueryPlan{}, fmt.Errorf("%w: top_n must be between 1 and %d", ErrInvalidProspectConfig, maximumQueryTopN)
	}
	cleaned := make([]string, 0, len(synonyms))
	for _, synonym := range synonyms {
		synonym = strings.TrimSpace(synonym)
		if synonym == "" {
			continue
		}
		if utf8.RuneCountInString(synonym) > maximumQuerySynonymLength {
			return ProspectQueryPlan{}, fmt.Errorf("%w: synonyms must contain at most %d characters", ErrInvalidProspectConfig, maximumQuerySynonymLength)
		}
		cleaned = append(cleaned, synonym)
	}
	if len(cleaned) == 0 {
		return ProspectQueryPlan{}, fmt.Errorf("%w: at least one category synonym is required", ErrInvalidProspectConfig)
	}
	if len(cleaned) > maximumQuerySynonyms {
		return ProspectQueryPlan{}, fmt.Errorf("%w: at most %d synonyms are allowed", ErrInvalidProspectConfig, maximumQuerySynonyms)
	}

	areas, embedErr := prospect.EmbeddedZIPDataset()
	if embedErr != nil || len(areas) == 0 {
		areas = prospect.SampleZIPAreas()
	}
	if zipCSV != nil {
		parsed, err := prospect.ParseZIPCSV(zipCSV)
		if err != nil {
			return ProspectQueryPlan{}, fmt.Errorf("%w: %v", ErrInvalidProspectConfig, err)
		}
		areas = parsed
	}
	selected := prospect.TopZIPs(areas, state, city, topN)
	if len(selected) == 0 {
		return ProspectQueryPlan{}, fmt.Errorf("%w: no ZIP areas match the state and city filters", ErrInvalidProspectConfig)
	}
	targets, err := prospect.BuildTargets(cleaned, selected, true)
	if err != nil {
		return ProspectQueryPlan{}, fmt.Errorf("%w: %v", ErrInvalidProspectConfig, err)
	}
	latitude, longitude := prospect.TargetCentre(targets)
	return ProspectQueryPlan{
		Queries:  prospect.TargetLines(targets),
		Centre:   [2]float64{latitude, longitude},
		ZIPCount: prospect.DistinctTargetZIPs(targets),
		Skipped:  len(areas) - len(selected),
		Targets:  targets,
	}, nil
}
