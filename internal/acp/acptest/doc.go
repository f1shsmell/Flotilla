// Package acptest provides an ACP agent with no model behind it.
//
// It exists so the client side can be driven end to end — initialize, session,
// prompt, permission, cancel — without a vendor CLI, credentials or spend. The
// fake is also the subprocess the session tests spawn over stdio.
package acptest
