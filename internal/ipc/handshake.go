package ipc

import (
	"encoding/json"
	"fmt"
)

// Handshake is the first control message either side sends. The server replies
// with its own; a wrong token ends the connection before any method is served.
type Handshake struct {
	// Version is the protocol version the caller speaks.
	Version int `json:"version"`
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

// ProtocolVersion is the control-plane version this build speaks. It is
// experimental until 1.0: a mismatch is rejected rather than negotiated.
const ProtocolVersion = 1

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
