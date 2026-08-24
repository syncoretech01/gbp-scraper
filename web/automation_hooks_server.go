package web

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// runJobCompletedHook invokes the operator's completion program once a job
// reaches a terminal state and its results are committed. A missing hook is a
// no-op, and a failing hook is recorded rather than allowed to affect the job:
// the scrape is already finished and its rows are already durable.
func (s *Server) runJobCompletedHook(ctx context.Context, jobID string, facts map[string]any) {
	if s == nil || !s.hooks.Enabled(HookJobCompleted) {
		return
	}

	_, run, invoked := s.hooks.Run(ctx, HookJobCompleted, jobID, facts)
	if !invoked {
		return
	}

	s.recordAutomationHookRun(ctx, jobID, run)
}

// RunAutomationHook invokes one extension point and returns whatever JSON the
// program emitted. Callers treat the result as advisory: a hook that is absent,
// slow, or malformed must never change the outcome of the pipeline it extends.
func (s *Server) RunAutomationHook(
	ctx context.Context,
	point, subjectID string,
	payload any,
) map[string]any {
	if s == nil || !s.hooks.Enabled(point) {
		return nil
	}

	result, run, invoked := s.hooks.Run(ctx, point, subjectID, payload)
	if !invoked {
		return nil
	}

	s.recordAutomationHookRun(ctx, subjectID, run)

	return result
}

// recordAutomationHookRun writes the audit trail. Job-scoped runs join the
// durable job event log so they appear in the Job Monitor beside everything
// else that happened to the job; the rest are best-effort.
func (s *Server) recordAutomationHookRun(ctx context.Context, subjectID string, run AutomationHookRun) {
	if s.svc == nil || subjectID == "" {
		return
	}

	severity := "information"
	message := fmt.Sprintf("Local automation hook %q completed with exit code %d", run.Point, run.ExitCode)

	if run.Err != "" || run.ExitCode != 0 {
		severity = "warning"
		message = fmt.Sprintf("Local automation hook %q reported a problem", run.Point)
	}

	_ = s.svc.RecordJobWorkerEvent(
		context.WithoutCancel(ctx), subjectID, "automation-hook", severity, message,
		map[string]any{
			"point":       run.Point,
			"program":     run.Program,
			"exit_code":   run.ExitCode,
			"duration_ms": run.Duration.Milliseconds(),
			"output":      run.Output,
			"error":       run.Err,
		},
	)
}

// automationHookView is the read-only status the System page renders. There is
// deliberately no writer: hooks are configured from the operator's environment
// so that no request can introduce a command.
type automationHookView struct {
	Point      string
	Program    string
	Source     string
	Configured bool
	LastRun    string
	LastExit   int
	LastStatus string
}

// automationHookViews describes every declared point, configured or not, so an
// operator can see which variable to set.
func (s *Server) automationHookViews() []automationHookView {
	runs := make(map[string]AutomationHookRun)
	for _, run := range s.hooks.LastRuns() {
		runs[run.Point] = run
	}

	configured := make(map[string]AutomationHook)
	for _, hook := range s.hooks.Configured() {
		configured[hook.Point] = hook
	}

	views := make([]automationHookView, 0, len(automationHookPoints()))

	for _, point := range automationHookPoints() {
		view := automationHookView{Point: point, Source: automationHookEnvName(point)}

		if hook, ok := configured[point]; ok {
			view.Configured = true
			view.Program = hook.Command[0]
		}

		if run, ok := runs[point]; ok {
			view.LastRun = run.OccurredAt.Format(time.RFC3339)
			view.LastExit = run.ExitCode
			view.LastStatus = "succeeded"

			if run.Err != "" || run.ExitCode != 0 {
				view.LastStatus = "failed"
			}
		}

		views = append(views, view)
	}

	return views
}

// registerAutomationHookRoutes exposes the read-only status. There is no
// mutation route by design.
func (s *Server) registerAutomationHookRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/automation/hooks", func(w http.ResponseWriter, _ *http.Request) {
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: s.automationHookViews()})
	})
}
