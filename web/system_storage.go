package web

import (
	"context"
	"errors"
	"os"
)

type workspaceStorageSnapshot struct {
	DataBytes        int64 `json:"data_bytes"`
	ExportsBytes     int64 `json:"exports_bytes"`
	ScreenshotsBytes int64 `json:"screenshots_bytes"`
	LogsBytes        int64 `json:"logs_bytes"`
	BackupsBytes     int64 `json:"backups_bytes"`
	CacheBytes       int64 `json:"cache_bytes"`
	TemporaryBytes   int64 `json:"temporary_bytes"`
	ScannedEntries   int   `json:"scanned_entries"`
	Truncated        bool  `json:"truncated"`
}

func (s *Server) workspaceStorageUsage(ctx context.Context) (workspaceStorageSnapshot, error) {
	if s == nil || s.svc == nil {
		return workspaceStorageSnapshot{}, nil
	}
	preferences := defaultStoragePreferences()
	if values, err := s.svc.LoadSettings(ctx); err == nil {
		preferences = storagePreferencesFromMap(values)
	} else if !errors.Is(err, ErrSettingsUnsupported) {
		return workspaceStorageSnapshot{}, err
	}

	snapshot := workspaceStorageSnapshot{}
	var err error
	snapshot.DataBytes, snapshot.ScannedEntries, snapshot.Truncated, err = boundedDirectorySize(
		ctx, s.svc.dataFolder, maximumStorageScanEntries,
	)
	if err != nil {
		return workspaceStorageSnapshot{}, err
	}

	readDirectory := func(relative string) (int64, error) {
		path, pathErr := safeDataPath(s.svc.dataFolder, relative)
		if pathErr != nil {
			return 0, pathErr
		}
		bytes, _, _, sizeErr := boundedDirectorySize(ctx, path, maximumStorageScanEntries)
		if errors.Is(sizeErr, os.ErrNotExist) {
			return 0, nil
		}

		return bytes, sizeErr
	}

	for _, directory := range []struct {
		relative    string
		destination *int64
	}{
		{relative: preferences.ExportsDirectory, destination: &snapshot.ExportsBytes},
		{relative: preferences.ScreenshotsDirectory, destination: &snapshot.ScreenshotsBytes},
		{relative: preferences.LogsDirectory, destination: &snapshot.LogsBytes},
		{relative: preferences.BackupsDirectory, destination: &snapshot.BackupsBytes},
		{relative: preferences.TemporaryDirectory, destination: &snapshot.TemporaryBytes},
		{relative: "map-tiles", destination: &snapshot.CacheBytes},
	} {
		value, readErr := readDirectory(directory.relative)
		if readErr != nil {
			return workspaceStorageSnapshot{}, readErr
		}
		*directory.destination = value
	}

	return snapshot, nil
}
