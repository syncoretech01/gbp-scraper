package web

import (
	"runtime/debug"
	"strings"
	"sync"
)

// buildVersion is an optional override stamped at link time with
// `-ldflags "-X github.com/gosom/google-maps-scraper/web.buildVersion=v1.17.3"`.
// It is deliberately unexported: a release pipeline sets it, and nothing in
// the product may change it at runtime.
var buildVersion string

// shortRevisionLength keeps a VCS revision readable in a status line while
// staying long enough to identify a commit in this repository.
const shortRevisionLength = 12

// scraperVersion resolves once per process. Reading the build info walks the
// embedded module table, so the result is cached rather than recomputed for
// every page render and every job start.
var scraperVersion = sync.OnceValue(resolveScraperVersion)

// ScraperVersion reports the exact version of the running scraper binary.
//
// Resolution order is deliberate, most authoritative first:
//
//  1. a link-time override, so a tagged release states its own tag;
//  2. the main module version recorded by the Go toolchain, which is set for
//     any binary produced by `go install module@version`;
//  3. the VCS revision embedded by `go build` inside a repository, suffixed
//     with "+dirty" when the tree carried uncommitted changes.
//
// It returns "unknown" only when the binary carries no build information at
// all, which happens for a stripped test binary. It never returns an empty
// string, so a caller can print it without a nil-ish placeholder check.
func ScraperVersion() string {
	return scraperVersion()
}

func resolveScraperVersion() string {
	if version := strings.TrimSpace(buildVersion); version != "" {
		return version
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}

	if version := strings.TrimSpace(info.Main.Version); version != "" && version != "(devel)" {
		return version
	}

	return developmentVersion(info.Settings)
}

// developmentVersion describes a binary built straight from a working copy,
// where the module version is "(devel)" and the commit is the only exact
// identity available.
func developmentVersion(settings []debug.BuildSetting) string {
	revision := ""
	modified := false
	for _, setting := range settings {
		switch setting.Key {
		case "vcs.revision":
			revision = strings.TrimSpace(setting.Value)
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}

	if revision == "" {
		return "unknown"
	}

	if len(revision) > shortRevisionLength {
		revision = revision[:shortRevisionLength]
	}

	if modified {
		return revision + "+dirty"
	}

	return revision
}
