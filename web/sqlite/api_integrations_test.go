package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

func TestAPIKeysStoreHashesAndRequestLogsOmitSecrets(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	record := web.APIKeyRecord{
		ID: "api-key-one", Name: "Automation", Permission: "full", Enabled: true, CreatedAt: now,
	}
	hash := strings.Repeat("a", 64)
	if err := repository.CreateAPIKey(ctx, record, hash); err != nil {
		t.Fatal(err)
	}
	var storedHash string
	if err := repository.db.QueryRow("SELECT key_hash FROM api_keys WHERE id = ?", record.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != hash {
		t.Fatalf("stored hash = %q", storedHash)
	}
	authenticated, err := repository.AuthenticateAPIKey(ctx, hash, now.Add(time.Minute))
	if err != nil || authenticated.ID != record.ID || authenticated.LastUsedAt == nil {
		t.Fatalf("AuthenticateAPIKey() = %+v, %v", authenticated, err)
	}
	if _, err := repository.AuthenticateAPIKey(ctx, strings.Repeat("b", 64), now); !errors.Is(err, web.ErrAPIKeyNotFound) {
		t.Fatalf("unknown hash error = %v", err)
	}

	logRecord := web.APIRequestLog{
		Method: "GET", Path: "/api/v1/results", StatusCode: 200,
		DurationMS: 12, APIKeyID: record.ID, CreatedAt: now,
	}
	if err := repository.RecordAPIRequest(ctx, logRecord); err != nil {
		t.Fatal(err)
	}
	logs, err := repository.ListAPIRequestLogs(ctx, 10)
	if err != nil || len(logs) != 1 || logs[0].Path != logRecord.Path || logs[0].APIKeyID != record.ID {
		t.Fatalf("request logs = %+v, %v", logs, err)
	}
	if err := repository.SetAPIKeyEnabled(ctx, record.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AuthenticateAPIKey(ctx, hash, now); !errors.Is(err, web.ErrAPIKeyNotFound) {
		t.Fatalf("disabled key authentication error = %v", err)
	}
}

func TestIntegrationsEncryptExecutableConfiguration(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	secret := `{"url":"http://127.0.0.1:5678/webhook?access_token=secret-token"}`
	record := web.IntegrationRecord{
		ID: "integration-one", Name: "n8n", Kind: web.IntegrationWebhook, Enabled: true,
		Configuration: `{"url":"http://127.0.0.1:5678/webhook?access_token=***"}`,
		CreatedAt:     now, UpdatedAt: now,
	}
	if err := repository.SaveIntegration(ctx, record, secret); err != nil {
		t.Fatal(err)
	}
	var encrypted string
	if err := repository.db.QueryRow(
		"SELECT secret_configuration FROM integrations WHERE id = ?", record.ID,
	).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if encrypted == secret || strings.Contains(encrypted, "secret-token") {
		t.Fatalf("secret configuration was not encrypted: %q", encrypted)
	}
	listed, err := repository.ListIntegrations(ctx, false, 10)
	if err != nil || len(listed) != 1 || strings.Contains(listed[0].Configuration, "secret-token") {
		t.Fatalf("safe integration list = %+v, %v", listed, err)
	}
	resolved, err := repository.GetIntegrationSecret(ctx, record.ID)
	if err != nil || resolved.Secret != secret || resolved.Record.Name != "n8n" {
		t.Fatalf("resolved integration = %+v, %v", resolved, err)
	}
	if err := repository.RecordIntegrationRun(ctx, record.ID, now.Add(time.Minute), "delivered"); err != nil {
		t.Fatal(err)
	}
	listed, err = repository.ListIntegrations(ctx, true, 10)
	if err != nil || len(listed) != 1 || listed[0].LastError != "delivered" || listed[0].LastRunAt == nil {
		t.Fatalf("updated integration = %+v, %v", listed, err)
	}
	if err := repository.DeleteIntegration(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetIntegrationSecret(ctx, record.ID); !errors.Is(err, web.ErrIntegrationNotFound) {
		t.Fatalf("deleted integration error = %v", err)
	}
}

func TestSelectedExportIDFilterUsesOneBoundCondition(t *testing.T) {
	t.Parallel()

	clause, arguments, err := resultFilterSQL(web.ResultFilter{
		Field: "id", Operator: "in", Value: "business-one,business-two,business_three",
	})
	if err != nil {
		t.Fatal(err)
	}
	if clause != "businesses.id IN (?,?,?)" || len(arguments) != 3 || arguments[1] != "business-two" {
		t.Fatalf("ID filter = %q, %#v", clause, arguments)
	}
	for _, value := range []string{"../unsafe", "tiny", "business-one,unsafe/id"} {
		if _, _, err := resultFilterSQL(web.ResultFilter{Field: "id", Operator: "in", Value: value}); err == nil {
			t.Fatalf("unsafe ID filter %q was accepted", value)
		}
	}
}
