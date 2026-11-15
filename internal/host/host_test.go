package host

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// waitFor bounds every wait in this file. It is a hang guard, not a condition:
// no assertion here depends on a timeout to pass.
const waitFor = 15 * time.Second

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// helperCommand returns the command line that re-runs this test binary as the
// fake agent, so the PTY tests need no external CLI.
func helperCommand(t *testing.T) (string, []string) {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	return exe, []string{"-test.run=TestHelperEchoAgent", "-test.v=false"}
}

func TestHostHoldsThePtyAndAnswersInfo(t *testing.T) {
	dir := t.TempDir()
	command, args := helperCommand(t)

	h, err := Start(context.Background(), Config{
		ID:       "sess-1",
		Token:    "shared",
		StateDir: dir,
		Command:  command,
		Args:     args,
		Env:      append(os.Environ(), helperEnv+"=1"),
	}, session.NewRegistry(dir), discardLogger())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	served := make(chan error, 1)
	go func() { served <- h.Serve(ctx) }()

	reg := session.NewRegistry(dir)
	rec, err := waitForRecord(t, reg, "sess-1")
	if err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}
	if rec.Endpoint.Name != "host" {
		t.Fatalf("record endpoint = %+v, want the host endpoint", rec.Endpoint)
	}

	conn, err := ipc.Dial(ctx, rec.Endpoint, dir, ipc.Config{Token: "shared", Role: "test"})
	if err != nil {
		t.Fatalf("dial the host: %v", err)
	}
	t.Cleanup(conn.Close)

	raw, err := conn.Call(ctx, session.MethodInfo, struct{}{})
	if err != nil {
		t.Fatalf("call session.info: %v", err)
	}

	var info session.Info
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("decode info: %v", err)
	}
	if info.ID != "sess-1" {
		t.Fatalf("info.ID = %q, want sess-1", info.ID)
	}
	if info.PID == 0 {
		t.Fatal("info carries no host pid")
	}
}

func TestHostPipesClientInputToTheAgent(t *testing.T) {
	dir := t.TempDir()
	command, args := helperCommand(t)

	h, err := Start(context.Background(), Config{
		ID:       "sess-echo",
		Token:    "shared",
		StateDir: dir,
		Command:  command,
		Args:     args,
		Env:      append(os.Environ(), helperEnv+"=1"),
	}, session.NewRegistry(dir), discardLogger())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()

	output := make(chan string, 32)
	served := make(chan error, 1)
	go func() { served <- h.Serve(ctx) }()

	reg := session.NewRegistry(dir)
	rec, err := waitForRecord(t, reg, "sess-echo")
	if err != nil {
		t.Fatalf("the host never published a record: %v", err)
	}

	conn, err := ipc.Dial(ctx, rec.Endpoint, dir, ipc.Config{
		Token: "shared",
		Role:  "test",
		OnData: func(stream uint32, payload []byte) {
			if stream == session.StreamOutput {
				output <- string(payload)
			}
		},
	})
	if err != nil {
		t.Fatalf("dial the host: %v", err)
	}
	t.Cleanup(conn.Close)

	if err := conn.SendData(session.StreamInput, []byte("hello\r\n")); err != nil {
		t.Fatalf("send input: %v", err)
	}

	deadline := time.After(waitFor)
	var seen strings.Builder
	for {
		select {
		case chunk := <-output:
			seen.WriteString(chunk)
			if strings.Contains(seen.String(), "echo: hello") {
				return
			}
		case <-deadline:
			t.Fatalf("the agent never echoed the input; output so far: %q", seen.String())
		}
	}
}

func waitForRecord(t *testing.T, reg session.Registry, id session.ID) (session.Record, error) {
	t.Helper()

	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		rec, err := reg.Read(id)
		if err == nil {
			return rec, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return session.Record{}, session.ErrNoRecord
}
