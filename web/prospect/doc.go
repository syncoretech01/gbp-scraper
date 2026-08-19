// Package prospect implements the pure GBP (Google Business Profile)
// prospecting core: a website-status classifier, a configurable and
// explainable "worth calling" score, editable call-opener templates,
// and ZIP x category-synonym query generation.
//
// The package is deliberately self-contained: it imports nothing from
// the rest of this repository (standard library only), so any layer —
// scraper jobs, web handlers, storage — can depend on it without
// creating import cycles.
package prospect
