package ipc

import "errors"

var (
	// ErrProtocol reports a peer that violated the framing or the handshake.
	ErrProtocol = errors.New("ipc: protocol violation")
	// ErrConnClosed reports use of a connection that has already stopped.
	ErrConnClosed = errors.New("ipc: connection closed")
	// ErrUnauthorized reports a handshake that did not present the token.
	ErrUnauthorized = errors.New("ipc: unauthorized")
	// ErrNoHandler reports a peer calling a method this side does not serve.
	ErrNoHandler = errors.New("ipc: unknown method")
	// ErrTimeout reports an operation whose context expired.
	ErrTimeout = errors.New("ipc: timed out")
	// ErrResponseTooLarge reports a reply that does not fit one frame, so it
	// could never reach the caller.
	ErrResponseTooLarge = errors.New("ipc: response exceeds the frame limit")
)
