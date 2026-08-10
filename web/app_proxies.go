package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
	xproxy "golang.org/x/net/proxy"
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
	Latency      string
	SuccessCount int64
	FailureCount int64
	BlockCount   int64
	UsageCount   int64
	LastSuccess  string
	Cooldown     string
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
	for _, proxy := range proxies {
		page.Proxies = append(page.Proxies, proxyRowView{
			ID: proxy.ID, PoolName: proxy.PoolName, MaskedURL: proxy.MaskedURL, Protocol: proxy.Protocol,
			Enabled: proxy.Enabled, Status: proxy.Status, Latency: optionalMilliseconds(proxy.LatencyMS),
			SuccessCount: proxy.SuccessCount, FailureCount: proxy.FailureCount, BlockCount: proxy.BlockCount,
			UsageCount: proxy.UsageCount, LastSuccess: optionalTimeLabel(proxy.LastSuccessAt),
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
	_, imported, err := s.svc.ImportProxyPool(r.Context(), name, strings.TrimSpace(r.FormValue("strategy")), values)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
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
	switch proxyURL.Scheme {
	case "http", "https":
		transport.Proxy = http.ProxyURL(proxyURL)
	case "socks5":
		var auth *xproxy.Auth
		if proxyURL.User != nil {
			password, _ := proxyURL.User.Password()
			auth = &xproxy.Auth{User: proxyURL.User.Username(), Password: password}
		}
		dialer, dialErr := xproxy.SOCKS5("tcp", proxyURL.Host, auth, &net.Dialer{Timeout: 8 * time.Second})
		if dialErr != nil {
			result.Error = jobruntime.RedactString(dialErr.Error())
			return result
		}
		transport.Proxy = nil
		transport.DialContext = func(_ context.Context, network, address string) (net.Conn, error) {
			return dialer.Dial(network, address)
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
	client := &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return nil
	}}
	started := time.Now()
	response, err := client.Do(request)
	latency := time.Since(started).Milliseconds()
	result.LatencyMS = &latency
	transport.CloseIdleConnections()
	if err != nil {
		result.Error = jobruntime.RedactString(err.Error())
		if strings.Contains(strings.ToLower(err.Error()), "authentication") {
			result.Status = "authentication-failed"
		}
		return result
	}
	defer response.Body.Close()
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
