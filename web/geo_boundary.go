package web

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/gosom/google-maps-scraper/grid"
)

// Geographic boundary reporting.
//
// Google Maps does not honour a radius. A grid-cell query is a viewport hint,
// and the platform is free to answer it with businesses far outside that
// viewport — most visibly when the category is sparse and Maps widens the
// search rather than returning a short list. A run therefore routinely
// collects businesses well beyond the radius the operator typed, and those
// businesses are real: deleting them would throw away discovery the run paid
// for.
//
// The honest answer is not to filter during collection but to make the
// geography explicit after it: say how far each business is from the
// configured centre, say whether it fell inside the area the planner actually
// queried, and offer a non-destructive filter for the operator who wants only
// the ones inside. Nothing here ever removes a row.
//
// Distance and zone are derived, never stored. Both are pure functions of the
// business coordinates (already persisted) and the job's own configuration
// (already persisted in jobs.data), so they cannot drift out of step with a
// job whose centre or radius is edited, and they are available retroactively
// for every run already in the workspace.

// earthRadiusMeters is the IUGG mean radius, which is the right constant for
// great-circle distance over mixed latitudes.
const earthRadiusMeters = 6371008.8

// GeoZone names where a collected business sits relative to the search the
// run actually performed.
type GeoZone string

const (
	// GeoZoneInsideRadius is a business within the configured radius of the
	// configured centre: exactly what the operator asked for.
	GeoZoneInsideRadius GeoZone = "inside-radius"
	// GeoZoneInsidePlanned is a business outside the radius but still inside
	// the bounding box the grid was cut from. The planner queries the square
	// that encloses the circle, so its corner cells legitimately reach
	// further out than the radius does.
	GeoZoneInsidePlanned GeoZone = "inside-planned"
	// GeoZoneOutsidePlanned is a business outside the whole planned area.
	// Nothing in the plan pointed at it; Maps returned it anyway.
	GeoZoneOutsidePlanned GeoZone = "outside-planned"
	// GeoZoneUnknown is a business the run stored without usable
	// coordinates, so no boundary claim can be made about it.
	GeoZoneUnknown GeoZone = "unknown"
)

// JobSearchArea is the geography a job was configured to search, reduced to
// the two shapes that can be compared against a coordinate: the radius circle
// the operator typed, and the bounding box the grid planner actually cut
// cells from.
type JobSearchArea struct {
	// HasCentre reports whether the job carries usable centre coordinates.
	HasCentre bool
	Latitude  float64
	Longitude float64
	// RadiusMeters is the configured radius, zero when none was configured.
	RadiusMeters float64
	// HasGrid reports whether the job carries a parsable grid bounding box.
	HasGrid bool
	Bounds  grid.BoundingBox
	// CellKM is the configured grid cell size, zero when none was set.
	CellKM float64
	// Label is the operator's own name for the area, used in prose.
	Label string
}

// NewJobSearchArea derives the comparable search geography from a job
// configuration. A job with neither a centre nor a grid yields a zero value
// whose Available method reports false, and every caller then declines to make
// a geographic claim rather than inventing one.
func NewJobSearchArea(data JobData) JobSearchArea {
	area := JobSearchArea{
		RadiusMeters: float64(data.Radius),
		CellKM:       data.GridCellKM,
		Label:        strings.TrimSpace(data.LocationLabel),
	}

	latitude, latErr := strconv.ParseFloat(strings.TrimSpace(data.Lat), 64)
	longitude, lonErr := strconv.ParseFloat(strings.TrimSpace(data.Lon), 64)
	if latErr == nil && lonErr == nil && validLatitude(latitude) && validLongitude(longitude) {
		area.HasCentre = true
		area.Latitude = latitude
		area.Longitude = longitude
	}

	if trimmed := strings.TrimSpace(data.GridBBox); trimmed != "" {
		if bounds, err := grid.ParseBoundingBox(trimmed); err == nil {
			area.HasGrid = true
			area.Bounds = bounds
		}
	}

	if area.RadiusMeters < 0 {
		area.RadiusMeters = 0
	}

	return area
}

// Available reports whether the area can classify a coordinate at all.
func (area JobSearchArea) Available() bool {
	return area.HasCentre && (area.RadiusMeters > 0 || area.HasGrid)
}

// Classify returns the great-circle distance in metres from the configured
// centre and the zone the coordinate falls in. A coordinate is treated as
// missing when it is not a valid pair or sits at Null Island, which is what
// the scraper writes when a listing carried no position.
func (area JobSearchArea) Classify(latitude, longitude float64) (float64, GeoZone) {
	if !area.HasCentre || !usableCoordinate(latitude, longitude) {
		return 0, GeoZoneUnknown
	}

	distance := haversineMeters(area.Latitude, area.Longitude, latitude, longitude)

	switch {
	case area.RadiusMeters > 0 && distance <= area.RadiusMeters:
		return distance, GeoZoneInsideRadius
	case area.HasGrid && area.withinBounds(latitude, longitude):
		return distance, GeoZoneInsidePlanned
	case !area.HasGrid && area.RadiusMeters <= 0:
		// No boundary was configured at all, so nothing can be called
		// outside one.
		return distance, GeoZoneUnknown
	default:
		return distance, GeoZoneOutsidePlanned
	}
}

func (area JobSearchArea) withinBounds(latitude, longitude float64) bool {
	return latitude >= area.Bounds.MinLat && latitude <= area.Bounds.MaxLat &&
		longitude >= area.Bounds.MinLon && longitude <= area.Bounds.MaxLon
}

// PlannedReachMeters is the furthest a planned query could legitimately point,
// measured from the configured centre: the far corner of the grid bounding
// box, or the radius when the job planned no grid. It is the yardstick that
// separates "the plan reached here" from "Maps volunteered this".
func (area JobSearchArea) PlannedReachMeters() float64 {
	if !area.HasCentre {
		return 0
	}

	reach := area.RadiusMeters
	if !area.HasGrid {
		return reach
	}

	corners := [4][2]float64{
		{area.Bounds.MinLat, area.Bounds.MinLon},
		{area.Bounds.MinLat, area.Bounds.MaxLon},
		{area.Bounds.MaxLat, area.Bounds.MinLon},
		{area.Bounds.MaxLat, area.Bounds.MaxLon},
	}
	for _, corner := range corners {
		if distance := haversineMeters(area.Latitude, area.Longitude, corner[0], corner[1]); distance > reach {
			reach = distance
		}
	}

	return reach
}

// RadiusFilterValue renders the value the stored-results "distance within"
// filter expects: latitude, longitude and radius in kilometres. It is the
// bridge between the job's own configuration and the non-destructive filter
// the operator can apply on the results page, so the two can never disagree
// about where the centre is.
func (area JobSearchArea) RadiusFilterValue() string {
	if !area.HasCentre || area.RadiusMeters <= 0 {
		return ""
	}

	return fmt.Sprintf("%s,%s,%s",
		trimFloat(area.Latitude),
		trimFloat(area.Longitude),
		trimFloat(area.RadiusMeters/1000),
	)
}

// ResultGeography is the distance distribution of one job's collected
// businesses, measured from the job's configured centre.
//
// It is a report, not a gate: every count below describes rows that are kept.
type ResultGeography struct {
	// Available reports whether the job carried enough configuration for the
	// counts to mean anything. When it is false every consumer must say so
	// rather than render zeros.
	Available bool `json:"available"`
	// Measured counts rows with usable coordinates; WithoutCoordinates counts
	// the rest. They sum to the collected rows.
	Measured           int `json:"measured"`
	WithoutCoordinates int `json:"without_coordinates"`
	// InsideRadius, InsidePlanned and OutsidePlanned partition Measured.
	InsideRadius   int `json:"inside_radius"`
	InsidePlanned  int `json:"inside_planned"`
	OutsidePlanned int `json:"outside_planned"`
	// MedianMeters and MaxMeters describe the distance distribution.
	MedianMeters float64 `json:"median_meters"`
	MaxMeters    float64 `json:"max_meters"`
	// FarthestName is the business at MaxMeters, so the operator can sanity
	// check the number against a place they recognise.
	FarthestName string `json:"farthest_name,omitempty"`
	// RadiusMeters and PlannedReachMeters echo the yardsticks used, so the
	// counts stay auditable after a job is edited.
	RadiusMeters       float64 `json:"radius_meters"`
	PlannedReachMeters float64 `json:"planned_reach_meters"`
	// RadiusFilterValue is the ready-made value for the stored-results
	// "distance within" filter; empty when no radius was configured.
	RadiusFilterValue string `json:"radius_filter_value,omitempty"`
	// Spillover measures the same businesses against the cells that actually
	// searched for them rather than against the configured centre. It is what
	// separates a mis-cut grid from the platform widening a query on its own,
	// and it reports itself unavailable when the evidence is not stored.
	Spillover CellSpillover `json:"spillover"`
}

// OutsideRadius counts every measured business beyond the configured radius,
// whichever side of the planned grid it fell on.
func (geography ResultGeography) OutsideRadius() int {
	return geography.InsidePlanned + geography.OutsidePlanned
}

// OutsideRadiusPercent is the share of measured businesses beyond the radius,
// rounded to one decimal.
func (geography ResultGeography) OutsideRadiusPercent() float64 {
	return sharePercent(geography.OutsideRadius(), geography.Measured)
}

// OutsidePlannedPercent is the share of measured businesses that no planned
// query pointed at. It is the spillover measurement.
func (geography ResultGeography) OutsidePlannedPercent() float64 {
	return sharePercent(geography.OutsidePlanned, geography.Measured)
}

// geographyAccumulator collects distances one row at a time so a CSV can be
// summarized in a single streaming pass.
type geographyAccumulator struct {
	area      JobSearchArea
	distances []float64
	geography ResultGeography
}

func newGeographyAccumulator(area JobSearchArea) *geographyAccumulator {
	return &geographyAccumulator{area: area}
}

// observe records one row. Rows without usable coordinates are counted, never
// dropped: a run that stored positions for only half its businesses must say
// so instead of quietly reporting on the half it can measure.
func (accumulator *geographyAccumulator) observe(name string, latitude, longitude float64, hasCoordinates bool) {
	if !hasCoordinates {
		accumulator.geography.WithoutCoordinates++

		return
	}

	distance, zone := accumulator.area.Classify(latitude, longitude)
	if zone == GeoZoneUnknown {
		accumulator.geography.WithoutCoordinates++

		return
	}

	accumulator.geography.Measured++
	accumulator.distances = append(accumulator.distances, distance)

	switch zone {
	case GeoZoneInsideRadius:
		accumulator.geography.InsideRadius++
	case GeoZoneInsidePlanned:
		accumulator.geography.InsidePlanned++
	case GeoZoneOutsidePlanned:
		accumulator.geography.OutsidePlanned++
	case GeoZoneUnknown:
	}

	if distance > accumulator.geography.MaxMeters {
		accumulator.geography.MaxMeters = distance
		accumulator.geography.FarthestName = strings.TrimSpace(name)
	}
}

// result finalises the distribution.
func (accumulator *geographyAccumulator) result() ResultGeography {
	geography := accumulator.geography
	if !accumulator.area.Available() {
		return ResultGeography{}
	}

	geography.Available = geography.Measured > 0 || geography.WithoutCoordinates > 0
	geography.RadiusMeters = accumulator.area.RadiusMeters
	geography.PlannedReachMeters = accumulator.area.PlannedReachMeters()
	geography.RadiusFilterValue = accumulator.area.RadiusFilterValue()
	geography.MedianMeters = medianOf(accumulator.distances)

	return geography
}

func medianOf(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}

	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)

	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}

	return (sorted[middle-1] + sorted[middle]) / 2
}

func sharePercent(part, whole int) float64 {
	if whole <= 0 {
		return 0
	}

	return math.Round(float64(part)/float64(whole)*1000) / 10
}

// haversineMeters is the great-circle distance between two coordinates.
func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	phi1 := lat1 * math.Pi / 180
	phi2 := lat2 * math.Pi / 180
	deltaPhi := (lat2 - lat1) * math.Pi / 180
	deltaLambda := (lon2 - lon1) * math.Pi / 180

	a := math.Sin(deltaPhi/2)*math.Sin(deltaPhi/2) +
		math.Cos(phi1)*math.Cos(phi2)*math.Sin(deltaLambda/2)*math.Sin(deltaLambda/2)

	return 2 * earthRadiusMeters * math.Asin(math.Sqrt(math.Min(1, a)))
}

func usableCoordinate(latitude, longitude float64) bool {
	if !validLatitude(latitude) || !validLongitude(longitude) {
		return false
	}

	// Null Island is what the extractor writes for a listing that carried no
	// position at all, so it is absence rather than a location.
	return latitude != 0 || longitude != 0
}

func validLatitude(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= -90 && value <= 90
}

func validLongitude(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= -180 && value <= 180
}

// trimFloat renders a coordinate without trailing zeroes so the generated
// filter value reads like the one an operator would type.
func trimFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// humanDistance renders a distance for an operator: metres below a kilometre,
// then kilometres with one decimal.
func humanDistance(meters float64) string {
	if meters < 1000 {
		return fmt.Sprintf("%.0f m", meters)
	}

	return fmt.Sprintf("%.1f km", meters/1000)
}
