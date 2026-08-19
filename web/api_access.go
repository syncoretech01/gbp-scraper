package web

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

var (
	ErrAPIKeyStoreUnsupported = errors.New("API-key storage is unavailable")
	ErrAPIKeyNotFound         = errors.New("API key not found")
)

const (
	apiKeyPrefix             = "gms_local_"
	defaultAPIRatePerMinute  = int64(240)
	maximumAPIRatePerMinute  = int64(100_000)
	maximumAPIRequestLogRows = 500
)

// APIKeyRecord contains API-key metadata only. The plaintext token is returned
// once by the creation endpoint and is never retained by the application.
type APIKeyRecord struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Permission string     `json:"permission"`
	Enabled    bool       `json:"enabled"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type APIRequestLog struct {
	ID         int64     `json:"id"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	StatusCode int       `json:"status_code"`
	DurationMS int64     `json:"duration_ms"`
	APIKeyID   string    `json:"api_key_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type apiAccessRepository interface {
	CreateAPIKey(context.Context, APIKeyRecord, string) error
	ListAPIKeys(context.Context, int) ([]APIKeyRecord, error)
	EnabledAPIKeyCount(context.Context) (int, error)
	AuthenticateAPIKey(context.Context, string, time.Time) (APIKeyRecord, error)
	SetAPIKeyEnabled(context.Context, string, bool) error
	RecordAPIRequest(context.Context, APIRequestLog) error
	ListAPIRequestLogs(context.Context, int) ([]APIRequestLog, error)
}

type apiAccessContextKey struct{}

type apiRateBucket struct {
	windowStart time.Time
	count       int64
}

type apiRateState struct {
	mu      sync.Mutex
	buckets map[string]apiRateBucket
}

type apiAtomicInt64 struct {
	atomic.Int64
}

func (s *Service) apiAccessRepository() (apiAccessRepository, error) {
	repository, ok := s.repo.(apiAccessRepository)
	if !ok {
		return nil, ErrAPIKeyStoreUnsupported
	}
	return repository, nil
}

func (s *Service) CreateAPIKey(ctx context.Context, record APIKeyRecord, hash string) error {
	repository, err := s.apiAccessRepository()
	if err != nil {
		return err
	}
	return repository.CreateAPIKey(ctx, record, hash)
}

func (s *Service) ListAPIKeys(ctx context.Context, limit int) ([]APIKeyRecord, error) {
	repository, err := s.apiAccessRepository()
	if err != nil {
		return nil, err
	}
	return repository.ListAPIKeys(ctx, limit)
}

func (s *Service) SetAPIKeyEnabled(ctx context.Context, id string, enabled bool) error {
	repository, err := s.apiAccessRepository()
	if err != nil {
		return err
	}
	return repository.SetAPIKeyEnabled(ctx, id, enabled)
}

func (s *Service) ListAPIRequestLogs(ctx context.Context, limit int) ([]APIRequestLog, error) {
	repository, err := s.apiAccessRepository()
	if err != nil {
		return nil, err
	}
	return repository.ListAPIRequestLogs(ctx, limit)
}

func newLocalAPIKey() (token, hash string, err error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", "", fmt.Errorf("generate API key: %w", err)
	}
	token = apiKeyPrefix + base64.RawURLEncoding.EncodeToString(secret)
	digest := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(digest[:]), nil
}

func hashLocalAPIKey(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func validAPIKeyName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 120 {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func (s *Server) registerAPIAccessRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/api-keys", s.listAPIKeys)
	mux.HandleFunc("POST /api/v1/api-keys", s.createAPIKey)
	mux.HandleFunc("POST /api/v1/api-keys/{id}/{action}", s.toggleAPIKey)
	mux.HandleFunc("GET /api/v1/api/request-logs", s.listAPIRequestLogs)
	mux.HandleFunc("GET /api/v1/api/settings", s.apiAccessSettings)
	mux.HandleFunc("PUT /api/v1/api/settings", s.apiAccessSettings)
}

func (s *Server) initializeAPIAccessSettings() {
	s.apiRateLimit.Store(defaultAPIRatePerMinute)
	settings, err := s.svc.LoadSettings(context.Background())
	if err != nil {
		return
	}
	value, err := strconv.ParseInt(strings.TrimSpace(settings["api.rate_limit_per_minute"]), 10, 64)
	if err == nil && value >= 0 && value <= maximumAPIRatePerMinute {
		s.apiRateLimit.Store(value)
	}
}

func (s *Server) apiAccessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		startedAt := time.Now()
		wrapped := newAPIResponseWriter(w)
		keyID := ""
		repository, repositoryErr := s.svc.apiAccessRepository()
		if repositoryErr == nil {
			key, authenticationErr := authenticateAPIRequest(r.Context(), r, repository)
			if authenticationErr != nil {
				renderLocalAPIError(wrapped, http.StatusUnauthorized, "api_key_required", authenticationErr.Error())
				s.recordAPIRequest(repository, r, wrapped.statusCode, startedAt, "")
				return
			}
			if key != nil {
				keyID = key.ID
				if isMutationMethod(r.Method) && key.Permission != "full" {
					renderLocalAPIError(wrapped, http.StatusForbidden, "api_key_read_only", "A full-access API key is required")
					s.recordAPIRequest(repository, r, wrapped.statusCode, startedAt, keyID)
					return
				}
				r = r.WithContext(context.WithValue(r.Context(), apiAccessContextKey{}, *key))
			}
		}
		identity := keyID
		if identity == "" {
			identity = requestClientAddress(r)
		}
		if !s.allowAPIRequest(identity, w) {
			renderLocalAPIError(wrapped, http.StatusTooManyRequests, "rate_limit_exceeded", "Local API rate limit exceeded")
			if repositoryErr == nil {
				s.recordAPIRequest(repository, r, wrapped.statusCode, startedAt, keyID)
			}
			return
		}
		next.ServeHTTP(wrapped, r)
		if repositoryErr == nil {
			s.recordAPIRequest(repository, r, wrapped.statusCode, startedAt, keyID)
		}
	})
}

func authenticateAPIRequest(
	ctx context.Context,
	r *http.Request,
	repository apiAccessRepository,
) (*APIKeyRecord, error) {
	credential, credentialErr := apiCredential(r)
	if credentialErr != nil {
		return nil, credentialErr
	}
	if credential == "" {
		if trustedSameOriginBrowserRequest(r) {
			return nil, nil
		}
		count, err := repository.EnabledAPIKeyCount(ctx)
		if err != nil {
			return nil, fmt.Errorf("could not verify API access")
		}
		if count == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("provide an API key with Authorization: Bearer or X-API-Key")
	}
	if !strings.HasPrefix(credential, apiKeyPrefix) || len(credential) > 256 {
		return nil, fmt.Errorf("API key is invalid")
	}
	record, err := repository.AuthenticateAPIKey(ctx, hashLocalAPIKey(credential), time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("API key is invalid or disabled")
	}
	return &record, nil
}

func apiCredential(r *http.Request) (string, error) {
	bearer := ""
	if authorization := strings.TrimSpace(r.Header.Get("Authorization")); authorization != "" {
		parts := strings.Fields(authorization)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return "", fmt.Errorf("Authorization must use a Bearer API key")
		}
		bearer = parts[1]
	}
	header := strings.TrimSpace(r.Header.Get("X-API-Key"))
	if bearer != "" && header != "" && subtle.ConstantTimeCompare([]byte(bearer), []byte(header)) != 1 {
		return "", fmt.Errorf("conflicting API-key headers")
	}
	if bearer != "" {
		return bearer, nil
	}
	return header, nil
}

func trustedSameOriginBrowserRequest(r *http.Request) bool {
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		parsed, err := url.Parse(origin)
		return err == nil && strings.EqualFold(parsed.Host, r.Host) &&
			(parsed.Scheme == "http" || parsed.Scheme == "https")
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "same-origin") {
		return false
	}
	referer := strings.TrimSpace(r.Referer())
	parsed, err := url.Parse(referer)
	return err == nil && strings.EqualFold(parsed.Host, r.Host) &&
		(parsed.Scheme == "http" || parsed.Scheme == "https")
}

func requestClientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	if r.RemoteAddr == "" {
		return "local"
	}
	return r.RemoteAddr
}

func (s *Server) allowAPIRequest(identity string, w http.ResponseWriter) bool {
	limit := s.apiRateLimit.Load()
	if limit <= 0 {
		return true
	}
	now := time.Now().UTC()
	window := now.Truncate(time.Minute)
	s.apiRates.mu.Lock()
	defer s.apiRates.mu.Unlock()
	if s.apiRates.buckets == nil {
		s.apiRates.buckets = make(map[string]apiRateBucket)
	}
	bucket := s.apiRates.buckets[identity]
	if bucket.windowStart != window {
		bucket = apiRateBucket{windowStart: window}
	}
	bucket.count++
	s.apiRates.buckets[identity] = bucket
	remaining := limit - bucket.count
	if remaining < 0 {
		remaining = 0
	}
	w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(limit, 10))
	w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(remaining, 10))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(window.Add(time.Minute).Unix(), 10))
	if len(s.apiRates.buckets) > 2048 {
		for key, candidate := range s.apiRates.buckets {
			if candidate.windowStart.Before(window) {
				delete(s.apiRates.buckets, key)
			}
		}
	}
	return bucket.count <= limit
}

func (s *Server) recordAPIRequest(
	repository apiAccessRepository,
	r *http.Request,
	status int,
	startedAt time.Time,
	keyID string,
) {
	if status == 0 {
		status = http.StatusOK
	}
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	duration := time.Since(startedAt).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	_ = repository.RecordAPIRequest(context.Background(), APIRequestLog{
		Method: r.Method, Path: path, StatusCode: status, DurationMS: duration,
		APIKeyID: keyID, CreatedAt: time.Now().UTC(),
	})
}

type apiResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func newAPIResponseWriter(writer http.ResponseWriter) *apiResponseWriter {
	return &apiResponseWriter{ResponseWriter: writer, statusCode: http.StatusOK}
}

func (writer *apiResponseWriter) WriteHeader(status int) {
	writer.statusCode = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *apiResponseWriter) Flush() {
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (writer *apiResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := writer.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func (writer *apiResponseWriter) ReadFrom(reader io.Reader) (int64, error) {
	if receiver, ok := writer.ResponseWriter.(io.ReaderFrom); ok {
		return receiver.ReadFrom(reader)
	}
	return io.Copy(writer.ResponseWriter, reader)
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	input, err := decodeAPIKeyInput(w, r)
	if err != nil {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_api_key", err.Error())
		return
	}
	if !validAPIKeyName(input.Name) || input.Permission != "read" && input.Permission != "full" {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_api_key", "Name and read/full permission are required")
		return
	}
	token, hash, err := newLocalAPIKey()
	if err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "api_key_generation_failed", "Could not generate API key")
		return
	}
	record := APIKeyRecord{
		ID: uuid.NewString(), Name: strings.TrimSpace(input.Name), Permission: input.Permission,
		Enabled: true, CreatedAt: time.Now().UTC(),
	}
	if err := s.svc.CreateAPIKey(r.Context(), record, hash); err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "api_key_save_failed", "Could not save API key")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	renderJSON(w, http.StatusCreated, localAPIEnvelope{Data: map[string]any{
		"key": record, "token": token, "warning": "Copy this token now; it is not stored and cannot be shown again.",
	}})
}

type apiKeyInput struct {
	Name       string `json:"name"`
	Permission string `json:"permission"`
}

func decodeAPIKeyInput(w http.ResponseWriter, r *http.Request) (apiKeyInput, error) {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var input apiKeyInput
		if err := decoder.Decode(&input); err != nil {
			return apiKeyInput{}, fmt.Errorf("invalid JSON")
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return apiKeyInput{}, fmt.Errorf("request must contain exactly one JSON object")
		}
		input.Permission = strings.ToLower(strings.TrimSpace(input.Permission))
		return input, nil
	}
	if err := parseBoundedRequestForm(w, r, 16<<10); err != nil {
		return apiKeyInput{}, fmt.Errorf("invalid form")
	}
	return apiKeyInput{
		Name:       strings.TrimSpace(r.FormValue("name")),
		Permission: strings.ToLower(strings.TrimSpace(r.FormValue("permission"))),
	}, nil
}

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	records, err := s.svc.ListAPIKeys(r.Context(), 200)
	if err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "api_key_list_failed", "Could not list API keys")
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: records})
}

func (s *Server) toggleAPIKey(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	action := strings.TrimSpace(r.PathValue("action"))
	if !validBusinessID(id) || action != "enable" && action != "disable" {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_api_key_action", "Invalid API key action")
		return
	}
	if err := s.svc.SetAPIKeyEnabled(r.Context(), id, action == "enable"); err != nil {
		if errors.Is(err, ErrAPIKeyNotFound) {
			renderLocalAPIError(w, http.StatusNotFound, "api_key_not_found", "API key not found")
		} else {
			renderLocalAPIError(w, http.StatusInternalServerError, "api_key_update_failed", "Could not update API key")
		}
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]string{"message": "API key " + action + "d"}})
}

func (s *Server) listAPIRequestLogs(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maximumAPIRequestLogRows {
			renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_limit", "Limit must be between 1 and 500")
			return
		}
		limit = value
	}
	logs, err := s.svc.ListAPIRequestLogs(r.Context(), limit)
	if err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "api_log_list_failed", "Could not list API request logs")
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: logs})
}

func (s *Server) apiAccessSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]int64{
			"rate_limit_per_minute": s.apiRateLimit.Load(),
		}})
		return
	}
	var input struct {
		RateLimit int64 `json:"rate_limit_per_minute"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.RateLimit < 0 || input.RateLimit > maximumAPIRatePerMinute {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_rate_limit", "Rate limit must be between 0 and 100000 requests per minute; 0 disables it")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_rate_limit", "Request must contain exactly one JSON object")
		return
	}
	if err := s.svc.SaveSettings(r.Context(), map[string]string{
		"api.rate_limit_per_minute": strconv.FormatInt(input.RateLimit, 10),
	}); err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "api_settings_failed", "Could not save API settings")
		return
	}
	s.apiRateLimit.Store(input.RateLimit)
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]int64{"rate_limit_per_minute": input.RateLimit}})
}
