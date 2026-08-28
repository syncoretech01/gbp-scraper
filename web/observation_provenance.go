package web

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ObservationProvenance is the exact provenance of one Maps observation: the
// task that produced the row, the query it ran, and the cell or ZIP target it
// ran from. It exists because the legacy per-job CSV carries none of this, so
// the importer used to stamp every row with the job's whole keyword list.
type ObservationProvenance struct {
	JobID       string
	IdentityKey string
	TaskKey     string
	SourceQuery string
	SourceCell  string
	ObservedAt  time.Time
}

// ErrObservationProvenanceUnsupported reports a repository without the
// provenance sidecar; callers treat it as "no exact provenance", never as a
// failure of the import.
var ErrObservationProvenanceUnsupported = errors.New("observation provenance is not supported by this repository")

// observationProvenanceRepository is the storage seam for the sidecar.
type observationProvenanceRepository interface {
	RecordObservationProvenance(context.Context, []ObservationProvenance) error
	ObservationProvenanceFor(context.Context, string, []string) ([]ObservationProvenance, error)
}

// RecordTaskObservations records that one task observed the rows identified by
// keys. It is called by the pool right after a task's rows are merged, while
// the task's query and cell are still known; a repository without the sidecar
// simply keeps the old joined-keyword provenance.
func (s *Service) RecordTaskObservations(ctx context.Context, jobID, taskKey, query, cell string, keys []string) error {
	repository, ok := s.repo.(observationProvenanceRepository)
	if !ok {
		return ErrObservationProvenanceUnsupported
	}

	rows := make([]ObservationProvenance, 0, len(keys))
	now := time.Now().UTC()

	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}

		rows = append(rows, ObservationProvenance{
			JobID: jobID, IdentityKey: key, TaskKey: taskKey,
			SourceQuery: strings.TrimSpace(query), SourceCell: strings.TrimSpace(cell),
			ObservedAt: now,
		})
	}

	if len(rows) == 0 {
		return nil
	}

	return repository.RecordObservationProvenance(ctx, rows)
}

// ObservationIdentityKeys returns the identity keys a row is filed under, in
// the same shape the pool records them: place, cid and data ids, lower-cased
// and prefixed by kind. The importer calls it with the record's identities so
// the lookup matches what the merge wrote.
func ObservationIdentityKeys(placeID, cid, dataID string) []string {
	keys := make([]string, 0, 3)
	add := func(kind, value string) {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
			keys = append(keys, kind+":"+value)
		}
	}
	add("place", placeID)
	add("cid", cid)
	add("data", dataID)

	return keys
}
