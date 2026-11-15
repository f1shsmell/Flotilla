package daemon

import (
	"context"
	"strings"
	"time"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// echoOnce dials one session host, sends a line to its agent, and returns what
// the agent printed until the echo appears.
func echoOnce(ctx context.Context, rec session.Record, stateDir, token, line string) (string, error) {
	output := make(chan string, 64)

	conn, err := ipc.Dial(ctx, rec.Endpoint, stateDir, ipc.Config{
		Token: token,
		Role:  "test",
		OnData: func(stream uint32, payload []byte) {
			if stream == session.StreamOutput {
				output <- string(payload)
			}
		},
	})
	if err != nil {
		return "", err
	}
	defer conn.Close()

	if err := conn.SendData(session.StreamInput, []byte(line+"\r\n")); err != nil {
		return "", err
	}

	var seen strings.Builder
	deadline := time.After(waitFor)
	for {
		select {
		case chunk := <-output:
			seen.WriteString(chunk)
			if strings.Contains(seen.String(), "echo: "+line) {
				return seen.String(), nil
			}
		case <-deadline:
			return seen.String(), errNoEcho
		}
	}
}
