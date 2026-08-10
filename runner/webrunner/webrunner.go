package webrunner

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/deduper"
	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/google-maps-scraper/grid"
	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/tlmt"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
	"github.com/gosom/google-maps-scraper/web/resultimport"
	"github.com/gosom/google-maps-scraper/web/sqlite"
	"github.com/gosom/scrapemate"
	"github.com/gosom/scrapemate/adapters/writers/csvwriter"
	"github.com/gosom/scrapemate/scrapemateapp"
	"golang.org/x/sync/errgroup"
)

type webrunner struct {
	srv       *web.Server
	svc       *web.Service
	cfg       *runner.Config
	setupMate func(context.Context, io.Writer, *web.Job) (mateRunner, error)
}

type mateRunner interface {
	Start(context.Context, ...scrapemate.IJob) error
	Close() error
}

func New(cfg *runner.Config) (runner.Runner, error) {
	if cfg.DataFolder == "" {
		return nil, fmt.Errorf("data folder is required")
	}

	if err := os.MkdirAll(cfg.DataFolder, os.ModePerm); err != nil {
		return nil, err
	}

	const dbfname = "jobs.db"

	dbpath := filepath.Join(cfg.DataFolder, dbfname)

	repo, err := sqlite.New(dbpath)
	if err != nil {
		return nil, err
	}

	svc := web.NewService(repo, cfg.DataFolder)

	srv, err := web.New(svc, cfg.Addr)
	if err != nil {
		return nil, err
	}

	ans := webrunner{
		srv:       srv,
		svc:       svc,
		cfg:       cfg,
		setupMate: defaultSetupMate(cfg),
	}

	return &ans, nil
}

func (w *webrunner) Run(ctx context.Context) error {
	if err := recoverResultRunFiles(ctx, w.cfg.DataFolder); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		log.Printf("warning: some interrupted result run files could not be recovered: %v", err)
	}

	if _, err := w.svc.BackfillLegacyResults(ctx); err != nil &&
		!errors.Is(err, web.ErrResultStoreUnsupported) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		log.Printf("warning: some legacy result CSV files could not be imported: %v", err)
	}

	egroup, ctx := errgroup.WithContext(ctx)

	egroup.Go(func() error {
		return w.work(ctx)
	})

	egroup.Go(func() error {
		return w.srv.Start(ctx)
	})

	return egroup.Wait()
}

func (w *webrunner) Close(context.Context) error {
	return nil
}

func (w *webrunner) work(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if scheduled, err := w.svc.StartDueSchedules(ctx, time.Now().UTC(), 10); err != nil {
				if !errors.Is(err, web.ErrScheduleStoreUnsupported) {
					return err
				}
			} else if len(scheduled) > 0 {
				log.Printf("queued %d due scheduled jobs", len(scheduled))
			}
			jobs, err := w.svc.SelectPending(ctx)
			if err != nil {
				return err
			}

			for i := range jobs {
				select {
				case <-ctx.Done():
					return nil
				default:
					t0 := time.Now().UTC()
					if err := w.scrapeJob(ctx, &jobs[i]); err != nil {
						params := map[string]any{
							"job_count": len(jobs[i].Data.Keywords),
							"duration":  time.Now().UTC().Sub(t0).String(),
							"error":     err.Error(),
						}

						evt := tlmt.NewEvent("web_runner", params)

						_ = runner.Telemetry().Send(ctx, evt)

						log.Printf("error scraping job %s: %v", jobs[i].ID, err)
					} else {
						params := map[string]any{
							"job_count": len(jobs[i].Data.Keywords),
							"duration":  time.Now().UTC().Sub(t0).String(),
						}

						_ = runner.Telemetry().Send(ctx, tlmt.NewEvent("web_runner", params))

						log.Printf("job %s scraped successfully", jobs[i].ID)
					}
				}
			}
		}
	}
}

func (w *webrunner) scrapeJob(ctx context.Context, job *web.Job) error {
	job.Status = web.StatusWorking

	err := w.svc.Update(ctx, job)
	if err != nil {
		return err
	}

	if len(job.Data.Keywords) == 0 {
		return w.failJob(ctx, job, fmt.Errorf("job has no queries"))
	}
	if job.Data.ProxyPoolID != "" {
		proxies, resolveErr := w.svc.ResolveProxyPool(ctx, job.Data.ProxyPoolID)
		if resolveErr != nil {
			return w.failJob(ctx, job, fmt.Errorf("resolve proxy pool: %w", resolveErr))
		}
		if len(proxies) == 0 {
			return w.failJob(ctx, job, fmt.Errorf("proxy pool has no enabled usable proxies"))
		}
		// Credentials remain transient: the persisted job stores only the pool
		// ID and this in-memory slice is created after the status update.
		job.Data.Proxies = proxies
	}

	outpath := filepath.Join(w.cfg.DataFolder, job.ID+".csv")

	useRunFile := false
	if info, statErr := os.Stat(outpath); statErr == nil {
		useRunFile = info.Size() > 0
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect existing result CSV: %w", statErr)
	}

	var outfile *os.File
	if useRunFile {
		outfile, err = os.CreateTemp(w.cfg.DataFolder, job.ID+".run-*.csv")
	} else {
		outfile, err = os.Create(outpath)
	}
	if err != nil {
		return err
	}
	runPath := outfile.Name()
	outfileClosed := false
	removeRunOnReturn := true

	defer func() {
		if !outfileClosed {
			_ = outfile.Close()
		}
		if removeRunOnReturn {
			_ = os.Remove(runPath)
		}
	}()

	setupMate := w.setupMate
	if setupMate == nil {
		setupMate = defaultSetupMate(w.cfg)
	}

	mate, err := setupMate(ctx, outfile, job)
	if err != nil {
		return w.failJob(ctx, job, err)
	}

	mateClosed := false
	defer func() {
		if !mateClosed {
			_ = mate.Close()
		}
	}()

	var coords string
	if job.Data.Lat != "" && job.Data.Lon != "" {
		coords = job.Data.Lat + "," + job.Data.Lon
	}

	dedup := deduper.New()
	exitMonitor := exiter.New()

	var seedJobs []scrapemate.IJob

	if job.Data.GridBBox != "" {
		var bbox grid.BoundingBox

		bbox, err = grid.ParseBoundingBox(job.Data.GridBBox)
		if err == nil {
			seedJobs, err = runner.CreateGridSeedJobs(
				job.Data.Lang,
				strings.NewReader(strings.Join(job.Data.Keywords, "\n")),
				job.Data.Depth,
				job.Data.Email,
				bbox,
				job.Data.GridCellKM,
				job.Data.Zoom,
				dedup,
				exitMonitor,
				w.cfg.ExtraReviews || job.Data.ExtraReviews,
			)
		}
	} else {
		seedJobs, err = runner.CreateSeedJobs(
			job.Data.FastMode,
			job.Data.Lang,
			strings.NewReader(strings.Join(job.Data.Keywords, "\n")),
			job.Data.Depth,
			job.Data.Email,
			coords,
			job.Data.Zoom,
			func() float64 {
				if job.Data.Radius <= 0 {
					return 10000 // 10 km
				}

				return float64(job.Data.Radius)
			}(),
			dedup,
			exitMonitor,
			w.cfg.ExtraReviews || job.Data.ExtraReviews,
		)
	}
	if err != nil {
		return w.failJob(ctx, job, err)
	}

	stopReason := make(chan jobruntime.StopReason, 1)

	if len(seedJobs) > 0 {
		exitMonitor.SetSeedCount(len(seedJobs))

		allowedSeconds := max(60, len(seedJobs)*10*job.Data.Depth/50+120)

		if job.Data.MaxTime > 0 {
			if job.Data.MaxTime.Seconds() < 180 {
				allowedSeconds = 180
			} else {
				allowedSeconds = int(job.Data.MaxTime.Seconds())
			}
		}

		log.Printf("running job %s with %d seed jobs and %d allowed seconds", job.ID, len(seedJobs), allowedSeconds)

		mateCtx, cancel := context.WithTimeout(ctx, time.Duration(allowedSeconds)*time.Second)
		defer cancel()

		exitMonitor.SetCancelFunc(cancel)

		go exitMonitor.Run(mateCtx)
		go w.watchRequestedStop(mateCtx, job.ID, cancel, stopReason)

		err = mate.Start(mateCtx, seedJobs...)
		removeRunOnReturn = false
		runContextErr := mateCtx.Err()
		cancel()

		if runContextErr != nil && errors.Is(runContextErr, context.DeadlineExceeded) {
			err = context.DeadlineExceeded
		}
	}

	reason := stoppedBecause(ctx, err, stopReason)
	var fatalRunErr error
	if err != nil &&
		!errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, context.Canceled) {
		fatalRunErr = err
	}

	var outcome jobruntime.Outcome
	var classifyErr error
	if fatalRunErr != nil {
		outcome, classifyErr = jobruntime.ClassifyOutcome(jobruntime.RunResult{
			Reason: jobruntime.StopReasonFatalError,
			Err:    fatalRunErr,
		})
	} else {
		tasks := jobruntime.TaskSummary{Total: int64(len(seedJobs))}
		if reason == jobruntime.StopReasonCompleted {
			tasks.Completed = tasks.Total
		}
		outcome, classifyErr = jobruntime.ClassifyOutcome(jobruntime.RunResult{
			Reason: reason,
			Tasks:  tasks,
		})
	}
	if classifyErr != nil {
		return classifyErr
	}

	if err := w.persistOutcome(ctx, job, outcome, outcomeMessage(outcome)); err != nil {
		return err
	}

	if err := mate.Close(); err != nil {
		return fmt.Errorf("close scrape worker: %w", err)
	}
	mateClosed = true
	if info, statErr := outfile.Stat(); statErr != nil {
		return fmt.Errorf("inspect result CSV: %w", statErr)
	} else if info.Size() == 0 {
		headerWriter := csv.NewWriter(outfile)
		if err := headerWriter.Write(resultimport.LegacyHeaders()); err != nil {
			return fmt.Errorf("write empty result CSV header: %w", err)
		}
		headerWriter.Flush()
		if err := headerWriter.Error(); err != nil {
			return fmt.Errorf("flush empty result CSV header: %w", err)
		}
	}
	if err := outfile.Sync(); err != nil {
		return fmt.Errorf("flush result CSV: %w", err)
	}
	if err := outfile.Close(); err != nil {
		return fmt.Errorf("close result CSV: %w", err)
	}
	outfileClosed = true

	mergeSummary := resultMergeSummary{}
	if useRunFile {
		mergeSummary, err = mergeResultCSV(context.Background(), outpath, runPath)
		if err != nil {
			return errors.Join(fatalRunErr, fmt.Errorf("merge result CSV: %w", err))
		}
	}
	removeRunOnReturn = false
	if mergeSummary.ExistingReplaced > 0 || mergeSummary.DuplicatesSkipped > 0 {
		log.Printf(
			"job %s result merge kept=%d replaced=%d added=%d duplicate_rows_skipped=%d",
			job.ID,
			mergeSummary.ExistingKept,
			mergeSummary.ExistingReplaced,
			mergeSummary.RunAdded,
			mergeSummary.DuplicatesSkipped,
		)
	}

	importCtx, cancelImport := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancelImport()
	if _, err := w.svc.ImportJobResults(importCtx, job.ID); err != nil &&
		!errors.Is(err, web.ErrResultStoreUnsupported) {
		return errors.Join(fatalRunErr, fmt.Errorf("import normalized job results: %w", err))
	}

	return fatalRunErr
}

func (w *webrunner) watchRequestedStop(
	ctx context.Context,
	jobID string,
	cancel context.CancelFunc,
	found chan<- jobruntime.StopReason,
) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runtime, err := w.svc.GetRuntime(ctx, jobID)
			if err != nil {
				continue
			}

			switch runtime.RequestedStop {
			case jobruntime.StopReasonPauseRequested, jobruntime.StopReasonUserCancelled:
				select {
				case found <- runtime.RequestedStop:
				default:
				}

				cancel()

				return
			}
		}
	}
}

func stoppedBecause(
	parent context.Context,
	runErr error,
	requested <-chan jobruntime.StopReason,
) jobruntime.StopReason {
	select {
	case reason := <-requested:
		return reason
	default:
	}

	if parent.Err() != nil {
		return jobruntime.StopReasonShutdown
	}

	if errors.Is(runErr, context.DeadlineExceeded) {
		return jobruntime.StopReasonRuntimeLimit
	}

	return jobruntime.StopReasonCompleted
}

func (w *webrunner) failJob(ctx context.Context, job *web.Job, runErr error) error {
	outcome, err := jobruntime.ClassifyOutcome(jobruntime.RunResult{
		Reason: jobruntime.StopReasonFatalError,
		Err:    runErr,
	})
	if err != nil {
		return errors.Join(runErr, err)
	}

	if err := w.persistOutcome(ctx, job, outcome, runErr.Error()); err != nil {
		return errors.Join(runErr, err)
	}

	return runErr
}

func (w *webrunner) persistOutcome(
	_ context.Context,
	job *web.Job,
	outcome jobruntime.Outcome,
	message string,
) error {
	legacyStatus, err := jobruntime.LegacyStatusForState(outcome.State)
	if err != nil {
		return err
	}

	job.Status = string(legacyStatus)

	// Persist terminal evidence even when the run context was cancelled. This
	// short, independent context only covers the local SQLite transaction.
	persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := w.svc.SetOutcome(persistCtx, job.ID, outcome, message); err == nil {
		return nil
	} else if !errors.Is(err, web.ErrLifecycleUnsupported) {
		return err
	}

	return w.svc.Update(persistCtx, job)
}

func outcomeMessage(outcome jobruntime.Outcome) string {
	switch outcome.Reason {
	case jobruntime.StopReasonRuntimeLimit:
		return "Runtime limit reached; partial results were saved"
	case jobruntime.StopReasonMaximumRecords:
		return "Maximum record limit reached; partial results were saved"
	case jobruntime.StopReasonPauseRequested:
		return "Paused at a safe checkpoint"
	case jobruntime.StopReasonUserCancelled:
		return "Cancelled by the operator"
	case jobruntime.StopReasonShutdown:
		return "Paused because the local service stopped"
	case jobruntime.StopReasonTaskFailures, jobruntime.StopReasonTasksIncomplete:
		return "Stopped with incomplete tasks; partial results were saved"
	case jobruntime.StopReasonFatalError:
		return "The scrape stopped after a fatal error"
	default:
		return "Scrape completed"
	}
}

func defaultSetupMate(cfg *runner.Config) func(context.Context, io.Writer, *web.Job) (mateRunner, error) {
	return func(_ context.Context, writer io.Writer, job *web.Job) (mateRunner, error) {
		jobConfig := *cfg
		if job.Data.Concurrency > 0 {
			jobConfig.Concurrency = job.Data.Concurrency
		}
		if job.Data.BrowserPool > 0 {
			jobConfig.BrowserPoolSize = job.Data.BrowserPool
		}
		if job.Data.PagesBrowser > 0 {
			jobConfig.MaxPagesPerBrowser = job.Data.PagesBrowser
		}

		opts := []func(*scrapemateapp.Config) error{
			scrapemateapp.WithConcurrency(jobConfig.Concurrency),
			scrapemateapp.WithExitOnInactivity(time.Minute * 3),
		}

		if !job.Data.FastMode {
			opts = append(opts,
				scrapemateapp.WithJS(scrapemateapp.DisableImages()),
			)
		} else {
			opts = append(opts,
				scrapemateapp.WithStealth("firefox"),
			)
		}

		opts = runner.AppendBrowserCapacityOptions(opts, &jobConfig)

		hasProxy := false

		if len(cfg.Proxies) > 0 {
			opts = append(opts, scrapemateapp.WithProxies(cfg.Proxies))
			hasProxy = true
		} else if len(job.Data.Proxies) > 0 {
			opts = append(opts,
				scrapemateapp.WithProxies(job.Data.Proxies),
			)
			hasProxy = true
		}

		if !cfg.DisablePageReuse {
			opts = append(opts,
				scrapemateapp.WithPageReuseLimit(2),
				scrapemateapp.WithBrowserReuseLimit(200),
			)
		}

		log.Printf("job %s has proxy: %v", job.ID, hasProxy)

		csvWriter := csvwriter.NewCsvWriter(csv.NewWriter(writer))

		writers := []scrapemate.ResultWriter{csvWriter}

		matecfg, err := scrapemateapp.NewConfig(
			writers,
			opts...,
		)
		if err != nil {
			return nil, err
		}

		return scrapemateapp.NewScrapeMateApp(matecfg)
	}
}
