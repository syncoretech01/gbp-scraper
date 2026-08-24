package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func hooksFromEnv(values map[string]string) *AutomationHooks {
	hooks := &AutomationHooks{lookup: func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}}
	hooks.load()

	return hooks
}

func absoluteProgram(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "hook")
}

// TestAutomationHookConfigurationComesOnlyFromTheEnvironment pins the safety
// model: a command is accepted from the process environment and nowhere else.
func TestAutomationHookConfigurationComesOnlyFromTheEnvironment(t *testing.T) {
	t.Parallel()

	program := absoluteProgram(t)
	hooks := hooksFromEnv(map[string]string{
		automationHookEnvName(HookJobCompleted): `["` + filepath.ToSlash(program) + `","--run"]`,
	})

	configured := hooks.Configured()
	if len(configured) != 1 {
		t.Fatalf("configured hooks = %d, want 1", len(configured))
	}

	if configured[0].Point != HookJobCompleted {
		t.Fatalf("point = %q", configured[0].Point)
	}

	if configured[0].Source != "GMAPS_HOOK_JOB_COMPLETED" {
		t.Fatalf("source = %q, want the environment variable name", configured[0].Source)
	}

	if !hooks.Enabled(HookJobCompleted) || hooks.Enabled(HookExport) {
		t.Fatal("only the configured point may be enabled")
	}
}

// TestNoHTTPRouteCanConfigureAnAutomationHook is the regression that keeps this
// feature from becoming a remote-code-execution surface: every registered route
// is exercised, and none of them may leave a hook configured.
func TestNoHTTPRouteCanConfigureAnAutomationHook(t *testing.T) {
	t.Parallel()

	server := &Server{hooks: hooksFromEnv(nil)}

	mux := http.NewServeMux()
	server.registerAutomationHookRoutes(mux)

	program := filepath.ToSlash(absoluteProgram(t))
	payloads := []string{
		`{"point":"job_completed","command":["` + program + `"]}`,
		`{"command":"` + program + `"}`,
		`point=job_completed&command=` + program,
	}

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch} {
		for _, payload := range payloads {
			request := httptest.NewRequest(method, "/api/v1/automation/hooks", strings.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(httptest.NewRecorder(), request)
		}
	}

	if len(server.hooks.Configured()) != 0 {
		t.Fatal("an HTTP request configured an automation hook; the command surface must stay out of reach of requests")
	}
}

func TestAutomationHookRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()

	program := filepath.ToSlash(absoluteProgram(t))

	cases := map[string]string{
		"shell string is not an argv array": `/bin/sh -c "rm -rf /"`,
		"relative program":                  `["hook.sh"]`,
		"empty array":                       `[]`,
		"blank argument":                    `["` + program + `","  "]`,
		"not json":                          `{"command":"x"}`,
	}

	for name, raw := range cases {
		hooks := hooksFromEnv(map[string]string{automationHookEnvName(HookExport): raw})
		if hooks.Enabled(HookExport) {
			t.Fatalf("%s: configuration was accepted", name)
		}
	}
}

// TestAutomationHookRunsWithoutAShellAndReportsItsResult covers the execution
// contract: argv only, JSON on stdin, parsed JSON back, audit record populated.
func TestAutomationHookRunsWithoutAShellAndReportsItsResult(t *testing.T) {
	t.Parallel()

	program := filepath.ToSlash(absoluteProgram(t))
	hooks := hooksFromEnv(map[string]string{
		automationHookEnvName(HookScoring): `["` + program + `","--json"]`,
	})

	var (
		gotArgv  []string
		gotStdin []byte
		gotEnv   []string
	)

	hooks.run = func(_ context.Context, hook AutomationHook, stdin []byte, env []string) ([]byte, int, error) {
		gotArgv = hook.Command
		gotStdin = stdin
		gotEnv = env

		return []byte(`{"score":42}`), 0, nil
	}

	result, run, invoked := hooks.Run(context.Background(), HookScoring, "biz-1", map[string]any{"name": "Acme"})
	if !invoked {
		t.Fatal("hook was not invoked")
	}

	if len(gotArgv) != 2 || gotArgv[1] != "--json" {
		t.Fatalf("argv = %#v, want the configured slice passed through untouched", gotArgv)
	}

	var document map[string]any
	if err := json.Unmarshal(gotStdin, &document); err != nil {
		t.Fatalf("stdin was not JSON: %v", err)
	}

	if document["point"] != HookScoring || document["subject_id"] != "biz-1" {
		t.Fatalf("stdin document = %#v", document)
	}

	if result["score"] != float64(42) {
		t.Fatalf("result = %#v, want the program's JSON", result)
	}

	if run.ExitCode != 0 || run.Program != gotArgv[0] {
		t.Fatalf("run = %#v", run)
	}

	var sawPoint bool

	for _, entry := range gotEnv {
		if entry == "GMAPS_HOOK_POINT="+HookScoring {
			sawPoint = true
		}
	}

	if !sawPoint {
		t.Fatal("hook facts must reach the program through the environment")
	}
}

func TestAutomationHookFailureIsRecordedNotFatal(t *testing.T) {
	t.Parallel()

	program := filepath.ToSlash(absoluteProgram(t))
	hooks := hooksFromEnv(map[string]string{
		automationHookEnvName(HookValidation): `["` + program + `"]`,
	})
	hooks.run = func(context.Context, AutomationHook, []byte, []string) ([]byte, int, error) {
		return []byte("boom"), 3, errors.New("exploded")
	}

	result, run, invoked := hooks.Run(context.Background(), HookValidation, "job-1", nil)
	if !invoked {
		t.Fatal("hook was not invoked")
	}

	if result != nil {
		t.Fatalf("a failing hook must contribute no result, got %#v", result)
	}

	if run.ExitCode != 3 || run.Err == "" {
		t.Fatalf("run = %#v, want the failure recorded", run)
	}

	if len(hooks.LastRuns()) != 1 {
		t.Fatal("the failure must still appear in the audit trail")
	}
}

func TestAutomationHookOutputIsBounded(t *testing.T) {
	t.Parallel()

	program := filepath.ToSlash(absoluteProgram(t))
	hooks := hooksFromEnv(map[string]string{
		automationHookEnvName(HookEnrichment): `["` + program + `"]`,
	})
	hooks.run = func(context.Context, AutomationHook, []byte, []string) ([]byte, int, error) {
		return []byte(strings.Repeat("x", automationHookOutputLimit*4)), 0, nil
	}

	_, run, _ := hooks.Run(context.Background(), HookEnrichment, "job-1", nil)
	if len(run.Output) > automationHookOutputLimit+64 {
		t.Fatalf("captured output = %d bytes, want it truncated", len(run.Output))
	}
}

func TestAutomationHookTimeoutIsBounded(t *testing.T) {
	t.Parallel()

	program := filepath.ToSlash(absoluteProgram(t))
	hooks := hooksFromEnv(map[string]string{
		automationHookEnvName(HookExport): `["` + program + `"]`,
		automationHookTimeoutEnv:          "100000",
	})

	if hooks.timeout > automationHookMaximumTimeout {
		t.Fatalf("timeout = %s, want it capped at %s", hooks.timeout, automationHookMaximumTimeout)
	}
}

func TestAutomationHookDisabledByDefault(t *testing.T) {
	t.Parallel()

	hooks := hooksFromEnv(nil)
	for _, point := range automationHookPoints() {
		if hooks.Enabled(point) {
			t.Fatalf("%s is enabled with no configuration", point)
		}
	}

	result, _, invoked := hooks.Run(context.Background(), HookJobCompleted, "job-1", nil)
	if invoked || result != nil {
		t.Fatal("an unconfigured hook must be a no-op")
	}
}

func TestAutomationHookStatusListsEveryDeclaredPoint(t *testing.T) {
	t.Parallel()

	program := filepath.ToSlash(absoluteProgram(t))
	server := &Server{hooks: hooksFromEnv(map[string]string{
		automationHookEnvName(HookJobCompleted): `["` + program + `"]`,
	})}

	views := server.automationHookViews()
	if len(views) != len(automationHookPoints()) {
		t.Fatalf("views = %d, want one per declared point", len(views))
	}

	var configured int

	for _, view := range views {
		if view.Source == "" {
			t.Fatalf("view %q does not name the variable that configures it", view.Point)
		}

		if view.Configured {
			configured++
		}
	}

	if configured != 1 {
		t.Fatalf("configured views = %d, want 1", configured)
	}
}

// TestExecHookCommandRunsARealProgram exercises the real executor once so the
// argv path is not only covered by the fake.
func TestExecHookCommandRunsARealProgram(t *testing.T) {
	t.Parallel()

	program, err := os.Executable()
	if err != nil {
		t.Skipf("cannot resolve the test binary: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hook := AutomationHook{Point: HookExport, Command: []string{program, "-test.run", "TestAutomationHookDisabledByDefault"}}

	_, exitCode, runErr := execHookCommand(ctx, hook, []byte("{}"), os.Environ())
	if runErr != nil {
		t.Fatalf("execHookCommand() error = %v", runErr)
	}

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
}
