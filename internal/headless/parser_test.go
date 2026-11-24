package headless

import (
	"strings"
	"testing"
)

// The lines below are copied from real runs rather than written by hand:
// `claude -p --output-format stream-json --verbose` (claude 2.1.329) and
// `codex exec --json` (codex-cli 0.171.0). A projection tested against an
// invented shape proves only that the parser matches itself.

// claudeLines is one conversation: an init event, a tool call, its result, the
// answer, and the closing result. Fields this build does not read are trimmed,
// but no field it does read was changed.
var claudeLines = []string{
	`{"type":"system","subtype":"init","session_id":"723da22c-cddc-4643-881b-f8b1edc3cf74","model":"claude-sonnet-4-5"}`,
	`{"type":"assistant","message":{"content":[{"id":"call_00_xqrhxr8ckuypqvab748pqyt0","input":{"file_path":"C:\\tmp\\sample.txt"},"name":"Read","type":"tool_use"}]},"session_id":"723da22c-cddc-4643-881b-f8b1edc3cf74"}`,
	`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"call_00_xqrhxr8ckuypqvab748pqyt0","type":"tool_result","content":"1\tline one of the file"}]},"session_id":"723da22c-cddc-4643-881b-f8b1edc3cf74"}`,
	`{"type":"assistant","message":{"content":[{"text":"line two","type":"text"}],"id":"msg_24478d7b67247bbdd08c7d62","role":"assistant","type":"message"},"session_id":"723da22c-cddc-4643-881b-f8b1edc3cf74"}`,
	`{"duration_api_ms":2658,"stop_reason":"end_turn","session_id":"723da22c-cddc-4643-881b-f8b1edc3cf74","total_cost_usd":0.13341,"usage":{"input_tokens":26272,"output_tokens":82},"result":"line two","subtype":"success","is_error":false,"type":"result"}`,
}

// codexLines is one codex run: a thread id, two warnings it reported as items,
// the answer, and the closing usage.
var codexLines = []string{
	`{"type":"thread.started","thread_id":"01a20df0-ade3-7df3-85d8-571f48036c1f"}`,
	`{"type":"item.completed","item":{"id":"item_2","type":"error","message":"Configured service tier priority is not advertised as supported."}}`,
	`{"type":"turn.started"}`,
	`{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"ok"}}`,
	`{"type":"turn.completed","usage":{"input_tokens":15075,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":0}}`,
}

// feed runs every line through a fresh projector for parser p.
func feed(t *testing.T, p Parser, lines []string) Result {
	t.Helper()

	proj, err := newProjector(p)
	if err != nil {
		t.Fatalf("build a projector for %q: %v", p, err)
	}

	var res Result
	for _, line := range lines {
		if err := proj.project([]byte(line), &res); err != nil {
			t.Fatalf("project %q: %v", line, err)
		}
	}
	return res
}

func TestClaudeProjection(t *testing.T) {
	res := feed(t, ParseClaudeStreamJSON, claudeLines)

	if res.SessionID != "723da22c-cddc-4643-881b-f8b1edc3cf74" {
		t.Fatalf("session id = %q, want the id the init event reported", res.SessionID)
	}
	if res.StopReason != "end_turn" {
		t.Fatalf("stop reason = %q, want end_turn", res.StopReason)
	}
	if res.Output != "line two" {
		t.Fatalf("output = %q, want the final result text", res.Output)
	}
	if !res.Usage.HasCost || res.Usage.CostUSD != 0.13341 {
		t.Fatalf("cost = %v (has %v), want the reported 0.13341", res.Usage.CostUSD, res.Usage.HasCost)
	}
	if res.Usage.InputTokens != 26272 || res.Usage.OutputTokens != 82 {
		t.Fatalf("usage = %d/%d tokens, want 26272/82", res.Usage.InputTokens, res.Usage.OutputTokens)
	}

	// A tool call and its outcome are two updates naming the same call: a
	// renderer needs the start to know what ran and the completion to know it
	// finished.
	var started, completed *UpdateRecord
	for i := range res.Updates {
		u := &res.Updates[i]
		if u.Kind != KindTool {
			continue
		}
		switch u.ToolStatus {
		case "started":
			started = u
		case "completed":
			completed = u
		}
	}
	if started == nil || completed == nil {
		t.Fatalf("tool updates = %+v, want one started and one completed", res.Updates)
	}
	if started.ToolCallID != completed.ToolCallID {
		t.Fatalf("tool ids %q and %q differ, want the call and its result to agree", started.ToolCallID, completed.ToolCallID)
	}
	if !strings.Contains(started.ToolTitle, "sample.txt") {
		t.Fatalf("tool title = %q, want the file the call touched", started.ToolTitle)
	}

	// The answer is an update in its own right, not only the closing summary.
	var said bool
	for _, u := range res.Updates {
		if u.Kind == KindMessage && u.Text == "line two" {
			said = true
		}
	}
	if !said {
		t.Fatalf("updates = %+v, want the answer among them", res.Updates)
	}

	// Every update carries a distinct sequence starting at one, which is what
	// lets a caller store several runs of one conversation apart.
	for i, u := range res.Updates {
		if u.Seq != uint64(i+1) {
			t.Fatalf("update %d has seq %d, want %d", i, u.Seq, i+1)
		}
	}
}

func TestCodexProjection(t *testing.T) {
	res := feed(t, ParseCodexExecJSON, codexLines)

	if res.SessionID != "01a20df0-ade3-7df3-85d8-571f48036c1f" {
		t.Fatalf("session id = %q, want the thread id", res.SessionID)
	}
	if res.StopReason != "end_turn" {
		t.Fatalf("stop reason = %q, want end_turn", res.StopReason)
	}
	if res.Output != "ok" {
		t.Fatalf("output = %q, want ok", res.Output)
	}
	if res.Usage.ReasoningTokens != 0 || res.Usage.InputTokens != 15075 || res.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v, want 15075/2 with no reasoning tokens", res.Usage)
	}
	if res.Usage.HasCost {
		// codex reports tokens, not money. Claiming a cost of zero would let a
		// budget gate read "unknown" as "free".
		t.Fatalf("cost reported as %v, want unknown: this dialect does not price a run", res.Usage.CostUSD)
	}

	// A configuration warning is recorded as something the agent said, and is
	// not the answer.
	var warnings int
	for _, u := range res.Updates {
		if u.Kind == KindOther {
			warnings++
		}
		if u.Kind == KindMessage && u.Text != "ok" {
			t.Fatalf("message update = %q, want only the agent's answer", u.Text)
		}
	}
	if warnings != 1 {
		t.Fatalf("other updates = %d, want the one warning codex reported", warnings)
	}
}

func TestProjectionIgnoresNoise(t *testing.T) {
	// An agent's stdout carries more than its events: a banner, a blank line,
	// a truncated write from a process that died mid-line. None of it is an
	// event, and none of it may end the run's projection.
	tests := []struct {
		parser   Parser
		noise    []string
		real     string
		expected string
	}{
		{
			parser:   ParseClaudeStreamJSON,
			noise:    []string{"", "Reading additional input from stdin...", `{"type":"assistant","message":`},
			real:     `{"type":"assistant","message":{"content":[{"text":"still here","type":"text"}]}}`,
			expected: "still here",
		},
		{
			parser:   ParseCodexExecJSON,
			noise:    []string{"", "Reading additional input from stdin...", `{"type":"item.completed","item":{"id":"it`},
			real:     `{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"still here"}}`,
			expected: "still here",
		},
	}

	for _, tc := range tests {
		lines := append(append([]string(nil), tc.noise...), tc.real)
		res := feed(t, tc.parser, lines)
		if len(res.Updates) != 1 || res.Updates[0].Text != tc.expected {
			t.Fatalf("%s: updates = %+v, want the one real event to survive the noise", tc.parser, res.Updates)
		}
	}
}

func TestClaudeThinkingIsAThought(t *testing.T) {
	// A thinking block carries its prose in `thinking`, not in `text`. Read
	// with the wrong field it would vanish, and a renderer that collapses
	// thoughts would show nothing where the agent reasoned.
	line := `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"weighing the options"}],"role":"assistant"}}`

	res := feed(t, ParseClaudeStreamJSON, []string{line})
	if len(res.Updates) != 1 || res.Updates[0].Kind != KindThought || res.Updates[0].Text != "weighing the options" {
		t.Fatalf("updates = %+v, want one thought", res.Updates)
	}
}

func TestArgvRendersDialect(t *testing.T) {
	claude, ok := DialectFor("claude")
	if !ok {
		t.Fatal("claude dialect is not built in")
	}
	codex, ok := DialectFor("codex")
	if !ok {
		t.Fatal("codex dialect is not built in")
	}

	tests := []struct {
		name string
		d    Dialect
		task Task
		want []string
	}{
		{
			name: "claude with an access level",
			d:    claude,
			task: Task{Prompt: "do it", Access: "acceptEdits"},
			want: []string{"-p", "--output-format", "stream-json", "--verbose", "--permission-mode", "acceptEdits", "do it"},
		},
		{
			name: "claude without one leaves the flag out",
			d:    claude,
			task: Task{Prompt: "do it"},
			want: []string{"-p", "--output-format", "stream-json", "--verbose", "do it"},
		},
		{
			name: "claude resume replaces the new-run arguments",
			d:    claude,
			task: Task{Prompt: "go on", ResumeID: "abc"},
			want: []string{"--resume", "abc", "go on"},
		},
		{
			name: "codex keeps its subcommand and appends the prompt last",
			d:    codex,
			task: Task{Prompt: "do it", Access: "workspace-write"},
			want: []string{"exec", "--json", "--skip-git-repo-check", "-s", "workspace-write", "do it"},
		},
		{
			name: "codex resume names its own subcommand",
			d:    codex,
			task: Task{Prompt: "go on", ResumeID: "t-1"},
			want: []string{"exec", "--json", "--skip-git-repo-check", "resume", "t-1", "go on"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.d.argv(tc.task)
			if err != nil {
				t.Fatalf("render argv: %v", err)
			}
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("argv = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDialectValidate(t *testing.T) {
	tests := []struct {
		name string
		d    Dialect
		ok   bool
	}{
		{name: "a builtin parses", d: Dialect{Command: "claude", Parser: ParseClaudeStreamJSON}, ok: true},
		{name: "no command", d: Dialect{Parser: ParseClaudeStreamJSON}, ok: false},
		{name: "unknown parser", d: Dialect{Command: "x", Parser: "guess"}, ok: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.d.Validate()
			if tc.ok && err != nil {
				t.Fatalf("validate: %v, want nil", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("validate returned nil, want a refusal")
			}
		})
	}
}

func TestBuiltinsAreValid(t *testing.T) {
	for _, d := range Builtins() {
		if err := d.Validate(); err != nil {
			t.Fatalf("builtin dialect %q: %v", d.Name, err)
		}
		if _, err := d.argv(Task{Prompt: "x"}); err != nil {
			t.Fatalf("builtin dialect %q renders no argv: %v", d.Name, err)
		}
	}
}
