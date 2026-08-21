package sqlite

import (
	"fmt"
	"strings"
)

// Discovery-history filter fields. They are additive members of the same
// bounded filter language the Results, Map, saved-view and export APIs
// already share, so a saved view can use them without any new plumbing.
const (
	// resultFilterSeenByJob matches businesses one job observed at all.
	resultFilterSeenByJob = "seen_by_job"
	// resultFilterFirstSeenJob matches businesses one job was the FIRST to
	// see: the import that linked them recorded them as new.
	resultFilterFirstSeenJob = "first_seen_job"
	// resultFilterChangedByJob matches businesses whose stored record the
	// job's import changed.
	resultFilterChangedByJob = "changed_by_job"
	// resultFilterSeenByCampaign, resultFilterFirstSeenCampaign and
	// resultFilterChangedByCampaign are the same three questions asked of a
	// whole rescan campaign rather than one run.
	resultFilterSeenByCampaign    = "seen_by_campaign"
	resultFilterFirstSeenCampaign = "first_seen_campaign"
	resultFilterChangedByCampaign = "changed_by_campaign"
	// resultFilterChangedField matches businesses with a recorded change to
	// a named field.
	resultFilterChangedField = "changed_field"
	// resultFilterChangedAt is the timestamp of the most recent recorded
	// field change, usable with every date operator.
	resultFilterChangedAt = "changed_at"
	// resultFilterVersionCount is how many immutable snapshots a business
	// has, usable with every numeric operator.
	resultFilterVersionCount = "version_count"
)

// lineageJobExists is the per-job discovery-history predicate; the extra
// clause narrows it to first-seen or changed observations.
const lineageJobExists = `EXISTS (
	SELECT 1 FROM job_businesses
	WHERE job_businesses.business_id = businesses.id AND job_businesses.job_id = ?%s
)`

// lineageCampaignExists is the same predicate over every generation of one
// rescan campaign.
const lineageCampaignExists = `EXISTS (
	SELECT 1 FROM job_businesses
	JOIN job_campaigns ON job_campaigns.job_id = job_businesses.job_id
	WHERE job_businesses.business_id = businesses.id AND job_campaigns.campaign_id = ?%s
)`

// lineageChangedFieldExists matches a recorded FIELD-LEVEL change. Rows with
// an empty field_name are whole-record status transitions (created,
// reappeared, possibly_removed), which every business gets and which would
// therefore tell an operator nothing.
const lineageChangedFieldExists = `EXISTS (
	SELECT 1 FROM business_changes
	WHERE business_changes.business_id = businesses.id
		AND business_changes.field_name <> ''
		AND business_changes.field_name %s
)`

// lineageLastChangedAt is the most recent recorded field-level change, which
// the shared date-operator builder is applied to. A business whose record was
// only ever created reports NULL, so a "changed since" query excludes it.
const lineageLastChangedAt = `(SELECT MAX(detected_at) FROM business_changes
	WHERE business_changes.business_id = businesses.id AND business_changes.field_name <> '')`

// lineageVersionCount is how many immutable snapshots a business has.
const lineageVersionCount = `(SELECT COUNT(*) FROM business_versions
	WHERE business_versions.business_id = businesses.id)`

// lineageResultFilterSQL builds the additive discovery-history filters that
// let an operator ask for businesses a given run or campaign first saw, or
// changed. handled is false for any field this file does not own, so the
// caller falls through to the historical filter vocabulary unchanged.
//
// Job and campaign identifiers are validated and always bound as parameters;
// no caller-supplied text ever reaches the statement text.
func lineageResultFilterSQL(field, operator, value string) (clause string, args []any, handled bool, err error) {
	switch field {
	case resultFilterSeenByJob, resultFilterFirstSeenJob, resultFilterChangedByJob:
		clause, args, err := lineageIdentityFilterSQL(lineageJobExists, field, operator, value)

		return clause, args, true, err
	case resultFilterSeenByCampaign, resultFilterFirstSeenCampaign, resultFilterChangedByCampaign:
		clause, args, err := lineageIdentityFilterSQL(lineageCampaignExists, field, operator, value)

		return clause, args, true, err
	case resultFilterChangedField:
		clause, args, err := lineageChangedFieldSQL(operator, value)

		return clause, args, true, err
	case resultFilterChangedAt:
		clause, args, err := dateFilterSQL(lineageLastChangedAt, field, operator, value)

		return clause, args, true, err
	case resultFilterVersionCount:
		clause, args, err := numericFilterSQL(lineageVersionCount, field, operator, value)

		return clause, args, true, err
	default:
		return "", nil, false, nil
	}
}

// lineageIdentityFilterSQL applies one job or campaign identifier to the
// given EXISTS template, narrowed by whichever discovery question the field
// asks.
func lineageIdentityFilterSQL(
	template, field, operator, value string,
) (string, []any, error) {
	if !validResultIdentifier(value) {
		return "", nil, fmt.Errorf("result filter %q needs a valid identifier", field)
	}

	narrow := ""

	// "Changed" excludes businesses the same link first discovered, matching
	// the New/Changed/Unchanged split the import summary already reports.
	switch field {
	case resultFilterFirstSeenJob, resultFilterFirstSeenCampaign:
		narrow = " AND job_businesses.is_new = 1"
	case resultFilterChangedByJob, resultFilterChangedByCampaign:
		narrow = " AND job_businesses.is_new = 0 AND job_businesses.is_changed = 1"
	}

	exists := fmt.Sprintf(template, narrow)

	switch operator {
	case "eq", "contains":
		return exists, []any{value}, nil
	case "neq", "not_contains":
		return "NOT " + exists, []any{value}, nil
	default:
		return "", nil, fmt.Errorf("unsupported result operator %q for %q", operator, field)
	}
}

// lineageChangedFieldSQL matches businesses with a recorded change to a named
// field, or with any recorded change at all.
func lineageChangedFieldSQL(operator, value string) (string, []any, error) {
	if operator == "empty" || operator == "not_empty" {
		anyChange := fmt.Sprintf(lineageChangedFieldExists, "IS NOT NULL")
		if operator == "empty" {
			return "NOT " + anyChange, nil, nil
		}

		return anyChange, nil, nil
	}

	if strings.TrimSpace(value) == "" {
		return "", nil, fmt.Errorf("result filter %q needs a field name", resultFilterChangedField)
	}

	switch operator {
	case "eq":
		return fmt.Sprintf(lineageChangedFieldExists, "= ? COLLATE NOCASE"), []any{value}, nil
	case "neq":
		return "NOT " + fmt.Sprintf(lineageChangedFieldExists, "= ? COLLATE NOCASE"), []any{value}, nil
	case "contains":
		return fmt.Sprintf(lineageChangedFieldExists, `LIKE ? ESCAPE '\'`),
			[]any{"%" + escapeLike(value) + "%"}, nil
	case "not_contains":
		return "NOT " + fmt.Sprintf(lineageChangedFieldExists, `LIKE ? ESCAPE '\'`),
			[]any{"%" + escapeLike(value) + "%"}, nil
	default:
		return "", nil, fmt.Errorf("unsupported result operator %q for %q", operator, resultFilterChangedField)
	}
}
