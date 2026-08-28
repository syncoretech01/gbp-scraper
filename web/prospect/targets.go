package prospect

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
)

// QueryTarget is one unit of prospecting work: a search term paired with the
// geography it must be executed from.
//
// The generator used to return query STRINGS only ("plumber in Los Angeles CA
// 90012"), so 25 ZIPs x 3 synonyms produced 75 queries that all ran from one
// global map centre. The wizard then honestly reported "1 area, 75 searches",
// and every one of those searches was aimed at the same point: the ZIP was in
// the words, never in the request. A QueryTarget carries the ZIP centroid, so
// the same 75 units of work are 75 geographic targets and Fast mode can aim
// each request at the ZIP it names.
//
// Geography is never encoded only inside the query text.
type QueryTarget struct {
	// ID is stable for the same (synonym, ZIP, centroid) triple across
	// reruns and across process restarts, so a checkpoint can identify the
	// target it completed and provenance can point back at it.
	ID string `json:"id"`
	// Query is the search text; Synonym is the category it was built from.
	Query   string `json:"query"`
	Synonym string `json:"synonym"`
	// ZIP, City and State name the area this target covers.
	ZIP   string `json:"zip"`
	City  string `json:"city,omitempty"`
	State string `json:"state,omitempty"`
	// Latitude and Longitude are the ZIP centroid the search runs from.
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	// Population is the ZIP's population where the dataset carries one, and
	// Rank is its 1-based position in the selected set (most populous
	// first). Both are 0 when the source data does not carry a population.
	Population int `json:"population,omitempty"`
	Rank       int `json:"rank,omitempty"`
	// Origin records how this target came to exist.
	Origin string `json:"origin"`
	// ParentID is the target an adaptive expansion grew from, empty for a
	// target that came straight from the ZIP selection.
	ParentID string `json:"parent_id,omitempty"`
}

// Target origins.
const (
	// OriginSelected marks a target built from the operator's ZIP selection.
	OriginSelected = "zip_selection"
	// OriginNeighbour marks a target created by adaptive expansion into a
	// neighbouring ZIP. It is a real geographic target, not a re-run of the
	// parent with different words.
	OriginNeighbour = "zip_neighbour"
)

// maxNeighbourTargets bounds one expansion so an adaptive run cannot grow
// without limit.
const maxNeighbourTargets = 200

// earthRadiusMetres is the mean Earth radius used for centroid distances.
const earthRadiusMetres = 6371000.0

// NewQueryTargetID derives the stable identity of a target from the facts that
// define it. Two runs that select the same ZIP for the same synonym produce
// the same id, and no two different (synonym, ZIP, centroid) triples collide
// in practice.
func NewQueryTargetID(synonym string, zip ZIPArea) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("prospect-target-v1|%s|%s|%.6f|%.6f",
		strings.ToLower(strings.TrimSpace(synonym)),
		strings.TrimSpace(zip.ZIP),
		zip.Latitude,
		zip.Longitude,
	)))

	return hex.EncodeToString(sum[:8])
}

// BuildTargets is BuildQueries with the geography kept. Ordering, query text,
// de-duplication and the 5000-unit bound are identical, so the query lines it
// produces are byte-for-byte the ones BuildQueries produces for the same
// input; only the retained geography is new.
//
// zips is expected in selection order (TopZIPs returns most populous first),
// which is what Rank records.
func BuildTargets(synonyms []string, zips []ZIPArea, includeCityInQuery bool) ([]QueryTarget, error) {
	generated, err := BuildQueries(synonyms, zips, includeCityInQuery)
	if err != nil {
		return nil, err
	}

	rank := make(map[string]int, len(zips))
	for i, zip := range zips {
		if _, seen := rank[zip.ZIP]; !seen {
			rank[zip.ZIP] = i + 1
		}
	}

	synonymOf := synonymIndex(synonyms)

	targets := make([]QueryTarget, 0, len(generated))

	for _, item := range generated {
		targets = append(targets, QueryTarget{
			ID:         NewQueryTargetID(synonymOf(item), item.ZIP),
			Query:      item.Query,
			Synonym:    synonymOf(item),
			ZIP:        item.ZIP.ZIP,
			City:       item.ZIP.City,
			State:      item.ZIP.State,
			Latitude:   item.ZIP.Latitude,
			Longitude:  item.ZIP.Longitude,
			Population: item.ZIP.Population,
			Rank:       rank[item.ZIP.ZIP],
			Origin:     OriginSelected,
		})
	}

	return targets, nil
}

// synonymIndex returns a lookup that recovers which synonym produced a
// generated query. BuildQueries formats the query as "<synonym> ..." with the
// synonym first, so the longest cleaned synonym that prefixes the query is the
// one it came from.
func synonymIndex(synonyms []string) func(GeneratedQuery) string {
	cleaned := make([]string, 0, len(synonyms))

	for _, synonym := range synonyms {
		if synonym = strings.TrimSpace(synonym); synonym != "" {
			cleaned = append(cleaned, synonym)
		}
	}

	sort.SliceStable(cleaned, func(i, j int) bool { return len(cleaned[i]) > len(cleaned[j]) })

	return func(item GeneratedQuery) string {
		lower := strings.ToLower(item.Query)
		for _, synonym := range cleaned {
			if strings.HasPrefix(lower, strings.ToLower(synonym)) {
				return synonym
			}
		}

		return ""
	}
}

// TargetLines flattens targets to their query strings in order, matching
// QueryLines so the two generators stay interchangeable for the keyword box.
func TargetLines(targets []QueryTarget) []string {
	lines := make([]string, len(targets))
	for i, target := range targets {
		lines[i] = target.Query
	}

	return lines
}

// TargetCentre is the population-weighted centroid of a target set, for the
// job's overall map centre. Each target still runs from its own ZIP centroid;
// this is only what the map opens on.
func TargetCentre(targets []QueryTarget) (lat, lon float64) {
	zips := make([]ZIPArea, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))

	for _, target := range targets {
		if _, ok := seen[target.ZIP]; ok {
			continue
		}

		seen[target.ZIP] = struct{}{}

		zips = append(zips, ZIPArea{
			ZIP: target.ZIP, City: target.City, State: target.State,
			Latitude: target.Latitude, Longitude: target.Longitude, Population: target.Population,
		})
	}

	return Centre(zips)
}

// NeighbourTargets expands one saturated target into its neighbouring ZIPs.
//
// Adaptive expansion has to create REAL geographic targets, not extra query
// text: each returned target carries the neighbour ZIP's own centroid, its own
// stable id, and a ParentID pointing at the target that triggered the
// expansion, so a resumed or re-run job can tell the two apart.
//
// candidates is the ZIP universe to expand into; covered is every ZIP the job
// already has a target for (the expansion never revisits one). Neighbours are
// ordered by distance from the parent centroid, ties broken by ZIP, so the
// expansion is deterministic. limit is bounded to 1..200.
func NeighbourTargets(parent QueryTarget, candidates []ZIPArea, covered []string, radiusMetres float64, limit int) []QueryTarget {
	if limit < 1 {
		return nil
	}

	if limit > maxNeighbourTargets {
		limit = maxNeighbourTargets
	}

	if radiusMetres <= 0 {
		return nil
	}

	skip := make(map[string]struct{}, len(covered)+1)
	skip[parent.ZIP] = struct{}{}

	for _, zip := range covered {
		skip[strings.TrimSpace(zip)] = struct{}{}
	}

	type scored struct {
		area     ZIPArea
		distance float64
	}

	near := make([]scored, 0, len(candidates))

	for _, area := range candidates {
		if _, ok := skip[area.ZIP]; ok {
			continue
		}

		distance := DistanceMetres(parent.Latitude, parent.Longitude, area.Latitude, area.Longitude)
		if distance > radiusMetres {
			continue
		}

		near = append(near, scored{area: area, distance: distance})
	}

	sort.SliceStable(near, func(i, j int) bool {
		if near[i].distance != near[j].distance {
			return near[i].distance < near[j].distance
		}

		return near[i].area.ZIP < near[j].area.ZIP
	})

	if len(near) > limit {
		near = near[:limit]
	}

	targets := make([]QueryTarget, 0, len(near))

	for _, item := range near {
		query := strings.Replace(parent.Query, parent.ZIP, item.area.ZIP, 1)
		if !strings.Contains(parent.Query, parent.ZIP) {
			query = strings.Join(strings.Fields(parent.Synonym+" "+item.area.ZIP), " ")
		}

		targets = append(targets, QueryTarget{
			ID:         NewQueryTargetID(parent.Synonym, item.area),
			Query:      query,
			Synonym:    parent.Synonym,
			ZIP:        item.area.ZIP,
			City:       item.area.City,
			State:      item.area.State,
			Latitude:   item.area.Latitude,
			Longitude:  item.area.Longitude,
			Population: item.area.Population,
			Origin:     OriginNeighbour,
			ParentID:   parent.ID,
		})
	}

	return targets
}

// DistanceMetres is the great-circle distance between two coordinates.
func DistanceMetres(lat1, lon1, lat2, lon2 float64) float64 {
	phi1 := lat1 * math.Pi / 180
	phi2 := lat2 * math.Pi / 180
	deltaPhi := (lat2 - lat1) * math.Pi / 180
	deltaLambda := (lon2 - lon1) * math.Pi / 180

	a := math.Sin(deltaPhi/2)*math.Sin(deltaPhi/2) +
		math.Cos(phi1)*math.Cos(phi2)*math.Sin(deltaLambda/2)*math.Sin(deltaLambda/2)

	return 2 * earthRadiusMetres * math.Asin(math.Min(1, math.Sqrt(a)))
}

// DistinctTargetZIPs counts the geographic targets in a set, which is the
// number the wizard must report as "areas". Counting query strings instead is
// what made a 25-ZIP plan report one area.
func DistinctTargetZIPs(targets []QueryTarget) int {
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		seen[target.ZIP] = struct{}{}
	}

	return len(seen)
}
