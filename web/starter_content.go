package web

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// starterContentSettingKey marks a workspace whose starter templates and
// saved result views have already been offered. The value is a content
// version, not a boolean, so a future content revision can seed additively
// without re-creating what the user has deleted.
const (
	starterContentSettingKey = "seed.starter_content"
	starterContentVersion    = "v1"
)

// SeedStarterContent populates a fresh local workspace with example scrape
// templates (specification section 18) and reusable result views
// (specification section 09). It runs at most once per workspace: the
// settings key "seed.starter_content" is persisted after the first pass so
// deleted starter content is never resurrected. Templates are only created
// when the user has no templates at all, and views only when the user has no
// saved views, so a workspace with any user content is left untouched. The
// returned count is the number of items actually created.
func (s *Service) SeedStarterContent(ctx context.Context) (int, error) {
	settings, err := s.LoadSettings(ctx)
	if err != nil {
		if errors.Is(err, ErrSettingsUnsupported) {
			// Without durable settings the "ran once" flag cannot be stored,
			// so seeding would repeat forever. Skip quietly.
			return 0, nil
		}

		return 0, fmt.Errorf("load starter content flag: %w", err)
	}

	if settings[starterContentSettingKey] != "" {
		return 0, nil
	}

	if _, err := s.reusableRepository(); err != nil {
		// The repository cannot store templates or views yet. Leave the flag
		// unset so a later, capable repository still receives the content.
		return 0, nil
	}

	seeded := 0

	templatesSeeded, err := s.seedStarterTemplates(ctx)
	if err != nil {
		return seeded, err
	}

	seeded += templatesSeeded

	viewsSeeded, err := s.seedStarterViews(ctx)
	if err != nil {
		return seeded, err
	}

	seeded += viewsSeeded

	if err := s.SaveSettings(ctx, map[string]string{
		starterContentSettingKey: starterContentVersion,
	}); err != nil {
		return seeded, fmt.Errorf("record starter content seed: %w", err)
	}

	return seeded, nil
}

// seedStarterTemplates creates the specification's starter scrape templates,
// but only into a workspace that has no templates at all. Each candidate is
// validated with the same JobData validation the template import endpoint
// uses; a candidate the validator rejects is skipped rather than forced.
func (s *Service) seedStarterTemplates(ctx context.Context) (int, error) {
	existing, err := s.ListScrapeTemplates(ctx, "")
	if err != nil {
		return 0, err
	}

	if len(existing) > 0 {
		return 0, nil
	}

	seeded := 0

	for _, template := range starterScrapeTemplates(time.Now().UTC()) {
		if err := template.Configuration.Validate(); err != nil {
			continue
		}

		if len(template.Configuration.Proxies) > 0 {
			// Mirrors the import rule: templates never carry inline proxy
			// credentials. Starter templates must not either.
			continue
		}

		if err := s.SaveScrapeTemplate(ctx, template); err != nil {
			return seeded, fmt.Errorf("save starter template %q: %w", template.Name, err)
		}

		seeded++
	}

	return seeded, nil
}

// seedStarterViews creates the specification's example reusable result views,
// but only into a workspace that has no saved views at all. Every candidate
// search is executed once against the repository's bounded query language;
// a search the validator rejects is skipped rather than forced.
const prospectStarterSettingKey = "seed.starter_content.gbp"

// SeedProspectStarterViews seeds the GBP prospecting saved views once. It is
// keyed independently of the original starter content so workspaces that were
// seeded before the prospecting layer existed still receive these views.
func (s *Service) SeedProspectStarterViews(ctx context.Context) (int, error) {
	settings, err := s.LoadSettings(ctx)
	if err != nil {
		if errors.Is(err, ErrSettingsUnsupported) {
			return 0, nil
		}

		return 0, fmt.Errorf("load prospect starter flag: %w", err)
	}

	if settings[prospectStarterSettingKey] != "" {
		return 0, nil
	}

	if _, err := s.reusableRepository(); err != nil {
		return 0, nil
	}

	seeded := 0

	for _, view := range prospectStarterViews(time.Now().UTC()) {
		if err := s.validateStarterViewSearch(ctx, view.Search); err != nil {
			continue
		}

		if err := s.SaveResultView(ctx, view); err != nil {
			return seeded, fmt.Errorf("save prospect starter view %q: %w", view.Name, err)
		}

		seeded++
	}

	if err := s.SaveSettings(ctx, map[string]string{prospectStarterSettingKey: "v1"}); err != nil {
		return seeded, fmt.Errorf("store prospect starter flag: %w", err)
	}

	return seeded, nil
}

// prospectStarterViews are the GBP call-sheet entry points: each one is a
// worth-calling slice of the prospect taxonomy.
func prospectStarterViews(now time.Time) []SavedResultView {
	view := func(name string, search ResultSearch) SavedResultView {
		search.Limit = 25
		search.Sort = "updated_desc"

		return SavedResultView{
			ID: uuid.NewString(), Name: name, Search: search,
			CreatedAt: now, UpdatedAt: now,
		}
	}

	return []SavedResultView{
		view("GBP: no website", ResultSearch{Filters: []ResultFilter{
			{Field: "prospect_status", Operator: "eq", Value: "NO_WEBSITE"},
		}}),
		view("GBP: dead or broken sites", ResultSearch{FilterGroup: &ResultFilterGroup{
			Logic: "or",
			Filters: []ResultFilter{
				{Field: "prospect_status", Operator: "eq", Value: "DEAD"},
				{Field: "prospect_status", Operator: "eq", Value: "SSL_BROKEN"},
				{Field: "prospect_status", Operator: "eq", Value: "PARKED"},
			},
		}}),
		view("GBP: social profile only", ResultSearch{Filters: []ResultFilter{
			{Field: "prospect_status", Operator: "eq", Value: "SOCIAL_ONLY"},
		}}),
		view("GBP: free site builders", ResultSearch{Filters: []ResultFilter{
			{Field: "prospect_status", Operator: "eq", Value: "FREE_BUILDER"},
		}}),
		view("GBP: tier A prospects", ResultSearch{Filters: []ResultFilter{
			{Field: "prospect_tier", Operator: "eq", Value: "A"},
		}}),
	}
}

func (s *Service) seedStarterViews(ctx context.Context) (int, error) {
	existing, err := s.ListSavedResultViews(ctx, "")
	if err != nil {
		return 0, err
	}

	if len(existing) > 0 {
		return 0, nil
	}

	seeded := 0

	for _, view := range starterResultViews(time.Now().UTC()) {
		if err := s.validateStarterViewSearch(ctx, view.Search); err != nil {
			continue
		}

		if err := s.SaveResultView(ctx, view); err != nil {
			return seeded, fmt.Errorf("save starter view %q: %w", view.Name, err)
		}

		seeded++
	}

	return seeded, nil
}

// validateStarterViewSearch runs a candidate search through the repository's
// own filter validation by executing it for a single row. The repository is
// the authority on filterable fields and operators, so a view is stored only
// when the exact search it saves would also load.
func (s *Service) validateStarterViewSearch(ctx context.Context, search ResultSearch) error {
	probe := search
	probe.Limit = 1
	probe.Offset = 0

	if _, err := s.SearchBusinesses(ctx, probe); err != nil && !errors.Is(err, ErrResultStoreUnsupported) {
		return err
	}

	return nil
}

// starterJobConfiguration mirrors the New Scrape wizard's San Francisco
// defaults: the balanced performance preset around the SF city centre.
func starterJobConfiguration(keywords ...string) JobData {
	return JobData{
		Keywords:      keywords,
		Lang:          "en",
		Zoom:          14,
		Lat:           "37.7749",
		Lon:           "-122.4194",
		LocationLabel: "San Francisco, California, United States",
		Depth:         10,
		MaxTime:       60 * time.Minute,
		Concurrency:   4,
		TaskWorkers:   4,
		BrowserPool:   2,
		PagesBrowser:  2,
		RetryCount:    3,
		RetryDelay:    2 * time.Second,
		PageTimeout:   45 * time.Second,
		Adaptive:      true,
		Proxies:       []string{},
	}
}

// starterScrapeTemplates returns the specification section 18 starter
// templates. Every description states that the template is an editable,
// deletable starter so users know nothing depends on it.
func starterScrapeTemplates(now time.Time) []ScrapeTemplate {
	starter := func(name, description string, configure func(*JobData), keywords ...string) ScrapeTemplate {
		configuration := starterJobConfiguration(keywords...)
		if configure != nil {
			configure(&configuration)
		}

		return ScrapeTemplate{
			ID:            uuid.NewString(),
			Name:          name,
			Description:   description,
			Configuration: configuration,
			Tags:          []string{"starter"},
			Folder:        "Starter templates",
			CreatedAt:     now,
			UpdatedAt:     now,
		}
	}

	return []ScrapeTemplate{
		starter(
			"Businesses without websites — audit pass",
			"Starter template — edit or delete it freely. A balanced first pass with email "+
				"extraction off: collect listings quickly, then open Results and apply the "+
				"\"Businesses without websites\" saved view to shortlist follow-up prospects "+
				"that have no site to crawl anyway.",
			func(data *JobData) { data.Email = false },
			"plumbers in San Francisco", "electricians in San Francisco",
		),
		starter(
			"High-rated businesses",
			"Starter template — edit or delete it freely. Uses the deep preset (depth 20, low "+
				"concurrency, 2h runtime) to reach further down each search. Pair it with the "+
				"\"Highly rated (4.5+)\" saved view to keep only strongly reviewed listings.",
			func(data *JobData) {
				data.Depth = 20
				data.Concurrency = 2
				data.PagesBrowser = 1
				data.MaxTime = 120 * time.Minute
			},
			"restaurants in San Francisco",
		),
		starter(
			"Website audit prospects",
			"Starter template — edit or delete it freely. Enables email extraction and the "+
				"local website/email/social enrichment audit so every listed site is checked "+
				"for reachability, HTTPS, and contact data you can review in the record drawer.",
			func(data *JobData) {
				data.Email = true
				data.Enrichment = &JobEnrichmentOptions{
					Website:        true,
					Emails:         true,
					SocialProfiles: true,
					CheckMX:        true,
				}
			},
			"dentists in San Francisco", "law firms in San Francisco",
		),
		starter(
			"Closed-business monitor",
			"Starter template — edit or delete it freely. A shallow, quick re-check (depth 3, "+
				"30m) meant to be re-run on a schedule; compare change status between runs and "+
				"use the \"Permanently closed listings\" saved view to spot closures.",
			func(data *JobData) {
				data.Depth = 3
				data.MaxTime = 30 * time.Minute
			},
			"restaurants in San Francisco",
		),
		starter(
			"New local businesses",
			"Starter template — edit or delete it freely. Fast Mode with a strict 10 km radius "+
				"and the fast preset (depth 3, 15m) for a rapid snapshot of what is currently "+
				"listed; re-run it to catch newly appearing businesses.",
			func(data *JobData) {
				data.FastMode = true
				data.Radius = 10000
				data.Depth = 3
				data.Concurrency = 6
				data.PagesBrowser = 3
				data.MaxTime = 15 * time.Minute
			},
			"coffee shops in San Francisco",
		),
	}
}

// starterResultViews returns the specification section 09 example reusable
// views. Only fields and operators the repository's bounded query language
// supports are used; seedStarterViews still validates each one by executing
// it before saving.
func starterResultViews(now time.Time) []SavedResultView {
	view := func(name string, search ResultSearch) SavedResultView {
		search.Limit = 25

		return SavedResultView{
			ID:        uuid.NewString(),
			Name:      name,
			Search:    search,
			CreatedAt: now,
			UpdatedAt: now,
		}
	}

	return []SavedResultView{
		view("Businesses without websites", ResultSearch{
			Filters: []ResultFilter{
				{Field: "website", Operator: "empty"},
			},
		}),
		view("Active website, no email", ResultSearch{
			Filters: []ResultFilter{
				{Field: "website_status", Operator: "eq", Value: "active"},
				{Field: "email", Operator: "empty"},
			},
		}),
		view("Has phone, no website", ResultSearch{
			Filters: []ResultFilter{
				{Field: "phone", Operator: "not_empty"},
				{Field: "website", Operator: "empty"},
			},
		}),
		view("Highly rated (4.5+)", ResultSearch{
			Filters: []ResultFilter{
				{Field: "rating", Operator: "gte", Value: "4.5"},
			},
		}),
		view("50+ reviews, open", ResultSearch{
			Filters: []ResultFilter{
				{Field: "review_count", Operator: "gte", Value: "50"},
			},
			// Imported business statuses are normalized words such as
			// "operational" or "open 24 hours", so open listings are matched
			// by either marker while flat filters stay ANDed with the count.
			FilterGroup: &ResultFilterGroup{
				Logic: "or",
				Filters: []ResultFilter{
					{Field: "business_status", Operator: "contains", Value: "open"},
					{Field: "business_status", Operator: "contains", Value: "operational"},
				},
			},
		}),
		view("Permanently closed listings", ResultSearch{
			Filters: []ResultFilter{
				{Field: "business_status", Operator: "contains", Value: "permanently closed"},
			},
		}),
	}
}
