package prospect

import "strings"

// RenderOpener fills a call-opener template. Recognized placeholders
// are lowercase snake_case tokens in curly braces — the canonical set
// is {name} {category} {city} {status} {status_reason} {rating}
// {reviews} {tier} — but any {lowercase_token} is substituted from
// fields. Placeholders missing from fields (including unknown ones)
// render as the empty string. Brace text that is not a valid
// placeholder token (uppercase, spaces, unclosed braces) is left
// untouched.
func RenderOpener(template string, fields map[string]string) string {
	var b strings.Builder

	b.Grow(len(template))

	for i := 0; i < len(template); {
		if template[i] != '{' {
			b.WriteByte(template[i])
			i++

			continue
		}

		end := strings.IndexByte(template[i+1:], '}')
		if end < 0 {
			b.WriteString(template[i:])

			break
		}

		key := template[i+1 : i+1+end]
		if isPlaceholderKey(key) {
			b.WriteString(fields[key])
			i += end + 2

			continue
		}

		b.WriteByte('{')
		i++
	}

	return b.String()
}

// isPlaceholderKey reports whether key is a valid placeholder token:
// non-empty lowercase letters, digits and underscores only.
func isPlaceholderKey(key string) bool {
	if key == "" {
		return false
	}

	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}

		return false
	}

	return true
}

// OpenerTemplateFor picks the template for a status, falling back to
// the "default" key when the status has no template of its own (or an
// empty one).
func OpenerTemplateFor(templates map[string]string, status string) string {
	if t, ok := templates[status]; ok && strings.TrimSpace(t) != "" {
		return t
	}

	return templates["default"]
}

// DefaultOpenerTemplates returns natural, non-pushy call openers keyed
// by website status, plus a "default" fallback used for any status
// without a template of its own. All templates are plain text with
// RenderOpener placeholders, so users can edit them freely.
func DefaultOpenerTemplates() map[string]string {
	return map[string]string{
		StatusNoWebsite:   "Hi {name}, I searched for {category} in {city} and found you on Google — but couldn't find a website to send people to. Do you have one I missed?",
		StatusSocialOnly:  "Hi {name}, I found your {category} in {city} on Google — the listing points to your social page rather than a website of your own. Is that where you prefer customers to land?",
		StatusDead:        "Hi {name}, quick heads-up: the website link on your Google listing doesn't load right now, so people searching for {category} in {city} hit an error. Wanted to make sure you knew.",
		StatusParked:      "Hi {name}, I noticed the website on your Google listing currently shows a placeholder page. Is a new site in the works, or did something get switched off?",
		StatusSSLBroken:   "Hi {name}, browsers are showing a security warning on your website's certificate, which tends to scare off people who find you on Google. Thought you'd want to know — it's usually a quick fix.",
		StatusFreeBuilder: "Hi {name}, I came across your {category} in {city} — your {rating}-star rating stood out. I noticed the site runs on a free builder; have you thought about a site that's fully yours?",
		StatusNoHTTPS:     "Hi {name}, your website works, but it loads without the padlock (HTTPS), so browsers label it \"not secure\" for visitors. It's a small fix — happy to point you in the right direction.",
		StatusLive:        "Hi {name}, I was looking at {category} options in {city} and your reviews stood out — {rating} stars across {reviews} reviews. Do you have a quick minute?",
		"default":         "Hi {name}, I came across your {category} in {city} on Google and wanted to reach out. Do you have a quick minute?",
	}
}
