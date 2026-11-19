package ipc

import (
	"encoding/json"
	"fmt"
)

// Handshake is the first control message either side sends. The server replies
// with its own; a wrong token ends the connection before any method is served.
type Handshake struct {
	// Version is the protocol dialect the caller speaks. It is a floor, not an
	// equality: a peer below MinProtocolVersion is a stranger, and one above it
	// is a newer dialect whose extra fields this build simply does not read.
	Version int `json:"version"`
	// Capabilities names what the caller understands, so the peer can trim
	// what it sends instead of refusing the connection. An older build sends
	// none, which reads as "understands only what was always there".
	Capabilities []Capability `json:"capabilities,omitempty"`
	// Token is the shared secret from the token file.
	Token string `json:"token"`
	// Role names the caller's side: "daemon", "host", "cli" or "gui".
	Role string `json:"role"`
	// Client names the window or tool behind the connection. One client opens
	// several connections (control, one per session), and this is what lets a
	// question be asked about that client rather than about one socket: whether
	// the window has already looked at a finished turn. Empty means anonymous.
	Client string `json:"client,omitempty"`
	// Session names the session this peer serves or asks for, when it has one.
	Session string `json:"session,omitempty"`
	// Error is set by the server when it rejects the handshake.
	Error string `json:"error,omitempty"`
}

// ProtocolVersion is the control-plane dialect this build speaks. It is no
// longer a gate: a peer that speaks an older dialect than MinProtocolVersion is
// refused, and one that speaks a newer dialect is accepted with whatever fields
// this build understands. The capability set below is what actually decides
// what crosses the wire.
const ProtocolVersion = 1

// MinProtocolVersion is the oldest dialect this build can still serve. It
// exists to refuse a peer that never filled the field in, not to police a
// difference: a higher number is a newer peer, and its extra fields are simply
// not read here.
const MinProtocolVersion = 1

func encodeHandshake(h Handshake) ([]byte, error) {
	b, err := json.Marshal(h)
	if err != nil {
		return nil, fmt.Errorf("encode handshake: %w", err)
	}
	return b, nil
}

func decodeHandshake(payload []byte) (Handshake, error) {
	var h Handshake
	if err := json.Unmarshal(payload, &h); err != nil {
		return Handshake{}, fmt.Errorf("decode handshake: %w", err)
	}
	return h, nil
}
