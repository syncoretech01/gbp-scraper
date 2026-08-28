package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// volatileBusinessFields are the field names a listing realistically changes
// between rescans. IncrementalModeVolatile narrows a rescan's retained set to
// businesses whose stored record moved on one of them.
//
// These are the exact field_name values the workspace writes into
// business_changes, not display labels: the import's version differ uses the
// normalized snapshot's top-level JSON keys (phones, website, address,
// category, review_count, review_rating, status, emails, and structured —
// which carries opening hours and popular times), while the local website
// audit writes website_status and website_final_url. Anything not on this list
// is a change the mode deliberately ignores.
var volatileBusinessFields = []string{
	"phones", "website", "address", "category", "normalized_category",
	"review_rating", "review_count", "status", "emails", "structured",
	"website_status", "website_final_url",
}

// JobCollectionPlan explains, for one job, exactly what the workspace keeps
// out of everything the engine collected. It is the honest bridge between the
// wizard's data-field and filter steps and the stored results: the engine and
// the per-job CSV are never narrowed, only this view of the workspace is.
type JobCollectionPlan struct {
	JobID string `json:"job_id"`
	// Fields is the resolved data-field selection in catalogue order.
	Fields []JobField `json:"fields"`
	// AllFields reports that no narrowing selection was stored, which is the
	// historical behaviour.
	AllFields bool `json:"all_fields"`
	// ExportColumns are the export-builder columns the selection resolves to.
	ExportColumns []string `json:"export_columns"`
	// Filters are the stored post-collection filters, if any.
	Filters *JobResultFilters `json:"filters,omitempty"`
	// IncrementalMode is the stored rescan mode.
	IncrementalMode string `json:"incremental_mode,omitempty"`
	// Search is the exact result query this plan resolves to.
	Search ResultSearch `json:"search"`
	// ResultsURL opens the Results page with the plan already applied.
	ResultsURL string `json:"results_url"`
	// Notices are the honesty statements every surface must repeat.
	Notices []string `json:"notices"`
}

// IncrementalModeLabel renders a stored rescan mode for operators.
func IncrementalModeLabel(mode string) string {
	switch mode {
	case IncrementalModeNewOnly:
		return "Only new listings"
	case IncrementalModeNewChanged:
		return "New and changed listings"
	case IncrementalModeVolatile:
		return "Recheck volatile fields only"
	case IncrementalModeStaleContacts:
		return "Re-enrich missing or stale contact data"
	default:
		return "Full collection"
	}
}

// incrementalModeNotice is the honest explanation of what a rescan mode can
// and cannot do against Google Maps.
func incrementalModeNotice(mode string) string {
	switch mode {
	case IncrementalModeNewOnly:
		return "Maps has no \"only new listings\" query, so the plan still visits every " +
			"cell. New/changed/unchanged is decided when results are imported, and this " +
			"mode keeps the run's view to businesses this job saw first."
	case IncrementalModeNewChanged:
		return "Maps has no \"only changed listings\" query, so the plan still visits every " +
			"cell. This mode keeps the run's view to businesses this job discovered or changed."
	case IncrementalModeVolatile:
		return "Maps has no partial-record fetch, so the full listing is still collected. " +
			"This mode keeps the run's view to businesses whose phone, website, address, " +
			"category, rating, review count, hours, or status actually moved."
	case IncrementalModeStaleContacts:
		return "Collection is unchanged. Only the local website audit is narrowed: a " +
			"business whose audit is newer than the configured staleness window is skipped."
	default:
		return ""
	}
}

// BuildJobCollectionPlan resolves one job's stored definition into the plan.
// It is a pure function of the job so it can be unit-tested and reused by the
// wizard's review step, the API, and the saved view the job creates.
func BuildJobCollectionPlan(jobID string, data JobData) JobCollectionPlan {
	jobID = strings.TrimSpace(jobID)
	plan := JobCollectionPlan{
		JobID:           jobID,
		Fields:          SelectedJobFields(data.Fields),
		AllFields:       len(data.Fields) == 0,
		ExportColumns:   JobFieldExportColumnKeys(data.Fields),
		Filters:         data.ResultFilters.Normalized(),
		IncrementalMode: data.IncrementalMode,
		Notices:         make([]string, 0, 5),
	}

	group := jobCollectionFilterGroup(jobID, data)
	plan.Search = ResultSearch{JobID: jobID, FilterGroup: group, Limit: 50}
	plan.ResultsURL = "/app/results?" + jobCollectionResultsQuery(jobID, data)

	if !plan.AllFields {
		plan.Notices = append(plan.Notices,
			"Field selection controls what this workspace displays and exports. "+
				"The engine still collects every Maps field and the per-job CSV keeps its full schema.")
	}
	if plan.Filters != nil {
		plan.Notices = append(plan.Notices, JobResultFilterNotice)
	}
	// Two of the rules can empty a result view for reasons that have nothing
	// to do with the businesses: a status Maps is not returning, and a review
	// count Fast mode cannot capture. Both are named here so the plan never
	// reads as "your filter found nothing".
	if plan.Filters.StatusFiltered() {
		plan.Notices = append(plan.Notices, JobStatusFilterNotice)
	}
	if plan.Filters.ReviewCountFiltered() {
		plan.Notices = append(plan.Notices, JobReviewFilterNotice)
	}
	if notice := incrementalModeNotice(data.IncrementalMode); notice != "" {
		plan.Notices = append(plan.Notices, notice)
	}

	return plan
}

// jobCollectionFilterGroup combines the post-collection filters with the
// rescan mode into one bounded expression.
func jobCollectionFilterGroup(jobID string, data JobData) *ResultFilterGroup {
	group := ResultFilterGroup{Logic: "and"}
	if filters := data.ResultFilters.FilterGroup(); filters != nil {
		group.Groups = append(group.Groups, *filters)
	}
	if lineage := incrementalLineageGroup(jobID, data.IncrementalMode); lineage != nil {
		group.Groups = append(group.Groups, *lineage)
	}
	if len(group.Groups) == 0 && len(group.Filters) == 0 {
		return nil
	}
	if len(group.Groups) == 1 && len(group.Filters) == 0 {
		single := group.Groups[0]

		return &single
	}

	return &group
}

// incrementalLineageGroup expresses a rescan mode using the discovery-history
// filter vocabulary the repository already supports.
func incrementalLineageGroup(jobID, mode string) *ResultFilterGroup {
	if jobID == "" {
		return nil
	}
	switch mode {
	case IncrementalModeNewOnly:
		return &ResultFilterGroup{Logic: "and", Filters: []ResultFilter{
			{Field: "first_seen_job", Operator: "eq", Value: jobID},
		}}
	case IncrementalModeNewChanged:
		return &ResultFilterGroup{Logic: "or", Filters: []ResultFilter{
			{Field: "first_seen_job", Operator: "eq", Value: jobID},
			{Field: "changed_by_job", Operator: "eq", Value: jobID},
		}}
	case IncrementalModeVolatile:
		volatile := ResultFilterGroup{Logic: "or"}
		for _, field := range volatileBusinessFields {
			volatile.Filters = append(volatile.Filters,
				ResultFilter{Field: "changed_field", Operator: "eq", Value: field})
		}

		return &ResultFilterGroup{Logic: "and", Groups: []ResultFilterGroup{
			{Logic: "and", Filters: []ResultFilter{
				{Field: "changed_by_job", Operator: "eq", Value: jobID},
			}},
			volatile,
		}}
	default:
		return nil
	}
}

// jobCollectionResultsQuery renders the plan as a Results page query string.
// The structured expression is authoritative: it carries both the OR
// alternatives and the rescan lineage, so it replaces any filter_json the
// filter set produced on its own.
func jobCollectionResultsQuery(jobID string, data JobData) string {
	query := data.ResultFilters.ResultsQuery(jobID)
	group := jobCollectionFilterGroup(jobID, data)
	if group == nil {
		return query
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return query
	}
	encoded, err := json.Marshal(group)
	if err != nil {
		return query
	}
	values.Set("filter_json", string(encoded))

	return values.Encode()
}

// JobCollectionPlanFor loads a job and resolves its plan.
func (s *Service) JobCollectionPlanFor(ctx context.Context, jobID string) (JobCollectionPlan, error) {
	job, err := s.Get(ctx, strings.TrimSpace(jobID))
	if err != nil {
		return JobCollectionPlan{}, err
	}

	return BuildJobCollectionPlan(job.ID, job.Data), nil
}

// maximumJobViewNameRunes keeps a generated saved-view name inside the same
// bound the saved-view UI uses for hand-written names.
const maximumJobViewNameRunes = 120

// SaveJobCollectionView stores the job's plan as a saved result view so the
// filters an operator chose in the wizard are reachable from Results and from
// Saved searches without retyping them. A repository without saved-view
// support is not an error: the plan simply stays available through the API.
func (s *Service) SaveJobCollectionView(ctx context.Context, job Job) (SavedResultView, bool, error) {
	plan := BuildJobCollectionPlan(job.ID, job.Data)
	if plan.Search.FilterGroup == nil {
		return SavedResultView{}, false, nil
	}
	repository, ok := s.repo.(reusableRepository)
	if !ok {
		return SavedResultView{}, false, nil
	}

	now := time.Now().UTC()
	view := SavedResultView{
		ID:        uuid.NewString(),
		Name:      boundedJobViewName(job.Name),
		Search:    plan.Search,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := repository.SaveResultView(ctx, view); err != nil {
		return SavedResultView{}, false, fmt.Errorf("save job collection view: %w", err)
	}

	return view, true, nil
}

func boundedJobViewName(jobName string) string {
	const suffix = " — collected set"
	name := strings.TrimSpace(jobName)
	if name == "" {
		name = "Scrape"
	}
	runes := []rune(name)
	if len(runes)+len([]rune(suffix)) > maximumJobViewNameRunes {
		runes = runes[:maximumJobViewNameRunes-len([]rune(suffix))]
		name = strings.TrimSpace(string(runes))
	}

	return name + suffix
}

// SaveJobFieldExportPreset stores the job's data-field selection as a
// repeatable export profile, which is what makes "choose which fields to
// export" do something real. A default (complete) selection stores nothing.
func (s *Service) SaveJobFieldExportPreset(ctx context.Context, job Job) (ExportPreset, bool, error) {
	if len(job.Data.Fields) == 0 {
		return ExportPreset{}, false, nil
	}
	if _, ok := s.repo.(richExportRepository); !ok {
		return ExportPreset{}, false, nil
	}

	plan := BuildJobCollectionPlan(job.ID, job.Data)
	columns := make([]ExportColumnSelection, 0, len(plan.ExportColumns))
	for _, key := range plan.ExportColumns {
		columns = append(columns, ExportColumnSelection{Key: key, Label: key})
	}
	search := plan.Search
	definition, err := exportProfileFromInput(exportProfileAPIInput{
		Name: boundedJobPresetName(job.Name), Format: "csv", Columns: columns, Search: &search,
	})
	if err != nil {
		return ExportPreset{}, false, fmt.Errorf("build job field export preset: %w", err)
	}

	preset, err := s.SaveExportPreset(ctx, definition)
	if err != nil {
		return ExportPreset{}, false, fmt.Errorf("save job field export preset: %w", err)
	}

	return preset, true, nil
}

func boundedJobPresetName(jobName string) string {
	const suffix = " — selected fields"
	name := strings.TrimSpace(jobName)
	if name == "" {
		name = "Scrape"
	}
	runes := []rune(name)
	if len(runes)+len([]rune(suffix)) > maximumJobViewNameRunes {
		runes = runes[:maximumJobViewNameRunes-len([]rune(suffix))]
		name = strings.TrimSpace(string(runes))
	}

	return name + suffix
}
