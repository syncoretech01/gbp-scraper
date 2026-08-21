package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// ErrInvalidCampaignTemplate identifies a rejected campaign-template
// capture or instantiation.
var ErrInvalidCampaignTemplate = errors.New("invalid campaign template")

// CampaignTemplateShape summarises what a saved template actually captures,
// so a caller can tell a bare keyword list apart from a full GBP campaign
// without decoding the whole configuration.
type CampaignTemplateShape struct {
	Queries int `json:"queries"`
	// ZIPQueries counts the GBP-shaped "<synonym> in <city> <ST> <zip5>"
	// queries, and ZIPs how many distinct ZIP cells they cover.
	ZIPQueries int `json:"zip_queries"`
	ZIPs       int `json:"zips"`
	// Coverage, Enrichment and Incremental report whether the template
	// carries adaptive-discovery options, website enrichment, and a rescan
	// collection mode.
	Coverage    bool   `json:"coverage"`
	Enrichment  bool   `json:"enrichment"`
	Incremental string `json:"incremental_mode,omitempty"`
	FastMode    bool   `json:"fast_mode"`
	// GridCells is set when the template covers an area by grid.
	GridBBox   string  `json:"grid_bbox,omitempty"`
	GridCellKM float64 `json:"grid_cell_km,omitempty"`
}

// CampaignTemplateShapeOf derives the shape summary of one configuration.
func CampaignTemplateShapeOf(data JobData) CampaignTemplateShape {
	shape := CampaignTemplateShape{
		Queries:     len(data.Keywords),
		Coverage:    data.Coverage != nil,
		Enrichment:  data.Enrichment != nil,
		Incremental: data.IncrementalMode,
		FastMode:    data.FastMode,
		GridBBox:    data.GridBBox,
		GridCellKM:  data.GridCellKM,
	}

	zips := make(map[string]struct{}, len(data.Keywords))

	for _, keyword := range data.Keywords {
		zip, ok := ParseGBPQueryZIP(keyword)
		if !ok {
			continue
		}

		shape.ZIPQueries++
		zips[zip] = struct{}{}
	}

	shape.ZIPs = len(zips)

	return shape
}

// CampaignTemplateResult is what a capture or instantiation produced.
type CampaignTemplateResult struct {
	TemplateID string                `json:"template_id"`
	Name       string                `json:"name"`
	Shape      CampaignTemplateShape `json:"shape"`
	// JobID and State are set by an instantiation only.
	JobID string `json:"job_id,omitempty"`
	State string `json:"state,omitempty"`
}

// CaptureCampaignTemplate saves an existing job's complete campaign shape —
// its queries, ZIP coverage settings, enrichment and adaptive-discovery
// options — as a reusable template.
//
// Proxy credentials are never captured: a template is an exportable,
// shareable document, and the pool reference is enough to rebind a run to
// the local pool it should use.
func (s *Service) CaptureCampaignTemplate(
	ctx context.Context,
	jobID, name, description string,
) (CampaignTemplateResult, error) {
	repository, err := s.reusableRepository()
	if err != nil {
		return CampaignTemplateResult{}, err
	}

	job, err := s.Get(ctx, strings.TrimSpace(jobID))
	if err != nil {
		return CampaignTemplateResult{}, err
	}

	if job.ID == "" {
		return CampaignTemplateResult{}, ErrNotFound
	}

	name, err = validCampaignTemplateName(name, job.Name)
	if err != nil {
		return CampaignTemplateResult{}, err
	}

	configuration := job.Data
	configuration.Proxies = nil

	if err := configuration.Validate(); err != nil {
		return CampaignTemplateResult{}, fmt.Errorf("%w: %s", ErrInvalidCampaignTemplate, err)
	}

	now := time.Now().UTC()
	template := ScrapeTemplate{
		ID:            uuid.NewString(),
		Name:          name,
		Description:   strings.TrimSpace(description),
		Configuration: configuration,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if len(template.Description) > MaximumTemplateDescriptionLength {
		return CampaignTemplateResult{}, fmt.Errorf("%w: description must be at most %d characters",
			ErrInvalidCampaignTemplate, MaximumTemplateDescriptionLength)
	}

	if err := repository.SaveScrapeTemplate(ctx, template); err != nil {
		return CampaignTemplateResult{}, err
	}

	return CampaignTemplateResult{
		TemplateID: template.ID,
		Name:       template.Name,
		Shape:      CampaignTemplateShapeOf(configuration),
	}, nil
}

// MaximumTemplateDescriptionLength bounds a captured template's description.
const MaximumTemplateDescriptionLength = 500

// InstantiateCampaignTemplate creates a NEW job from a saved template.
//
// The template's stored configuration is used verbatim apart from the two
// things a run owns rather than a template: the job name and the rescan
// collection mode. Instantiating never mutates the template beyond bumping
// its usage counters, so the same template can seed any number of runs.
func (s *Service) InstantiateCampaignTemplate(
	ctx context.Context,
	templateID, name, mode string,
	draft bool,
) (CampaignTemplateResult, error) {
	repository, err := s.reusableRepository()
	if err != nil {
		return CampaignTemplateResult{}, err
	}

	template, err := repository.GetScrapeTemplate(ctx, strings.TrimSpace(templateID))
	if err != nil {
		return CampaignTemplateResult{}, err
	}

	configuration := template.Configuration
	configuration.Proxies = nil

	if mode = strings.TrimSpace(mode); mode != "" {
		incremental, modeErr := incrementalModeForRerun(mode)
		if modeErr != nil {
			return CampaignTemplateResult{}, fmt.Errorf("%w: %s", ErrInvalidCampaignTemplate, modeErr)
		}

		configuration.IncrementalMode = incremental
	}

	jobName, err := validCampaignTemplateName(name, template.Name)
	if err != nil {
		return CampaignTemplateResult{}, err
	}

	job := Job{
		ID:     uuid.NewString(),
		Name:   jobName,
		Date:   time.Now().UTC(),
		Status: StatusPending,
		Data:   configuration,
	}

	if err := job.Validate(); err != nil {
		return CampaignTemplateResult{}, fmt.Errorf("%w: %s", ErrInvalidCampaignTemplate, err)
	}

	state := jobruntime.StateQueued
	if draft {
		state = jobruntime.StateDraft
	}

	if err := s.CreateWithState(ctx, &job, state); err != nil {
		return CampaignTemplateResult{}, err
	}

	// Usage statistics are best-effort: a run that was created must never be
	// reported as failed because a counter could not be bumped.
	_ = repository.RecordScrapeTemplateUse(ctx, template.ID, job.Date)

	return CampaignTemplateResult{
		TemplateID: template.ID,
		Name:       job.Name,
		Shape:      CampaignTemplateShapeOf(configuration),
		JobID:      job.ID,
		State:      string(state),
	}, nil
}

// validCampaignTemplateName trims an override, falls back to the source
// name, and bounds the result the same way the wizard does.
func validCampaignTemplateName(override, fallback string) (string, error) {
	name := strings.TrimSpace(override)
	if name == "" {
		name = strings.TrimSpace(fallback)
	}

	if name == "" || len(name) > MaximumTemplateNameLength {
		return "", fmt.Errorf("%w: name must be 1 to %d characters",
			ErrInvalidCampaignTemplate, MaximumTemplateNameLength)
	}

	for _, character := range name {
		if character < 0x20 || character == 0x7f {
			return "", fmt.Errorf("%w: name contains control characters", ErrInvalidCampaignTemplate)
		}
	}

	return name, nil
}
