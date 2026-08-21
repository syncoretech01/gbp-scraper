package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

const (
	maximumProxyImportCount = 5000
	maximumProxyImportBytes = 1 << 20
)

type proxiesPageData struct {
	Pools   []proxyPoolView
	Proxies []proxyRowView
	Notice  string
}

type proxyPoolView struct {
	ID           string
	Name         string
	Strategy     string
	TotalCount   int64
	EnabledCount int64
	HealthyCount int64
}

type proxyRowView struct {
	ID           string
	PoolName     string
	MaskedURL    string
	Protocol     string
	Enabled      bool
	Status       string
	StatusClass  string
	Latency      string
	ExitIP       string
	ExitIPHint   string
	Country      string
	SuccessCount int64
	FailureCount int64
	BlockCount   int64
	UsageCount   int64
	LastSuccess  string
	LastUsed     string
	Cooldown     string
}

// proxyStatusClass maps a proxy status onto a badge state the shared
// component library already defines, so a new status shows the right severity
// without adding a page-level colour.
func proxyStatusClass(status string) string {
	switch status {
	case ProxyStatusHealthy:
		return "healthy"
	case ProxyStatusSlow, ProxyStatusRateLimited:
		return "warning"
	case ProxyStatusBlocked, ProxyStatusOffline, ProxyStatusAuthentication:
		return "failed"
	case ProxyStatusCoolingDown:
		return "paused"
	default:
		return "unknown"
	}
}

// exitIPHint explains, in the operator's language, how far an exit address can
// be trusted. The interface must never present a gateway entry address as a
// confirmed exit.
func exitIPHint(source string) string {
	switch source {
	case ExitIPSourceSOCKS5Bind:
		return "reported by the SOCKS5 server as its outbound address"
	case ExitIPSourceEndpoint:
		return "endpoint this host connected to; the exit only for a single-hop proxy"
	default:
		return ""
	}
}

// missingLabel keeps empty proxy evidence readable instead of blank.
func missingLabel(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}

	return value
}

func (s *Server) proxiesPage(w http.ResponseWriter, r *http.Request) {
	pools, err := s.svc.ListProxyPools(r.Context())
	if err != nil {
		http.Error(w, "could not load proxy pools", http.StatusInternalServerError)
		return
	}
	selectedPool := strings.TrimSpace(r.URL.Query().Get("pool"))
	proxies, err := s.svc.ListProxies(r.Context(), selectedPool)
	if err != nil {
		http.Error(w, "could not load proxies", http.StatusInternalServerError)
		return
	}
	page := proxiesPageData{Notice: strings.TrimSpace(r.URL.Query().Get("notice"))}
	for _, pool := range pools {
		page.Pools = append(page.Pools, proxyPoolView{
			ID: pool.ID, Name: pool.Name, Strategy: pool.Strategy, TotalCount: pool.TotalCount,
			EnabledCount: pool.EnabledCount, HealthyCount: pool.HealthyCount,
		})
	}
	now := time.Now().UTC()
	for _, proxy := range proxies {
		page.Proxies = append(page.Proxies, proxyRowView{
			ID: proxy.ID, PoolName: proxy.PoolName, MaskedURL: proxy.MaskedURL, Protocol: proxy.Protocol,
			Enabled: proxy.Enabled, Status: proxy.EffectiveStatus(now), StatusClass: proxyStatusClass(proxy.EffectiveStatus(now)),
			Latency: optionalMilliseconds(proxy.LatencyMS),
			ExitIP: missingLabel(proxy.ExitIP, "not reported"), ExitIPHint: exitIPHint(proxy.ExitIPSource),
			Country:      missingLabel(proxy.Country, "unknown"),
			SuccessCount: proxy.SuccessCount, FailureCount: proxy.FailureCount, BlockCount: proxy.BlockCount,
			UsageCount: proxy.UsageCount, LastSuccess: optionalTimeLabel(proxy.LastSuccessAt),
			LastUsed: optionalTimeLabel(proxy.LastUsedAt),
			Cooldown: optionalTimeLabel(proxy.CooldownUntil),
		})
	}
	activity, _ := s.appActivity(r)
	s.renderAppPage(w, "proxies", appPageData{
		Title:     "Proxy manager",
		Subtitle:  "Store encrypted proxy credentials in named local pools and test Google Maps access on demand.",
		ActiveNav: "proxies",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page:      page,
	})
}

func optionalMilliseconds(value *int64) string {
	if value == nil {
		return "not tested"
	}
	return fmt.Sprintf("%d ms", *value)
}

func (s *Server) importProxyPool(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maximumProxyImportBytes)
	if !s.requireCSRF(w, r) {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 120 {
		http.Error(w, "pool name is required and must be at most 120 characters", http.StatusUnprocessableEntity)
		return
	}
	values := splitNonEmptyLines(r.FormValue("proxies"))
	if len(values) > maximumProxyImportCount {
		http.Error(w, "proxy import exceeds 5,000 entries", http.StatusUnprocessableEntity)
		return
	}
	pool, imported, err := s.svc.ImportProxyPool(r.Context(), name, strings.TrimSpace(r.FormValue("strategy")), values)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	if raw := strings.TrimSpace(r.FormValue("max_tasks_per_proxy")); raw != "" && raw != "0" {
		capValue, capErr := strconv.Atoi(raw)
		if capErr != nil || capValue < 0 || capValue > 10_000 {
			http.Error(w, "tasks per proxy must be between 0 and 10000", http.StatusUnprocessableEntity)

			return
		}

		if capErr := s.svc.SetProxyPoolTaskCap(r.Context(), pool.ID, capValue); capErr != nil {
			http.Error(w, "could not store the per-proxy task cap", http.StatusInternalServerError)

			return
		}
	}

	http.Redirect(w, r, fmt.Sprintf("/app/proxies?notice=%d+proxies+imported", imported), http.StatusSeeOther)
}

func (s *Server) testProxy(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	secret, err := s.svc.GetProxySecret(r.Context(), id)
	if err != nil {
		http.Error(w, "proxy not found", http.StatusNotFound)
		return
	}
	result := checkProxyAccess(r.Context(), secret)
	if err := s.svc.RecordProxyTest(r.Context(), id, result); err != nil {
		http.Error(w, "could not save proxy test", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app/proxies?notice=Proxy+test+completed", http.StatusSeeOther)
}

// proxyExitObservation collects the addresses one proxy test actually
// observed. It is written from the dialler goroutine and read after the
// request finishes, so it is guarded.
type proxyExitObservation struct {
	mu       sync.Mutex
	bound    string
	endpoint string
}

func (observation *proxyExitObservation) record(bound, endpoint string) {
	observation.mu.Lock()
	defer observation.mu.Unlock()

	if bound != "" {
		observation.bound = bound
	}

	if endpoint != "" {
		observation.endpoint = endpoint
	}
}

// exitIP reports the best exit address the test measured and how it was
// determined, so the interface can label it honestly.
func (observation *proxyExitObservation) exitIP() (address, source string) {
	observation.mu.Lock()
	defer observation.mu.Unlock()

	if observation.bound != "" {
		return observation.bound, ExitIPSourceSOCKS5Bind
	}

	if observation.endpoint != "" {
		return observation.endpoint, ExitIPSourceEndpoint
	}

	return "", ""
}

func checkProxyAccess(parent context.Context, secret string) ProxyTestResult {
	checkedAt := time.Now().UTC()
	result := ProxyTestResult{Status: "offline", CheckedAt: checkedAt}
	proxyURL, err := url.Parse(secret)
	if err != nil {
		result.Error = "invalid encrypted proxy URL"
		return result
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	observation := &proxyExitObservation{}
	switch proxyURL.Scheme {
	case "http", "https":
		transport.Proxy = http.ProxyURL(proxyURL)
		// The proxy endpoint is the only address an HTTP CONNECT tunnel
		// exposes locally; the protocol carries no outbound-address field.
		baseDialer := &net.Dialer{Timeout: 8 * time.Second}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, dialErr := baseDialer.DialContext(ctx, network, address)
			if dialErr == nil {
				observation.record("", endpointAddress(conn.RemoteAddr()))
			}

			return conn, dialErr
		}
	case "socks5":
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, _, address string) (net.Conn, error) {
			conn, bound, dialErr := dialSOCKS5(ctx, proxyURL, address)
			if dialErr != nil {
				return nil, dialErr
			}

			observation.record(bound, endpointAddress(conn.RemoteAddr()))

			return conn, nil
		}
	default:
		result.Error = "unsupported proxy protocol"
		return result
	}
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.google.com/maps?hl=en", http.NoBody)
	if err != nil {
		result.Error = "could not create Maps test request"
		return result
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; GoogleMapsScraperLocal/1.0)")
	// Every hop is remembered so the country Google served the request as can
	// be read from the chain without a geolocation lookup.
	visited := []string{request.URL.String()}
	client := &http.Client{Transport: transport, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		visited = append(visited, next.URL.String())

		return nil
	}}
	started := time.Now()
	response, err := client.Do(request)
	latency := time.Since(started).Milliseconds()
	result.LatencyMS = &latency
	result.ExitIP, result.ExitIPSource = observation.exitIP()
	transport.CloseIdleConnections()
	if err != nil {
		result.Error = jobruntime.RedactString(err.Error())
		if strings.Contains(strings.ToLower(err.Error()), "authentication") {
			result.Status = "authentication-failed"
		}
		return result
	}
	defer response.Body.Close()
	if response.Request != nil && response.Request.URL != nil {
		visited = append(visited, response.Request.URL.String())
	}
	result.Country = googleCountryHint(visited...)
	switch {
	case response.StatusCode == http.StatusProxyAuthRequired:
		result.Status = "authentication-failed"
	case response.StatusCode == http.StatusTooManyRequests:
		result.Status = "rate-limited"
	case response.StatusCode == http.StatusForbidden:
		result.Status = "blocked"
	case response.StatusCode >= 200 && response.StatusCode < 400 && latency > 5000:
		result.Status = "slow"
	case response.StatusCode >= 200 && response.StatusCode < 400:
		result.Status = "healthy"
	default:
		result.Status = "offline"
	}
	if result.Status != "healthy" && result.Status != "slow" {
		result.Error = fmt.Sprintf("Maps returned HTTP %d", response.StatusCode)
	}
	return result
}

func (s *Server) mutateProxy(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	action := strings.TrimSpace(r.PathValue("action"))
	var err error
	switch action {
	case "enable":
		err = s.svc.SetProxyEnabled(r.Context(), id, true)
	case "disable":
		err = s.svc.SetProxyEnabled(r.Context(), id, false)
	case "delete":
		err = s.svc.DeleteProxy(r.Context(), id)
	default:
		http.Error(w, "invalid proxy action", http.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		if errors.Is(err, ErrProxyNotFound) {
			http.Error(w, "proxy not found", http.StatusNotFound)
		} else {
			http.Error(w, "could not update proxy", http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, "/app/proxies?notice=Proxy+updated", http.StatusSeeOther)
}

func (s *Server) deleteProxyPool(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	if err := s.svc.DeleteProxyPool(r.Context(), strings.TrimSpace(r.PathValue("id"))); err != nil {
		if errors.Is(err, ErrProxyNotFound) {
			http.Error(w, "proxy pool not found", http.StatusNotFound)
		} else {
			http.Error(w, "could not delete proxy pool", http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, "/app/proxies?notice=Proxy+pool+deleted", http.StatusSeeOther)
}

// proxyRetestReport is one disabled proxy's retest outcome, with the
// credential kept masked and the re-enable decision recorded.
type proxyRetestReport struct {
	ID        string `json:"id"`
	PoolID    string `json:"pool_id"`
	PoolName  string `json:"pool_name,omitempty"`
	MaskedURL string `json:"masked_url"`
	Status    string `json:"status"`
	LatencyMS *int64 `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
	Reenabled bool   `json:"reenabled"`
}

// proxyAccessCheck is the live Maps probe used by the disabled-proxy retest.
// It is a variable only so package tests can substitute a hermetic probe;
// the application always uses checkProxyAccess.
var proxyAccessCheck = checkProxyAccess

func (s *Server) registerProxyRetestRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/proxy-pools/{id}/retest-disabled", s.retestDisabledProxies)
}

// retestDisabledProxies implements the specification's "retest disabled
// proxies" action as an on-demand, bounded pass: up to maximumProxyTestBatch
// disabled proxies of one pool are tested against Maps, each result is
// recorded, and a proxy whose test reports "healthy" is re-enabled so the
// rotation can use it again.
func (s *Server) retestDisabledProxies(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}

	poolID := strings.TrimSpace(r.PathValue("id"))

	proxies, err := s.svc.ListProxies(r.Context(), poolID)
	if err != nil {
		if errors.Is(err, ErrProxyStoreUnsupported) {
			renderLocalAPIError(w, http.StatusNotImplemented, "proxies_unavailable", "The proxy manager is unavailable")

			return
		}

		renderLocalAPIError(w, http.StatusInternalServerError, "proxy_retest_failed", "Could not read the proxy pool")

		return
	}

	reports := make([]proxyRetestReport, 0)
	reenabled := 0

	for _, proxy := range proxies {
		if proxy.Enabled {
			continue
		}

		if len(reports) >= maximumProxyTestBatch {
			break
		}

		report := proxyRetestReport{
			ID: proxy.ID, PoolID: proxy.PoolID, PoolName: proxy.PoolName, MaskedURL: proxy.MaskedURL,
		}

		secret, secretErr := s.svc.GetProxySecret(r.Context(), proxy.ID)
		if secretErr != nil {
			report.Status = "offline"
			report.Error = "proxy credential is unavailable"
			reports = append(reports, report)

			continue
		}

		result := proxyAccessCheck(r.Context(), secret)
		report.Status = result.Status
		report.LatencyMS = result.LatencyMS
		report.Error = result.Error

		if saveErr := s.svc.RecordProxyTest(r.Context(), proxy.ID, result); saveErr != nil {
			report.Error = strings.TrimSpace(report.Error + " (result not saved)")
		}

		if result.Status == "healthy" {
			if enableErr := s.svc.SetProxyEnabled(r.Context(), proxy.ID, true); enableErr == nil {
				report.Reenabled = true
				reenabled++
			} else {
				report.Error = strings.TrimSpace(report.Error + " (could not re-enable)")
			}
		}

		reports = append(reports, report)
	}

	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]any{
			"pool_id":    poolID,
			"tested":     len(reports),
			"reenabled":  reenabled,
			"checked_at": time.Now().UTC(),
			"results":    reports,
		}})

		return
	}

	notice := fmt.Sprintf("%d disabled proxies retested; %d re-enabled", len(reports), reenabled)
	http.Redirect(w, r, "/app/proxies?notice="+url.QueryEscape(notice), http.StatusSeeOther)
}
