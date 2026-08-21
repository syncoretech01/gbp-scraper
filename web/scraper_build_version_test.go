package web

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestScraperVersionAlwaysReportsAnExactIdentity(t *testing.T) {
	t.Parallel()

	version := ScraperVersion()
	if strings.TrimSpace(version) == "" {
		t.Fatal("ScraperVersion() is empty; the monitor would print a blank identity")
	}
	if version == "not recorded" {
		t.Fatal("ScraperVersion() returned the UI placeholder instead of a build identity")
	}
}

func TestDevelopmentVersionShortensRevisionAndMarksDirtyTrees(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings []debug.BuildSetting
		want     string
	}{{
		name:     "no vcs information",
		settings: nil,
		want:     "unknown",
	}, {
		name: "clean tree shortens the revision",
		settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
			{Key: "vcs.modified", Value: "false"},
		},
		want: "0123456789ab",
	}, {
		name: "modified tree is flagged",
		settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef123456789"},
			{Key: "vcs.modified", Value: "true"},
		},
		want: "abcdef123456+dirty",
	}, {
		name: "short revision is kept whole",
		settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc1234"},
		},
		want: "abc1234",
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := developmentVersion(test.settings); got != test.want {
				t.Fatalf("developmentVersion() = %q, want %q", got, test.want)
			}
		})
	}
}
