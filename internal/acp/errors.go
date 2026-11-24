// Package acp drives an Agent Client Protocol agent over stdio.
//
// The GUI never talks to this package directly: a session host owns the agent
// process and the ACP connection, then relays both to the daemon.
package acp

import (
	"errors"

	sdk "github.com/coder/acp-go-sdk"
)

// ErrNotInitialized reports a session used before Initialize succeeded.
var ErrNotInitialized = errors.New("acp: agent not initialized")

// ErrOutsideWorkspace reports a file request that escapes the session's root.
// The agent asks for absolute paths, so the client, not the agent, is what keeps
// a session inside its worktree.
var ErrOutsideWorkspace = errors.New("acp: path outside the session workspace")

// ErrPermissionDenied reports a tool-permission request a policy refused.
var ErrPermissionDenied = errors.New("acp: permission denied")

// ErrUnsupported reports a capability the agent did not advertise.
var ErrUnsupported = errors.New("acp: capability not advertised")

// ErrProtocol reports a response that does not match what this build speaks.
var ErrProtocol = errors.New("acp: protocol violation")

// ErrFileTooLarge reports a read whose reply cannot fit one transport frame.
var ErrFileTooLarge = errors.New("acp: file too large to return in one reply")

// Kinds used by Update.Kind and by a permission request's tool kind.
const (
	kindMessage = "message"
	kindThought = "thought"
	kindTool    = "tool"
	kindPlan    = "plan"
	// kindSubagent is a child-session registration or state change.
	kindSubagent = "subagent"
	kindOther    = "other"

	kindRead  = "read"
	kindEdit  = "edit"
	kindFetch = "fetch"
)

// Update kinds, exported for callers that switch on them.
const (
	UpdateMessage  = kindMessage
	UpdateThought  = kindThought
	UpdateTool     = kindTool
	UpdatePlan     = kindPlan
	UpdateSubagent = kindSubagent
	UpdateOther    = kindOther
)

// cancelled answers a permission request with no grant.
func cancelled() sdk.RequestPermissionResponse {
	return sdk.RequestPermissionResponse{Outcome: sdk.NewRequestPermissionOutcomeCancelled()}
}

// selected answers a permission request with one option.
func selected(optionID string) sdk.RequestPermissionResponse {
	return sdk.RequestPermissionResponse{
		Outcome: sdk.NewRequestPermissionOutcomeSelected(sdk.PermissionOptionId(optionID)),
	}
}

// firstGrant returns the first option that lets the call proceed.
func firstGrant(options []Option) (string, bool) {
	for _, opt := range options {
		if opt.Granted() {
			return opt.ID, true
		}
	}
	return "", false
}
