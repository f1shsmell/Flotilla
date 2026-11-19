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
	// ErrLocked reports a second process holding the channel's lock.
	ErrLocked = errors.New("ipc: another process holds the lock")
)

// Code is the identity of a failure on the wire. A caller branches on this and
// never on the message, which exists for a log. The number is part of the
// contract: the GUI mirrors it, so a code outlives the wording it replaced.
//
// JSON-RPC owns -32700..-32600 for its own errors and reserves
// -32000..-32099 for the server, which is where these live.
type Code int

const (
	// CodeMethodFailed is a failure the serving side could not classify. It is
	// the default, not a category: a caller must not read it as a specific one.
	CodeMethodFailed Code = -32000
	// CodeResponseTooLarge reports a reply the wire cannot carry in one frame.
	CodeResponseTooLarge Code = -32001
	// CodeUnknownMethod reports a call this side does not serve.
	CodeUnknownMethod Code = -32002
	// CodeUnknownSession reports a session this daemon does not serve.
	CodeUnknownSession Code = -32003
	// CodeNoRecord reports a session whose host published no record here.
	CodeNoRecord Code = -32004
	// CodeNoAgent reports an agent id the catalog does not hold.
	CodeNoAgent Code = -32005
	// CodeBadRequest reports a request this side cannot decode or use.
	CodeBadRequest Code = -32006
	// CodeWrongMode reports a call that does not fit the session's mode: a
	// resize to a structured session, or a cancel to a terminal.
	CodeWrongMode Code = -32007
	// CodeHostUnreachable reports a session whose host stopped answering.
	CodeHostUnreachable Code = -32008
	// CodeNotARepository reports a directory that is not a git repository.
	CodeNotARepository Code = -32009
	// CodeNothingToCommit reports a commit with no change to record.
	CodeNothingToCommit Code = -32010
	// CodeGhMissing reports a pull request asked for on a machine without gh.
	CodeGhMissing Code = -32011
	// CodeOutsideRepo reports a path outside the session's repository.
	CodeOutsideRepo Code = -32012
	// CodeUnknownScope reports a diff scope this build does not read.
	CodeUnknownScope Code = -32013
	// CodeNoComment reports a diff note that is not there.
	CodeNoComment Code = -32014
	// CodeBadComment reports a diff note that cannot be stored.
	CodeBadComment Code = -32015
	// CodeAlreadyRunning reports a second daemon asked to serve one channel.
	CodeAlreadyRunning Code = -32016
	// CodeAgentBlocked reports a prompt aimed at a session waiting for the
	// user: injecting text would answer a question nobody saw.
	CodeAgentBlocked Code = -32017
)

// CodedError carries a failure's wire identity. The layer that first knows what
// went wrong wraps the error once; every layer above projects that class and
// never re-derives it from the message.
type CodedError struct {
	Code Code
	Err  error
}

func (e *CodedError) Error() string { return e.Err.Error() }

func (e *CodedError) Unwrap() error { return e.Err }

// Class gives err the identity code. An error that already carries one keeps
// it: the layer closest to the cause owns the class, and a forwarding layer
// must not overwrite it.
func Class(err error, code Code) error {
	if err == nil {
		return nil
	}
	var coded *CodedError
	if errors.As(err, &coded) {
		return err
	}
	var callErr *CallError
	if errors.As(err, &callErr) {
		return err
	}
	return &CodedError{Code: code, Err: err}
}

// CodeOf reports the identity of a failure, whether this side produced it or a
// peer did. An unclassified error is CodeMethodFailed, never a guess.
func CodeOf(err error) Code {
	var coded *CodedError
	if errors.As(err, &coded) {
		return coded.Code
	}
	var callErr *CallError
	if errors.As(err, &callErr) {
		return Code(callErr.Code)
	}
	switch {
	case errors.Is(err, ErrNoHandler):
		return CodeUnknownMethod
	case errors.Is(err, ErrResponseTooLarge):
		return CodeResponseTooLarge
	case errors.Is(err, ErrLocked):
		return CodeAlreadyRunning
	}
	return CodeMethodFailed
}
