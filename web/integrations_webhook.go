package web

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const (
	// webhookPayloadVersion is bumped only when the envelope shape changes in a
	// way an existing receiver could not ignore.
	webhookPayloadVersion = 1

	webhookAttemptTimeout  = 15 * time.Second
	webhookMaximumAttempts = 4
	webhookInitialBackoff  = time.Second
	webhookMaximumBackoff  = 8 * time.Second
)

// integrationEvent is one deliverable local automation event. Export-backed
// events additionally carry the verified artifact so file destinations can act
// on it without re-reading the database.
type integrationEvent struct {
	Name       string
	SubjectID  string
	OccurredAt time.Time
	Data       any
	Export     *ExportRecord
	ExportPath string
}

// webhookEnvelope is the JSON body posted to a local listener. n8n and
// Activepieces both consume it as a plain webhook trigger body; the signature
// travels in headers so the body stays exactly what the receiver maps.
type webhookEnvelope struct {
	Version    int       `json:"version"`
	Delivery   string    `json:"delivery_id"`
	Event      string    `json:"event"`
	OccurredAt time.Time `json:"occurred_at"`
	Source     string    `json:"source"`
	Data       any       `json:"data"`
}

// Signature and routing headers. They are prefixed so a receiver that fans in
// several producers can tell this workspace's deliveries apart.
const (
	webhookEventHeader     = "X-GMS-Event"
	webhookDeliveryHeader  = "X-GMS-Delivery"
	webhookTimestampHeader = "X-GMS-Timestamp"
	webhookSignatureHeader = "X-GMS-Signature"
	webhookAttemptHeader   = "X-GMS-Attempt"
)

// signWebhookPayload returns the hex HMAC-SHA256 of "<timestamp>.<body>". The
// timestamp is inside the signed material so a captured delivery cannot be
// replayed later with a fresh timestamp header.
func signWebhookPayload(secret string, timestamp int64, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(payload)

	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// webhookBackoff returns the wait before the given one-based attempt. It grows
// geometrically and is capped so a stopped local listener cannot hold a
// delivery goroutine for an unbounded time.
func webhookBackoff(attempt int) time.Duration {
	// The shift is clamped before it is applied: an unclamped shift past the
	// width of a Duration wraps to zero, which would turn a capped backoff
	// into a hot retry loop.
	const maximumBackoffShift = 16
	if attempt < 1 {
		attempt = 1
	}
	if attempt > maximumBackoffShift {
		return webhookMaximumBackoff
	}
	wait := webhookInitialBackoff << (attempt - 1)
	if wait > webhookMaximumBackoff {
		return webhookMaximumBackoff
	}

	return wait
}

// deliverLocalWebhook posts one signed event, retrying transient failures with
// backoff. A 4xx other than 408/429 is treated as a permanent receiver-side
// rejection and is not retried.
func deliverLocalWebhook(ctx context.Context, configuration integrationConfiguration, event integrationEvent) error {
	target, err := validateLocalWebhookURL(configuration.URL)
	if err != nil {
		return err
	}
	if !integrationSubscribed(configuration.Events, event.Name) {
		return errIntegrationNotApplicable
	}
	delivery := event.SubjectID
	if delivery == "" {
		delivery = newDeliveryIdentifier()
	}
	occurredAt := event.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	payload, err := json.Marshal(webhookEnvelope{
		Version: webhookPayloadVersion, Delivery: delivery, Event: event.Name,
		OccurredAt: occurredAt.UTC(), Source: "google-maps-scraper", Data: event.Data,
	})
	if err != nil {
		return fmt.Errorf("encode webhook payload: %w", err)
	}

	client := newLocalWebhookClient()
	defer client.CloseIdleConnections()

	var lastErr error
	for attempt := 1; attempt <= webhookMaximumAttempts; attempt++ {
		status, attemptErr := postLocalWebhook(ctx, client, target.String(), configuration.Secret, delivery, event.Name, attempt, payload)
		if attemptErr == nil {
			return nil
		}
		lastErr = attemptErr
		if !retryableWebhookStatus(status) || attempt == webhookMaximumAttempts {
			break
		}
		timer := time.NewTimer(webhookBackoff(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()

			return ctx.Err()
		case <-timer.C:
		}
	}

	return fmt.Errorf("webhook delivery failed after %d attempts: %w", webhookMaximumAttempts, lastErr)
}

// retryableWebhookStatus reports whether another attempt is worthwhile. A zero
// status means the request never produced a response (connection refused, DNS,
// timeout) and is always worth retrying.
func retryableWebhookStatus(status int) bool {
	switch {
	case status == 0:
		return true
	case status == http.StatusRequestTimeout, status == http.StatusTooManyRequests:
		return true
	case status >= 500:
		return true
	default:
		return false
	}
}

func newLocalWebhookClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		MaxIdleConns:          2,
		IdleConnTimeout:       10 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		DialContext:           dialLocalIntegration,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   webhookAttemptTimeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("webhook redirected too many times")
			}
			if _, err := validateLocalWebhookURL(request.URL.String()); err != nil {
				return err
			}

			return nil
		},
	}
}

// postLocalWebhook performs one attempt and returns the HTTP status it saw, or
// zero when no response arrived.
func postLocalWebhook(
	ctx context.Context,
	client *http.Client,
	target, secret, delivery, event string,
	attempt int,
	payload []byte,
) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	timestamp := time.Now().UTC().Unix()
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "google-maps-scraper-local-integration/1")
	request.Header.Set(webhookEventHeader, event)
	request.Header.Set(webhookDeliveryHeader, delivery)
	request.Header.Set(webhookTimestampHeader, strconv.FormatInt(timestamp, 10))
	request.Header.Set(webhookAttemptHeader, strconv.Itoa(attempt))
	if secret != "" {
		request.Header.Set(webhookSignatureHeader, signWebhookPayload(secret, timestamp, payload))
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("deliver local webhook: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumWebhookResponse))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, fmt.Errorf("local webhook returned HTTP %d", response.StatusCode)
	}

	return response.StatusCode, nil
}

// integrationSubscribed reports whether the stored subscription covers an
// event. An empty subscription means the historical export-only behaviour.
func integrationSubscribed(events []string, name string) bool {
	if name == IntegrationEventTest {
		return true
	}
	if len(events) == 0 {
		return name == IntegrationEventExportCompleted
	}
	for _, event := range events {
		if event == name {
			return true
		}
	}

	return false
}
