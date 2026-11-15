package ipc

import (
	"strings"
	"testing"
)

func TestTransportCarriesBytesBothWays(t *testing.T) {
	accepted, dialed := netPipe(t)

	// A Windows named-pipe write blocks until the peer reads, so the writer
	// runs in its own goroutine.
	writec := make(chan error, 1)
	go func() {
		_, err := dialed.Write([]byte("ping"))
		writec <- err
	}()

	buf := make([]byte, 4)
	if _, err := accepted.Read(buf); err != nil {
		t.Fatalf("read on the accepted side: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("accepted side read %q, want ping", buf)
	}
	if err := <-writec; err != nil {
		t.Fatalf("write from the dialled side: %v", err)
	}
}

func TestAddressNamesAChannelEndpoint(t *testing.T) {
	stable := Address{Channel: "stable", Name: "daemon"}
	preview := Address{Channel: "preview", Name: "daemon"}

	if stable.String() == preview.String() {
		t.Fatalf("both channels resolve to %q: a preview install would share a pipe with a stable one", stable)
	}
	if !strings.Contains(stable.String(), "stable") {
		t.Fatalf("address %q does not name its channel", stable)
	}

	if path := stable.SocketPath(t.TempDir()); !strings.Contains(path, "stable-daemon") {
		t.Fatalf("socket path %q does not name the channel and endpoint", path)
	}
	if path := stable.SocketPath(""); path != "" {
		t.Fatalf("SocketPath with no directory = %q, want empty", path)
	}
}
