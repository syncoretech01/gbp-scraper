package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultRequestTimeout bounds a single non-streaming API call.
	defaultRequestTimeout = 30 * time.Second
	// maxResponseBytes caps how much of any single JSON response the harness
	// reads, so a misbehaving endpoint cannot exhaust memory.
	maxResponseBytes = 32 << 20
	// resultsPageSize is the page the harness requests when counting a job's
	// normalized results. The route caps it server-side.
	resultsPageSize = 500
)

// ErrAPIStatus is returned when the local API answers a request with a
// non-success HTTP status. Callers can inspect Status and Body.
type ErrAPIStatus struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *ErrAPIStatus) Error() string {
	return fmt.Sprintf("acceptance: %s %s returned HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// Client talks to one local scraper application over HTTP. It is safe for use
// by a single experiment run; it holds no per-job state.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// ClientOption customises a Client at construction.
type ClientOption func(*Client)

// WithToken sets the API bearer token sent on every request. It is only needed
// when the target application has enabled API keys or local login.
func WithToken(token string) ClientOption {
	return func(c *Client) {
		c.token = strings.TrimSpace(token)
	}
}

// WithHTTPClient overrides the underlying HTTP client, chiefly for tests.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *Client) {
		if client != nil {
			c.httpClient = client
		}
	}
}

// NewClient builds a Client for the application reachable at baseURL, for
// example "http://127.0.0.1:8080".
func NewClient(baseURL string, options ...ClientOption) (*Client, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return nil, errors.New("acceptance: base URL is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("acceptance: invalid base URL %q: %w", baseURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("acceptance: base URL must be http or https, got %q", parsed.Scheme)
	}

	client := &Client{
		baseURL:    trimmed,
		httpClient: &http.Client{Timeout: defaultRequestTimeout},
	}
	for _, option := range options {
		option(client)
	}

	return client, nil
}

// BaseURL reports the application root this client targets.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// newRequest builds a request against the application, attaching the bearer
// token when one is configured.
func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("acceptance: build request %s %s: %w", method, path, err)
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}

	return request, nil
}

// doJSON performs a request and decodes the standard local-API envelope,
// returning the Data payload. A non-2xx status yields an *ErrAPIStatus.
func (c *Client) doJSON(ctx context.Context, method, path string, body io.Reader) (json.RawMessage, json.RawMessage, error) {
	request, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("acceptance: %s %s: %w", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("acceptance: read %s %s: %w", method, path, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, nil, &ErrAPIStatus{Method: method, Path: path, Status: response.StatusCode, Body: truncate(string(raw), 512)}
	}

	var envelope apiEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, nil, fmt.Errorf("acceptance: decode %s %s envelope: %w", method, path, err)
	}
	if envelope.Error != nil {
		return nil, nil, fmt.Errorf("acceptance: %s %s: %s (%s)", method, path, envelope.Error.Message, envelope.Error.Code)
	}

	return envelope.Data, envelope.Meta, nil
}

// CreateJob posts a scrape job and returns the new job's id. max_time is sent
// in seconds, matching how POST /api/v1/jobs interprets it.
func (c *Client) CreateJob(ctx context.Context, request JobRequest) (string, error) {
	payload, err := json.Marshal(request.toWire())
	if err != nil {
		return "", fmt.Errorf("acceptance: encode job request: %w", err)
	}

	httpRequest, err := c.newRequest(ctx, http.MethodPost, "/api/v1/jobs", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return "", fmt.Errorf("acceptance: create job: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("acceptance: read create job response: %w", err)
	}
	if response.StatusCode != http.StatusCreated {
		return "", &ErrAPIStatus{Method: http.MethodPost, Path: "/api/v1/jobs", Status: response.StatusCode, Body: truncate(string(raw), 512)}
	}

	var created createJobResponse
	if err := json.Unmarshal(raw, &created); err != nil {
		return "", fmt.Errorf("acceptance: decode create job response: %w", err)
	}
	if strings.TrimSpace(created.ID) == "" {
		return "", errors.New("acceptance: create job response carried no id")
	}

	return created.ID, nil
}

// Progress reads the live progress DTO for a job.
func (c *Client) Progress(ctx context.Context, jobID string) (jobProgress, error) {
	data, _, err := c.doJSON(ctx, http.MethodGet, "/api/v1/jobs/"+url.PathEscape(jobID)+"/progress", nil)
	if err != nil {
		return jobProgress{}, err
	}
	var progress jobProgress
	if err := json.Unmarshal(data, &progress); err != nil {
		return jobProgress{}, fmt.Errorf("acceptance: decode progress: %w", err)
	}

	return progress, nil
}

// Coverage reads the coverage report for a job. It reports ok=false when the
// application does not support coverage for this job, which is not an error.
func (c *Client) Coverage(ctx context.Context, jobID string) (coverageReport, bool, error) {
	data, _, err := c.doJSON(ctx, http.MethodGet, "/api/v1/jobs/"+url.PathEscape(jobID)+"/coverage", nil)
	if err != nil {
		if unsupported(err) {
			return coverageReport{}, false, nil
		}

		return coverageReport{}, false, err
	}
	var report coverageReport
	if err := json.Unmarshal(data, &report); err != nil {
		return coverageReport{}, false, fmt.Errorf("acceptance: decode coverage: %w", err)
	}

	return report, true, nil
}

// Benchmark reads the acceptance/benchmark report for a job. It reports
// ok=false when benchmark evidence is unavailable, which is not an error.
func (c *Client) Benchmark(ctx context.Context, jobID string) (benchmarkReport, bool, error) {
	data, _, err := c.doJSON(ctx, http.MethodGet, "/api/v1/jobs/"+url.PathEscape(jobID)+"/benchmark", nil)
	if err != nil {
		if unsupported(err) {
			return benchmarkReport{}, false, nil
		}

		return benchmarkReport{}, false, err
	}
	var report benchmarkReport
	if err := json.Unmarshal(data, &report); err != nil {
		return benchmarkReport{}, false, fmt.Errorf("acceptance: decode benchmark: %w", err)
	}

	return report, true, nil
}

// ResultsTotal reads the total count of normalized businesses linked to a job
// through GET /api/v1/results. It reports ok=false when normalized result
// storage is unavailable, which is not an error.
func (c *Client) ResultsTotal(ctx context.Context, jobID string) (int64, bool, error) {
	path := "/api/v1/results?job_id=" + url.QueryEscape(jobID) + "&page_size=" + strconv.Itoa(resultsPageSize)
	_, meta, err := c.doJSON(ctx, http.MethodGet, path, nil)
	if err != nil {
		if unsupported(err) {
			return 0, false, nil
		}

		return 0, false, err
	}
	var parsed resultsMeta
	if len(meta) > 0 {
		if err := json.Unmarshal(meta, &parsed); err != nil {
			return 0, false, fmt.Errorf("acceptance: decode results meta: %w", err)
		}
	}

	return parsed.Total, true, nil
}

// SystemMetrics reads the app-reported system metrics snapshot.
func (c *Client) SystemMetrics(ctx context.Context) (systemMetrics, error) {
	data, _, err := c.doJSON(ctx, http.MethodGet, "/api/v1/system/metrics", nil)
	if err != nil {
		return systemMetrics{}, err
	}
	var metrics systemMetrics
	if err := json.Unmarshal(data, &metrics); err != nil {
		return systemMetrics{}, fmt.Errorf("acceptance: decode system metrics: %w", err)
	}

	return metrics, nil
}

// Logs downloads the plain-text job log (one "ISO\tseverity\tmessage" line per
// event). It reports ok=false when logs are unavailable, which is not an error.
func (c *Client) Logs(ctx context.Context, jobID string) (string, bool, error) {
	request, err := c.newRequest(ctx, http.MethodGet, "/api/v1/jobs/"+url.PathEscape(jobID)+"/logs", nil)
	if err != nil {
		return "", false, err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", false, fmt.Errorf("acceptance: get logs: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return "", false, fmt.Errorf("acceptance: read logs: %w", err)
	}
	if response.StatusCode == http.StatusNotImplemented {
		return "", false, nil
	}
	if response.StatusCode != http.StatusOK {
		return "", false, &ErrAPIStatus{Method: http.MethodGet, Path: "/logs", Status: response.StatusCode, Body: truncate(string(raw), 256)}
	}

	return string(raw), true, nil
}

// unsupported reports whether an API error is a benign "capability not
// available" answer (HTTP 501) rather than a real failure.
func unsupported(err error) bool {
	var status *ErrAPIStatus
	if errors.As(err, &status) {
		return status.Status == http.StatusNotImplemented
	}

	return false
}

// truncate shortens s to at most limit runes for error messages.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	return s[:limit] + "..."
}
