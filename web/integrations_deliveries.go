package web

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

const (
	// integrationDeliveryBudget bounds one broadcast, including every retry of
	// every destination, so a stopped local listener cannot leak goroutines.
	integrationDeliveryBudget = 3 * time.Minute

	// maximumDeliveryHistory is the largest page of delivery history a caller
	// can ask for.
	maximumDeliveryHistory = 200

	// maximumDeliveryMessage bounds the stored (already redacted) failure text.
	maximumDeliveryMessage = 2000

	// terminalJobEventPollInterval is how often terminal jobs are checked for
	// undelivered completion events.
	terminalJobEventPollInterval = 30 * time.Second

	// maximumWatchedTerminalJobs bounds one poll of terminal jobs.
	maximumWatchedTerminalJobs = 200
)

// IntegrationDelivery is one durable record of an attempted local delivery.
type IntegrationDelivery struct {
	ID            string     `json:"id"`
	IntegrationID string     `json:"integration_id"`
	Event         string     `json:"event"`
	SubjectID     string     `json:"subject_id,omitempty"`
	State         string     `json:"state"`
	Attempts      int        `json:"attempts"`
	HTTPStatus    int        `json:"http_status,omitempty"`
	Message       string     `json:"message,omitempty"`
	PayloadBytes  int64      `json:"payload_bytes,omitempty"`
	DurationMS    int64      `json:"duration_ms"`
	CreatedAt     time.Time  `json:"created_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

// integrationDeliveryRepository stores delivery history. It is additive: an
// embedder without it keeps working and simply records no history.
type integrationDeliveryRepository interface {
	ClaimIntegrationDelivery(context.Context, IntegrationDelivery) (bool, error)
	CompleteIntegrationDelivery(context.Context, IntegrationDelivery) error
	ListIntegrationDeliveries(context.Context, string, int) ([]IntegrationDelivery, error)
}

// SupportsIntegrationDeliveries reports whether delivery history is durable.
func (s *Service) SupportsIntegrationDeliveries() bool {
	_, ok := s.repo.(integrationDeliveryRepository)

	return ok
}

// ClaimIntegrationDelivery reserves one (integration, event, subject) delivery.
// It returns false when the subject was already delivered, which is what makes
// repeated polling and a restart safe.
func (s *Service) ClaimIntegrationDelivery(ctx context.Context, delivery IntegrationDelivery) (bool, error) {
	repository, ok := s.repo.(integrationDeliveryRepository)
	if !ok {
		return true, nil
	}

	return repository.ClaimIntegrationDelivery(ctx, delivery)
}

// CompleteIntegrationDelivery records the outcome of a claimed delivery.
func (s *Service) CompleteIntegrationDelivery(ctx context.Context, delivery IntegrationDelivery) error {
	repository, ok := s.repo.(integrationDeliveryRepository)
	if !ok {
		return nil
	}

	return repository.CompleteIntegrationDelivery(ctx, delivery)
}

// ListIntegrationDeliveries returns recent delivery history, newest first. An
// empty integration ID returns history across every destination.
func (s *Service) ListIntegrationDeliveries(ctx context.Context, integrationID string, limit int) ([]IntegrationDelivery, error) {
	repository, ok := s.repo.(integrationDeliveryRepository)
	if !ok {
		return nil, nil
	}

	return repository.ListIntegrationDeliveries(ctx, integrationID, limit)
}

func newDeliveryIdentifier() string {
	return uuid.NewString()
}

// broadcastIntegrationEvent fans one event out to every enabled destination,
// claiming and recording each delivery. Destinations run concurrently and one
// failing destination never blocks another.
func (s *Server) broadcastIntegrationEvent(ctx context.Context, event integrationEvent) {
	if s == nil || s.svc == nil {
		return
	}
	integrations, err := s.svc.ListIntegrations(ctx, true, maximumIntegrations)
	if err != nil {
		return
	}
	var wait sync.WaitGroup
	for _, integration := range integrations {
		wait.Add(1)
		go func(id string) {
			defer wait.Done()
			s.runIntegrationDelivery(ctx, id, event)
		}(integration.ID)
	}
	wait.Wait()
}

// runIntegrationDelivery claims, performs, and records one delivery.
func (s *Server) runIntegrationDelivery(ctx context.Context, integrationID string, event integrationEvent) {
	claim := IntegrationDelivery{
		ID: newDeliveryIdentifier(), IntegrationID: integrationID, Event: event.Name,
		SubjectID: event.SubjectID, State: "pending", CreatedAt: time.Now().UTC(),
	}
	claimed, err := s.svc.ClaimIntegrationDelivery(ctx, claim)
	if err != nil || !claimed {
		return
	}
	integration, err := s.svc.GetIntegrationSecret(ctx, integrationID)
	started := time.Now()
	if err == nil {
		err = s.deliverIntegrationEvent(ctx, integration, event)
	}
	finished := time.Now().UTC()
	claim.DurationMS = time.Since(started).Milliseconds()
	claim.FinishedAt = &finished
	switch {
	case err == nil:
		claim.State = "delivered"
	case errors.Is(err, errIntegrationNotApplicable):
		claim.State = "skipped"
		claim.Message = "destination does not handle this event"
	default:
		claim.State = "failed"
		claim.Message = redactedDeliveryMessage(err)
	}
	claim.Attempts = deliveryAttempts(claim.State)
	if completeErr := s.svc.CompleteIntegrationDelivery(context.WithoutCancel(ctx), claim); completeErr != nil {
		log.Printf("integration delivery history was not recorded: %v", jobruntime.RedactString(completeErr.Error()))
	}
	if repository, repositoryErr := s.svc.integrationRepository(); repositoryErr == nil && claim.State != "skipped" {
		_ = repository.RecordIntegrationRun(context.WithoutCancel(ctx), integrationID, finished, claim.Message)
	}
}

// deliveryAttempts reports how many requests a finished delivery made. A
// success is one attempt by construction; a failure exhausted the retry budget
// unless the receiver rejected it permanently, which the message records.
func deliveryAttempts(state string) int {
	if state == "failed" {
		return webhookMaximumAttempts
	}

	return 1
}

func redactedDeliveryMessage(err error) string {
	if err == nil {
		return ""
	}
	message := jobruntime.RedactString(err.Error())
	if len(message) > maximumDeliveryMessage {
		message = message[:maximumDeliveryMessage]
	}

	return message
}

// watchTerminalJobEvents emits job.completed and job.failed events for jobs
// that have reached a terminal legacy status. Delivery claims make the poll
// idempotent, so a restart re-checks the same jobs without re-sending.
func (s *Server) watchTerminalJobEvents(ctx context.Context) {
	ticker := time.NewTicker(terminalJobEventPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.emitTerminalJobEvents(ctx)
		}
	}
}

// emitTerminalJobEvents runs one poll. It is exported to tests through the
// server rather than a package-level function so the repository stays injected.
func (s *Server) emitTerminalJobEvents(ctx context.Context) {
	if s == nil || s.svc == nil || !s.svc.SupportsIntegrationDeliveries() {
		return
	}
	for status, event := range map[string]string{
		StatusOK:     IntegrationEventJobCompleted,
		StatusFailed: IntegrationEventJobFailed,
	} {
		jobs, err := s.svc.repo.Select(ctx, SelectParams{Status: status, Limit: maximumWatchedTerminalJobs})
		if err != nil {
			continue
		}
		for _, job := range jobs {
			s.broadcastIntegrationEvent(ctx, integrationEvent{
				Name: event, SubjectID: job.ID, OccurredAt: time.Now().UTC(),
				Data: map[string]any{
					"job_id":     job.ID,
					"job_name":   job.Name,
					"status":     job.Status,
					"created_at": job.Date.UTC(),
				},
			})
		}
	}
}
