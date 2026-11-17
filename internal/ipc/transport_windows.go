//go:build windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// pipePrefix is the one namespace Flotilla endpoints live in.
const pipePrefix = `\\.\pipe\flotilla-`

func platformAddr(a Address) string {
	return pipePrefix + a.Channel + "-" + a.Name
}

func listen(addr Address, _ string) (Listener, error) {
	sddl, err := ownerOnlySDDL()
	if err != nil {
		return nil, err
	}

	l, err := winio.ListenPipe(platformAddr(addr), &winio.PipeConfig{
		SecurityDescriptor: sddl,
	})
	if err != nil {
		// A pipe name that exists already belongs to a live daemon: Windows
		// makes the second first-instance creation fail rather than hand over
		// a second endpoint, and that is what keeps one channel to one daemon.
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, fmt.Errorf("listen on %s: %w", addr, ErrLocked)
		}
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}

	return &pipeListener{Listener: l, addr: addr}, nil
}

// dialRetryWindow is how long a dial keeps trying an endpoint that exists but
// has no instance at this instant.
const dialRetryWindow = 500 * time.Millisecond

// dialRetryInterval is the pause between those attempts.
const dialRetryInterval = 10 * time.Millisecond

func dial(ctx context.Context, addr Address, _ string) (net.Conn, error) {
	// A named pipe has no pending instance between one Accept and the next, so a
	// dial can land in that gap and be told the endpoint does not exist. Retrying
	// briefly is what makes reconnecting after another client left reliable.
	deadline := time.Now().Add(dialRetryWindow)
	for {
		conn, err := winio.DialPipeContext(ctx, platformAddr(addr))
		if err == nil {
			return conn, nil
		}
		if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, fmt.Errorf("dial %s: %w", addr, err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("dial %s: %w", addr, err)
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("dial %s: %w", addr, errors.Join(ctx.Err(), err))
		case <-time.After(dialRetryInterval):
		}
	}
}

type pipeListener struct {
	net.Listener
	addr Address
}

func (l *pipeListener) Address() Address { return l.addr }
