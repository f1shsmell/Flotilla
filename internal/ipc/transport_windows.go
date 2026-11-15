//go:build windows

package ipc

import (
	"context"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
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
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}

	return &pipeListener{Listener: l, addr: addr}, nil
}

func dial(ctx context.Context, addr Address, _ string) (net.Conn, error) {
	conn, err := winio.DialPipeContext(ctx, platformAddr(addr))
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	return conn, nil
}

type pipeListener struct {
	net.Listener
	addr Address
}

func (l *pipeListener) Address() Address { return l.addr }
