package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func validJobData() JobData {
	return JobData{
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
		mutate  func(*JobData)
		wantErr string
	}{
		{
			name: "fast mode conflict",
			mutate: func(data *JobData) {
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
			mutate: func(data *JobData) {
				data.GridBBox = "not-a-box"
				data.GridCellKM = 2.5
			},
			wantErr: "invalid grid bounding box",
		},
		{
			name: "missing cell size",
			mutate: func(data *JobData) {
				data.GridBBox = "37.708,-122.515,37.833,-122.354"
			},
			wantErr: "grid cell size",
		},
		{
			name: "unsafe task count",
			mutate: func(data *JobData) {
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

func TestJobDataValidatesIncrementalMode(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"", IncrementalModeNewOnly, IncrementalModeNewChanged} {
		data := validJobData()
		data.IncrementalMode = mode
		if err := data.Validate(); err != nil {
			t.Fatalf("Validate(mode %q) error = %v", mode, err)
		}
	}

	for _, mode := range []string{"full", "NEW_ONLY", "new-changed", "garbage"} {
		data := validJobData()
		data.IncrementalMode = mode
		err := data.Validate()
		if err == nil || !strings.Contains(err.Error(), "rescan mode") {
			t.Fatalf("Validate(mode %q) error = %v, want rescan mode error", mode, err)
		}
	}
}

func TestWizardParsesIncrementalMode(t *testing.T) {
	t.Parallel()

	form := validWizardForm("")
	form.Set("incremental_mode", IncrementalModeNewOnly)
	request := httptest.NewRequest(http.MethodPost, "/app/scrapes", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	job, _, err := parseWizardJob(request)
	if err != nil {
		t.Fatalf("parseWizardJob: %v", err)
	}
	if job.Data.IncrementalMode != IncrementalModeNewOnly {
		t.Fatalf("IncrementalMode = %q, want %q", job.Data.IncrementalMode, IncrementalModeNewOnly)
	}

	form.Set("incremental_mode", "garbage")
	request = httptest.NewRequest(http.MethodPost, "/app/scrapes", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, _, err := parseWizardJob(request); err == nil || !strings.Contains(err.Error(), "rescan mode") {
		t.Fatalf("parseWizardJob(garbage mode) error = %v, want rescan mode error", err)
	}
}
