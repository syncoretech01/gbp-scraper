package prospect

import (
	"bytes"
	"compress/gzip"
	_ "embed" // embeds the bundled US ZIP dataset
	"fmt"
	"sort"
	"sync"
)

// embeddedZIPCSVGZ is the gzipped uszips.csv dataset bundled with the
// binary. See ZIPDATA.md for provenance, licensing and the
// regeneration recipe.
//
//go:embed uszips.csv.gz
var embeddedZIPCSVGZ []byte

// embeddedZIPData caches the decoded embedded dataset so the ~41k rows
// are gunzipped and parsed exactly once per process.
var embeddedZIPData struct {
	once   sync.Once
	areas  []ZIPArea
	states []string
	err    error
}

// loadEmbeddedZIPData decodes and caches the embedded dataset on first
// use.
func loadEmbeddedZIPData() {
	embeddedZIPData.once.Do(func() {
		gz, err := gzip.NewReader(bytes.NewReader(embeddedZIPCSVGZ))
		if err != nil {
			embeddedZIPData.err = fmt.Errorf("open embedded zip dataset: %w", err)
			return
		}

		defer gz.Close()

		areas, err := ParseZIPCSV(gz)
		if err != nil {
			embeddedZIPData.err = fmt.Errorf("parse embedded zip dataset: %w", err)
			return
		}

		seen := make(map[string]struct{})

		var states []string

		for _, area := range areas {
			if _, ok := seen[area.State]; ok {
				continue
			}

			seen[area.State] = struct{}{}

			states = append(states, area.State)
		}

		sort.Strings(states)

		embeddedZIPData.areas = areas
		embeddedZIPData.states = states
	})
}

// EmbeddedZIPDataset returns the full embedded US ZIP dataset (about
// 41k ZIP codes covering every state and DC), decoded lazily and
// cached for the lifetime of the process. The returned slice is the
// shared cache, not a copy: callers must treat it as read-only and
// must not mutate its elements. A non-nil error means the embedded
// data could not be decoded (which would indicate a corrupted build);
// callers should fall back to SampleZIPAreas or a user-supplied CSV.
func EmbeddedZIPDataset() ([]ZIPArea, error) {
	loadEmbeddedZIPData()

	return embeddedZIPData.areas, embeddedZIPData.err
}

// EmbeddedZIPAreas is a convenience wrapper around EmbeddedZIPDataset
// that returns nil when the embedded data cannot be decoded. The
// returned slice is shared and must not be mutated.
func EmbeddedZIPAreas() []ZIPArea {
	areas, err := EmbeddedZIPDataset()
	if err != nil {
		return nil
	}

	return areas
}

// EmbeddedZIPStates returns the sorted unique state and territory
// codes present in the embedded dataset, for populating UI dropdowns.
// It returns nil when the embedded data cannot be decoded. The
// returned slice is shared and must not be mutated.
func EmbeddedZIPStates() []string {
	loadEmbeddedZIPData()

	if embeddedZIPData.err != nil {
		return nil
	}

	return embeddedZIPData.states
}
