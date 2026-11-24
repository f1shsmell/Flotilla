package headless

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// helperEnv marks the helper process: the test binary re-executes itself as a
// stand-in agent, so the runner's real process path is covered without needing
// claude or codex on the machine, and without spending a token.
const helperEnv = "FLT_TEST_HELPER_HEADLESS"

// TestHelperHeadlessAgent is not a test in the ordinary sense: it is the body of
// the fake agent the runner tests spawn. FLT_TEST_HELPER_SCRIPT selects which
// behaviour it plays.
func TestHelperHeadlessAgent(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("helper process only")
	}

	switch os.Getenv("FLT_TEST_HELPER_SCRIPT") {
	case "claude":
		// The shape a real claude run printed, including its noise on stdout.
		fmt.Println("Reading additional input from stdin...")
		for _, line := range claudeLines {
			fmt.Println(line)
		}
	case "exit":
		fmt.Fprintln(os.Stderr, "the agent crashed while starting")
		os.Exit(3)
	case "hang":
		// A run that never ends, so the deadline path is the one under test.
		time.Sleep(2 * time.Minute)
	default:
		fmt.Println("no script selected")
	}
}

// helperRunner returns a Runner and dialect that re-run this test binary instead
// of a real agent. The script is chosen by helperEnvFor per test, because a
// dialect's command line is the same whichever behaviour the helper plays.
func helperRunner(t *testing.T) (Runner, Dialect) {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}

	d := Dialect{
		Name:    "helper",
		Command: exe,
		Args:    []string{"-test.run=TestHelperHeadlessAgent", "-test.v=false"},
		Parser:  ParseClaudeStreamJSON,
	}
	// LookPath is pinned to the test binary: the runner resolves a bare command
	// name, and resolution is not what these tests are about.
	return Runner{LookPath: func(string) (string, error) { return exe, nil }}, d
}

// helperEnvFor returns the environment the helper needs to play script.
func helperEnvFor(t *testing.T, script string) []string {
	t.Helper()
	return append(os.Environ(), helperEnv+"=1", "FLT_TEST_HELPER_SCRIPT="+script)
}

func TestRunProjectsTheAgentsStream(t *testing.T) {
	runner, d := helperRunner(t)

	res, err := runner.Run(t.Context(), d, Task{Prompt: "do it", Env: helperEnvFor(t, "claude")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Output != "line two" {
		t.Fatalf("output = %q, want the run's answer", res.Output)
	}
	if res.StopReason != "end_turn" {
		t.Fatalf("stop reason = %q, want end_turn", res.StopReason)
	}
	if res.SessionID == "" {
		t.Fatal("session id is empty, want the id the run reported for a later resume")
	}
	if len(res.Updates) == 0 {
		t.Fatal("no updates, want the stream projected")
	}
}

func TestRunResolvesTheCommandThroughLookPath(t *testing.T) {
	// A dialect names a bare command, and the runner resolves it. Getting this
	// wrong is invisible on a machine where the agent is on PATH and broken on
	// the one where it is not.
	_, d := helperRunner(t)
	d.Command = "claude"

	var asked string
	runner := Runner{LookPath: func(name string) (string, error) {
		asked = name
		return "/nowhere/claude", nil
	}}

	_, err := runner.Run(t.Context(), d, Task{Prompt: "x"})
	if asked != "claude" {
		t.Fatalf("LookPath asked for %q, want the dialect's command", asked)
	}
	// The resolved path does not exist, so the run fails here — but it fails
	// after resolving, which is the path this test covers.
	if err == nil {
		t.Fatal("Run returned nil, want a failure starting a binary that is not there")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Fatalf("error = %v, want it to name the command that could not start", err)
	}
}

func TestRunReportsAFailedRunWithItsReason(t *testing.T) {
	runner, d := helperRunner(t)

	res, err := runner.Run(t.Context(), d, Task{Prompt: "do it", Env: helperEnvFor(t, "exit")})
	if err == nil {
		t.Fatal("Run returned nil, want the agent's non-zero exit reported")
	}
	if !strings.Contains(err.Error(), "exited 3") {
		t.Fatalf("error = %v, want the exit code", err)
	}
	// The agent's own last words are the only explanation of a crash, so they
	// have to survive into the error.
	if !strings.Contains(err.Error(), "crashed while starting") {
		t.Fatalf("error = %v, want the reason the agent printed", err)
	}
	// Nothing was streamed, so the result is empty — but it is returned, not
	// withheld: a caller that got only an error could not tell "it said
	// nothing" from "it said something we dropped".
	if len(res.Updates) != 0 {
		t.Fatalf("updates = %+v, want none: the helper printed no events", res.Updates)
	}
}

func TestRunHonoursItsDeadline(t *testing.T) {
	runner, d := helperRunner(t)

	start := time.Now()
	_, err := runner.Run(t.Context(), d, Task{Prompt: "do it", Env: helperEnvFor(t, "hang"), Timeout: 300 * time.Millisecond})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	// A timeout that only reports itself after the run finishes is not a
	// timeout. The bound is loose because a loaded machine still has to reap
	// the process, and the helper sleeps far longer than this.
	if elapsed > 30*time.Second {
		t.Fatalf("Run returned after %v, want it bounded by its deadline", elapsed)
	}
}

func TestRunRefusesADialectWithNoParser(t *testing.T) {
	runner := Runner{}
	_, err := runner.Run(context.Background(), Dialect{Name: "x", Command: "x"}, Task{Prompt: "x"})
	if err == nil {
		t.Fatal("Run returned nil, want a dialect with no parser refused")
	}
}
