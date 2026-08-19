package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maximumProxyTestRequestBytes bounds the JSON body of a bulk proxy test.
const maximumProxyTestRequestBytes = 16 << 10

// maximumProxyTestBatch bounds one bulk test so a large pool cannot occupy the
// local worker indefinitely. Each proxy is contacted sequentially with the same
// bounded timeout the single-proxy test uses.
const maximumProxyTestBatch = 50

// proxyTestInput selects what to test. Exactly one of the two fields may be set;
// an empty request tests every enabled proxy in every pool.
type proxyTestInput struct {
	PoolID  string   `json:"pool_id,omitempty"`
	ProxyID []string `json:"proxy_ids,omitempty"`
}

// proxyTestReport is one proxy's outcome, with the credential kept masked.
type proxyTestReport struct {
	ID        string `json:"id"`
	PoolID    string `json:"pool_id"`
	PoolName  string `json:"pool_name,omitempty"`
	MaskedURL string `json:"masked_url"`
	Status    string `json:"status"`
	LatencyMS *int64 `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (s *Server) registerProxyTestRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/proxies/test", s.apiTestProxies)
}

// apiTestProxies implements the specification's pool-level proxy test. The
// existing POST /api/v1/proxies/{id}/test remains the single-proxy form used by
// the Proxies page; this endpoint tests a whole pool or an explicit selection
// and reports each result rather than redirecting.
func (s *Server) apiTestProxies(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}

	input, err := decodeProxyTestInput(w, r)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_proxy_test", err.Error())

		return
	}

	candidates, err := s.selectProxiesForTest(r, input)
	if err != nil {
		if errors.Is(err, ErrProxyStoreUnsupported) {
			renderLocalAPIError(w, http.StatusNotImplemented, "proxies_unavailable", "The proxy manager is unavailable")

			return
		}

		renderLocalAPIError(w, http.StatusInternalServerError, "proxy_test_failed", "Could not read the proxy pool")

		return
	}

	reports := make([]proxyTestReport, 0, len(candidates))
	healthy := 0

	for _, proxy := range candidates {
		report := proxyTestReport{
			ID: proxy.ID, PoolID: proxy.PoolID, PoolName: proxy.PoolName, MaskedURL: proxy.MaskedURL,
		}

		secret, secretErr := s.svc.GetProxySecret(r.Context(), proxy.ID)
		if secretErr != nil {
			report.Status = "offline"
			report.Error = "proxy credential is unavailable"
			reports = append(reports, report)

			continue
		}

		result := checkProxyAccess(r.Context(), secret)
		report.Status = result.Status
		report.LatencyMS = result.LatencyMS
		report.Error = result.Error

		if saveErr := s.svc.RecordProxyTest(r.Context(), proxy.ID, result); saveErr != nil {
			report.Error = strings.TrimSpace(report.Error + " (result not saved)")
		}

		if result.Status == "healthy" {
			healthy++
		}

		reports = append(reports, report)
	}

	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]any{
		"tested":     len(reports),
		"healthy":    healthy,
		"checked_at": time.Now().UTC(),
		"results":    reports,
	}})
}

func (s *Server) selectProxiesForTest(r *http.Request, input proxyTestInput) ([]ProxyRecord, error) {
	proxies, err := s.svc.ListProxies(r.Context(), input.PoolID)
	if err != nil {
		return nil, err
	}

	selected := make(map[string]struct{}, len(input.ProxyID))
	for _, id := range input.ProxyID {
		selected[strings.TrimSpace(id)] = struct{}{}
	}

	candidates := make([]ProxyRecord, 0, len(proxies))

	for _, proxy := range proxies {
		if len(selected) > 0 {
			if _, ok := selected[proxy.ID]; !ok {
				continue
			}
		} else if !proxy.Enabled {
			// A whole-pool test only contacts proxies the runner would use. An
			// explicit selection may still re-test a disabled proxy.
			continue
		}

		candidates = append(candidates, proxy)
		if len(candidates) >= maximumProxyTestBatch {
			break
		}
	}

	return candidates, nil
}

func decodeProxyTestInput(w http.ResponseWriter, r *http.Request) (proxyTestInput, error) {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		if err := parseBoundedRequestForm(w, r, maximumProxyTestRequestBytes); err != nil {
			return proxyTestInput{}, fmt.Errorf("invalid form")
		}

		return proxyTestInput{
			PoolID:  strings.TrimSpace(r.FormValue("pool_id")),
			ProxyID: splitNonEmptyLines(r.FormValue("proxy_ids")),
		}, nil
	}

	r.Body = http.MaxBytesReader(w, r.Body, maximumProxyTestRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var input proxyTestInput
	if err := decoder.Decode(&input); err != nil {
		return proxyTestInput{}, fmt.Errorf("invalid JSON")
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return proxyTestInput{}, fmt.Errorf("request must contain exactly one JSON object")
	}

	if len(input.ProxyID) > maximumProxyTestBatch {
		return proxyTestInput{}, fmt.Errorf("select at most %d proxies", maximumProxyTestBatch)
	}

	input.PoolID = strings.TrimSpace(input.PoolID)

	return input, nil
}
