package web

import "strings"

// defaultWizardGeographyMode is the geometry a new scrape starts in. It is a
// mode, not content: it says how the area is expressed, never where it is.
const defaultWizardGeographyMode = "bbox"

// freshWizardInitialValues is what a genuinely new scrape starts from.
//
// The wizard used to open pre-filled with one real job's content: the name
// "San Francisco dentists", the queries "dentists in San Francisco" and
// "dental clinics in San Francisco", and the San Francisco centre. Every new
// scrape inherited another job's subject, and an operator who changed the
// queries but not the centre silently searched the wrong city.
//
// A new scrape therefore carries NO job-specific content -- no query text, no
// name, no geography label, no coordinates. Only global operator defaults may
// prefill it, and only the ones an operator deliberately saved in Settings:
// a saved location label and centre. Loading a template, duplicating a job,
// rerunning a campaign, or opening a saved area all layer their own values on
// top of this afterwards, because those are explicit choices.
func freshWizardInitialValues(defaults scrapeDefaults) wizardInitialValues {
	initial := wizardInitialValues{GeographyMode: defaultWizardGeographyMode}

	// Saved defaults are the operator's own persisted starting point, not
	// another job's content, so they are the one thing allowed to prefill a
	// blank wizard. All three are set together or not at all: half a centre
	// is worse than none.
	if label := strings.TrimSpace(defaults.LocationLabel); label != "" {
		initial.LocationLabel = label
	}

	if lat, lon := strings.TrimSpace(defaults.Lat), strings.TrimSpace(defaults.Lon); lat != "" && lon != "" {
		initial.Latitude = lat
		initial.Longitude = lon
	}

	return initial
}

// carriesJobContent reports whether a set of initial wizard values holds
// anything that could only have come from a specific job: a name, query text,
// a saved-area or template reference, or a stored geometry. It exists so the
// "a new scrape inherits nothing" rule can be asserted rather than trusted.
func (v wizardInitialValues) carriesJobContent() bool {
	return strings.TrimSpace(v.Name) != "" ||
		strings.TrimSpace(v.Keywords) != "" ||
		strings.TrimSpace(v.SavedAreaID) != "" ||
		strings.TrimSpace(v.SavedAreaName) != "" ||
		strings.TrimSpace(v.AreaGeoJSON) != ""
}
