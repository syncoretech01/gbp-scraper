package prospect

import "fmt"

// Signal names used in Reason entries for the non-status signals. The
// status contribution uses the status constant itself as its signal.
const (
	SignalPhonePresent = "PHONE_PRESENT"
	SignalEmailFound   = "EMAIL_FOUND"
	SignalMXPresent    = "MX_PRESENT"
	SignalGoodRating   = "GOOD_RATING"
	SignalManyReviews  = "MANY_REVIEWS"
	SignalOldCopyright = "OLD_COPYRIGHT"
	SignalNoAdsTag     = "NO_ADS_TAG"
)

// ScoreWeights configures the worth-calling score: one weight per
// signal, the thresholds that arm the rating/review/copyright signals,
// and the tier cut-offs. All weights are additive points; Live is the
// only weight allowed to be negative (a healthy website can subtract
// from the score).
type ScoreWeights struct {
	NoWebsite   float64 `json:"no_website"`
	SocialOnly  float64 `json:"social_only"`
	Dead        float64 `json:"dead"`
	Parked      float64 `json:"parked"`
	SSLBroken   float64 `json:"ssl_broken"`
	FreeBuilder float64 `json:"free_builder"`
	NoHTTPS     float64 `json:"no_https"`
	Live        float64 `json:"live"`

	PhonePresent float64 `json:"phone_present"`
	EmailFound   float64 `json:"email_found"`
	MXPresent    float64 `json:"mx_present"`
	GoodRating   float64 `json:"good_rating"`
	ManyReviews  float64 `json:"many_reviews"`
	OldCopyright float64 `json:"old_copyright"`
	NoAdsTag     float64 `json:"no_ads_tag"`

	RatingThreshold   float64 `json:"rating_threshold"`
	ReviewThreshold   int64   `json:"review_threshold"`
	CopyrightAgeYears int     `json:"copyright_age_years"`

	TierA float64 `json:"tier_a"`
	TierB float64 `json:"tier_b"`
	TierC float64 `json:"tier_c"`
	TierD float64 `json:"tier_d"`
	TierE float64 `json:"tier_e"`
}

// DefaultScoreWeights returns the recommended starting configuration.
// The status weights rank how actionable each website problem is for an
// outreach call; the reputation signals reward businesses that are
// clearly real and reachable.
func DefaultScoreWeights() ScoreWeights {
	return ScoreWeights{
		NoWebsite:   30,
		SocialOnly:  26,
		Dead:        35,
		Parked:      28,
		SSLBroken:   32,
		FreeBuilder: 20,
		NoHTTPS:     12,
		Live:        0,

		PhonePresent: 10,
		EmailFound:   8,
		MXPresent:    4,
		GoodRating:   8,
		ManyReviews:  6,
		OldCopyright: 6,
		NoAdsTag:     4,

		RatingThreshold:   4.0,
		ReviewThreshold:   25,
		CopyrightAgeYears: 2,

		TierA: 70,
		TierB: 55,
		TierC: 40,
		TierD: 25,
		TierE: 10,
	}
}

// Validate checks that every weight stays within a 0..100 magnitude
// (Live may go down to -100), that the thresholds are sane, and that
// the tier cut-offs descend strictly from TierA to TierE within 0..100.
func (w ScoreWeights) Validate() error {
	nonNegative := []struct {
		name  string
		value float64
	}{
		{"no_website", w.NoWebsite},
		{"social_only", w.SocialOnly},
		{"dead", w.Dead},
		{"parked", w.Parked},
		{"ssl_broken", w.SSLBroken},
		{"free_builder", w.FreeBuilder},
		{"no_https", w.NoHTTPS},
		{"phone_present", w.PhonePresent},
		{"email_found", w.EmailFound},
		{"mx_present", w.MXPresent},
		{"good_rating", w.GoodRating},
		{"many_reviews", w.ManyReviews},
		{"old_copyright", w.OldCopyright},
		{"no_ads_tag", w.NoAdsTag},
	}

	for _, item := range nonNegative {
		if item.value < 0 || item.value > 100 {
			return fmt.Errorf("weight %s must be between 0 and 100, got %v", item.name, item.value)
		}
	}

	if w.Live < -100 || w.Live > 100 {
		return fmt.Errorf("weight live must be between -100 and 100, got %v", w.Live)
	}

	if w.RatingThreshold < 0 || w.RatingThreshold > 5 {
		return fmt.Errorf("rating_threshold must be between 0 and 5, got %v", w.RatingThreshold)
	}

	if w.ReviewThreshold < 0 {
		return fmt.Errorf("review_threshold must not be negative, got %d", w.ReviewThreshold)
	}

	if w.CopyrightAgeYears < 0 {
		return fmt.Errorf("copyright_age_years must not be negative, got %d", w.CopyrightAgeYears)
	}

	if w.TierA > 100 {
		return fmt.Errorf("tier_a must not exceed 100, got %v", w.TierA)
	}

	if w.TierE < 0 {
		return fmt.Errorf("tier_e must not be negative, got %v", w.TierE)
	}

	if !(w.TierA > w.TierB && w.TierB > w.TierC && w.TierC > w.TierD && w.TierD > w.TierE) {
		return fmt.Errorf("tier cut-offs must descend strictly (tier_a > tier_b > tier_c > tier_d > tier_e), got %v > %v > %v > %v > %v",
			w.TierA, w.TierB, w.TierC, w.TierD, w.TierE)
	}

	return nil
}

// Reason explains one contribution to the worth-calling score, so the
// UI can show operators exactly why a lead scored the way it did.
type Reason struct {
	Signal       string  `json:"signal"`
	Contribution float64 `json:"contribution"`
	Detail       string  `json:"detail"`
}

// Score computes the worth-calling score for a business given its
// classified website status, its Signals, and the configured weights.
//
// The score is the sum of the status weight plus every armed signal
// weight, clamped to 0..100. Reasons list every non-zero contribution
// (with its pre-clamp value) in a fixed, deterministic order: status
// first, then phone, email, MX, rating, reviews, copyright age, and
// missing ads tag.
//
// Signal arming rules:
//   - GoodRating fires when Rating is known (> 0) and >= RatingThreshold.
//   - ManyReviews fires when ReviewCount is > 0 and >= ReviewThreshold.
//   - OldCopyright fires when CopyrightYear is known (> 0) and
//     CurrentYear-CopyrightYear >= CopyrightAgeYears.
//   - NoAdsTag fires only when an audit was performed and no
//     analytics/pixel tracker was found (without an audit the absence
//     means nothing).
//   - A FREE_BUILDER status on a retired Google business.site page gets
//     the stronger "owner absent 2+ years" detail.
func Score(status string, signals Signals, weights ScoreWeights) (score float64, tier string, reasons []Reason) {
	add := func(signal string, contribution float64, detail string) {
		if contribution == 0 {
			return
		}

		score += contribution
		reasons = append(reasons, Reason{Signal: signal, Contribution: contribution, Detail: detail})
	}

	switch status {
	case StatusNoWebsite:
		add(status, weights.NoWebsite, "no website listed")
	case StatusSocialOnly:
		add(status, weights.SocialOnly, "only a social media page stands in for a website")
	case StatusDead:
		add(status, weights.Dead, "website link is dead — visitors hit an error")
	case StatusParked:
		add(status, weights.Parked, "website is a parked or placeholder page")
	case StatusSSLBroken:
		add(status, weights.SSLBroken, "certificate is broken — urgent")
	case StatusFreeBuilder:
		detail := "website runs on a free site builder"
		if IsGoogleBusinessSite(signals.WebsiteURL) {
			detail = "still points to a retired Google business.site page — owner likely absent for 2+ years"
		}

		add(status, weights.FreeBuilder, detail)
	case StatusNoHTTPS:
		add(status, weights.NoHTTPS, "website loads without HTTPS — browsers mark it not secure")
	case StatusLive:
		add(status, weights.Live, "website is live and healthy")
	}

	if signals.PhonePresent {
		add(SignalPhonePresent, weights.PhonePresent, "phone number available")
	}

	if signals.EmailFound {
		add(SignalEmailFound, weights.EmailFound, "email address found")
	}

	if signals.MXPresent {
		add(SignalMXPresent, weights.MXPresent, "email domain accepts mail (MX records present)")
	}

	if signals.Rating > 0 && signals.Rating >= weights.RatingThreshold {
		add(SignalGoodRating, weights.GoodRating,
			fmt.Sprintf("strong reputation: rated %.1f stars", signals.Rating))
	}

	if signals.ReviewCount > 0 && signals.ReviewCount >= weights.ReviewThreshold {
		add(SignalManyReviews, weights.ManyReviews,
			fmt.Sprintf("established business: %d reviews", signals.ReviewCount))
	}

	if signals.CopyrightYear > 0 && signals.CurrentYear-signals.CopyrightYear >= weights.CopyrightAgeYears {
		add(SignalOldCopyright, weights.OldCopyright,
			fmt.Sprintf("copyright footer stuck at %d — site looks unmaintained", signals.CopyrightYear))
	}

	if signals.AuditPerformed && !signals.HasAdsTag {
		add(SignalNoAdsTag, weights.NoAdsTag, "no analytics or ad pixel installed — nobody is measuring the site")
	}

	if score < 0 {
		score = 0
	}

	if score > 100 {
		score = 100
	}

	tier = tierFor(score, weights)

	return score, tier, reasons
}

// tierFor buckets a clamped score into the configured tiers.
func tierFor(score float64, w ScoreWeights) string {
	switch {
	case score >= w.TierA:
		return "A"
	case score >= w.TierB:
		return "B"
	case score >= w.TierC:
		return "C"
	case score >= w.TierD:
		return "D"
	case score >= w.TierE:
		return "E"
	default:
		return "F"
	}
}
