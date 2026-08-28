package gmaps

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/scrapemate"
)

type SearchJobOptions func(*SearchJob)

type MapLocation struct {
	Lat     float64
	Lon     float64
	ZoomLvl float64
	Radius  float64
}

type MapSearchParams struct {
	Location  MapLocation
	Query     string
	ViewportW int
	ViewportH int
	Hl        string
}

type SearchJob struct {
	scrapemate.Job

	params                  *MapSearchParams
	ExitMonitor             exiter.Exiter
	WriterManagedCompletion bool
	Runtime                 RuntimeOptions
}

func NewSearchJob(params *MapSearchParams, opts ...SearchJobOptions) *SearchJob {
	const (
		defaultPrio       = scrapemate.PriorityMedium
		defaultMaxRetries = 3
		baseURL           = "https://maps.google.com/search"
	)

	job := SearchJob{
		Job: scrapemate.Job{
			ID:         uuid.New().String(),
			Method:     http.MethodGet,
			URL:        baseURL,
			URLParams:  buildGoogleMapsParams(params),
			MaxRetries: defaultMaxRetries,
			Priority:   defaultPrio,
		},
	}

	job.params = params

	for _, opt := range opts {
		opt(&job)
	}

	return &job
}

func WithSearchJobExitMonitor(exitMonitor exiter.Exiter) SearchJobOptions {
	return func(j *SearchJob) {
		j.ExitMonitor = exitMonitor
	}
}

func WithSearchJobWriterManagedCompletion() SearchJobOptions {
	return func(j *SearchJob) {
		j.WriterManagedCompletion = true
	}
}

func (j *SearchJob) ProcessOnFetchError() bool {
	return true
}

func (j *SearchJob) Process(_ context.Context, resp *scrapemate.Response) (any, []scrapemate.IJob, error) {
	defer func() {
		resp.Document = nil
		resp.Body = nil
		resp.Meta = nil
	}()

	if resp.Error != nil {
		if j.ExitMonitor != nil {
			j.ExitMonitor.IncrSeedCompleted(1)
		}

		return nil, nil, resp.Error
	}

	body := removeFirstLine(resp.Body)
	if len(body) == 0 {
		if j.ExitMonitor != nil {
			j.ExitMonitor.IncrSeedCompleted(1)
		}

		return nil, nil, fmt.Errorf("empty response body")
	}

	entries, err := ParseSearchResults(body)
	if err != nil {
		if j.ExitMonitor != nil {
			j.ExitMonitor.IncrSeedCompleted(1)
		}

		return nil, nil, fmt.Errorf("failed to parse search results: %w", err)
	}

	entries = filterAndSortEntriesWithinRadius(entries,
		j.params.Location.Lat,
		j.params.Location.Lon,
		j.params.Location.Radius,
	)

	if j.ExitMonitor != nil {
		j.ExitMonitor.IncrPlacesFound(len(entries))
		j.ExitMonitor.IncrSeedCompleted(1)

		if !j.WriterManagedCompletion {
			j.ExitMonitor.IncrPlacesCompleted(len(entries))
		}
	}

	return entries, nil, nil
}

func removeFirstLine(data []byte) []byte {
	if len(data) == 0 {
		return data
	}

	index := bytes.IndexByte(data, '\n')
	if index == -1 {
		return []byte{}
	}

	return data[index+1:]
}

// Camera bounds for the Maps search request.
//
// The "!1d" field of the pb parameter is the search camera's altitude in
// metres, and it is what actually decides how far from the centre Maps ranks
// results. It used to be hard-coded at legacyCameraAltitudeMetres regardless
// of the radius the operator asked for, so a 15 km Fast search retrieved the
// same ~3.4 km of city a 2 km one did and the radius only ever trimmed the
// answer afterwards. Measured against live Maps from 34.0522,-118.2437 for
// "tattoo shop", the furthest result returned was:
//
//	!1d      7500 ->  2.2 km    !1d  60000 ->  7.7 km
//	!1d     30000 ->  3.9 km    !1d 120000 -> 12.8 km
//	!1d    240000 -> 21.7 km (4 of 20 results fell outside a 15 km radius)
//
// cameraAltitudeFactor = 8 is the calibration that follows: at 8 x radius the
// retrieved set reaches most of the requested radius while still landing
// inside it, so none of the response's twenty slots is spent on a listing the
// radius filter is about to discard.
const (
	legacyCameraAltitudeMetres = 3826.902183192154
	cameraAltitudeFactor       = 8
	minCameraAltitudeMetres    = 800
	maxCameraAltitudeMetres    = 400000

	// searchViewportW and searchViewportH are the pixel viewport reported to
	// Maps. They are fixed because the response does not vary with them; the
	// camera altitude above is the knob that does.
	searchViewportW = 600
	searchViewportH = 800
)

// cameraAltitudeMetres maps a requested search radius onto the camera
// altitude that retrieves it. A run with no radius keeps the historical
// altitude exactly, so nothing about existing no-radius callers changes.
func cameraAltitudeMetres(radius float64) float64 {
	if radius <= 0 {
		return legacyCameraAltitudeMetres
	}

	altitude := radius * cameraAltitudeFactor

	return math.Min(maxCameraAltitudeMetres, math.Max(minCameraAltitudeMetres, altitude))
}

func buildGoogleMapsParams(params *MapSearchParams) map[string]string {
	params.ViewportH = searchViewportH
	params.ViewportW = searchViewportW

	ans := map[string]string{
		"tbm":      "map",
		"authuser": "0",
		"hl":       params.Hl,
		"q":        params.Query,
	}

	pb := fmt.Sprintf("!4m12!1m3!1d%.9f!2d%.4f!3d%.4f!2m3!1f0!2f0!3f0!3m2!1i%d!2i%d!4f%.1f!7i20!8i0"+
		"!10b1!12m22!1m3!18b1!30b1!34e1!2m3!5m1!6e2!20e3!4b0!10b1!12b1!13b1!16b1!17m1!3e1!20m3!5e2!6b1!14b1!46m1!1b0"+
		"!96b1!19m4!2m3!1i360!2i120!4i8",
		cameraAltitudeMetres(params.Location.Radius),
		params.Location.Lon,
		params.Location.Lat,
		params.ViewportW,
		params.ViewportH,
		params.Location.ZoomLvl,
	)

	ans["pb"] = pb

	return ans
}
