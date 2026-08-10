package web

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gosom/google-maps-scraper/grid"
)

var jobs []Job

const (
	StatusPending = "pending"
	StatusWorking = "working"
	StatusOK      = "ok"
	StatusFailed  = "failed"
)

type SelectParams struct {
	Status string
	Limit  int
}

type JobRepository interface {
	Get(context.Context, string) (Job, error)
	Create(context.Context, *Job) error
	Delete(context.Context, string) error
	Select(context.Context, SelectParams) ([]Job, error)
	Update(context.Context, *Job) error
}

type Job struct {
	ID     string
	Name   string
	Date   time.Time
	Status string
	Data   JobData
}

func (j *Job) Validate() error {
	if j.ID == "" {
		return errors.New("missing id")
	}

	if j.Name == "" {
		return errors.New("missing name")
	}

	if j.Status == "" {
		return errors.New("missing status")
	}

	if j.Date.IsZero() {
		return errors.New("missing date")
	}

	if err := j.Data.Validate(); err != nil {
		return err
	}

	return nil
}

type JobData struct {
	Keywords      []string      `json:"keywords"`
	Lang          string        `json:"lang"`
	Zoom          int           `json:"zoom"`
	Lat           string        `json:"lat"`
	Lon           string        `json:"lon"`
	LocationLabel string        `json:"location_label,omitempty"`
	FastMode      bool          `json:"fast_mode"`
	Radius        int           `json:"radius"`
	Depth         int           `json:"depth"`
	Email         bool          `json:"email"`
	ExtraReviews  bool          `json:"extra_reviews"`
	MaxTime       time.Duration `json:"max_time"`
	Concurrency   int           `json:"concurrency,omitempty"`
	BrowserPool   int           `json:"browser_pool_size,omitempty"`
	PagesBrowser  int           `json:"pages_per_browser,omitempty"`
	ProxyPoolID   string        `json:"proxy_pool_id,omitempty"`
	Proxies       []string      `json:"proxies"`
	GridBBox      string        `json:"grid_bbox,omitempty"`
	GridCellKM    float64       `json:"grid_cell_km,omitempty"`
}

func (d *JobData) Validate() error {
	if len(d.Keywords) == 0 {
		return errors.New("missing keywords")
	}

	if d.Lang == "" {
		return errors.New("missing lang")
	}

	if len(d.Lang) != 2 {
		return errors.New("invalid lang")
	}

	if d.Depth < 1 {
		return errors.New("missing depth")
	}

	if d.MaxTime <= 0 {
		return errors.New("missing max time")
	}

	if d.Concurrency < 0 || d.Concurrency > 64 {
		return errors.New("concurrency must be between 1 and 64 when set")
	}

	if d.BrowserPool < 0 || d.BrowserPool > 32 {
		return errors.New("browser pool size must be between 1 and 32 when set")
	}

	if d.PagesBrowser < 0 || d.PagesBrowser > 16 {
		return errors.New("pages per browser must be between 1 and 16 when set")
	}

	if d.Zoom < 1 || d.Zoom > 21 {
		return errors.New("zoom must be between 1 and 21")
	}

	if d.GridBBox != "" {
		if d.FastMode {
			return errors.New("grid coverage and fast mode cannot be used together")
		}

		if d.GridCellKM <= 0 {
			return errors.New("grid cell size must be greater than 0")
		}

		bbox, err := grid.ParseBoundingBox(d.GridBBox)
		if err != nil {
			return fmt.Errorf("invalid grid bounding box: %w", err)
		}

		const maxGridCells = 2500

		if count := grid.EstimateCellCount(bbox, d.GridCellKM); count > maxGridCells {
			return fmt.Errorf("grid creates %d cells; maximum is %d", count, maxGridCells)
		}
	}

	if d.FastMode && (d.Lat == "" || d.Lon == "") {
		return errors.New("missing geo coordinates")
	}

	if d.FastMode && d.Radius <= 0 {
		return errors.New("fast mode radius must be greater than 0")
	}

	return nil
}
