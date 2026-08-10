package web_test

import (
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

func validJobData() web.JobData {
	return web.JobData{
		Keywords: []string{"dentists"},
		Lang:     "en",
		Zoom:     14,
		Depth:    5,
		MaxTime:  30 * time.Minute,
	}
}

func TestJobDataValidatesGridCoverage(t *testing.T) {
	t.Parallel()

	data := validJobData()
	data.GridBBox = "37.708,-122.515,37.833,-122.354"
	data.GridCellKM = 2.5

	if err := data.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestJobDataRejectsInvalidGridCoverage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*web.JobData)
		wantErr string
	}{
		{
			name: "fast mode conflict",
			mutate: func(data *web.JobData) {
				data.GridBBox = "37.708,-122.515,37.833,-122.354"
				data.GridCellKM = 2.5
				data.FastMode = true
				data.Lat = "37.7749"
				data.Lon = "-122.4194"
			},
			wantErr: "cannot be used together",
		},
		{
			name: "bad bounding box",
			mutate: func(data *web.JobData) {
				data.GridBBox = "not-a-box"
				data.GridCellKM = 2.5
			},
			wantErr: "invalid grid bounding box",
		},
		{
			name: "missing cell size",
			mutate: func(data *web.JobData) {
				data.GridBBox = "37.708,-122.515,37.833,-122.354"
			},
			wantErr: "grid cell size",
		},
		{
			name: "unsafe task count",
			mutate: func(data *web.JobData) {
				data.GridBBox = "37,-123,38,-122"
				data.GridCellKM = 0.25
			},
			wantErr: "maximum is 2500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := validJobData()
			tt.mutate(&data)

			err := data.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
