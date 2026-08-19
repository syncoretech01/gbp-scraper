package enrichment

import (
	"sort"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

type signatureRule struct {
	name       string
	confidence float64
	patterns   []string
}

func detectSignatures(bodyHTML string, document *goquery.Document) (technologies, trackers []Detection) {
	searchText := strings.ToLower(bodyHTML)
	if generator := metaContent(document, "generator"); generator != "" {
		searchText += " generator:" + strings.ToLower(generator)
	}

	technologies = matchSignatureRules(searchText, []signatureRule{
		{name: "Drupal", confidence: 0.9, patterns: []string{"drupalsettings", "/sites/default/files/", "generator:drupal"}},
		{name: "Elementor", confidence: 0.92, patterns: []string{"elementor-widget", "elementor-frontend"}},
		{name: "Joomla", confidence: 0.9, patterns: []string{"generator:joomla", "/media/system/js/", "option=com_"}},
		{name: "Magento", confidence: 0.88, patterns: []string{"generator:magento", "mage/cookies", "/static/version", "magento_"}},
		{name: "Next.js", confidence: 0.96, patterns: []string{"__next_data__", "/_next/static/", "next-route-announcer"}},
		{name: "React", confidence: 0.82, patterns: []string{"data-reactroot", "react.production.min.js", "__reactcontainer$"}},
		{name: "Shopify", confidence: 0.94, patterns: []string{"cdn.shopify.com", "shopify.theme", "shopify-section"}},
		{name: "Squarespace", confidence: 0.94, patterns: []string{"static1.squarespace.com", "squarespace-cdn.com", "squarespace-block"}},
		{name: "Webflow", confidence: 0.94, patterns: []string{"webflow.js", "data-wf-page", "webflow.com"}},
		{name: "Wix", confidence: 0.94, patterns: []string{"wixstatic.com", "wix-image", "generator:wix"}},
		{name: "WooCommerce", confidence: 0.94, patterns: []string{"woocommerce", "wc-add-to-cart", "wc-block-"}},
		{name: "WordPress", confidence: 0.94, patterns: []string{"wp-content/", "wp-includes/", "generator:wordpress"}},
		{name: "Divi", confidence: 0.9, patterns: []string{"et_pb_section", "divi-builder"}},
		{name: "WPBakery", confidence: 0.9, patterns: []string{"vc_row", "wpb_wrapper", "js_composer"}},
	})
	trackers = matchSignatureRules(searchText, []signatureRule{
		{name: "Google Analytics", confidence: 0.95, patterns: []string{"google-analytics.com/analytics.js", "googletagmanager.com/gtag/js", "gtag('config'", "gtag(\"config\""}},
		{name: "Google Tag Manager", confidence: 0.97, patterns: []string{"googletagmanager.com/gtm.js", "gtm.start", "gtm-"}},
		{name: "Hotjar", confidence: 0.95, patterns: []string{"static.hotjar.com", "hotjar.com/c/hotjar-", "hj('trigger'"}},
		{name: "Meta Pixel", confidence: 0.95, patterns: []string{"connect.facebook.net/en_us/fbevents.js", "fbq('init'", "fbq(\"init\""}},
		{name: "Microsoft Clarity", confidence: 0.95, patterns: []string{"clarity.ms/tag/", "clarity('set'", "clarity(\"set\""}},
		{name: "Matomo", confidence: 0.92, patterns: []string{"matomo.js", "_paq.push", "piwik.js"}},
	})

	return technologies, trackers
}

func matchSignatureRules(searchText string, rules []signatureRule) []Detection {
	detections := make([]Detection, 0)

	for _, rule := range rules {
		evidence := make([]string, 0, len(rule.patterns))

		for _, pattern := range rule.patterns {
			if strings.Contains(searchText, pattern) {
				evidence = append(evidence, pattern)
			}
		}

		if len(evidence) == 0 {
			continue
		}

		confidence := rule.confidence + (float64(len(evidence)-1) * 0.02)
		if confidence > 0.99 {
			confidence = 0.99
		}

		detections = append(detections, Detection{
			Name:       rule.name,
			Confidence: confidence,
			Evidence:   evidence,
		})
	}

	sort.Slice(detections, func(left, right int) bool {
		return detections[left].Name < detections[right].Name
	})

	return detections
}

func detectPlaceholderSignals(searchText string) (
	parked bool,
	comingSoon bool,
	placeholder bool,
	indicators []string,
) {
	parkedSignals := matchedPhrases(searchText, []string{
		"buy this domain",
		"domain is for sale",
		"domain may be for sale",
		"afternic.com",
		"parkingcrew.net",
		"sedoparking.com",
		"this domain has expired",
	})
	comingSoonSignals := matchedPhrases(searchText, []string{
		"coming soon",
		"launching soon",
		"site is under construction",
		"website is under construction",
	})
	placeholderSignals := matchedPhrases(searchText, []string{
		"default web site page",
		"future home of something quite cool",
		"just another wordpress site",
		"lorem ipsum dolor sit amet",
		"sample page",
		"replace this text",
		"website setup is incomplete",
	})

	indicators = make([]string, 0, len(parkedSignals)+len(comingSoonSignals)+len(placeholderSignals))
	indicators = append(indicators, parkedSignals...)
	indicators = append(indicators, comingSoonSignals...)
	indicators = append(indicators, placeholderSignals...)
	sort.Strings(indicators)

	return len(parkedSignals) > 0, len(comingSoonSignals) > 0, len(placeholderSignals) > 0, indicators
}

func matchedPhrases(searchText string, phrases []string) []string {
	matches := make([]string, 0)

	for _, phrase := range phrases {
		if strings.Contains(searchText, phrase) {
			matches = append(matches, phrase)
		}
	}

	return matches
}
