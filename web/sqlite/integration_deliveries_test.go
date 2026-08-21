package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

func TestIntegrationDeliveriesClaimOnceAndRecordOutcomes(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()

	integration := web.IntegrationRecord{
		ID: "integration-one", Name: "Local n8n", Kind: web.IntegrationWebhook, Enabled: true,
		Configuration: `{"url":"http://127.0.0.1:5678/webhook"}`, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.SaveIntegration(ctx, integration, `{"url":"http://127.0.0.1:5678/webhook"}`); err != nil {
		t.Fatal(err)
	}

	delivery := web.IntegrationDelivery{
		ID: "delivery-one", IntegrationID: integration.ID,
		Event: web.IntegrationEventExportCompleted, SubjectID: "export-one", CreatedAt: now,
	}

	claimed, err := repository.ClaimIntegrationDelivery(ctx, delivery)
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}

	// The same subject must never be claimed twice; this is what makes the
	// terminal-job poll and a restart safe to repeat.
	repeat := delivery
	repeat.ID = "delivery-two"

	claimed, err = repository.ClaimIntegrationDelivery(ctx, repeat)
	if err != nil || claimed {
		t.Fatalf("repeat claim = %v, %v", claimed, err)
	}

	// A subject-less delivery (an ad-hoc test) is always allowed.
	adHoc := web.IntegrationDelivery{
		ID: "delivery-three", IntegrationID: integration.ID,
		Event: web.IntegrationEventTest, CreatedAt: now.Add(time.Second),
	}
	if claimed, err := repository.ClaimIntegrationDelivery(ctx, adHoc); err != nil || !claimed {
		t.Fatalf("ad-hoc claim = %v, %v", claimed, err)
	}

	adHoc.ID = "delivery-four"
	if claimed, err := repository.ClaimIntegrationDelivery(ctx, adHoc); err != nil || !claimed {
		t.Fatalf("second ad-hoc claim = %v, %v", claimed, err)
	}

	finished := now.Add(2 * time.Second)
	delivery.State = "delivered"
	delivery.Attempts = 2
	delivery.HTTPStatus = 200
	delivery.DurationMS = 42
	delivery.FinishedAt = &finished

	if err := repository.CompleteIntegrationDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}

	history, err := repository.ListIntegrationDeliveries(ctx, integration.ID, 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(history) != 3 {
		t.Fatalf("history rows = %d, want 3", len(history))
	}

	var completed web.IntegrationDelivery

	for _, record := range history {
		if record.ID == delivery.ID {
			completed = record
		}
	}

	if completed.State != "delivered" || completed.Attempts != 2 || completed.DurationMS != 42 {
		t.Fatalf("completed delivery = %+v", completed)
	}

	if completed.FinishedAt == nil || !completed.FinishedAt.Equal(finished) {
		t.Fatalf("completed finished at = %v", completed.FinishedAt)
	}

	all, err := repository.ListIntegrationDeliveries(ctx, "", 10)
	if err != nil || len(all) != 3 {
		t.Fatalf("unfiltered history = %d rows, %v", len(all), err)
	}

	// Deleting the integration must not leave orphaned delivery history.
	if err := repository.DeleteIntegration(ctx, integration.ID); err != nil {
		t.Fatal(err)
	}

	remaining, err := repository.ListIntegrationDeliveries(ctx, "", 10)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("history after delete = %d rows, %v", len(remaining), err)
	}
}
