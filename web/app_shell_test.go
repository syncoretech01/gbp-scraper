package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// viewStylesheets are the per-page stylesheets the application shell links
// after app.css. Each one is owned by a single page so page-level rules can
// never collide, and every page loads all of them.
var viewStylesheets = []string{
	"/static/css/views/dashboard.css",
	"/static/css/views/results.css",
	"/static/css/views/monitor.css",
	"/static/css/views/discovery.css",
	"/static/css/views/system.css",
}

// renderShell renders one app page with zero-value page data so shell markup
// can be asserted without a repository behind it.
func renderShell(t *testing.T, key, nav string, page any) string {
	t.Helper()

	server := newTestServer(t, t.TempDir())
	recorder := httptest.NewRecorder()
	server.renderAppPage(recorder, key, appPageData{
		Title: "Smoke", ActiveNav: nav, Theme: "system", Page: page,
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("render %q = %d: %s", key, recorder.Code, recorder.Body.String())
	}

	return recorder.Body.String()
}

// cssBlock returns the declaration body of the first rule whose selector list
// matches exactly. It deliberately does not handle nested at-rules; every
// selector it is asked about is a top-level rule.
func cssBlock(t *testing.T, stylesheet, selector string) string {
	t.Helper()

	marker := "\n" + selector + " {"
	start := strings.Index(stylesheet, marker)
	if start < 0 {
		t.Fatalf("app.css has no rule for %q", selector)
	}

	start += len(marker)
	end := strings.Index(stylesheet[start:], "}")
	if end < 0 {
		t.Fatalf("rule %q is not closed", selector)
	}

	return stylesheet[start : start+end]
}

func readStylesheet(t *testing.T) string {
	t.Helper()

	raw, err := fs.ReadFile(static, "static/css/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}

	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// The layout links app.css first and then every view stylesheet, in a fixed
// order. A page stylesheet that loads before app.css would lose to the
// component library it is supposed to extend.
func TestLayoutLinksTheDesignSystemThenEveryViewStylesheet(t *testing.T) {
	t.Parallel()

	body := renderShell(t, "dashboard", "dashboard", dashboardPageData{})

	base := strings.Index(body, `<link rel="stylesheet" href="/static/css/app.css">`)
	if base < 0 {
		t.Fatal("layout does not link /static/css/app.css")
	}

	previous := base
	for _, href := range viewStylesheets {
		at := strings.Index(body, `<link rel="stylesheet" href="`+href+`">`)
		if at < 0 {
			t.Fatalf("layout does not link %s", href)
		}

		if at < previous {
			t.Fatalf("%s is linked before the stylesheet that precedes it", href)
		}

		previous = at
	}
}

// The view stylesheets live under web/static, which is embedded wholesale, so
// they must be reachable through the same file server as app.css.
func TestViewStylesheetsAreServedFromTheEmbeddedFileSystem(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, t.TempDir())

	for _, href := range viewStylesheets {
		recorder := httptest.NewRecorder()
		server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, href, http.NoBody))

		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", href, recorder.Code)
		}

		if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "css") {
			t.Fatalf("GET %s served %q, want a CSS content type", href, contentType)
		}

		if recorder.Body.Len() == 0 {
			t.Fatalf("GET %s served an empty file", href)
		}
	}
}

// Navigation used to draw bare capital letters where icons belong. Every nav
// entry now carries an inline SVG from the icon set defined in the sidebar
// partial: no icon font, no external file, and nothing the CSP would block.
func TestSidebarNavigationUsesInlineSVGIcons(t *testing.T) {
	t.Parallel()

	body := renderShell(t, "results", "results", resultsPageData{})

	navItem := regexp.MustCompile(`<a class="nav-item"[^>]*>(.*?)</a>`)
	matches := navItem.FindAllStringSubmatch(body, -1)
	if len(matches) < 6 {
		t.Fatalf("sidebar rendered %d navigation items, want at least 6", len(matches))
	}

	for _, match := range matches {
		inner := match[1]
		if !strings.Contains(inner, `<span class="nav-icon">`) || !strings.Contains(inner, "<svg") {
			t.Fatalf("navigation item has no inline SVG icon: %s", inner)
		}

		if !strings.Contains(inner, `<span class="nav-label">`) {
			t.Fatalf("navigation item has no label element: %s", inner)
		}

		if strings.Contains(inner, `<img`) || strings.Contains(inner, "url(") {
			t.Fatalf("navigation item pulls an external icon asset: %s", inner)
		}
	}

	// The active page is marked for assistive technology, not by colour alone.
	if !strings.Contains(body, `data-nav="results" data-tooltip="Results" aria-current="page"`) {
		t.Fatal("the active navigation item is not marked aria-current=page")
	}

	// Groups, not one flat list. Scrape, Results, and Operations always render;
	// the System group depends on repository capabilities the smoke server has
	// deliberately not wired up.
	for _, group := range []string{"Scrape", "Results &amp; prospects", "Operations"} {
		if !strings.Contains(body, `<p class="nav-section">`+group+`</p>`) {
			t.Fatalf("sidebar is missing the %q group heading", group)
		}
	}
}

// The top bar is the shell's global control strip: breadcrumb context, search,
// a visible command-palette affordance, the active-job indicator, and the
// appearance toggle. Losing any of them strands a keyboard-only route.
func TestTopbarCarriesGlobalControls(t *testing.T) {
	t.Parallel()

	body := renderShell(t, "jobs", "jobs", jobsPageData{})

	for _, want := range []string{
		`<nav class="topbar-context" aria-label="Breadcrumb">`,
		`<p class="eyebrow">Scrape</p>`,
		`<form class="global-search"`,
		`id="global-search"`,
		`class="command-affordance"`,
		`data-action="open-command-palette"`,
		`aria-keyshortcuts="Control+K"`,
		`class="activity-pill"`,
		`data-activity-label`,
		`data-action="cycle-theme"`,
		`data-theme-icon="system"`,
		`data-theme-icon="light"`,
		`data-theme-icon="dark"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("top bar is missing %q", want)
		}
	}

	// Pages extend the top bar through this slot rather than editing the shell.
	if !strings.Contains(body, `{{template "app/topbar-slot"`) && !strings.Contains(body, `class="app-topbar"`) {
		t.Fatal("top bar lost its page-level context slot")
	}
}

// Every page renders inside the same scroll-safe region. The shell is exactly
// one viewport tall and hides its own overflow, main scrolls vertically and
// keeps wide content reachable rather than clipping it at the right edge, and
// the canvas caps the measure without letting a grid child force it wider.
func TestAppShellCannotScrollTheDocumentHorizontally(t *testing.T) {
	t.Parallel()

	stylesheet := readStylesheet(t)

	shell := cssBlock(t, stylesheet, ".app-shell")
	if !strings.Contains(shell, "overflow: hidden") {
		t.Fatalf(".app-shell must hide its own overflow, got %q", shell)
	}

	if !strings.Contains(shell, "minmax(0, var(--sidebar-width)) minmax(0, 1fr)") {
		t.Fatalf(".app-shell columns must both be able to shrink, got %q", shell)
	}

	main := cssBlock(t, stylesheet, ".app-main")
	if !strings.Contains(main, "overflow: auto") {
		t.Fatalf(".app-main must scroll rather than clip, got %q", main)
	}

	if strings.Contains(main, "overflow-x: hidden") || strings.Contains(main, "overflow: hidden auto") {
		t.Fatalf(".app-main must not clip horizontally, got %q", main)
	}

	canvas := cssBlock(t, stylesheet, ".app-canvas")
	for _, want := range []string{"max-width: var(--content-max)", "min-width: 0"} {
		if !strings.Contains(canvas, want) {
			t.Fatalf(".app-canvas is missing %q, got %q", want, canvas)
		}
	}

	if !strings.Contains(stylesheet, ".app-canvas > * { min-width: 0; }") {
		t.Fatal(".app-canvas children must be allowed to shrink below their content width")
	}

	// The generic escape hatch page agents wrap wide content in.
	overflowSafe := cssBlock(t, stylesheet, ".overflow-safe")
	if !strings.Contains(overflowSafe, "overflow-x: auto") {
		t.Fatalf(".overflow-safe must scroll horizontally, got %q", overflowSafe)
	}
}

// Both themes must define the same tokens. A colour that exists only in the
// light block silently falls back to the light value in dark mode, which is
// how contrast regressions ship unnoticed.
func TestLightAndDarkPalettesDefineTheSameTokens(t *testing.T) {
	t.Parallel()

	stylesheet := readStylesheet(t)

	if !strings.Contains(stylesheet, `:root:not([data-theme="light"]) {`) {
		t.Fatal(`the system-preference dark block must be guarded by :root:not([data-theme="light"])`)
	}

	light := declaredTokens(cssBlock(t, stylesheet, ":root"))
	explicitDark := declaredTokens(cssBlock(t, stylesheet, `:root[data-theme="dark"]`))
	systemDark := declaredTokens(cssBlock(t, stylesheet, `    :root:not([data-theme="light"])`))

	if len(systemDark) == 0 {
		t.Fatal("the prefers-color-scheme block redefines no tokens")
	}

	if diff := missing(explicitDark, systemDark); len(diff) > 0 {
		t.Fatalf("the explicit and system dark palettes disagree on %v", diff)
	}

	if diff := missing(systemDark, explicitDark); len(diff) > 0 {
		t.Fatalf("the explicit and system dark palettes disagree on %v", diff)
	}

	// Every token a dark block redefines must already exist in the light one.
	if diff := missing(explicitDark, light); len(diff) > 0 {
		t.Fatalf("dark mode introduces tokens the light palette never defines: %v", diff)
	}

	// The tokens the whole interface reads must be re-stated for dark mode.
	for _, token := range []string{
		"--bg", "--surface", "--surface-muted", "--surface-hover",
		"--text", "--text-muted", "--text-faint",
		"--border", "--border-strong", "--border-subtle",
		"--primary", "--primary-contrast", "--primary-soft",
		"--success", "--warning", "--danger", "--purple",
		"--sidebar", "--sidebar-text", "--sidebar-muted",
	} {
		if _, ok := explicitDark[token]; !ok {
			t.Fatalf("dark mode does not redefine %s", token)
		}
	}
}

// Metric tiles used to print "not recorded" in headline type, which reads as a
// broken number. The design system gives that case a muted treatment and the
// shell script tags the server's placeholder strings so it applies without
// changing any handler.
func TestEmptyMetricsRenderAsAMutedEmptyState(t *testing.T) {
	t.Parallel()

	stylesheet := readStylesheet(t)

	empty := cssBlock(t, stylesheet, `.stat-value[data-empty="true"], .metric-value[data-empty="true"]`)
	for _, want := range []string{"color: var(--text-faint)", "font-size: var(--text-sm)"} {
		if !strings.Contains(empty, want) {
			t.Fatalf("the empty metric state is missing %q, got %q", want, empty)
		}
	}

	if strings.Contains(empty, "var(--weight-bold)") || strings.Contains(empty, "var(--weight-black)") {
		t.Fatalf("the empty metric state must not stay at headline weight, got %q", empty)
	}

	script, err := static.ReadFile("static/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}

	for _, want := range []string{"normalizeEmptyMetrics", `"not recorded"`, `element.dataset.empty = "true"`} {
		if !strings.Contains(string(script), want) {
			t.Fatalf("app.js is missing %q", want)
		}
	}
}

var cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// declaredTokens collects the custom properties a declaration body defines.
// Comments are stripped first: the token blocks are annotated section by
// section, and a comment would otherwise swallow the declaration that follows
// it when the body is split on semicolons.
func declaredTokens(block string) map[string]string {
	tokens := make(map[string]string)

	for _, declaration := range strings.Split(cssComment.ReplaceAllString(block, ""), ";") {
		declaration = strings.TrimSpace(declaration)
		if !strings.HasPrefix(declaration, "--") {
			continue
		}

		name, value, ok := strings.Cut(declaration, ":")
		if !ok {
			continue
		}

		tokens[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}

	return tokens
}

// missing returns the sorted names present in want but absent from have.
func missing(want, have map[string]string) []string {
	var names []string

	for name := range want {
		if _, ok := have[name]; !ok {
			names = append(names, name)
		}
	}

	sort.Strings(names)

	return names
}
