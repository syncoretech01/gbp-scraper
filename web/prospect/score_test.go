package prospect

import (
	"reflect"
	"strings"
	"testing"
)

func TestDefaultScoreWeightsValidate(t *testing.T) {
	if err := DefaultScoreWeights().Validate(); err != nil {
		t.Fatalf("DefaultScoreWeights().Validate() = %v, want nil", err)
	}
}

func TestScoreWeightsValidateRejections(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*ScoreWeights)
		wantSub string
	}{
		{
			name:    "status weight above 100",
			mutate:  func(w *ScoreWeights) { w.Dead = 101 },
			wantSub: "dead",
		},
		{
			name:    "negative non-live weight",
			mutate:  func(w *ScoreWeights) { w.PhonePresent = -1 },
			wantSub: "phone_present",
		},
		{
			name:    "live below -100",
			mutate:  func(w *ScoreWeights) { w.Live = -101 },
			wantSub: "live",
		},
		{
			name:    "rating threshold above 5",
			mutate:  func(w *ScoreWeights) { w.RatingThreshold = 5.5 },
			wantSub: "rating_threshold",
		},
		{
			name:    "negative review threshold",
			mutate:  func(w *ScoreWeights) { w.ReviewThreshold = -1 },
			wantSub: "review_threshold",
		},
		{
			name:    "negative copyright age",
			mutate:  func(w *ScoreWeights) { w.CopyrightAgeYears = -2 },
			wantSub: "copyright_age_years",
		},
		{
			name:    "tiers not strictly descending",
			mutate:  func(w *ScoreWeights) { w.TierB = w.TierA },
			wantSub: "descend strictly",
		},
		{
			name:    "tier A above 100",
			mutate:  func(w *ScoreWeights) { w.TierA = 101 },
			wantSub: "tier_a",
		},
		{
			name:    "negative tier E",
			mutate:  func(w *ScoreWeights) { w.TierE = -1 },
			wantSub: "tier_e",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			weights := DefaultScoreWeights()
			tc.mutate(&weights)

			err := weights.Validate()
			if err == nil {
				t.Fatal("Validate() = nil, want error")
			}

			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("Validate() error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestScoreReasonsAreExplanatory(t *testing.T) {
	weights := DefaultScoreWeights()
	signals := Signals{
		WebsiteURL:   "",
		PhonePresent: true,
		EmailFound:   true,
		MXPresent:    true,
		Rating:       4.6,
		ReviewCount:  120,
	}

	score, tier, reasons := Score(StatusNoWebsite, signals, weights)

	// 30 + 10 + 8 + 4 + 8 + 6 = 66 (no audit -> NoAdsTag stays silent).
	if score != 66 {
		t.Fatalf("score = %v, want 66", score)
	}

	if tier != "B" {
		t.Fatalf("tier = %q, want B", tier)
	}

	if len(reasons) != 6 {
		t.Fatalf("got %d reasons, want 6: %+v", len(reasons), reasons)
	}

	wantDetails := []string{
		"no website listed",
		"phone number available",
		"email address found",
		"MX records present",
		"strong reputation",
		"120 reviews",
	}

	for i, want := range wantDetails {
		if reasons[i].Contribution == 0 {
			t.Errorf("reason %d has zero contribution: %+v", i, reasons[i])
		}

		if reasons[i].Detail == "" || !strings.Contains(reasons[i].Detail, want) {
			t.Errorf("reason %d detail %q does not contain %q", i, reasons[i].Detail, want)
		}
	}

	if reasons[0].Signal != StatusNoWebsite {
		t.Errorf("status reason signal = %q, want %q", reasons[0].Signal, StatusNoWebsite)
	}
}

func TestScoreMXAbsentContributesNothing(t *testing.T) {
	weights := DefaultScoreWeights()

	_, _, reasons := Score(StatusNoWebsite, Signals{MXPresent: false, PhonePresent: true}, weights)

	for _, r := range reasons {
		if r.Signal == SignalMXPresent {
			t.Fatalf("MX_PRESENT reason found although MXPresent=false: %+v", r)
		}
	}
}

func TestScoreSSLBrokenUrgentDetail(t *testing.T) {
	_, _, reasons := Score(StatusSSLBroken, Signals{}, DefaultScoreWeights())

	if len(reasons) != 1 {
		t.Fatalf("got %d reasons, want 1", len(reasons))
	}

	if !strings.Contains(reasons[0].Detail, "certificate is broken — urgent") {
		t.Fatalf("detail = %q, want it to flag the broken certificate as urgent", reasons[0].Detail)
	}
}

func TestScoreBusinessSiteEdgeCaseDetail(t *testing.T) {
	signals := Signals{WebsiteURL: "https://some-biz.business.site"}

	status, conclusive := Classify(signals)
	if status != StatusFreeBuilder || !conclusive {
		t.Fatalf("Classify() = (%q, %v), want (%q, true)", status, conclusive, StatusFreeBuilder)
	}

	_, _, reasons := Score(status, signals, DefaultScoreWeights())

	if len(reasons) == 0 {
		t.Fatal("expected at least the status reason")
	}

	if !strings.Contains(reasons[0].Detail, "business.site") || !strings.Contains(reasons[0].Detail, "2+ years") {
		t.Fatalf("business.site detail = %q, want the owner-absent-2+-years wording", reasons[0].Detail)
	}

	// An ordinary free builder keeps the generic wording.
	_, _, generic := Score(StatusFreeBuilder, Signals{WebsiteURL: "https://x.wixsite.com/a"}, DefaultScoreWeights())
	if strings.Contains(generic[0].Detail, "business.site") {
		t.Fatalf("wixsite detail %q should not mention business.site", generic[0].Detail)
	}
}

func TestScoreOldCopyrightAndNoAdsTag(t *testing.T) {
	weights := DefaultScoreWeights()
	signals := Signals{
		WebsiteURL:     "http://example.com",
		AuditPerformed: true,
		Reachable:      true,
		StatusCode:     200,
		ContentBytes:   4000,
		CopyrightYear:  2021,
		CurrentYear:    2026,
		HasAdsTag:      false,
	}

	score, _, reasons := Score(StatusNoHTTPS, signals, weights)

	// 12 (NO_HTTPS) + 6 (old copyright) + 4 (no ads tag) = 22.
	if score != 22 {
		t.Fatalf("score = %v, want 22", score)
	}

	var sawCopyright, sawNoAds bool

	for _, r := range reasons {
		if r.Signal == SignalOldCopyright {
			sawCopyright = true

			if !strings.Contains(r.Detail, "2021") {
				t.Errorf("old copyright detail %q should mention the year", r.Detail)
			}
		}

		if r.Signal == SignalNoAdsTag {
			sawNoAds = true
		}
	}

	if !sawCopyright || !sawNoAds {
		t.Fatalf("missing expected reasons, got %+v", reasons)
	}

	// With a tracker installed NoAdsTag must stay silent.
	signals.HasAdsTag = true

	score, _, _ = Score(StatusNoHTTPS, signals, weights)
	if score != 18 {
		t.Fatalf("score with ads tag = %v, want 18", score)
	}

	// Without an audit the absence of a tracker means nothing.
	signals.AuditPerformed = false
	signals.HasAdsTag = false

	score, _, _ = Score(StatusNoHTTPS, signals, weights)
	if score != 18 {
		t.Fatalf("score without audit = %v, want 18", score)
	}
}

func TestScoreDeterminism(t *testing.T) {
	weights := DefaultScoreWeights()
	signals := Signals{
		WebsiteURL:     "https://dead.example.com",
		AuditPerformed: true,
		Reachable:      false,
		PhonePresent:   true,
		EmailFound:     true,
		MXPresent:      true,
		Rating:         4.9,
		ReviewCount:    300,
		CopyrightYear:  2020,
		CurrentYear:    2026,
	}

	firstScore, firstTier, firstReasons := Score(StatusDead, signals, weights)

	for i := 0; i < 10; i++ {
		score, tier, reasons := Score(StatusDead, signals, weights)
		if score != firstScore || tier != firstTier || !reflect.DeepEqual(reasons, firstReasons) {
			t.Fatalf("run %d differs: (%v, %q, %+v) vs (%v, %q, %+v)",
				i, score, tier, reasons, firstScore, firstTier, firstReasons)
		}
	}
}

func TestScoreClamping(t *testing.T) {
	weights := DefaultScoreWeights()
	weights.Dead = 100
	weights.PhonePresent = 100

	score, tier, reasons := Score(StatusDead, Signals{PhonePresent: true, AuditPerformed: true}, weights)
	if score != 100 {
		t.Fatalf("score = %v, want clamp to 100", score)
	}

	if tier != "A" {
		t.Fatalf("tier = %q, want A", tier)
	}

	// Reasons keep the raw pre-clamp contributions.
	var sum float64
	for _, r := range reasons {
		sum += r.Contribution
	}

	if sum <= 100 {
		t.Fatalf("raw contributions sum %v should exceed the clamp", sum)
	}

	// Negative totals clamp to zero.
	weights = DefaultScoreWeights()
	weights.Live = -50

	score, tier, reasons = Score(StatusLive, Signals{HasAdsTag: true, AuditPerformed: true}, weights)
	if score != 0 {
		t.Fatalf("score = %v, want clamp to 0", score)
	}

	if tier != "F" {
		t.Fatalf("tier = %q, want F", tier)
	}

	if len(reasons) != 1 || reasons[0].Contribution != -50 {
		t.Fatalf("reasons = %+v, want the single -50 live contribution", reasons)
	}
}

func TestScoreTierBoundaries(t *testing.T) {
	weights := DefaultScoreWeights()

	cases := []struct {
		statusWeight float64
		wantTier     string
	}{
		{70, "A"},
		{69.9, "B"},
		{55, "B"},
		{54.9, "C"},
		{40, "C"},
		{39.9, "D"},
		{25, "D"},
		{24.9, "E"},
		{10, "E"},
		{9.9, "F"},
		{0, "F"},
	}

	for _, tc := range cases {
		weights.NoWebsite = tc.statusWeight

		score, tier, _ := Score(StatusNoWebsite, Signals{}, weights)
		if score != tc.statusWeight {
			t.Fatalf("score = %v, want %v", score, tc.statusWeight)
		}

		if tier != tc.wantTier {
			t.Errorf("score %v -> tier %q, want %q", tc.statusWeight, tier, tc.wantTier)
		}
	}
}

func TestScoreZeroWeightsYieldNoReasons(t *testing.T) {
	score, tier, reasons := Score(StatusLive, Signals{}, DefaultScoreWeights())

	if score != 0 || tier != "F" || len(reasons) != 0 {
		t.Fatalf("Score(LIVE, zero signals) = (%v, %q, %+v), want (0, F, none)", score, tier, reasons)
	}
}
