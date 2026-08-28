package web

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
)

// ErrJobGeographyUnsupported indicates that the active repository cannot join
// a collected business back to the search that first returned it.
var ErrJobGeographyUnsupported = errors.New("job geography evidence is unavailable")

// JobCellObservation is one collected business paired with the grid cell whose
// search first returned it.
//
// The pairing is durable evidence, not inference: the importer records the
// task key that produced each source row (business_sources.input_id) and the
// task plan records the cell that key was searched at (job_tasks.source_cell).
// Joining them through job_businesses.first_source_id yields exactly one row
// per business, which is what keeps this measurement immune to the source
// fan-out that inflates other per-job joins.
type JobCellObservation struct {
	// Cell is the searched cell's centre, "latitude,longitude".
	Cell string
	// Latitude and Longitude are the business's own position.
	Latitude  float64
	Longitude float64
}

// jobGeographyRepository is the optional capability behind that join.
type jobGeographyRepository interface {
	JobCellObservations(ctx context.Context, jobID string) ([]JobCellObservation, error)
}

func (s *Service) jobGeographyRepository() (jobGeographyRepository, error) {
	if s.repo == nil {
		return nil, ErrJobGeographyUnsupported
	}

	repository, ok := s.repo.(jobGeographyRepository)
	if !ok {
		return nil, ErrJobGeographyUnsupported
	}

	return repository, nil
}

// CellSpillover measures how far Google's answers landed from the cells that
// asked for them.
//
// This is the measurement that settles whether businesses outside a job's
// radius are a planner defect or the platform's own behaviour. The planner's
// reach is knowable from the configuration alone; how far past its own cell a
// result landed is knowable only from this join, and it is the difference
// between "the grid was cut wrong" and "Maps widened the search".
type CellSpillover struct {
	// Available reports whether the join produced anything to measure.
	Available bool `json:"available"`
	// Measured is how many businesses were paired with a searched cell, and
	// Cells is how many distinct cells contributed them.
	Measured int `json:"measured"`
	Cells    int `json:"cells"`
	// WithinOwnCell counts businesses inside the half-diagonal of the cell
	// that found them, which is the furthest a correctly answered cell query
	// can legitimately reach. Beyond counts the rest.
	WithinOwnCell int `json:"within_own_cell"`
	Beyond        int `json:"beyond"`
	// CellReachMeters is that half-diagonal: the yardstick used above.
	CellReachMeters float64 `json:"cell_reach_meters"`
	// MedianMeters and MaxMeters describe how far results landed from the
	// cell that asked for them.
	MedianMeters float64 `json:"median_meters"`
	MaxMeters    float64 `json:"max_meters"`
}

// BeyondPercent is the share of measured businesses Maps returned from outside
// the cell that queried for them.
func (spillover CellSpillover) BeyondPercent() float64 {
	return sharePercent(spillover.Beyond, spillover.Measured)
}

// JobCellSpillover measures one job's results against the cells that found
// them. A repository without the join, or a job with no grid, reports an
// unavailable value rather than an invented one.
func (s *Service) JobCellSpillover(ctx context.Context, jobID string, cellKM float64) (CellSpillover, error) {
	repository, err := s.jobGeographyRepository()
	if err != nil {
		return CellSpillover{}, err
	}

	observations, err := repository.JobCellObservations(ctx, jobID)
	if err != nil {
		return CellSpillover{}, err
	}

	return summarizeCellSpillover(observations, cellKM), nil
}

// cellHalfDiagonalMeters is the furthest point of a square cell from its own
// centre. A business further away than this was never inside the area the cell
// covered, whatever the platform decided to answer with.
func cellHalfDiagonalMeters(cellKM float64) float64 {
	if cellKM <= 0 {
		return 0
	}

	// (cellKM / 2) * sqrt(2), in metres.
	return cellKM * 1000 * math.Sqrt2 / 2
}

func summarizeCellSpillover(observations []JobCellObservation, cellKM float64) CellSpillover {
	reach := cellHalfDiagonalMeters(cellKM)
	spillover := CellSpillover{CellReachMeters: reach}
	distances := make([]float64, 0, len(observations))
	cells := map[string]struct{}{}

	for _, observation := range observations {
		latitude, longitude, ok := parseCellCentre(observation.Cell)
		if !ok || !usableCoordinate(observation.Latitude, observation.Longitude) {
			continue
		}

		cells[observation.Cell] = struct{}{}
		distance := haversineMeters(latitude, longitude, observation.Latitude, observation.Longitude)
		distances = append(distances, distance)
		spillover.Measured++

		switch {
		case reach <= 0:
			// Without a cell size there is no yardstick, so no claim is made
			// about which side of it a business fell on.
		case distance <= reach:
			spillover.WithinOwnCell++
		default:
			spillover.Beyond++
		}

		if distance > spillover.MaxMeters {
			spillover.MaxMeters = distance
		}
	}

	if spillover.Measured == 0 {
		return CellSpillover{}
	}

	sort.Float64s(distances)
	spillover.Available = true
	spillover.Cells = len(cells)
	spillover.MedianMeters = medianOf(distances)

	return spillover
}

// parseCellCentre reads the "latitude,longitude" the task plan stores.
func parseCellCentre(cell string) (float64, float64, bool) {
	latitudeText, longitudeText, found := strings.Cut(strings.TrimSpace(cell), ",")
	if !found {
		return 0, 0, false
	}

	latitude, latErr := strconv.ParseFloat(strings.TrimSpace(latitudeText), 64)
	longitude, lonErr := strconv.ParseFloat(strings.TrimSpace(longitudeText), 64)
	if latErr != nil || lonErr != nil {
		return 0, 0, false
	}

	if !validLatitude(latitude) || !validLongitude(longitude) {
		return 0, 0, false
	}

	return latitude, longitude, true
}
