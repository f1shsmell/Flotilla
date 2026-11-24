// Package headless runs an agent once, unattended, and projects what it printed
// into the updates a structured session produces.
//
// It is the execution layer the orchestrator drives: one process per task, no
// client attached and nobody to answer a dialog. ACP sessions are the
// interactive path and PTY sessions are the fallback; this is the third way an
// agent runs, and the only one that ends by itself.
//
// The two dialects below were read off the real binaries rather than inferred:
// `claude -p --output-format stream-json --verbose` and `codex exec --json`.
// Their event shapes differ in nesting, not only in names, so the projection is
// a per-dialect parser named by data rather than a universal expression
// language. That is the same split FLOTILLA.md draws for adapters: what JSON can
// describe is JSON, and the rest is one shared engine.
package headless

import (
	"fmt"
	"time"
)

// Parser names the projection a dialect's event stream needs.
type Parser string

const (
	// ParseClaudeStreamJSON reads `claude -p --output-format stream-json`:
	// system/init, assistant and result events, one JSON object per line.
	ParseClaudeStreamJSON Parser = "claude-stream-json"
	// ParseCodexExecJSON reads `codex exec --json`: thread.started,
	// item.completed and turn.completed events, one JSON object per line.
	ParseCodexExecJSON Parser = "codex-exec-json"
)

// Valid reports whether p names a parser this build has.
func (p Parser) Valid() bool {
	return p == ParseClaudeStreamJSON || p == ParseCodexExecJSON
}

// Dialect is one agent's unattended command line, as data.
//
// A dialect says how to start the agent and which parser reads its output. It
// deliberately does not describe the events themselves: two dialects with
// different nesting would need an expression language, and one parser per shape
// is smaller and easier to verify.
type Dialect struct {
	// Name identifies the dialect in a log or in an assignment.
	Name string
	// Command is the executable to run.
	Command string
	// Args are the arguments, in order. The prompt is appended last unless
	// PromptArg says where it goes.
	Args []string
	// Parser names the projection for this dialect's event stream.
	Parser Parser
	// AccessFlag is the switch that takes an access level, such as
	// "--permission-mode" or "-s". Empty means this dialect has none, and the
	// task's access level is ignored rather than guessed into a flag.
	AccessFlag string
	// ResumeArgs reopen a saved conversation, with {id} substituted. Empty
	// means the dialect cannot resume, which the orchestrator then treats as
	// "a new conversation", not as a retry.
	ResumeArgs []string
}

// Validate refuses a dialect this build could not run.
func (d Dialect) Validate() error {
	if d.Command == "" {
		return fmt.Errorf("headless dialect %q: empty command", d.Name)
	}
	if !d.Parser.Valid() {
		return fmt.Errorf("headless dialect %q: unknown parser %q", d.Name, d.Parser)
	}
	return nil
}

// Task is one unattended run.
type Task struct {
	// Prompt is the instruction the agent receives.
	Prompt string
	// Dir is the working directory. Empty inherits this process's.
	Dir string
	// Env is the agent's environment. Nil inherits this process's.
	Env []string
	// Access is the dialect's own word for what the agent may do without
	// asking. Empty leaves the flag out and lets the agent's own default stand:
	// a value invented here would be a permission level nobody chose.
	Access string
	// ResumeID continues a saved conversation. Empty starts a new one.
	ResumeID string
	// Timeout bounds the run. Zero waits as long as the context allows.
	Timeout time.Duration
}

// Usage is what one run cost, as the agent reported it.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	// ReasoningTokens is the share of the output a reasoning model spent
	// thinking, when the dialect reports it separately. Zero means either none
	// or unreported, and the distinction is not worth inventing.
	ReasoningTokens int64
	// CostUSD is the run's own estimate. HasCost says whether the dialect
	// reported one at all: a zero cost and an unknown cost are different, and
	// a budget gate must not read the second as the first.
	CostUSD float64
	HasCost bool
}

// Result is what one run produced.
type Result struct {
	// SessionID is the id the agent assigned, so a later run can resume it.
	// Empty means the dialect reported none.
	SessionID string
	// StopReason is the agent's own word for why the turn ended, such as
	// "end_turn". Empty means it reported none.
	StopReason string
	// Output is the agent's final message text, when the dialect reports one.
	Output string
	// Usage is what the run cost.
	Usage Usage
	// Updates is everything the run streamed, in order. It is filled even when
	// the run failed: what an agent did before it died is still what it did.
	Updates []UpdateRecord
}

// UpdateRecord is one projected update plus the sequence the run gave it. The
// sequence restarts at 1 for every run, so a caller storing several runs of one
// conversation keeps them apart.
type UpdateRecord struct {
	Seq  uint64
	Kind string
	Text string
	// ToolCallID, ToolTitle and ToolStatus describe a tool event.
	ToolCallID string
	ToolTitle  string
	ToolStatus string
}

// Builtins lists the dialects this build ships. Like the agent catalogue, the
// list is a convenience rather than a capability claim: what an agent can do is
// measured against the binary, not read off this table.
func Builtins() []Dialect {
	return []Dialect{
		{
			Name:       "claude",
			Command:    "claude",
			Args:       []string{"-p", "--output-format", "stream-json", "--verbose"},
			Parser:     ParseClaudeStreamJSON,
			AccessFlag: "--permission-mode",
			ResumeArgs: []string{"--resume", "{id}"},
		},
		{
			Name:       "codex",
			Command:    "codex",
			Args:       []string{"exec", "--json", "--skip-git-repo-check"},
			Parser:     ParseCodexExecJSON,
			AccessFlag: "-s",
			ResumeArgs: []string{"exec", "--json", "--skip-git-repo-check", "resume", "{id}"},
		},
	}
}

// DialectFor returns the builtin dialect by name.
func DialectFor(name string) (Dialect, bool) {
	for _, d := range Builtins() {
		if d.Name == name {
			return d, true
		}
	}
	return Dialect{}, false
}

// argv renders one run as the command line the dialect describes. The prompt is
// the last argument: both dialects read it positionally, and a flag after it
// would be parsed as prompt text by codex.
func (d Dialect) argv(t Task) ([]string, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}

	args := make([]string, 0, len(d.Args)+len(d.ResumeArgs)+3)
	if t.ResumeID != "" && len(d.ResumeArgs) > 0 {
		for _, arg := range d.ResumeArgs {
			args = append(args, replaceID(arg, t.ResumeID))
		}
		// The resume form already carries this dialect's own subcommand and
		// flags, so the new-run arguments are not repeated.
	} else {
		args = append(args, d.Args...)
	}

	if t.Access != "" && d.AccessFlag != "" {
		args = append(args, d.AccessFlag, t.Access)
	}
	if t.Prompt != "" {
		args = append(args, t.Prompt)
	}
	return args, nil
}

// replaceID substitutes a session id into one resume argument.
func replaceID(arg, id string) string {
	const marker = "{id}"
	for i := 0; i+len(marker) <= len(arg); i++ {
		if arg[i:i+len(marker)] == marker {
			return arg[:i] + id + arg[i+len(marker):]
		}
	}
	return arg
}
