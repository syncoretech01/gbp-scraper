package prospect

import "testing"

func TestClassifyStaticStatuses(t *testing.T) {
	cases := []struct {
		name           string
		signals        Signals
		wantStatus     string
		wantConclusive bool
	}{
		{
			name:           "empty website is NO_WEBSITE",
			signals:        Signals{WebsiteURL: ""},
			wantStatus:     StatusNoWebsite,
			wantConclusive: true,
		},
		{
			name:           "blank website is NO_WEBSITE",
			signals:        Signals{WebsiteURL: "   "},
			wantStatus:     StatusNoWebsite,
			wantConclusive: true,
		},
		{
			name: "website equal to maps url is NO_WEBSITE (named edge case)",
			signals: Signals{
				WebsiteURL: "https://g.co/kgs/AbC123",
				MapsURL:    "https://g.co/kgs/AbC123",
			},
			wantStatus:     StatusNoWebsite,
			wantConclusive: true,
		},
		{
			name: "website equal to maps url ignoring case is NO_WEBSITE",
			signals: Signals{
				WebsiteURL: "HTTPS://G.CO/kgs/AbC123",
				MapsURL:    "https://g.co/kgs/abc123",
			},
			wantStatus:     StatusNoWebsite,
			wantConclusive: true,
		},
		{
			name:           "google.com/maps website is NO_WEBSITE",
			signals:        Signals{WebsiteURL: "https://www.google.com/maps/place/Some+Biz"},
			wantStatus:     StatusNoWebsite,
			wantConclusive: true,
		},
		{
			name:           "maps.google.com website is NO_WEBSITE",
			signals:        Signals{WebsiteURL: "http://maps.google.com/?cid=12345"},
			wantStatus:     StatusNoWebsite,
			wantConclusive: true,
		},
		{
			name:           "goo.gl short link is NO_WEBSITE",
			signals:        Signals{WebsiteURL: "https://goo.gl/maps/xyz"},
			wantStatus:     StatusNoWebsite,
			wantConclusive: true,
		},
		{
			name:           "maps.app.goo.gl short link is NO_WEBSITE",
			signals:        Signals{WebsiteURL: "https://maps.app.goo.gl/XyZ9"},
			wantStatus:     StatusNoWebsite,
			wantConclusive: true,
		},
		{
			name:           "g.page link is NO_WEBSITE",
			signals:        Signals{WebsiteURL: "https://g.page/some-biz"},
			wantStatus:     StatusNoWebsite,
			wantConclusive: true,
		},
		{
			name:           "facebook page is SOCIAL_ONLY",
			signals:        Signals{WebsiteURL: "https://www.facebook.com/somebiz"},
			wantStatus:     StatusSocialOnly,
			wantConclusive: true,
		},
		{
			name:           "mobile facebook subdomain is SOCIAL_ONLY",
			signals:        Signals{WebsiteURL: "http://m.facebook.com/somebiz"},
			wantStatus:     StatusSocialOnly,
			wantConclusive: true,
		},
		{
			name:           "instagram uppercase is SOCIAL_ONLY",
			signals:        Signals{WebsiteURL: "HTTPS://WWW.INSTAGRAM.COM/SOMEBIZ"},
			wantStatus:     StatusSocialOnly,
			wantConclusive: true,
		},
		{
			name:           "linktr.ee is SOCIAL_ONLY",
			signals:        Signals{WebsiteURL: "https://linktr.ee/somebiz"},
			wantStatus:     StatusSocialOnly,
			wantConclusive: true,
		},
		{
			name:           "wa.me is SOCIAL_ONLY",
			signals:        Signals{WebsiteURL: "https://wa.me/15551234567"},
			wantStatus:     StatusSocialOnly,
			wantConclusive: true,
		},
		{
			name:           "x.com is SOCIAL_ONLY and does not match suffix of other hosts",
			signals:        Signals{WebsiteURL: "https://x.com/somebiz"},
			wantStatus:     StatusSocialOnly,
			wantConclusive: true,
		},
		{
			name:           "wixsite is FREE_BUILDER",
			signals:        Signals{WebsiteURL: "https://somebiz.wixsite.com/home"},
			wantStatus:     StatusFreeBuilder,
			wantConclusive: true,
		},
		{
			name:           "business.site is FREE_BUILDER (named edge case)",
			signals:        Signals{WebsiteURL: "https://some-biz.business.site"},
			wantStatus:     StatusFreeBuilder,
			wantConclusive: true,
		},
		{
			name:           "wordpress.com is FREE_BUILDER",
			signals:        Signals{WebsiteURL: "http://somebiz.wordpress.com"},
			wantStatus:     StatusFreeBuilder,
			wantConclusive: true,
		},
		{
			name:           "carrd.co is FREE_BUILDER",
			signals:        Signals{WebsiteURL: "https://somebiz.carrd.co/"},
			wantStatus:     StatusFreeBuilder,
			wantConclusive: true,
		},
		{
			name:           "similar but different host is not SOCIAL_ONLY",
			signals:        Signals{WebsiteURL: "https://notfacebook.com", AuditPerformed: true, Reachable: true, StatusCode: 200, HTTPS: true, TLSValid: true, ContentBytes: 5000},
			wantStatus:     StatusLive,
			wantConclusive: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, conclusive := Classify(tc.signals)
			if status != tc.wantStatus || conclusive != tc.wantConclusive {
				t.Fatalf("Classify() = (%q, %v), want (%q, %v)", status, conclusive, tc.wantStatus, tc.wantConclusive)
			}
		})
	}
}

func TestClassifyInconclusiveWithoutAudit(t *testing.T) {
	status, conclusive := Classify(Signals{WebsiteURL: "https://example.com"})
	if status != "" || conclusive {
		t.Fatalf("Classify() without audit = (%q, %v), want (\"\", false)", status, conclusive)
	}

	// Static classes never need an audit.
	status, conclusive = Classify(Signals{WebsiteURL: "https://facebook.com/biz"})
	if status != StatusSocialOnly || !conclusive {
		t.Fatalf("Classify() social without audit = (%q, %v), want (%q, true)", status, conclusive, StatusSocialOnly)
	}
}

func TestClassifyAuditedStatuses(t *testing.T) {
	audited := func(mutate func(*Signals)) Signals {
		s := Signals{
			WebsiteURL:     "https://example.com",
			AuditPerformed: true,
			Reachable:      true,
			StatusCode:     200,
			HTTPS:          true,
			TLSValid:       true,
			ContentBytes:   4096,
		}
		mutate(&s)

		return s
	}

	cases := []struct {
		name       string
		signals    Signals
		wantStatus string
	}{
		{
			name:       "unreachable is DEAD",
			signals:    audited(func(s *Signals) { s.Reachable = false }),
			wantStatus: StatusDead,
		},
		{
			name:       "http 404 is DEAD",
			signals:    audited(func(s *Signals) { s.StatusCode = 404 }),
			wantStatus: StatusDead,
		},
		{
			name:       "http 500 is DEAD",
			signals:    audited(func(s *Signals) { s.StatusCode = 500 }),
			wantStatus: StatusDead,
		},
		{
			name:       "status code 0 with audit performed is DEAD",
			signals:    audited(func(s *Signals) { s.StatusCode = 0 }),
			wantStatus: StatusDead,
		},
		{
			name:       "certificate error is SSL_BROKEN",
			signals:    audited(func(s *Signals) { s.CertificateError = "x509: certificate has expired" }),
			wantStatus: StatusSSLBroken,
		},
		{
			name:       "https without valid tls is SSL_BROKEN",
			signals:    audited(func(s *Signals) { s.TLSValid = false }),
			wantStatus: StatusSSLBroken,
		},
		{
			name:       "parked flag is PARKED",
			signals:    audited(func(s *Signals) { s.Parked = true }),
			wantStatus: StatusParked,
		},
		{
			name:       "coming soon flag is PARKED",
			signals:    audited(func(s *Signals) { s.ComingSoon = true }),
			wantStatus: StatusParked,
		},
		{
			name:       "placeholder flag is PARKED",
			signals:    audited(func(s *Signals) { s.Placeholder = true }),
			wantStatus: StatusParked,
		},
		{
			name:       "tiny known body is PARKED",
			signals:    audited(func(s *Signals) { s.ContentBytes = 200 }),
			wantStatus: StatusParked,
		},
		{
			name:       "unknown body size is not PARKED",
			signals:    audited(func(s *Signals) { s.ContentBytes = 0 }),
			wantStatus: StatusLive,
		},
		{
			name: "plain http is NO_HTTPS",
			signals: audited(func(s *Signals) {
				s.HTTPS = false
				s.TLSValid = false
			}),
			wantStatus: StatusNoHTTPS,
		},
		{
			name:       "healthy site is LIVE",
			signals:    audited(func(s *Signals) {}),
			wantStatus: StatusLive,
		},
		{
			name: "DEAD wins over SSL_BROKEN and PARKED",
			signals: audited(func(s *Signals) {
				s.Reachable = false
				s.CertificateError = "x509: bad cert"
				s.Parked = true
			}),
			wantStatus: StatusDead,
		},
		{
			name: "SSL_BROKEN wins over PARKED and NO_HTTPS",
			signals: audited(func(s *Signals) {
				s.CertificateError = "x509: bad cert"
				s.Parked = true
				s.HTTPS = false
			}),
			wantStatus: StatusSSLBroken,
		},
		{
			name: "static SOCIAL_ONLY wins over dead audit",
			signals: audited(func(s *Signals) {
				s.WebsiteURL = "https://www.yelp.com/biz/somebiz"
				s.Reachable = false
			}),
			wantStatus: StatusSocialOnly,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, conclusive := Classify(tc.signals)
			if status != tc.wantStatus || !conclusive {
				t.Fatalf("Classify() = (%q, %v), want (%q, true)", status, conclusive, tc.wantStatus)
			}
		})
	}
}

func TestIsGoogleBusinessSite(t *testing.T) {
	if !IsGoogleBusinessSite("https://some-biz.business.site/") {
		t.Fatal("expected business.site subdomain to be detected")
	}

	if !IsGoogleBusinessSite("BUSINESS.SITE") {
		t.Fatal("expected bare business.site host to be detected")
	}

	if IsGoogleBusinessSite("https://mybusiness.site.example.com") {
		t.Fatal("did not expect unrelated host to be detected")
	}

	if IsGoogleBusinessSite("https://example.com") {
		t.Fatal("did not expect example.com to be detected")
	}
}

func TestDomainFromWebsite(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://WWW.Foo.com/bar", "foo.com"},
		{"", ""},
		{"   ", ""},
		{"foo.com", "foo.com"},
		{"foo.com/path/deeper", "foo.com"},
		{"http://example.com/path", "example.com"},
		{"  HTTP://Example.COM  ", "example.com"},
		{"www.example.com", "example.com"},
		{"https://", ""},
		{"https://www.", ""},
	}

	for _, tc := range cases {
		if got := DomainFromWebsite(tc.in); got != tc.want {
			t.Errorf("DomainFromWebsite(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
