package web

import (
	"testing"
	"time"
)

// TestResolveWebsiteStateSeparatesUncheckedFromFailed is the regression guard
// for the defect the canonical state exists to fix: an audit that ran and
// failed used to be indistinguishable from a website nobody had ever looked
// at, because both left businesses.website_status at its 'unknown' default.
func TestResolveWebsiteStateSeparatesUncheckedFromFailed(t *testing.T) {
	t.Parallel()

	completedAt := time.Date(2026, time.August, 20, 9, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		evidence  WebsiteStateEvidence
		want      string
		checked   bool
		auditable bool
		platform  string
	}{
		{
			name:     "no website listed",
			evidence: WebsiteStateEvidence{},
			want:     WebsiteStateNoWebsite,
		},
		{
			name: "website field points back at maps",
			evidence: WebsiteStateEvidence{
				Website: "https://maps.app.goo.gl/abc123",
				MapsURL: "https://maps.app.goo.gl/abc123",
			},
			want: WebsiteStateNoWebsite,
		},
		{
			name:     "instagram listing is never an owned website",
			evidence: WebsiteStateEvidence{Website: "https://www.instagram.com/brownpridetattooshop?hl=en"},
			want:     WebsiteStateSocialOnly,
			platform: "instagram",
		},
		{
			name: "a reachable social profile is still social only",
			evidence: WebsiteStateEvidence{
				Website:         "https://www.instagram.com/someshop",
				LegacyStatus:    "active",
				AuditCompleted:  true,
				AuditReachable:  true,
				AuditStatusCode: 200,
			},
			want:     WebsiteStateSocialOnly,
			platform: "instagram",
		},
		{
			name:      "never audited",
			evidence:  WebsiteStateEvidence{Website: "https://tintarebelde.com", LegacyStatus: "unknown"},
			want:      WebsiteStateNeverChecked,
			auditable: true,
		},
		{
			name: "queued task",
			evidence: WebsiteStateEvidence{
				Website: "https://tintarebelde.com", LegacyStatus: "unknown", TaskState: "queued",
			},
			want:      WebsiteStateQueued,
			auditable: true,
		},
		{
			name: "running task",
			evidence: WebsiteStateEvidence{
				Website: "https://tintarebelde.com", LegacyStatus: "unknown", TaskState: "running",
			},
			want:      WebsiteStateChecking,
			auditable: true,
		},
		{
			name: "dns failure is an error, never never-checked",
			evidence: WebsiteStateEvidence{
				Website:          "https://www.floatingstreams.com",
				LegacyStatus:     "unknown",
				AuditCompleted:   true,
				AuditStatusCode:  0,
				AuditError:       `resolve host "www.floatingstreams.com": lookup failed: no such host`,
				AuditCompletedAt: completedAt,
			},
			want:      WebsiteStateError,
			checked:   true,
			auditable: true,
		},
		{
			name: "http error status is dead",
			evidence: WebsiteStateEvidence{
				Website:          "https://gone.example",
				AuditCompleted:   true,
				AuditReachable:   true,
				AuditStatusCode:  410,
				AuditCompletedAt: completedAt,
			},
			want:      WebsiteStateDead,
			checked:   true,
			auditable: true,
		},
		{
			name: "serving site is live",
			evidence: WebsiteStateEvidence{
				Website:          "https://reservoirtattoostudio.com",
				AuditCompleted:   true,
				AuditReachable:   true,
				AuditStatusCode:  200,
				AuditCompletedAt: completedAt,
			},
			want:      WebsiteStateLive,
			checked:   true,
			auditable: true,
		},
		{
			name: "stored error status survives a pruned audit row",
			evidence: WebsiteStateEvidence{
				Website: "https://madrabbit.com", LegacyStatus: "error",
			},
			want:      WebsiteStateError,
			checked:   true,
			auditable: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := ResolveWebsiteState(test.evidence)
			if got.State != test.want {
				t.Fatalf("state = %q, want %q (reason %q)", got.State, test.want, got.Reason)
			}
			if got.Checked != test.checked {
				t.Fatalf("checked = %v, want %v", got.Checked, test.checked)
			}
			if got.Auditable != test.auditable {
				t.Fatalf("auditable = %v, want %v", got.Auditable, test.auditable)
			}
			if got.Platform != test.platform {
				t.Fatalf("platform = %q, want %q", got.Platform, test.platform)
			}
			if got.Label == "" || got.Reason == "" {
				t.Fatalf("state %q has no operator-facing label/reason: %+v", got.State, got)
			}
		})
	}
}

// TestResolveWebsiteStateReusesDomainEvidence proves a second business on an
// already audited domain adopts that observation and says where it came from.
func TestResolveWebsiteStateReusesDomainEvidence(t *testing.T) {
	t.Parallel()

	resolution := ResolveWebsiteState(WebsiteStateEvidence{
		Website:          "https://shared-domain.example/branch-2",
		AuditCompleted:   true,
		AuditReachable:   true,
		AuditStatusCode:  200,
		AuditCompletedAt: time.Date(2026, time.August, 20, 9, 0, 0, 0, time.UTC),
		EvidenceDomain:   "shared-domain.example",
	})
	if resolution.State != WebsiteStateLive {
		t.Fatalf("state = %q, want %q", resolution.State, WebsiteStateLive)
	}
	if resolution.ReusedFromDomain != "shared-domain.example" {
		t.Fatalf("reused domain = %q, want shared-domain.example", resolution.ReusedFromDomain)
	}
	if resolution.CheckedAt == nil {
		t.Fatal("reused evidence must carry the audit timestamp")
	}
}

// TestWebsiteHealthRefusesToGradeUnknownState is the regression guard for
// score #2: website health must never be invented for a state that carries no
// measurement of an owned site.
func TestWebsiteHealthRefusesToGradeUnknownState(t *testing.T) {
	t.Parallel()

	for _, state := range []string{
		WebsiteStateNeverChecked,
		WebsiteStateQueued,
		WebsiteStateChecking,
		WebsiteStateNoWebsite,
		WebsiteStateSocialOnly,
		WebsiteStateError,
	} {
		report := ScoreWebsiteHealth("biz-1", WebsiteHealthEvidence{
			State: state,
			// Deliberately perfect-looking evidence: it must be ignored,
			// because the state says nothing was measured on an owned site.
			Reachable: true, StatusCode: 200, HTTPS: true, TLSValid: true,
			ResponseMS: 90, PageTitle: "T", MetaDescription: "D", MobileViewport: true,
		})
		if report.Available {
			t.Fatalf("state %s produced a health score of %v; it must be unavailable", state, report.Score)
		}
		if report.Score != 0 || report.Grade != "" {
			t.Fatalf("state %s leaked a score/grade: %+v", state, report)
		}
		if report.Reason == "" {
			t.Fatalf("state %s must explain why no grade exists", state)
		}
	}
}

// TestWebsiteHealthIsDeterministicAndExplainable pins the model: the same
// evidence always produces the same number, and the number is exactly the sum
// of its published checks.
func TestWebsiteHealthIsDeterministicAndExplainable(t *testing.T) {
	t.Parallel()

	evidence := WebsiteHealthEvidence{
		State: WebsiteStateLive, Reachable: true, StatusCode: 200,
		HTTPS: true, TLSValid: true, ResponseMS: 900,
		LinksChecked: 10, BrokenLinks: 0,
		PageTitle: "Reservoir Tattoo Studio", MetaDescription: "Los Angeles tattoo studio",
		MobileViewport: true, CopyrightYear: 2026, CurrentYear: 2026,
		CompletedAt: time.Date(2026, time.August, 20, 9, 0, 0, 0, time.UTC),
	}

	first := ScoreWebsiteHealth("biz-1", evidence)
	second := ScoreWebsiteHealth("biz-1", evidence)
	if first.Score != second.Score || first.Grade != second.Grade {
		t.Fatalf("health scoring is not deterministic: %v vs %v", first, second)
	}
	if first.Score != 100 || first.Grade != "healthy" {
		t.Fatalf("perfect evidence scored %v (%s), want 100 healthy", first.Score, first.Grade)
	}
	if first.RuleVersion != WebsiteHealthRuleVersion {
		t.Fatalf("rule version = %q, want %q", first.RuleVersion, WebsiteHealthRuleVersion)
	}

	total := 0.0
	for _, check := range first.Checks {
		total += check.Points
		if check.Detail == "" {
			t.Fatalf("check %q has no explanation", check.Check)
		}
	}
	if total != first.Score {
		t.Fatalf("checks sum to %v but the score is %v", total, first.Score)
	}

	// A site that answers with an error page is measured, not unknown, so it
	// gets a real (low) grade rather than an absent one.
	deadEvidence := evidence
	deadEvidence.State = WebsiteStateDead
	deadEvidence.StatusCode = 503
	dead := ScoreWebsiteHealth("biz-1", deadEvidence)
	if !dead.Available {
		t.Fatal("a dead site is a measured condition and must carry a grade")
	}
	if dead.Score >= first.Score {
		t.Fatalf("dead score %v should be below live score %v", dead.Score, first.Score)
	}
}

// TestNormalizeWebsiteSweepStatesRejectsUnauditableTargets keeps the bulk
// operation from queueing HTTP probes for things that are not owned websites.
func TestNormalizeWebsiteSweepStatesRejectsUnauditableTargets(t *testing.T) {
	t.Parallel()

	defaults, err := normalizeWebsiteSweepStates(nil)
	if err != nil {
		t.Fatalf("normalizeWebsiteSweepStates(nil) error = %v", err)
	}
	if len(defaults) != 1 || defaults[0] != WebsiteStateNeverChecked {
		t.Fatalf("default sweep states = %v, want [%s]", defaults, WebsiteStateNeverChecked)
	}

	for _, state := range []string{WebsiteStateSocialOnly, WebsiteStateNoWebsite, "NONSENSE"} {
		if _, err := normalizeWebsiteSweepStates([]string{state}); err == nil {
			t.Fatalf("state %q was accepted as a sweep target", state)
		}
	}

	deduped, err := normalizeWebsiteSweepStates([]string{"never_checked", "NEVER_CHECKED", "error"})
	if err != nil {
		t.Fatalf("normalizeWebsiteSweepStates() error = %v", err)
	}
	if len(deduped) != 2 {
		t.Fatalf("deduped states = %v, want two entries", deduped)
	}
}
