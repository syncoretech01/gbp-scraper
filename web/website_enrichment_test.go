package web

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web/enrichment"
)

func TestEnrichmentOptionsAreBoundedAndLegacyJobsRemainCompatible(t *testing.T) {
	t.Parallel()

	options, enabled, err := EnrichmentOptionsForJob(JobData{Email: true})
	if err != nil || !enabled || options.Scope != string(enrichment.ScopeContactAbout) ||
		options.MaxPages != 3 || options.TimeoutSeconds != 10 {
		t.Fatalf("legacy enrichment options = %+v, %v, %v", options, enabled, err)
	}
	if _, _, err := EnrichmentOptionsForJob(JobData{Enrichment: &JobEnrichmentOptions{
		Website: true, Scope: "everything", MaxPages: 3,
	}}); !errors.Is(err, ErrInvalidEnrichment) {
		t.Fatalf("invalid scope error = %v", err)
	}
	if _, err := (EnrichmentOptions{MaxPages: maximumEnrichmentPages + 1}).normalized(); !errors.Is(err, ErrInvalidEnrichment) {
		t.Fatalf("unbounded pages error = %v", err)
	}
}

func TestProcessEnrichmentQueuePersistsAndFinishesTask(t *testing.T) {
	t.Parallel()

	task := EnrichmentTask{
		ID: "task-1", BusinessID: "business-1", WebsiteURL: "https://example.com",
		State: "queued", Options: EnrichmentOptions{
			Scope: string(enrichment.ScopeHomepage), MaxPages: 1, TimeoutSeconds: 5,
			MaxBodyBytes: 2048, MaxRedirects: 2, DisableInternalChecks: true,
		},
	}
	repository := &enrichmentRepositoryStub{next: &task}
	service := NewService(repository, t.TempDir())
	factory := func(received EnrichmentOptions) (websiteAnalyzer, error) {
		if received.Scope != string(enrichment.ScopeHomepage) {
			t.Fatalf("factory options = %+v", received)
		}
		return analyzerStub{result: enrichment.Result{
			RequestedURL: task.WebsiteURL, FinalURL: task.WebsiteURL,
			Reachable: true, StatusCode: 200,
		}}, nil
	}

	processed, err := service.processEnrichmentQueue(context.Background(), 2, factory)
	if err != nil || processed != 1 {
		t.Fatalf("processEnrichmentQueue() = %d, %v", processed, err)
	}
	if repository.stored != task.ID || repository.finished != task.ID || repository.finishErr != nil ||
		repository.finishedAudit == nil || *repository.finishedAudit != 42 {
		t.Fatalf("repository state = %+v", repository)
	}
}

type analyzerStub struct {
	result enrichment.Result
	err    error
}

func (analyzer analyzerStub) Analyze(context.Context, string) (enrichment.Result, error) {
	return analyzer.result, analyzer.err
}

type enrichmentRepositoryStub struct {
	next          *EnrichmentTask
	pending       []EnrichmentTask
	stored        string
	finished      string
	finishedAudit *int64
	finishErr     error
	attachedAudit int64
	attachedPath  string
	errorAudit    int64
	errorPath     string
	attachErr     error
	events        []string
	eventDetails  []string
}

func (*enrichmentRepositoryStub) Get(context.Context, string) (Job, error) {
	return Job{}, errors.New("not implemented")
}
func (*enrichmentRepositoryStub) Create(context.Context, *Job) error   { return nil }
func (*enrichmentRepositoryStub) Delete(context.Context, string) error { return nil }
func (*enrichmentRepositoryStub) Select(context.Context, SelectParams) ([]Job, error) {
	return nil, nil
}
func (*enrichmentRepositoryStub) Update(context.Context, *Job) error { return nil }

func (*enrichmentRepositoryStub) QueueBusinessEnrichment(
	context.Context, []string, EnrichmentOptions, string, string,
) (EnrichmentBatch, error) {
	return EnrichmentBatch{}, nil
}

func (*enrichmentRepositoryStub) QueueJobEnrichment(
	context.Context, string, EnrichmentOptions,
) (EnrichmentBatch, error) {
	return EnrichmentBatch{}, nil
}

func (*enrichmentRepositoryStub) RecoverEnrichmentTasks(context.Context) (int, error) {
	return 0, nil
}

func (repository *enrichmentRepositoryStub) ClaimEnrichmentTask(context.Context) (EnrichmentTask, bool, error) {
	if len(repository.pending) > 0 {
		task := repository.pending[0]
		repository.pending = repository.pending[1:]

		return task, true, nil
	}
	if repository.next == nil {
		return EnrichmentTask{}, false, nil
	}
	task := *repository.next
	repository.next = nil

	return task, true, nil
}

func (repository *enrichmentRepositoryStub) StoreWebsiteAudit(
	_ context.Context,
	task EnrichmentTask,
	_ enrichment.Result,
	_ time.Time,
	_ time.Time,
) (int64, error) {
	repository.stored = task.ID

	return 42, nil
}

func (repository *enrichmentRepositoryStub) FinishEnrichmentTask(
	_ context.Context,
	taskID string,
	auditID *int64,
	taskErr error,
) error {
	repository.finished = taskID
	repository.finishedAudit = auditID
	repository.finishErr = taskErr

	return nil
}

func (*enrichmentRepositoryStub) ListEnrichmentTasks(context.Context, int) ([]EnrichmentTask, error) {
	return nil, nil
}

func (*enrichmentRepositoryStub) WebsiteAuditHistory(context.Context, string, int) ([]WebsiteAuditView, error) {
	return nil, nil
}

func (repository *enrichmentRepositoryStub) AttachAuditScreenshot(
	_ context.Context,
	auditID int64,
	relativePath string,
) error {
	repository.attachedAudit = auditID
	repository.attachedPath = relativePath

	return repository.attachErr
}

// AttachAuditErrorScreenshot satisfies the optional error-capture
// attachment point used by the enrichment queue.
func (repository *enrichmentRepositoryStub) AttachAuditErrorScreenshot(
	_ context.Context,
	auditID int64,
	relativePath string,
) error {
	repository.errorAudit = auditID
	repository.errorPath = relativePath

	return repository.attachErr
}

func (repository *enrichmentRepositoryStub) RecordScreenshotEvent(
	_ context.Context,
	action string,
	_ string,
	details string,
) error {
	repository.events = append(repository.events, action)
	repository.eventDetails = append(repository.eventDetails, details)

	return nil
}
