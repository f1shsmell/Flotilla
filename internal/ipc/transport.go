package ipc

import (
	"context"
	"net"
	"path/filepath"
)

// Address names one IPC endpoint. Each channel owns its endpoints, so a preview
// install never shares a pipe with a stable one.
type Address struct {
	Channel string
	Name    string
}

// String renders the address for logs: the pipe path on Windows, a synthetic
// "unix:" reference elsewhere.
func (a Address) String() string { return platformAddr(a) }

// SocketPath is where a Unix transport puts the endpoint. It is empty on
// Windows, where the pipe namespace carries the name instead.
func (a Address) SocketPath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "ipc", a.Channel+"-"+a.Name+".sock")
}

// Listener accepts IPC connections for one address.
type Listener interface {
	// Accept blocks until a peer connects.
	Accept() (net.Conn, error)
	// Close stops accepting. Connections already accepted are unaffected.
	Close() error
	// Address is the endpoint this listener owns.
	Address() Address
}

// Listen starts accepting on addr. dir holds the socket file on Unix and is
// ignored on Windows.
func Listen(addr Address, dir string) (Listener, error) {
	return listen(addr, dir)
}

// DialConn connects to addr and returns the bare transport. Callers that want
// the authenticated, framed connection use Dial; this is for the listener side
// and for tests.
func DialConn(ctx context.Context, addr Address, dir string) (net.Conn, error) {
	return dial(ctx, addr, dir)
}
