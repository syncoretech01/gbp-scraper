package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	maximumMapGeoJSONBytes   = 1 << 20
	maximumMapCoordinates    = 10_000
	maximumMapRingPoints     = 2_000
	maximumMapPolygons       = 128
	maximumMapRings          = 256
	maximumMapImportFeatures = 100
	maximumSavedAreaList     = 100
	maximumMapGridCells      = 2_500
	maximumMapGridCandidates = 100_000
	maximumSpatialScanRows   = 1_000_000
	minimumMapCellKM         = 0.05
	maximumMapCellKM         = 100.0
	minimumCircleRadiusM     = 1.0
	maximumCircleRadiusM     = 500_000.0
	mapKilometresPerDegree   = 111.32
	mapEarthRadiusM          = 6_371_008.8
	mapCoordinateEpsilon     = 1e-10
	maximumMapTileBytes      = 2 << 20
	maximumMapTileZoom       = 19
	maximumMapExportRows     = 100_000
	mapTileCacheMaxAge       = 7 * 24 * time.Hour
)

var (
	// ErrMapStoreUnsupported indicates that saved-area persistence is not
	// available for the configured repository.
	ErrMapStoreUnsupported = errors.New("map storage is unavailable")
	// ErrSavedAreaNotFound indicates that a saved geographic area is unknown.
	ErrSavedAreaNotFound = errors.New("saved map area not found")
	// ErrSavedAreaConflict indicates that a saved area identifier already exists.
	ErrSavedAreaConflict = errors.New("saved map area already exists")
	// ErrInvalidMapGeometry identifies malformed, unsafe, or unsupported GeoJSON.
	ErrInvalidMapGeometry = errors.New("invalid map geometry")
	// ErrMapGridTooLarge identifies a preview that exceeds its bounded cell limit.
	ErrMapGridTooLarge = errors.New("map grid preview is too large")
	// ErrMapSpatialQueryTooLarge identifies a spatial scan beyond the local API bound.
	ErrMapSpatialQueryTooLarge = errors.New("spatial result query is too large")
	// ErrMapCoverageUnsupported indicates that durable task coverage is not
	// available from the configured repository.
	ErrMapCoverageUnsupported = errors.New("map coverage storage is unavailable")
	// ErrMapCellScrapeSelection identifies a selected-cell scrape request that
	// does not match the current deterministic grid or retry eligibility rules.
	ErrMapCellScrapeSelection = errors.New("invalid map cell scrape selection")
)

// MapPoint is one geographic coordinate in latitude/longitude order.
type MapPoint struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// MapBounds is an inclusive geographic rectangle.
type MapBounds struct {
	MinLatitude  float64 `json:"min_latitude"`
	MinLongitude float64 `json:"min_longitude"`
	MaxLatitude  float64 `json:"max_latitude"`
	MaxLongitude float64 `json:"max_longitude"`
}

// SavedArea is one validated, canonical GeoJSON definition persisted locally.
type SavedArea struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	GeoJSON   json.RawMessage `json:"geojson"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type mapPolygon struct {
	Rings [][]MapPoint
}

type mapCircle struct {
	Centre  MapPoint
	RadiusM float64
}

// MapGeometry is a parsed and validated local search area. Its internals are
// immutable outside this package; callers can use the bounded query helpers.
type MapGeometry struct {
	kind       string
	polygons   []mapPolygon
	circle     *mapCircle
	bounds     MapBounds
	canonical  json.RawMessage
	contentKey string
}

// Kind reports polygon, multipolygon, circle, or bbox.
func (geometry MapGeometry) Kind() string {
	return geometry.kind
}

// Bounds reports the validated extent of the geometry.
func (geometry MapGeometry) Bounds() MapBounds {
	return geometry.bounds
}

// Centre returns the circle centre or the midpoint of the geometry bounds.
// It is intended for map defaults and never replaces the full saved geometry.
func (geometry MapGeometry) Centre() MapPoint {
	if geometry.circle != nil {
		return geometry.circle.Centre
	}

	return MapPoint{
		Latitude:  (geometry.bounds.MinLatitude + geometry.bounds.MaxLatitude) / 2,
		Longitude: (geometry.bounds.MinLongitude + geometry.bounds.MaxLongitude) / 2,
	}
}

// CircleRadiusMetres reports the exact radius for circle geometries.
func (geometry MapGeometry) CircleRadiusMetres() (float64, bool) {
	if geometry.circle == nil {
		return 0, false
	}

	return geometry.circle.RadiusM, true
}

// ExcludedCellIDs returns validated deterministic grid-cell IDs stored in the
// GeoJSON properties. Invalid or duplicate values are ignored defensively.
func (geometry MapGeometry) ExcludedCellIDs() []string {
	var feature struct {
		Properties struct {
			ExcludedCells []string `json:"excluded_cells"`
		} `json:"properties"`
	}
	if json.Unmarshal(geometry.canonical, &feature) != nil {
		return nil
	}
	seen := make(map[string]struct{}, len(feature.Properties.ExcludedCells))
	result := make([]string, 0, len(feature.Properties.ExcludedCells))
	for _, value := range feature.Properties.ExcludedCells {
		value = strings.TrimSpace(value)
		if !validMapEntityID(value) {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == maximumMapGridCells {
			break
		}
	}
	sort.Strings(result)

	return result
}

// GeoJSON returns a defensive copy of the canonical GeoJSON feature.
func (geometry MapGeometry) GeoJSON() json.RawMessage {
	return append(json.RawMessage(nil), geometry.canonical...)
}

// Valid reports whether the geometry was produced by the bounded parser.
func (geometry MapGeometry) Valid() bool {
	return geometry.valid()
}

// ContainsPoint reports whether a valid latitude/longitude lies in the area.
// Polygon outer boundaries and hole boundaries are treated as included.
func (geometry MapGeometry) ContainsPoint(latitude, longitude float64) bool {
	if !finiteMapCoordinate(latitude, longitude) || !geometry.valid() {
		return false
	}
	if latitude < geometry.bounds.MinLatitude-mapCoordinateEpsilon ||
		latitude > geometry.bounds.MaxLatitude+mapCoordinateEpsilon ||
		longitude < geometry.bounds.MinLongitude-mapCoordinateEpsilon ||
		longitude > geometry.bounds.MaxLongitude+mapCoordinateEpsilon {
		return false
	}

	point := MapPoint{Latitude: latitude, Longitude: longitude}
	if geometry.circle != nil {
		return mapDistanceMetres(geometry.circle.Centre, point) <= geometry.circle.RadiusM+0.01
	}
	if geometry.kind == "bbox" {
		return true
	}
	for _, polygon := range geometry.polygons {
		if pointInMapPolygon(point, polygon) {
			return true
		}
	}

	return false
}

func (geometry MapGeometry) valid() bool {
	return geometry.kind != "" && len(geometry.canonical) > 0 &&
		validMapBounds(geometry.bounds) && (geometry.circle != nil || len(geometry.polygons) > 0)
}

// MapGridCell is one deterministic grid centre and its approximate footprint.
type MapGridCell struct {
	ID             string    `json:"id"`
	Number         int       `json:"number"`
	Centre         MapPoint  `json:"centre"`
	Bounds         MapBounds `json:"bounds"`
	State          string    `json:"state"`
	Selected       bool      `json:"selected"`
	AreaKind       string    `json:"area_kind"`
	CellSizeKM     float64   `json:"cell_size_km"`
	TaskCount      int64     `json:"task_count,omitempty"`
	PendingTasks   int64     `json:"pending_tasks,omitempty"`
	RunningTasks   int64     `json:"running_tasks,omitempty"`
	CompletedTasks int64     `json:"completed_tasks,omitempty"`
	FailedTasks    int64     `json:"failed_tasks,omitempty"`
	BlockedTasks   int64     `json:"blocked_tasks,omitempty"`
	SkippedTasks   int64     `json:"skipped_tasks,omitempty"`
	WarningCount   int64     `json:"warning_count,omitempty"`
	ResultCount    int64     `json:"result_count,omitempty"`
	DuplicateCount int64     `json:"duplicate_count,omitempty"`
	// Duplicates is the checkpoint-recorded duplicate evidence for the cell's
	// durable tasks (rows skipped as duplicates plus rows replacing an earlier
	// copy). See MapCellActivity.CheckpointDuplicates.
	Duplicates int64 `json:"duplicates,omitempty"`
	Empty      bool  `json:"empty,omitempty"`
}

// MapGridPreview is a deterministic, geometry-clipped planning result.
type MapGridPreview struct {
	GeometryKind string        `json:"geometry_kind"`
	Bounds       MapBounds     `json:"bounds"`
	CellSizeKM   float64       `json:"cell_size_km"`
	Cells        []MapGridCell `json:"cells"`
}

// MapRepository is the additive saved-area and normalized spatial-query
// capability implemented by the local SQLite repository.
type MapRepository interface {
	ListSavedAreas(context.Context, int) ([]SavedArea, error)
	GetSavedArea(context.Context, string) (SavedArea, error)
	CreateSavedArea(context.Context, SavedArea) error
	UpdateSavedArea(context.Context, SavedArea) error
	DeleteSavedArea(context.Context, string) error
	SearchBusinessesInArea(context.Context, ResultSearch, MapGeometry) (ResultPage, error)
}

func (s *Service) mapRepository() (MapRepository, error) {
	repository, ok := s.repo.(MapRepository)
	if !ok {
		return nil, ErrMapStoreUnsupported
	}

	return repository, nil
}

// ListSavedAreas lists recently updated local map definitions.
func (s *Service) ListSavedAreas(ctx context.Context, limit int) ([]SavedArea, error) {
	repository, err := s.mapRepository()
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > maximumSavedAreaList {
		limit = 50
	}

	return repository.ListSavedAreas(ctx, limit)
}

// GetSavedArea returns one local map definition.
func (s *Service) GetSavedArea(ctx context.Context, id string) (SavedArea, error) {
	repository, err := s.mapRepository()
	if err != nil {
		return SavedArea{}, err
	}
	if !validMapEntityID(id) {
		return SavedArea{}, fmt.Errorf("%w: invalid area id", ErrInvalidMapGeometry)
	}

	return repository.GetSavedArea(ctx, id)
}

// CreateSavedArea validates, canonicalizes, and persists a new map definition.
func (s *Service) CreateSavedArea(ctx context.Context, area SavedArea) (SavedArea, error) {
	repository, err := s.mapRepository()
	if err != nil {
		return SavedArea{}, err
	}
	if strings.TrimSpace(area.ID) == "" {
		area.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if area.CreatedAt.IsZero() {
		area.CreatedAt = now
	}
	if area.UpdatedAt.IsZero() {
		area.UpdatedAt = now
	}
	area, _, err = NormalizeSavedArea(area)
	if err != nil {
		return SavedArea{}, err
	}
	if err := repository.CreateSavedArea(ctx, area); err != nil {
		return SavedArea{}, err
	}

	return area, nil
}

// UpdateSavedArea replaces a saved definition while retaining its creation time.
func (s *Service) UpdateSavedArea(ctx context.Context, area SavedArea) (SavedArea, error) {
	repository, err := s.mapRepository()
	if err != nil {
		return SavedArea{}, err
	}
	current, err := repository.GetSavedArea(ctx, area.ID)
	if err != nil {
		return SavedArea{}, err
	}
	if strings.TrimSpace(area.Name) == "" {
		area.Name = current.Name
	}
	if len(bytes.TrimSpace(area.GeoJSON)) == 0 {
		area.GeoJSON = current.GeoJSON
	}
	area.CreatedAt = current.CreatedAt
	area.UpdatedAt = time.Now().UTC()
	area, _, err = NormalizeSavedArea(area)
	if err != nil {
		return SavedArea{}, err
	}
	if err := repository.UpdateSavedArea(ctx, area); err != nil {
		return SavedArea{}, err
	}

	return area, nil
}

// DeleteSavedArea removes a saved definition without touching jobs or results.
func (s *Service) DeleteSavedArea(ctx context.Context, id string) error {
	repository, err := s.mapRepository()
	if err != nil {
		return err
	}
	if !validMapEntityID(id) {
		return fmt.Errorf("%w: invalid area id", ErrInvalidMapGeometry)
	}

	return repository.DeleteSavedArea(ctx, id)
}

// SearchBusinessesInArea applies an existing normalized ResultSearch and then
// its validated spatial geometry in one repository query contract.
func (s *Service) SearchBusinessesInArea(
	ctx context.Context,
	search ResultSearch,
	geometry MapGeometry,
) (ResultPage, error) {
	repository, err := s.mapRepository()
	if err != nil {
		return ResultPage{}, err
	}
	if !geometry.Valid() {
		return ResultPage{}, fmt.Errorf("%w: empty geometry", ErrInvalidMapGeometry)
	}
	if err := validateSpatialResultSearch(search); err != nil {
		return ResultPage{}, err
	}

	return repository.SearchBusinessesInArea(ctx, search, geometry)
}

// SearchAllBusinessesInArea streams bounded spatial matches through a callback
// without repeatedly rescanning the normalized result set. It is used by area
// exports so large local datasets remain practical.
func (s *Service) SearchAllBusinessesInArea(
	ctx context.Context,
	search ResultSearch,
	geometry MapGeometry,
	maximumRows int,
	visit func(BusinessResult) error,
) (int64, error) {
	repository, err := s.mapRepository()
	if err != nil {
		return 0, err
	}
	if !geometry.Valid() {
		return 0, fmt.Errorf("%w: empty geometry", ErrInvalidMapGeometry)
	}
	if err := validateSpatialResultSearch(search); err != nil {
		return 0, err
	}
	if maximumRows < 1 || maximumRows > maximumMapExportRows || visit == nil {
		return 0, fmt.Errorf("%w: invalid spatial export bound", ErrInvalidResultQuery)
	}
	if streamer, ok := repository.(interface {
		VisitBusinessesInArea(context.Context, ResultSearch, MapGeometry, int, func(BusinessResult) error) (int64, error)
	}); ok {
		return streamer.VisitBusinessesInArea(ctx, search, geometry, maximumRows, visit)
	}

	search.Limit = 250
	search.Offset = 0
	var total int64
	for {
		page, searchErr := repository.SearchBusinessesInArea(ctx, search, geometry)
		if searchErr != nil {
			return total, searchErr
		}
		if page.Total > int64(maximumRows) {
			return total, fmt.Errorf("%w: area export matches %d rows; narrow the filters below %d", ErrMapSpatialQueryTooLarge, page.Total, maximumRows)
		}
		for _, result := range page.Results {
			if err := visit(result); err != nil {
				return total, err
			}
			total++
		}
		if len(page.Results) == 0 || int64(search.Offset+len(page.Results)) >= page.Total {
			break
		}
		search.Offset += len(page.Results)
	}

	return total, nil
}

// NormalizeSavedArea validates its metadata and replaces GeoJSON with its
// canonical feature representation. Repositories call this as defense in depth.
func NormalizeSavedArea(area SavedArea) (SavedArea, MapGeometry, error) {
	area.ID = strings.TrimSpace(area.ID)
	area.Name = strings.TrimSpace(area.Name)
	if !validMapEntityID(area.ID) {
		return SavedArea{}, MapGeometry{}, fmt.Errorf("%w: invalid area id", ErrInvalidMapGeometry)
	}
	if area.Name == "" || utf8.RuneCountInString(area.Name) > 120 {
		return SavedArea{}, MapGeometry{}, fmt.Errorf("%w: area name must contain 1 to 120 characters", ErrInvalidMapGeometry)
	}
	geometry, err := ParseMapGeometry(area.GeoJSON)
	if err != nil {
		return SavedArea{}, MapGeometry{}, err
	}
	area.GeoJSON = geometry.GeoJSON()
	area.CreatedAt = area.CreatedAt.UTC()
	area.UpdatedAt = area.UpdatedAt.UTC()

	return area, geometry, nil
}

// ParseMapGeometry accepts GeoJSON Polygon/MultiPolygon features, a circle
// feature represented by a Point plus radius properties, and bbox properties.
func ParseMapGeometry(raw []byte) (MapGeometry, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > maximumMapGeoJSONBytes {
		return MapGeometry{}, fmt.Errorf("%w: GeoJSON must contain at most %d bytes", ErrInvalidMapGeometry, maximumMapGeoJSONBytes)
	}
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return MapGeometry{}, fmt.Errorf("%w: malformed GeoJSON", ErrInvalidMapGeometry)
	}

	var feature geoJSONFeature
	switch strings.ToLower(strings.TrimSpace(header.Type)) {
	case "feature":
		if err := json.Unmarshal(raw, &feature); err != nil {
			return MapGeometry{}, fmt.Errorf("%w: malformed feature", ErrInvalidMapGeometry)
		}
	case "polygon", "multipolygon":
		feature = geoJSONFeature{Type: "Feature", Geometry: append(json.RawMessage(nil), raw...), Properties: map[string]any{}}
	default:
		return MapGeometry{}, fmt.Errorf("%w: expected Feature, Polygon, or MultiPolygon", ErrInvalidMapGeometry)
	}
	if !strings.EqualFold(feature.Type, "Feature") {
		return MapGeometry{}, fmt.Errorf("%w: expected a GeoJSON Feature", ErrInvalidMapGeometry)
	}
	if feature.Properties == nil {
		feature.Properties = make(map[string]any)
	}

	shape := normalizeMapShape(mapStringProperty(feature.Properties, "shape", "geometry_type", "kind"))
	if shape == "circle" {
		return parseCircleFeature(feature)
	}
	if shape == "bbox" {
		return parseBBoxFeature(feature)
	}

	var geometry geoJSONGeometry
	if len(bytes.TrimSpace(feature.Geometry)) == 0 || bytes.Equal(bytes.TrimSpace(feature.Geometry), []byte("null")) {
		if _, ok := mapBBoxProperty(feature); ok {
			return parseBBoxFeature(feature)
		}
		return MapGeometry{}, fmt.Errorf("%w: feature geometry is required", ErrInvalidMapGeometry)
	}
	if err := json.Unmarshal(feature.Geometry, &geometry); err != nil {
		return MapGeometry{}, fmt.Errorf("%w: malformed feature geometry", ErrInvalidMapGeometry)
	}
	if strings.EqualFold(geometry.Type, "Point") && mapRadiusProperty(feature.Properties) > 0 {
		return parseCircleFeature(feature)
	}

	switch strings.ToLower(strings.TrimSpace(geometry.Type)) {
	case "polygon":
		polygons, err := decodeMapPolygons(geometry.Coordinates, false)
		if err != nil {
			return MapGeometry{}, err
		}
		return newPolygonGeometry("polygon", polygons, feature.Properties)
	case "multipolygon":
		polygons, err := decodeMapPolygons(geometry.Coordinates, true)
		if err != nil {
			return MapGeometry{}, err
		}
		return newPolygonGeometry("multipolygon", polygons, feature.Properties)
	default:
		return MapGeometry{}, fmt.Errorf("%w: unsupported geometry type %q", ErrInvalidMapGeometry, geometry.Type)
	}
}

type geoJSONFeature struct {
	Type       string          `json:"type"`
	Geometry   json.RawMessage `json:"geometry"`
	Properties map[string]any  `json:"properties"`
	BBox       []float64       `json:"bbox,omitempty"`
}

type geoJSONGeometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

func parseCircleFeature(feature geoJSONFeature) (MapGeometry, error) {
	radius := mapRadiusProperty(feature.Properties)
	if radius < minimumCircleRadiusM || radius > maximumCircleRadiusM || math.IsNaN(radius) || math.IsInf(radius, 0) {
		return MapGeometry{}, fmt.Errorf("%w: circle radius must be between %.0f and %.0f metres", ErrInvalidMapGeometry, minimumCircleRadiusM, maximumCircleRadiusM)
	}

	var centre MapPoint
	foundCentre := false
	if len(bytes.TrimSpace(feature.Geometry)) > 0 && !bytes.Equal(bytes.TrimSpace(feature.Geometry), []byte("null")) {
		var geometry struct {
			Type        string    `json:"type"`
			Coordinates []float64 `json:"coordinates"`
		}
		if err := json.Unmarshal(feature.Geometry, &geometry); err != nil || !strings.EqualFold(geometry.Type, "Point") || len(geometry.Coordinates) < 2 {
			return MapGeometry{}, fmt.Errorf("%w: circle geometry must be a Point", ErrInvalidMapGeometry)
		}
		centre = MapPoint{Latitude: geometry.Coordinates[1], Longitude: geometry.Coordinates[0]}
		foundCentre = true
	}
	if !foundCentre {
		coordinates, ok := mapCoordinateProperty(feature.Properties, "center", "centre")
		if !ok {
			return MapGeometry{}, fmt.Errorf("%w: circle centre is required", ErrInvalidMapGeometry)
		}
		centre = coordinates
	}
	if !finiteMapCoordinate(centre.Latitude, centre.Longitude) {
		return MapGeometry{}, fmt.Errorf("%w: invalid circle centre", ErrInvalidMapGeometry)
	}

	bounds, err := circleMapBounds(centre, radius)
	if err != nil {
		return MapGeometry{}, err
	}
	feature.Properties["shape"] = "circle"
	feature.Properties["radius_m"] = radius
	canonicalFeature := map[string]any{
		"type": "Feature",
		"geometry": map[string]any{
			"type":        "Point",
			"coordinates": []float64{centre.Longitude, centre.Latitude},
		},
		"properties": feature.Properties,
	}
	canonical, err := marshalCanonicalMapFeature(canonicalFeature)
	if err != nil {
		return MapGeometry{}, err
	}

	return finishMapGeometry(MapGeometry{
		kind: "circle", circle: &mapCircle{Centre: centre, RadiusM: radius},
		bounds: bounds, canonical: canonical,
	}), nil
}

func parseBBoxFeature(feature geoJSONFeature) (MapGeometry, error) {
	bounds, ok := mapBBoxProperty(feature)
	if !ok && len(bytes.TrimSpace(feature.Geometry)) > 0 && !bytes.Equal(bytes.TrimSpace(feature.Geometry), []byte("null")) {
		var geometry geoJSONGeometry
		if err := json.Unmarshal(feature.Geometry, &geometry); err == nil && strings.EqualFold(geometry.Type, "Polygon") {
			polygons, decodeErr := decodeMapPolygons(geometry.Coordinates, false)
			if decodeErr != nil {
				return MapGeometry{}, decodeErr
			}
			if len(polygons) == 1 {
				bounds, ok = rectanglePolygonBounds(polygons[0])
			}
		}
	}
	if !ok || !validMapBounds(bounds) || bounds.MinLatitude >= bounds.MaxLatitude || bounds.MinLongitude >= bounds.MaxLongitude {
		return MapGeometry{}, fmt.Errorf("%w: bbox must be [minLon,minLat,maxLon,maxLat]", ErrInvalidMapGeometry)
	}

	polygon := bboxMapPolygon(bounds)
	feature.Properties["shape"] = "bbox"
	feature.Properties["bbox"] = []float64{bounds.MinLongitude, bounds.MinLatitude, bounds.MaxLongitude, bounds.MaxLatitude}
	canonicalFeature := map[string]any{
		"type":       "Feature",
		"bbox":       []float64{bounds.MinLongitude, bounds.MinLatitude, bounds.MaxLongitude, bounds.MaxLatitude},
		"geometry":   polygonGeoJSON(polygon),
		"properties": feature.Properties,
	}
	canonical, err := marshalCanonicalMapFeature(canonicalFeature)
	if err != nil {
		return MapGeometry{}, err
	}

	return finishMapGeometry(MapGeometry{
		kind: "bbox", polygons: []mapPolygon{polygon}, bounds: bounds, canonical: canonical,
	}), nil
}

func newPolygonGeometry(kind string, polygons []mapPolygon, properties map[string]any) (MapGeometry, error) {
	if len(polygons) == 0 {
		return MapGeometry{}, fmt.Errorf("%w: polygon has no coordinates", ErrInvalidMapGeometry)
	}
	bounds := polygonsMapBounds(polygons)
	geometryType := "Polygon"
	var coordinates any = polygonCoordinates(polygons[0])
	if kind == "multipolygon" {
		geometryType = "MultiPolygon"
		multi := make([][][][]float64, 0, len(polygons))
		for _, polygon := range polygons {
			multi = append(multi, polygonCoordinates(polygon))
		}
		coordinates = multi
	}
	canonicalFeature := map[string]any{
		"type": "Feature",
		"geometry": map[string]any{
			"type":        geometryType,
			"coordinates": coordinates,
		},
		"properties": properties,
	}
	canonical, err := marshalCanonicalMapFeature(canonicalFeature)
	if err != nil {
		return MapGeometry{}, err
	}

	return finishMapGeometry(MapGeometry{
		kind: kind, polygons: polygons, bounds: bounds, canonical: canonical,
	}), nil
}

func finishMapGeometry(geometry MapGeometry) MapGeometry {
	// Cell identities describe spatial work, not display metadata. Saved-area
	// names and excluded_cells live in GeoJSON properties and must not renumber
	// every cell when those properties change.
	identity := strings.Builder{}
	_, _ = fmt.Fprintf(&identity, "%s|%.12f|%.12f|%.12f|%.12f", geometry.kind,
		geometry.bounds.MinLatitude, geometry.bounds.MinLongitude,
		geometry.bounds.MaxLatitude, geometry.bounds.MaxLongitude)
	if geometry.circle != nil {
		_, _ = fmt.Fprintf(&identity, "|circle|%.12f|%.12f|%.6f",
			geometry.circle.Centre.Latitude, geometry.circle.Centre.Longitude, geometry.circle.RadiusM)
	}
	for _, polygon := range geometry.polygons {
		identity.WriteString("|polygon")
		for _, ring := range polygon.Rings {
			identity.WriteString("|ring")
			for _, point := range ring {
				_, _ = fmt.Fprintf(&identity, "|%.12f,%.12f", point.Latitude, point.Longitude)
			}
		}
	}
	digest := sha256.Sum256([]byte(identity.String()))
	geometry.contentKey = hex.EncodeToString(digest[:])

	return geometry
}

func marshalCanonicalMapFeature(feature any) (json.RawMessage, error) {
	canonical, err := json.Marshal(feature)
	if err != nil || len(canonical) > maximumMapGeoJSONBytes {
		return nil, fmt.Errorf("%w: GeoJSON cannot be canonicalized safely", ErrInvalidMapGeometry)
	}

	return canonical, nil
}

func decodeMapPolygons(raw json.RawMessage, multi bool) ([]mapPolygon, error) {
	positionCount := 0
	if multi {
		var coordinates [][][][]float64
		if err := json.Unmarshal(raw, &coordinates); err != nil {
			return nil, fmt.Errorf("%w: malformed MultiPolygon coordinates", ErrInvalidMapGeometry)
		}
		if len(coordinates) == 0 || len(coordinates) > maximumMapPolygons {
			return nil, fmt.Errorf("%w: MultiPolygon polygon count is out of bounds", ErrInvalidMapGeometry)
		}
		polygons := make([]mapPolygon, 0, len(coordinates))
		for _, polygonCoordinates := range coordinates {
			polygon, err := validateMapPolygon(polygonCoordinates, &positionCount)
			if err != nil {
				return nil, err
			}
			polygons = append(polygons, polygon)
		}

		return polygons, nil
	}

	var coordinates [][][]float64
	if err := json.Unmarshal(raw, &coordinates); err != nil {
		return nil, fmt.Errorf("%w: malformed Polygon coordinates", ErrInvalidMapGeometry)
	}
	polygon, err := validateMapPolygon(coordinates, &positionCount)
	if err != nil {
		return nil, err
	}

	return []mapPolygon{polygon}, nil
}

func validateMapPolygon(coordinates [][][]float64, positionCount *int) (mapPolygon, error) {
	if len(coordinates) == 0 || len(coordinates) > maximumMapRings {
		return mapPolygon{}, fmt.Errorf("%w: polygon ring count is out of bounds", ErrInvalidMapGeometry)
	}
	polygon := mapPolygon{Rings: make([][]MapPoint, 0, len(coordinates))}
	for _, rawRing := range coordinates {
		if len(rawRing) < 4 || len(rawRing) > maximumMapRingPoints {
			return mapPolygon{}, fmt.Errorf("%w: each ring must contain 4 to %d positions", ErrInvalidMapGeometry, maximumMapRingPoints)
		}
		*positionCount += len(rawRing)
		if *positionCount > maximumMapCoordinates {
			return mapPolygon{}, fmt.Errorf("%w: geometry contains too many positions", ErrInvalidMapGeometry)
		}
		ring := make([]MapPoint, 0, len(rawRing))
		for _, position := range rawRing {
			if len(position) < 2 || len(position) > 3 || !finiteMapCoordinate(position[1], position[0]) {
				return mapPolygon{}, fmt.Errorf("%w: invalid polygon position", ErrInvalidMapGeometry)
			}
			ring = append(ring, MapPoint{Latitude: position[1], Longitude: position[0]})
		}
		if !sameMapPoint(ring[0], ring[len(ring)-1]) {
			return mapPolygon{}, fmt.Errorf("%w: polygon rings must be closed", ErrInvalidMapGeometry)
		}
		if math.Abs(mapRingSignedArea(ring)) <= mapCoordinateEpsilon {
			return mapPolygon{}, fmt.Errorf("%w: polygon ring has zero area", ErrInvalidMapGeometry)
		}
		if mapRingSelfIntersects(ring) {
			return mapPolygon{}, fmt.Errorf("%w: polygon ring self-intersects", ErrInvalidMapGeometry)
		}
		polygon.Rings = append(polygon.Rings, ring)
	}

	outer := polygon.Rings[0]
	for index := 1; index < len(polygon.Rings); index++ {
		hole := polygon.Rings[index]
		inside, boundary := pointInMapRing(hole[0], outer)
		if !inside || boundary || mapRingsIntersect(hole, outer) {
			return mapPolygon{}, fmt.Errorf("%w: polygon hole must be strictly inside its outer ring", ErrInvalidMapGeometry)
		}
		for previous := 1; previous < index; previous++ {
			other := polygon.Rings[previous]
			insideOther, _ := pointInMapRing(hole[0], other)
			otherInside, _ := pointInMapRing(other[0], hole)
			if insideOther || otherInside || mapRingsIntersect(hole, other) {
				return mapPolygon{}, fmt.Errorf("%w: polygon holes overlap", ErrInvalidMapGeometry)
			}
		}
	}

	return polygon, nil
}

func mapRingSelfIntersects(ring []MapPoint) bool {
	segments := len(ring) - 1
	for left := 0; left < segments; left++ {
		for right := left + 1; right < segments; right++ {
			if right == left+1 || left == 0 && right == segments-1 {
				continue
			}
			if mapSegmentsIntersect(ring[left], ring[left+1], ring[right], ring[right+1]) {
				return true
			}
		}
	}

	return false
}

func mapRingsIntersect(left, right []MapPoint) bool {
	for leftIndex := 0; leftIndex < len(left)-1; leftIndex++ {
		for rightIndex := 0; rightIndex < len(right)-1; rightIndex++ {
			if mapSegmentsIntersect(left[leftIndex], left[leftIndex+1], right[rightIndex], right[rightIndex+1]) {
				return true
			}
		}
	}

	return false
}

func mapSegmentsIntersect(a, b, c, d MapPoint) bool {
	o1 := mapOrientation(a, b, c)
	o2 := mapOrientation(a, b, d)
	o3 := mapOrientation(c, d, a)
	o4 := mapOrientation(c, d, b)
	if o1*o2 < -mapCoordinateEpsilon && o3*o4 < -mapCoordinateEpsilon {
		return true
	}

	return math.Abs(o1) <= mapCoordinateEpsilon && mapPointOnSegment(c, a, b) ||
		math.Abs(o2) <= mapCoordinateEpsilon && mapPointOnSegment(d, a, b) ||
		math.Abs(o3) <= mapCoordinateEpsilon && mapPointOnSegment(a, c, d) ||
		math.Abs(o4) <= mapCoordinateEpsilon && mapPointOnSegment(b, c, d)
}

func mapOrientation(a, b, c MapPoint) float64 {
	return (b.Longitude-a.Longitude)*(c.Latitude-a.Latitude) -
		(b.Latitude-a.Latitude)*(c.Longitude-a.Longitude)
}

func mapPointOnSegment(point, start, end MapPoint) bool {
	return point.Longitude >= min(start.Longitude, end.Longitude)-mapCoordinateEpsilon &&
		point.Longitude <= max(start.Longitude, end.Longitude)+mapCoordinateEpsilon &&
		point.Latitude >= min(start.Latitude, end.Latitude)-mapCoordinateEpsilon &&
		point.Latitude <= max(start.Latitude, end.Latitude)+mapCoordinateEpsilon
}

func pointInMapPolygon(point MapPoint, polygon mapPolygon) bool {
	inside, boundary := pointInMapRing(point, polygon.Rings[0])
	if !inside {
		return false
	}
	if boundary {
		return true
	}
	for index := 1; index < len(polygon.Rings); index++ {
		insideHole, holeBoundary := pointInMapRing(point, polygon.Rings[index])
		if holeBoundary {
			return true
		}
		if insideHole {
			return false
		}
	}

	return true
}

func pointInMapRing(point MapPoint, ring []MapPoint) (inside bool, boundary bool) {
	for index := 0; index < len(ring)-1; index++ {
		start, end := ring[index], ring[index+1]
		if math.Abs(mapOrientation(start, end, point)) <= mapCoordinateEpsilon && mapPointOnSegment(point, start, end) {
			return true, true
		}
		crosses := (start.Latitude > point.Latitude) != (end.Latitude > point.Latitude)
		if crosses {
			intersection := (end.Longitude-start.Longitude)*(point.Latitude-start.Latitude)/
				(end.Latitude-start.Latitude) + start.Longitude
			if point.Longitude < intersection {
				inside = !inside
			}
		}
	}

	return inside, false
}

func mapRingSignedArea(ring []MapPoint) float64 {
	area := 0.0
	for index := 0; index < len(ring)-1; index++ {
		area += ring[index].Longitude*ring[index+1].Latitude - ring[index+1].Longitude*ring[index].Latitude
	}

	return area / 2
}

func sameMapPoint(left, right MapPoint) bool {
	return math.Abs(left.Latitude-right.Latitude) <= mapCoordinateEpsilon &&
		math.Abs(left.Longitude-right.Longitude) <= mapCoordinateEpsilon
}

func finiteMapCoordinate(latitude, longitude float64) bool {
	return !math.IsNaN(latitude) && !math.IsInf(latitude, 0) && latitude >= -90 && latitude <= 90 &&
		!math.IsNaN(longitude) && !math.IsInf(longitude, 0) && longitude >= -180 && longitude <= 180
}

func validMapBounds(bounds MapBounds) bool {
	return finiteMapCoordinate(bounds.MinLatitude, bounds.MinLongitude) &&
		finiteMapCoordinate(bounds.MaxLatitude, bounds.MaxLongitude) &&
		bounds.MinLatitude <= bounds.MaxLatitude && bounds.MinLongitude <= bounds.MaxLongitude
}

func polygonsMapBounds(polygons []mapPolygon) MapBounds {
	bounds := MapBounds{
		MinLatitude: 90, MinLongitude: 180, MaxLatitude: -90, MaxLongitude: -180,
	}
	for _, polygon := range polygons {
		for _, ring := range polygon.Rings {
			for _, point := range ring {
				bounds.MinLatitude = min(bounds.MinLatitude, point.Latitude)
				bounds.MinLongitude = min(bounds.MinLongitude, point.Longitude)
				bounds.MaxLatitude = max(bounds.MaxLatitude, point.Latitude)
				bounds.MaxLongitude = max(bounds.MaxLongitude, point.Longitude)
			}
		}
	}

	return bounds
}

func circleMapBounds(centre MapPoint, radiusM float64) (MapBounds, error) {
	angular := radiusM / mapEarthRadiusM
	latitudeDelta := angular * 180 / math.Pi
	minLatitude := centre.Latitude - latitudeDelta
	maxLatitude := centre.Latitude + latitudeDelta
	if minLatitude <= -90 || maxLatitude >= 90 {
		return MapBounds{}, fmt.Errorf("%w: bounded circles may not cross a pole", ErrInvalidMapGeometry)
	}
	longitudeDelta := math.Asin(math.Sin(angular)/math.Cos(centre.Latitude*math.Pi/180)) * 180 / math.Pi
	minLongitude := centre.Longitude - longitudeDelta
	maxLongitude := centre.Longitude + longitudeDelta
	if minLongitude < -180 || maxLongitude > 180 {
		return MapBounds{}, fmt.Errorf("%w: bounded circles may not cross the antimeridian", ErrInvalidMapGeometry)
	}

	return MapBounds{
		MinLatitude: minLatitude, MinLongitude: minLongitude,
		MaxLatitude: maxLatitude, MaxLongitude: maxLongitude,
	}, nil
}

func mapDistanceMetres(left, right MapPoint) float64 {
	latitudeDelta := (right.Latitude - left.Latitude) * math.Pi / 180
	longitudeDelta := (right.Longitude - left.Longitude) * math.Pi / 180
	leftLatitude := left.Latitude * math.Pi / 180
	rightLatitude := right.Latitude * math.Pi / 180
	a := math.Sin(latitudeDelta/2)*math.Sin(latitudeDelta/2) +
		math.Cos(leftLatitude)*math.Cos(rightLatitude)*
			math.Sin(longitudeDelta/2)*math.Sin(longitudeDelta/2)

	return mapEarthRadiusM * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(max(0, 1-a)))
}

func rectanglePolygonBounds(polygon mapPolygon) (MapBounds, bool) {
	if len(polygon.Rings) != 1 || len(polygon.Rings[0]) != 5 {
		return MapBounds{}, false
	}
	bounds := polygonsMapBounds([]mapPolygon{polygon})
	expected := map[MapPoint]struct{}{
		{Latitude: bounds.MinLatitude, Longitude: bounds.MinLongitude}: {},
		{Latitude: bounds.MinLatitude, Longitude: bounds.MaxLongitude}: {},
		{Latitude: bounds.MaxLatitude, Longitude: bounds.MinLongitude}: {},
		{Latitude: bounds.MaxLatitude, Longitude: bounds.MaxLongitude}: {},
	}
	for _, point := range polygon.Rings[0][:4] {
		matched := false
		for corner := range expected {
			if sameMapPoint(point, corner) {
				delete(expected, corner)
				matched = true
				break
			}
		}
		if !matched {
			return MapBounds{}, false
		}
	}

	return bounds, len(expected) == 0
}

func bboxMapPolygon(bounds MapBounds) mapPolygon {
	return mapPolygon{Rings: [][]MapPoint{{
		{Latitude: bounds.MinLatitude, Longitude: bounds.MinLongitude},
		{Latitude: bounds.MinLatitude, Longitude: bounds.MaxLongitude},
		{Latitude: bounds.MaxLatitude, Longitude: bounds.MaxLongitude},
		{Latitude: bounds.MaxLatitude, Longitude: bounds.MinLongitude},
		{Latitude: bounds.MinLatitude, Longitude: bounds.MinLongitude},
	}}}
}

func polygonCoordinates(polygon mapPolygon) [][][]float64 {
	coordinates := make([][][]float64, 0, len(polygon.Rings))
	for _, ring := range polygon.Rings {
		positions := make([][]float64, 0, len(ring))
		for _, point := range ring {
			positions = append(positions, []float64{point.Longitude, point.Latitude})
		}
		coordinates = append(coordinates, positions)
	}

	return coordinates
}

func polygonGeoJSON(polygon mapPolygon) map[string]any {
	return map[string]any{"type": "Polygon", "coordinates": polygonCoordinates(polygon)}
}

func normalizeMapShape(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("-", "_", " ", "_").Replace(value)
	switch value {
	case "circle":
		return "circle"
	case "bbox", "bounding_box", "rectangle":
		return "bbox"
	default:
		return ""
	}
}

func mapStringProperty(properties map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := properties[key]; ok {
			if text, ok := value.(string); ok {
				return strings.TrimSpace(text)
			}
		}
	}

	return ""
}

func mapNumberProperty(properties map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		value, ok := properties[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case float64:
			return typed, true
		case json.Number:
			parsed, err := typed.Float64()
			return parsed, err == nil
		case string:
			parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
			return parsed, err == nil
		}
	}

	return 0, false
}

func mapRadiusProperty(properties map[string]any) float64 {
	if radius, ok := mapNumberProperty(properties, "radius_m", "radius_metres", "radius_meters"); ok {
		return radius
	}
	if radiusKM, ok := mapNumberProperty(properties, "radius_km"); ok {
		return radiusKM * 1000
	}

	return 0
}

func mapCoordinateProperty(properties map[string]any, keys ...string) (MapPoint, bool) {
	for _, key := range keys {
		value, ok := properties[key]
		if !ok {
			continue
		}
		values, ok := value.([]any)
		if !ok || len(values) < 2 {
			continue
		}
		longitude, longitudeOK := mapAnyFloat(values[0])
		latitude, latitudeOK := mapAnyFloat(values[1])
		if longitudeOK && latitudeOK {
			return MapPoint{Latitude: latitude, Longitude: longitude}, true
		}
	}

	return MapPoint{}, false
}

func mapBBoxProperty(feature geoJSONFeature) (MapBounds, bool) {
	if len(feature.BBox) == 4 {
		return bboxFromMapValues(feature.BBox)
	}
	if raw, ok := feature.Properties["bbox"]; ok {
		if values, ok := raw.([]any); ok && len(values) == 4 {
			parsed := make([]float64, 4)
			for index := range values {
				value, valueOK := mapAnyFloat(values[index])
				if !valueOK {
					return MapBounds{}, false
				}
				parsed[index] = value
			}
			return bboxFromMapValues(parsed)
		}
	}
	minLatitude, minLatitudeOK := mapNumberProperty(feature.Properties, "min_lat", "min_latitude")
	minLongitude, minLongitudeOK := mapNumberProperty(feature.Properties, "min_lon", "min_lng", "min_longitude")
	maxLatitude, maxLatitudeOK := mapNumberProperty(feature.Properties, "max_lat", "max_latitude")
	maxLongitude, maxLongitudeOK := mapNumberProperty(feature.Properties, "max_lon", "max_lng", "max_longitude")
	if minLatitudeOK && minLongitudeOK && maxLatitudeOK && maxLongitudeOK {
		return MapBounds{
			MinLatitude: minLatitude, MinLongitude: minLongitude,
			MaxLatitude: maxLatitude, MaxLongitude: maxLongitude,
		}, true
	}

	return MapBounds{}, false
}

func bboxFromMapValues(values []float64) (MapBounds, bool) {
	if len(values) != 4 {
		return MapBounds{}, false
	}
	bounds := MapBounds{
		MinLongitude: values[0], MinLatitude: values[1],
		MaxLongitude: values[2], MaxLatitude: values[3],
	}

	return bounds, validMapBounds(bounds) && bounds.MinLatitude < bounds.MaxLatitude &&
		bounds.MinLongitude < bounds.MaxLongitude
}

func mapAnyFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	default:
		return 0, false
	}
}

// PreviewMapGrid creates stable cells whose centres fall inside the geometry.
func PreviewMapGrid(geometry MapGeometry, cellSizeKM float64, maximumCells int) (MapGridPreview, error) {
	if !geometry.Valid() {
		return MapGridPreview{}, fmt.Errorf("%w: empty geometry", ErrInvalidMapGeometry)
	}
	if math.IsNaN(cellSizeKM) || math.IsInf(cellSizeKM, 0) ||
		cellSizeKM < minimumMapCellKM || cellSizeKM > maximumMapCellKM {
		return MapGridPreview{}, fmt.Errorf("%w: cell size must be between %.2f and %.0f km", ErrInvalidMapGeometry, minimumMapCellKM, maximumMapCellKM)
	}
	if maximumCells < 1 || maximumCells > maximumMapGridCells {
		maximumCells = maximumMapGridCells
	}

	bounds := geometry.Bounds()
	latitudeStep := cellSizeKM / mapKilometresPerDegree
	midLatitude := (bounds.MinLatitude + bounds.MaxLatitude) / 2
	cosine := math.Cos(midLatitude * math.Pi / 180)
	if math.Abs(cosine) < 1e-6 {
		return MapGridPreview{}, fmt.Errorf("%w: grid is too close to a pole", ErrInvalidMapGeometry)
	}
	longitudeStep := cellSizeKM / (mapKilometresPerDegree * math.Abs(cosine))
	preview := MapGridPreview{
		GeometryKind: geometry.Kind(), Bounds: bounds, CellSizeKM: cellSizeKM,
		Cells: make([]MapGridCell, 0),
	}
	candidates := 0
	for latitude := bounds.MinLatitude + latitudeStep/2; latitude < bounds.MaxLatitude; latitude += latitudeStep {
		for longitude := bounds.MinLongitude + longitudeStep/2; longitude < bounds.MaxLongitude; longitude += longitudeStep {
			candidates++
			if candidates > maximumMapGridCandidates {
				return MapGridPreview{}, fmt.Errorf("%w: candidate grid exceeds %d cells", ErrMapGridTooLarge, maximumMapGridCandidates)
			}
			if !geometry.ContainsPoint(latitude, longitude) {
				continue
			}
			if len(preview.Cells) >= maximumCells {
				return MapGridPreview{}, fmt.Errorf("%w: preview exceeds %d clipped cells", ErrMapGridTooLarge, maximumCells)
			}
			preview.Cells = append(preview.Cells, newMapGridCell(
				geometry, len(preview.Cells)+1, latitude, longitude,
				latitudeStep, longitudeStep, cellSizeKM,
			))
		}
	}
	if len(preview.Cells) == 0 {
		representative := mapGeometryRepresentativePoint(geometry)
		preview.Cells = append(preview.Cells, newMapGridCell(
			geometry, 1, representative.Latitude, representative.Longitude,
			latitudeStep, longitudeStep, cellSizeKM,
		))
	}

	return preview, nil
}

func newMapGridCell(
	geometry MapGeometry,
	number int,
	latitude, longitude, latitudeStep, longitudeStep, cellSizeKM float64,
) MapGridCell {
	identity := fmt.Sprintf("%s|%.9f|%.9f|%.6f", geometry.contentKey, latitude, longitude, cellSizeKM)
	digest := sha256.Sum256([]byte(identity))

	return MapGridCell{
		ID:     "cell-" + hex.EncodeToString(digest[:8]),
		Number: number,
		Centre: MapPoint{Latitude: latitude, Longitude: longitude},
		Bounds: MapBounds{
			MinLatitude:  max(-90, latitude-latitudeStep/2),
			MinLongitude: max(-180, longitude-longitudeStep/2),
			MaxLatitude:  min(90, latitude+latitudeStep/2),
			MaxLongitude: min(180, longitude+longitudeStep/2),
		},
		State: "waiting", AreaKind: geometry.Kind(), CellSizeKM: cellSizeKM,
	}
}

func mapGeometryRepresentativePoint(geometry MapGeometry) MapPoint {
	if geometry.circle != nil {
		return geometry.circle.Centre
	}
	centre := MapPoint{
		Latitude:  (geometry.bounds.MinLatitude + geometry.bounds.MaxLatitude) / 2,
		Longitude: (geometry.bounds.MinLongitude + geometry.bounds.MaxLongitude) / 2,
	}
	if geometry.ContainsPoint(centre.Latitude, centre.Longitude) {
		return centre
	}
	for _, polygon := range geometry.polygons {
		for _, point := range polygon.Rings[0] {
			if geometry.ContainsPoint(point.Latitude, point.Longitude) {
				return point
			}
		}
	}

	return centre
}

func validateSpatialResultSearch(search ResultSearch) error {
	if len(search.Query) > maximumResultQueryLength || len(search.JobID) > 128 || len(search.Sort) > 64 {
		return fmt.Errorf("%w: spatial result search is too long", ErrInvalidResultQuery)
	}
	if search.Limit < 0 || search.Limit > 250 || search.Offset < 0 || search.Offset > maximumSpatialScanRows {
		return fmt.Errorf("%w: invalid spatial result page", ErrInvalidResultQuery)
	}
	if len(search.Filters) > 25 {
		return fmt.Errorf("%w: too many spatial result filters", ErrInvalidResultQuery)
	}
	for _, filter := range search.Filters {
		if len(filter.Field) > 64 || len(filter.Operator) > 64 || len(filter.Value) > 1000 {
			return fmt.Errorf("%w: spatial result filter is too long", ErrInvalidResultQuery)
		}
	}
	if search.FilterGroup != nil {
		if err := validateResultFilterGroup(*search.FilterGroup); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidResultQuery, err)
		}
	}

	return nil
}

func validMapEntityID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-' || character == '_' {
			continue
		}
		return false
	}

	return true
}

// registerMapRoutes exposes the isolated Map API. The main router calls this
// method when the Map checkpoint is enabled.
func (s *Server) registerMapRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/maps/areas", s.apiListSavedAreas)
	mux.HandleFunc("POST /api/v1/maps/areas", s.apiCreateSavedArea)
	mux.HandleFunc("POST /api/v1/maps/areas/import", s.apiImportSavedAreas)
	mux.HandleFunc("GET /api/v1/maps/areas/{id}", s.apiGetSavedArea)
	mux.HandleFunc("PUT /api/v1/maps/areas/{id}", s.apiUpdateSavedArea)
	mux.HandleFunc("DELETE /api/v1/maps/areas/{id}", s.apiDeleteSavedArea)
	mux.HandleFunc("GET /api/v1/maps/areas/{id}/export", s.apiExportSavedArea)
	mux.HandleFunc("POST /api/v1/maps/grid/preview", s.apiPreviewMapGrid)
	mux.HandleFunc("POST /api/v1/maps/grid/coverage", s.apiMapCoverage)
	mux.HandleFunc("GET /api/v1/maps/results", s.apiMapResults)
	mux.HandleFunc("POST /api/v1/maps/results", s.apiMapResults)
	mux.HandleFunc("POST /api/v1/maps/results/export", s.apiExportMapResults)
	mux.HandleFunc("POST /api/v1/maps/cells/rescrape", s.apiRescrapeMapCells)
	mux.HandleFunc("GET /api/v1/maps/tiles/{z}/{x}/{y}", s.apiMapTile)
	// The template rename action lives in app_reusable.go; it is wired here
	// because the main router registers reusable-template routes directly and
	// this is the register function closest to the Map cell-action templates.
	// Move this call to web.go (`ans.registerTemplateRenameRoutes(mux)`) when
	// the router file is next edited.
	s.registerTemplateRenameRoutes(mux)
}

func (s *Server) apiMapTile(w http.ResponseWriter, r *http.Request) {
	zoom, x, y, ok := mapTileCoordinates(r.PathValue("z"), r.PathValue("x"), r.PathValue("y"))
	if !ok {
		http.Error(w, "invalid map tile", http.StatusUnprocessableEntity)
		return
	}
	if s == nil || s.svc == nil || strings.TrimSpace(s.svc.dataFolder) == "" {
		http.Error(w, "map tile cache is unavailable", http.StatusServiceUnavailable)
		return
	}

	relativePath := filepath.ToSlash(filepath.Join("map-tiles", strconv.Itoa(zoom), strconv.Itoa(x), strconv.Itoa(y)+".png"))
	cachePath, err := safeDataPath(s.svc.dataFolder, relativePath)
	if err != nil {
		http.Error(w, "map tile cache is unavailable", http.StatusInternalServerError)
		return
	}
	if serveCachedMapTile(w, cachePath) {
		return
	}

	requestURL := fmt.Sprintf("https://tile.openstreetmap.org/%d/%d/%d.png", zoom, x, y)
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, requestURL, nil)
	if err != nil {
		http.Error(w, "could not prepare map tile request", http.StatusBadGateway)
		return
	}
	request.Header.Set("User-Agent", "gosom-google-maps-scraper-local/1.0 (+https://github.com/gosom/google-maps-scraper)")
	request.Header.Set("Accept", "image/png")
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		http.Error(w, "map tile is not cached and could not be downloaded", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		http.Error(w, "map tile provider returned an error", http.StatusBadGateway)
		return
	}
	tile, err := io.ReadAll(io.LimitReader(response.Body, maximumMapTileBytes+1))
	if err != nil || len(tile) > maximumMapTileBytes || !isPNGMapTile(tile) {
		http.Error(w, "map tile response was invalid", http.StatusBadGateway)
		return
	}
	if err := persistMapTile(cachePath, tile); err != nil {
		// A read-only or temporarily full cache must not hide an otherwise valid
		// tile from the current local browser session.
		serveMapTileBytes(w, tile)
		return
	}
	serveMapTileBytes(w, tile)
}

func mapTileCoordinates(rawZoom, rawX, rawY string) (int, int, int, bool) {
	if !strings.HasSuffix(rawY, ".png") {
		return 0, 0, 0, false
	}
	rawY = strings.TrimSuffix(rawY, ".png")
	zoom, errZoom := strconv.Atoi(rawZoom)
	x, errX := strconv.Atoi(rawX)
	y, errY := strconv.Atoi(rawY)
	if errZoom != nil || errX != nil || errY != nil || zoom < 0 || zoom > maximumMapTileZoom {
		return 0, 0, 0, false
	}
	maximumCoordinate := 1 << zoom
	if x < 0 || x >= maximumCoordinate || y < 0 || y >= maximumCoordinate {
		return 0, 0, 0, false
	}

	return zoom, x, y, true
}

func serveCachedMapTile(w http.ResponseWriter, cachePath string) bool {
	tile, err := os.ReadFile(cachePath)
	if err != nil || len(tile) > maximumMapTileBytes || !isPNGMapTile(tile) {
		return false
	}
	serveMapTileBytes(w, tile)

	return true
}

func serveMapTileBytes(w http.ResponseWriter, tile []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age="+strconv.FormatInt(int64(mapTileCacheMaxAge/time.Second), 10)+", immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(tile)
}

func isPNGMapTile(tile []byte) bool {
	return len(tile) >= 8 && bytes.Equal(tile[:8], []byte{'\x89', 'P', 'N', 'G', '\r', '\n', '\x1a', '\n'})
}

func persistMapTile(cachePath string, tile []byte) error {
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(cachePath), ".map-tile-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := temporary.Write(tile); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, cachePath); err != nil {
		// Windows cannot atomically replace a file another concurrent request
		// just populated. If that file is now valid, the cache operation won.
		cached, readErr := os.ReadFile(cachePath)
		if readErr == nil && len(cached) <= maximumMapTileBytes && isPNGMapTile(cached) {
			return nil
		}
		return err
	}

	return nil
}

type savedAreaInput struct {
	ID      string          `json:"id,omitempty"`
	Name    string          `json:"name,omitempty"`
	GeoJSON json.RawMessage `json:"geojson,omitempty"`
}

type mapGridPreviewInput struct {
	AreaID    string          `json:"area_id,omitempty"`
	GeoJSON   json.RawMessage `json:"geojson,omitempty"`
	CellSizeK float64         `json:"cell_size_km"`
}

type mapResultsInput struct {
	AreaID  string          `json:"area_id,omitempty"`
	GeoJSON json.RawMessage `json:"geojson,omitempty"`
	Search  ResultSearch    `json:"search"`
}

func (s *Server) apiListSavedAreas(w http.ResponseWriter, r *http.Request) {
	limit := positiveQueryInt(r.URL.Query().Get("limit"), 50)
	areas, err := s.svc.ListSavedAreas(r.Context(), limit)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: areas, Meta: map[string]any{"count": len(areas)}})
}

func (s *Server) apiCreateSavedArea(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	raw, err := readBoundedMapBody(w, r)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	input, err := decodeSavedAreaInput(raw, true)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	area, err := s.svc.CreateSavedArea(r.Context(), SavedArea{ID: input.ID, Name: input.Name, GeoJSON: input.GeoJSON})
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	renderJSON(w, http.StatusCreated, localAPIEnvelope{Data: area})
}

func (s *Server) apiGetSavedArea(w http.ResponseWriter, r *http.Request) {
	area, err := s.svc.GetSavedArea(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: area})
}

func (s *Server) apiUpdateSavedArea(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	raw, err := readBoundedMapBody(w, r)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	input, err := decodeSavedAreaInput(raw, false)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	area, err := s.svc.UpdateSavedArea(r.Context(), SavedArea{
		ID: strings.TrimSpace(r.PathValue("id")), Name: input.Name, GeoJSON: input.GeoJSON,
	})
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: area})
}

func (s *Server) apiDeleteSavedArea(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	if err := s.svc.DeleteSavedArea(r.Context(), strings.TrimSpace(r.PathValue("id"))); err != nil {
		renderMapAPIError(w, err)
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]string{"message": "Saved area deleted"}})
}

func (s *Server) apiExportSavedArea(w http.ResponseWriter, r *http.Request) {
	area, err := s.svc.GetSavedArea(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/geo+json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "map-area-"+area.ID+".geojson"))
	_, _ = w.Write(append(area.GeoJSON, '\n'))
}

func (s *Server) apiImportSavedAreas(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	raw, err := readBoundedMapBody(w, r)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	features, names, err := decodeMapImport(raw, strings.TrimSpace(r.URL.Query().Get("name")))
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	areas := make([]SavedArea, 0, len(features))
	for index, feature := range features {
		area, createErr := s.svc.CreateSavedArea(r.Context(), SavedArea{Name: names[index], GeoJSON: feature})
		if createErr != nil {
			renderMapAPIError(w, createErr)
			return
		}
		areas = append(areas, area)
	}
	renderJSON(w, http.StatusCreated, localAPIEnvelope{Data: areas, Meta: map[string]any{"count": len(areas)}})
}

func (s *Server) apiPreviewMapGrid(w http.ResponseWriter, r *http.Request) {
	raw, err := readBoundedMapBody(w, r)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	var input mapGridPreviewInput
	if err := decodeStrictMapJSON(raw, &input); err != nil {
		renderMapAPIError(w, err)
		return
	}
	geometry, err := s.resolveMapGeometry(r.Context(), input.AreaID, input.GeoJSON)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	preview, err := PreviewMapGrid(geometry, input.CellSizeK, maximumMapGridCells)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: preview})
}

func (s *Server) apiMapResults(w http.ResponseWriter, r *http.Request) {
	var search ResultSearch
	var areaID string
	var rawGeometry json.RawMessage
	var err error
	if r.Method == http.MethodGet {
		search, err = parseResultSearch(r)
		areaID = strings.TrimSpace(r.URL.Query().Get("area_id"))
	} else {
		var raw []byte
		raw, err = readBoundedMapBody(w, r)
		if err == nil {
			var input mapResultsInput
			err = decodeStrictMapJSON(raw, &input)
			search, areaID, rawGeometry = input.Search, input.AreaID, input.GeoJSON
		}
	}
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	geometry, err := s.resolveMapGeometry(r.Context(), areaID, rawGeometry)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	page, err := s.svc.SearchBusinessesInArea(r.Context(), search, geometry)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{
		Data: page.Results,
		Meta: map[string]any{"total": page.Total, "limit": page.Limit, "offset": page.Offset, "area_kind": geometry.Kind()},
	})
}

func (s *Server) resolveMapGeometry(ctx context.Context, areaID string, raw json.RawMessage) (MapGeometry, error) {
	areaID = strings.TrimSpace(areaID)
	if areaID != "" && len(bytes.TrimSpace(raw)) > 0 {
		return MapGeometry{}, fmt.Errorf("%w: provide area_id or geojson, not both", ErrInvalidMapGeometry)
	}
	if areaID != "" {
		area, err := s.svc.GetSavedArea(ctx, areaID)
		if err != nil {
			return MapGeometry{}, err
		}
		return ParseMapGeometry(area.GeoJSON)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return MapGeometry{}, fmt.Errorf("%w: area_id or geojson is required", ErrInvalidMapGeometry)
	}

	return ParseMapGeometry(raw)
}

func readBoundedMapBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maximumMapGeoJSONBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: request body exceeds the map limit", ErrInvalidMapGeometry)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("%w: request body is empty", ErrInvalidMapGeometry)
	}

	return raw, nil
}

func decodeStrictMapJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: malformed request JSON", ErrInvalidMapGeometry)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: request contains trailing JSON", ErrInvalidMapGeometry)
	}

	return nil
}

func decodeSavedAreaInput(raw []byte, requireGeometry bool) (savedAreaInput, error) {
	var header map[string]json.RawMessage
	if err := json.Unmarshal(raw, &header); err != nil {
		return savedAreaInput{}, fmt.Errorf("%w: malformed request JSON", ErrInvalidMapGeometry)
	}
	var typeName string
	_ = json.Unmarshal(header["type"], &typeName)
	if typeName != "" {
		name := mapFeatureName(raw)
		if name == "" {
			name = "Imported map area"
		}
		return savedAreaInput{Name: name, GeoJSON: append(json.RawMessage(nil), raw...)}, nil
	}
	var input savedAreaInput
	if err := decodeStrictMapJSON(raw, &input); err != nil {
		return savedAreaInput{}, err
	}
	if requireGeometry && len(bytes.TrimSpace(input.GeoJSON)) == 0 {
		return savedAreaInput{}, fmt.Errorf("%w: geojson is required", ErrInvalidMapGeometry)
	}

	return input, nil
}

func decodeMapImport(raw []byte, fallbackName string) ([]json.RawMessage, []string, error) {
	var header struct {
		Type     string            `json:"type"`
		Features []json.RawMessage `json:"features"`
		GeoJSON  json.RawMessage   `json:"geojson"`
		Name     string            `json:"name"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return nil, nil, fmt.Errorf("%w: malformed import JSON", ErrInvalidMapGeometry)
	}
	if len(header.GeoJSON) > 0 {
		name := firstNonEmpty(strings.TrimSpace(header.Name), fallbackName, mapFeatureName(header.GeoJSON), "Imported map area")
		if _, err := ParseMapGeometry(header.GeoJSON); err != nil {
			return nil, nil, err
		}
		return []json.RawMessage{header.GeoJSON}, []string{name}, nil
	}
	if strings.EqualFold(header.Type, "FeatureCollection") {
		if len(header.Features) == 0 || len(header.Features) > maximumMapImportFeatures {
			return nil, nil, fmt.Errorf("%w: feature collection must contain 1 to %d features", ErrInvalidMapGeometry, maximumMapImportFeatures)
		}
		names := make([]string, 0, len(header.Features))
		for index, feature := range header.Features {
			if _, err := ParseMapGeometry(feature); err != nil {
				return nil, nil, fmt.Errorf("feature %d: %w", index+1, err)
			}
			name := mapFeatureName(feature)
			if name == "" {
				name = fmt.Sprintf("Imported map area %d", index+1)
			}
			names = append(names, name)
		}
		return header.Features, names, nil
	}
	if _, err := ParseMapGeometry(raw); err != nil {
		return nil, nil, err
	}
	name := firstNonEmpty(fallbackName, mapFeatureName(raw), "Imported map area")

	return []json.RawMessage{append(json.RawMessage(nil), raw...)}, []string{name}, nil
}

func mapFeatureName(raw []byte) string {
	var feature struct {
		Properties map[string]any `json:"properties"`
	}
	if json.Unmarshal(raw, &feature) != nil {
		return ""
	}

	return mapStringProperty(feature.Properties, "name", "title")
}

func renderMapAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrSavedAreaNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "saved_area_not_found", "Saved map area not found")
	case errors.Is(err, ErrReusableNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "keyword_group_not_found", "Saved keyword group not found")
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrLifecycleNotFound), errors.Is(err, ErrPlacesNotFound):
		renderLocalAPIError(w, http.StatusNotFound, "map_job_not_found", "Map source job not found")
	case errors.Is(err, ErrSavedAreaConflict):
		renderLocalAPIError(w, http.StatusConflict, "saved_area_conflict", "A saved map area with this ID already exists")
	case errors.Is(err, ErrInvalidMapGeometry), errors.Is(err, ErrInvalidResultQuery), errors.Is(err, ErrMapGridTooLarge):
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_map_request", err.Error())
	case errors.Is(err, ErrMapSpatialQueryTooLarge):
		renderLocalAPIError(w, http.StatusRequestEntityTooLarge, "spatial_query_too_large", err.Error())
	case errors.Is(err, ErrMapCellScrapeSelection):
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_cell_selection", err.Error())
	case errors.Is(err, ErrMapCoverageUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "map_coverage_unavailable", "Durable map coverage is unavailable")
	case errors.Is(err, ErrMapStoreUnsupported), errors.Is(err, ErrResultStoreUnsupported):
		renderLocalAPIError(w, http.StatusNotImplemented, "map_store_unavailable", "Local map storage is unavailable")
	default:
		renderLocalAPIError(w, http.StatusInternalServerError, "map_request_failed", "The local map request could not be completed")
	}
}
