package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// Local automation hooks run an operator's own program at defined points in
// the workspace's lifecycle: after a job finishes, and around enrichment,
// validation, scoring, and export.
//
// SAFETY MODEL — read this before changing anything here.
//
// A hook command is read ONLY from the process environment at start-up. It is
// deliberately not a setting, not a database row, and not a form field, so no
// HTTP request — authenticated, forged, or otherwise — can introduce or alter
// a command. Anyone able to set these variables already chooses which binary
// this process is, so a hook grants them nothing they did not already have;
// that is what keeps this from being a privilege escalation, and it is why the
// configuration surface must never move into the UI or the database.
//
// Each command is an argv slice parsed as JSON, never a shell string. Nothing
// is passed through a shell, and no scraped value is ever interpolated into a
// command line: job facts travel as a JSON document on stdin and as a small
// set of GMAPS_HOOK_* environment variables. Every run is bounded by a
// timeout, its output is captured and truncated, a non-zero exit is recorded
// but never fails the job, and every invocation is audit-logged.
const (
	// automationHookEnvPrefix is the environment prefix an operator sets to
	// configure a hook, e.g. GMAPS_HOOK_JOB_COMPLETED.
	automationHookEnvPrefix = "GMAPS_HOOK_"
	// automationHookTimeoutEnv optionally overrides the per-run timeout.
	automationHookTimeoutEnv = "GMAPS_HOOK_TIMEOUT_SECONDS"

	automationHookDefaultTimeout = 60 * time.Second
	automationHookMaximumTimeout = 10 * time.Minute
	// automationHookOutputLimit bounds captured stdout/stderr so a chatty
	// program cannot grow the event log without bound.
	automationHookOutputLimit = 8 << 10
	// automationHookMaximumArgs bounds a configured argv slice.
	automationHookMaximumArgs = 64
)

// Automation hook points. Each is configured independently.
const (
	HookJobCompleted = "job_completed"
	HookEnrichment   = "enrichment"
	HookValidation   = "validation"
	HookScoring      = "scoring"
	HookExport       = "export"
)

// automationHookPoints is the declared set, in presentation order.
func automationHookPoints() []string {
	return []string{HookJobCompleted, HookEnrichment, HookValidation, HookScoring, HookExport}
}

// automationHookEnvName is the environment variable that configures one point.
func automationHookEnvName(point string) string {
	return automationHookEnvPrefix + strings.ToUpper(point)
}

// AutomationHook is one configured extension point.
type AutomationHook struct {
	// Point is the lifecycle position, e.g. HookJobCompleted.
	Point string `json:"point"`
	// Command is the argv slice; Command[0] is the program.
	Command []string `json:"command"`
	// Source names where the configuration came from, for the UI.
	Source string `json:"source"`
}

// AutomationHookRun is the audit record of one invocation.
type AutomationHookRun struct {
	Point      string        `json:"point"`
	Program    string        `json:"program"`
	Subject    string        `json:"subject_id,omitempty"`
	ExitCode   int           `json:"exit_code"`
	Duration   time.Duration `json:"duration"`
	Output     string        `json:"output,omitempty"`
	Err        string        `json:"error,omitempty"`
	OccurredAt time.Time     `json:"occurred_at"`
}

// AutomationHooks resolves and runs the configured hooks. The zero value is
// usable and simply has nothing configured.
type AutomationHooks struct {
	mu      sync.RWMutex
	hooks   map[string]AutomationHook
	timeout time.Duration
	last    map[string]AutomationHookRun

	// lookup is the environment reader; tests replace it.
	lookup func(string) (string, bool)
	// run executes the command; tests replace it.
	run func(ctx context.Context, hook AutomationHook, stdin []byte, env []string) (output []byte, exitCode int, err error)
}

// NewAutomationHooks reads the operator's configuration from the environment.
// A malformed value disables that point rather than failing start-up: a typo
// in a hook must never stop the workspace from serving.
func NewAutomationHooks() *AutomationHooks {
	hooks := &AutomationHooks{lookup: os.LookupEnv}
	hooks.load()

	return hooks
}

func (a *AutomationHooks) load() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.hooks = make(map[string]AutomationHook)
	a.last = make(map[string]AutomationHookRun)
	a.timeout = automationHookDefaultTimeout

	if raw, ok := a.lookup(automationHookTimeoutEnv); ok {
		if seconds, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && seconds > 0 {
			timeout := time.Duration(seconds) * time.Second
			if timeout > automationHookMaximumTimeout {
				timeout = automationHookMaximumTimeout
			}
			a.timeout = timeout
		}
	}

	for _, point := range automationHookPoints() {
		raw, ok := a.lookup(automationHookEnvName(point))
		if !ok {
			continue
		}

		command, err := parseHookCommand(raw)
		if err != nil {
			continue
		}

		a.hooks[point] = AutomationHook{
			Point: point, Command: command, Source: automationHookEnvName(point),
		}
	}
}

// parseHookCommand accepts a JSON argv array. A JSON array is required rather
// than a command line because splitting a string would reintroduce quoting
// rules, and quoting rules are how command injection happens.
func parseHookCommand(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty hook command")
	}

	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		return nil, fmt.Errorf("hook command must be a JSON array of arguments: %w", err)
	}

	if len(command) == 0 || len(command) > automationHookMaximumArgs {
		return nil, fmt.Errorf("hook command must hold between 1 and %d arguments", automationHookMaximumArgs)
	}

	for _, argument := range command {
		if strings.TrimSpace(argument) == "" {
			return nil, fmt.Errorf("hook command arguments must not be blank")
		}
	}

	if !filepath.IsAbs(command[0]) {
		return nil, fmt.Errorf("hook program must be an absolute path")
	}

	return command, nil
}

// Configured reports the hooks an operator has set, in presentation order.
func (a *AutomationHooks) Configured() []AutomationHook {
	if a == nil {
		return nil
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	configured := make([]AutomationHook, 0, len(a.hooks))
	for _, hook := range a.hooks {
		configured = append(configured, hook)
	}

	sort.Slice(configured, func(i, j int) bool {
		return configured[i].Point < configured[j].Point
	})

	return configured
}

// LastRuns returns the most recent invocation per point, for display only.
func (a *AutomationHooks) LastRuns() []AutomationHookRun {
	if a == nil {
		return nil
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	runs := make([]AutomationHookRun, 0, len(a.last))
	for _, run := range a.last {
		runs = append(runs, run)
	}

	sort.Slice(runs, func(i, j int) bool { return runs[i].Point < runs[j].Point })

	return runs
}

// Enabled reports whether a point has a command configured.
func (a *AutomationHooks) Enabled(point string) bool {
	if a == nil {
		return false
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.hooks[point]

	return ok
}

// Run invokes the hook for one point with a JSON payload on stdin. It returns
// the program's parsed JSON response when it emitted one, so an extension
// point can contribute a result; malformed output is ignored rather than
// failing the surrounding pipeline. A point with no configured command is a
// no-op, which is what keeps the default build byte-identical.
func (a *AutomationHooks) Run(
	ctx context.Context,
	point, subjectID string,
	payload any,
) (map[string]any, AutomationHookRun, bool) {
	if a == nil {
		return nil, AutomationHookRun{}, false
	}

	a.mu.RLock()
	hook, ok := a.hooks[point]
	timeout := a.timeout
	runner := a.run
	a.mu.RUnlock()

	if !ok {
		return nil, AutomationHookRun{}, false
	}

	document, err := json.Marshal(map[string]any{
		"point": point, "subject_id": subjectID, "payload": payload,
	})
	if err != nil {
		document = []byte("{}")
	}

	environment := append(os.Environ(),
		"GMAPS_HOOK_POINT="+point,
		"GMAPS_HOOK_SUBJECT_ID="+subjectID,
	)

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	started := time.Now()

	if runner == nil {
		runner = execHookCommand
	}

	output, exitCode, runErr := runner(runCtx, hook, document, environment)

	run := AutomationHookRun{
		Point: point, Program: hook.Command[0], Subject: subjectID,
		ExitCode: exitCode, Duration: time.Since(started),
		Output:     truncateHookOutput(output),
		OccurredAt: started.UTC(),
	}
	if runErr != nil {
		run.Err = jobruntime.RedactString(runErr.Error())
	}

	a.mu.Lock()
	a.last[point] = run
	a.mu.Unlock()

	var result map[string]any
	if runErr == nil && exitCode == 0 {
		_ = json.Unmarshal(bytes.TrimSpace(output), &result)
	}

	return result, run, true
}

// execHookCommand is the real executor. The command is run directly from its
// argv slice: there is no shell, so no quoting or expansion can occur.
func execHookCommand(
	ctx context.Context,
	hook AutomationHook,
	stdin []byte,
	env []string,
) ([]byte, int, error) {
	command := exec.CommandContext(ctx, hook.Command[0], hook.Command[1:]...) //nolint:gosec // argv comes from the operator's own environment, never from a request
	command.Stdin = bytes.NewReader(stdin)
	command.Env = env

	output, err := command.CombinedOutput()
	exitCode := 0

	var exitErr *exec.ExitError
	if err != nil {
		if ok := asExitError(err, &exitErr); ok {
			exitCode = exitErr.ExitCode()
			err = nil
		}
	}

	return output, exitCode, err
}

func truncateHookOutput(output []byte) string {
	text := strings.TrimSpace(string(output))
	if len(text) <= automationHookOutputLimit {
		return jobruntime.RedactString(text)
	}

	return jobruntime.RedactString(text[:automationHookOutputLimit]) + "\n… output truncated"
}

// asExitError reports whether err is an *exec.ExitError, keeping the errors
// import local to one helper.
func asExitError(err error, target **exec.ExitError) bool {
	return errors.As(err, target)
}
