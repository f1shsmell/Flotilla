package headless

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The update kinds a projection produces. They are the same words an ACP
// session produces, so a renderer drawing a headless run needs no second
// vocabulary: the log a person reads looks the same however the agent ran.
const (
	KindMessage = "message"
	KindThought = "thought"
	KindTool    = "tool"
	KindOther   = "other"
)

// projector reads one dialect's event stream line by line. Each call takes one
// complete line of stdout and returns the updates that line carries. A line
// that is not an event — a stray log line, an empty line — produces nothing and
// no error: an agent's stdout is a stream this build reads, not a format it can
// insist on.
type projector interface {
	// project consumes one line and appends whatever updates it carries.
	project(line []byte, res *Result) error
}

// newProjector returns the projector for one parser.
func newProjector(p Parser) (projector, error) {
	switch p {
	case ParseClaudeStreamJSON:
		return &claudeProjector{}, nil
	case ParseCodexExecJSON:
		return &codexProjector{}, nil
	default:
		return nil, fmt.Errorf("headless: unknown parser %q", p)
	}
}

// record appends one update to a result's list with the next sequence number.
func record(res *Result, kind, text string) {
	res.Updates = append(res.Updates, UpdateRecord{
		Seq:  uint64(len(res.Updates) + 1),
		Kind: kind,
		Text: text,
	})
}

// decodeEvent reads one line into an event. It reports false for a line that is
// not that event — noise on stdout, or a write cut short by a dying process — so
// a caller skips the line instead of failing the run over it.
func decodeEvent(line string, out any) bool {
	return json.Unmarshal([]byte(line), out) == nil
}

// recordTool appends one tool event.
func recordTool(res *Result, id, title, status string) {
	res.Updates = append(res.Updates, UpdateRecord{
		Seq:        uint64(len(res.Updates) + 1),
		Kind:       KindTool,
		ToolCallID: id,
		ToolTitle:  title,
		ToolStatus: status,
	})
}

// claudeEvent is the subset of one `claude -p --output-format stream-json` line
// this build reads. The real line carries far more; a field that is not here is
// simply not projected.
//
// The shapes, read off claude 2.1.329:
//
//	{"type":"system","subtype":"init","session_id":"…","model":"…"}
//	{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}],"usage":{…}}}
//	{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call_…","name":"Read","input":{"file_path":"…"}}]}}
//	{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call_…","content":"…"}]}}
//	{"type":"result","subtype":"success","stop_reason":"end_turn","result":"ok",
//	 "session_id":"…","total_cost_usd":0.06,"usage":{"input_tokens":…,"output_tokens":…}}
type claudeEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	// SessionID is the conversation's id, on every event that carries one.
	SessionID string `json:"session_id"`
	// Message is the assistant turn's payload, and — for a tool result — the
	// user event's. A tool result arrives as a user message whose content holds
	// the outcome, which is why one shape serves both.
	Message *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			// Thinking is the text of a `thinking` block, which carries its prose
			// in its own field rather than in `text`.
			Thinking string `json:"thinking"`
			// ID names a tool call; ToolUseID answers one.
			ID        string `json:"id"`
			ToolUseID string `json:"tool_use_id"`
			Name      string `json:"name"`
			Input     struct {
				// Description is what a tool call is for, in the agent's words.
				Description string `json:"description"`
				Command     string `json:"command"`
				FilePath    string `json:"file_path"`
			} `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	// Result is the final message text, on the result event.
	Result string `json:"result"`
	// StopReason is the agent's own word for why the turn ended.
	StopReason string `json:"stop_reason"`
	// TotalCostUSD is the run's own cost estimate.
	TotalCostUSD *float64 `json:"total_cost_usd"`
	// Usage is the run's token totals.
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
	// IsError marks a result event that reports a failure.
	IsError bool `json:"is_error"`
}

// claudeProjector reads claude's stream-json.
type claudeProjector struct{}

func (p *claudeProjector) project(line []byte, res *Result) error {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		// Anything that is not JSON is the agent's own chatter on stdout — a
		// warning, a banner — and is not an event. Dropping it silently is the
		// honest choice: inventing a message from it would put words in the
		// agent's mouth that it did not say as its answer.
		return nil
	}

	// A partial line is not an event either, and a stream that is one line off
	// should not lose the rest of the run, so a line that does not decode is
	// skipped rather than returned as a failure.
	var ev claudeEvent
	if !decodeEvent(trimmed, &ev) {
		return nil
	}
	if ev.SessionID != "" {
		res.SessionID = ev.SessionID
	}

	switch ev.Type {
	case "assistant":
		return p.projectAssistant(&ev, res)
	case "user":
		// A tool's outcome comes back as a user event whose content holds a
		// tool_result block. It belongs in the log: a tool call that started and
		// never completed reads as work still running.
		return p.projectToolResults(&ev, res)
	case "result":
		return p.projectResult(&ev, res)
	default:
		// system/init and stream deltas: nothing this build reports.
		return nil
	}
}

// projectAssistant turns one assistant turn into updates: its text, its
// thinking, and each tool call it announced.
func (p *claudeProjector) projectAssistant(ev *claudeEvent, res *Result) error {
	if ev.Message == nil {
		return nil
	}

	for _, block := range ev.Message.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				record(res, KindMessage, block.Text)
			}
		case "thinking":
			// A thinking block carries its prose in `thinking`, not in `text`.
			// A renderer already has a shape for one, and it is a thought rather
			// than a message: the agent did not say it to anyone.
			if block.Thinking != "" {
				record(res, KindThought, block.Thinking)
			}
		case "tool_use":
			recordTool(res, block.ID, toolTitle(block.Name, block.Input.Description, block.Input.Command, block.Input.FilePath), "started")
		}
	}
	return nil
}

// projectToolResults closes the tool calls a user event answers. The outcome is
// matched by the tool_use_id it names, so a call reads as completed exactly when
// the agent said it was.
func (p *claudeProjector) projectToolResults(ev *claudeEvent, res *Result) error {
	if ev.Message == nil {
		return nil
	}

	for _, block := range ev.Message.Content {
		if block.Type != "tool_result" || block.ToolUseID == "" {
			continue
		}
		recordTool(res, block.ToolUseID, "", "completed")
	}
	return nil
}

// projectResult reads the closing event: what the run cost, how it ended, and
// the final text when the assistant turn carried none.
func (p *claudeProjector) projectResult(ev *claudeEvent, res *Result) error {
	res.StopReason = ev.StopReason
	if ev.Subtype != "" && res.StopReason == "" {
		res.StopReason = ev.Subtype
	}
	if ev.Result != "" {
		res.Output = ev.Result
	}

	res.Usage.InputTokens = ev.Usage.InputTokens
	res.Usage.OutputTokens = ev.Usage.OutputTokens
	if ev.TotalCostUSD != nil {
		res.Usage.CostUSD = *ev.TotalCostUSD
		res.Usage.HasCost = true
	}
	return nil
}

// toolTitle names a tool call in the user's terms: the agent's description when
// it gave one, else its command, else the file it touched, else the tool's own
// name. Empty means the agent named nothing, and the id is all there is.
func toolTitle(name, description, command, path string) string {
	for _, candidate := range []string{description, command, path} {
		if strings.TrimSpace(candidate) != "" {
			return candidate
		}
	}
	return name
}

// codexEvent is the subset of one `codex exec --json` line this build reads.
//
// The shapes, read off codex-cli 0.171.0:
//
//	{"type":"thread.started","thread_id":"…"}
//	{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"ok"}}
//	{"type":"item.completed","item":{"id":"item_0","type":"error","message":"…"}}
//	{"type":"turn.completed","usage":{"input_tokens":15075,"output_tokens":2,"reasoning_output_tokens":0}}
type codexEvent struct {
	Type string `json:"type"`
	// ThreadID is the conversation id, on thread.started.
	ThreadID string `json:"thread_id"`
	// Item is the completed item, on item.completed.
	Item *struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Text string `json:"text"`
		// Message is the text of an error item.
		Message string `json:"message"`
		// Command is what a command item ran.
		Command string `json:"command"`
	} `json:"item"`
	// Usage is the turn's token totals.
	Usage struct {
		InputTokens     int64 `json:"input_tokens"`
		OutputTokens    int64 `json:"output_tokens"`
		ReasoningTokens int64 `json:"reasoning_output_tokens"`
		CacheReadTokens int64 `json:"cached_input_tokens"`
	} `json:"usage"`
}

// codexProjector reads codex's exec --json.
type codexProjector struct{}

func (p *codexProjector) project(line []byte, res *Result) error {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return nil
	}

	// Same as the claude projection: a line that does not decode is not an
	// event, and skipping it keeps the rest of the run.
	var ev codexEvent
	if !decodeEvent(trimmed, &ev) {
		return nil
	}

	switch ev.Type {
	case "thread.started":
		if ev.ThreadID != "" {
			res.SessionID = ev.ThreadID
		}
	case "item.completed":
		if ev.Item == nil {
			return nil
		}
		switch ev.Item.Type {
		case "agent_message":
			if ev.Item.Text != "" {
				// The last agent message is the run's answer; the earlier ones
				// are what it said along the way, and both belong in the log.
				res.Output = ev.Item.Text
				record(res, KindMessage, ev.Item.Text)
			}
		case "reasoning":
			if ev.Item.Text != "" {
				record(res, KindThought, ev.Item.Text)
			}
		case "command_execution":
			recordTool(res, ev.Item.ID, ev.Item.Command, "completed")
		case "error":
			// A configuration warning is not a failed run, and a caller that
			// read one as failure would retry a task that never ran. It is
			// recorded as what it is: something the agent said.
			if ev.Item.Message != "" {
				record(res, KindOther, ev.Item.Message)
			}
		}
	case "turn.completed":
		res.StopReason = "end_turn"
		res.Usage.InputTokens = ev.Usage.InputTokens
		res.Usage.OutputTokens = ev.Usage.OutputTokens
		res.Usage.ReasoningTokens = ev.Usage.ReasoningTokens
	case "turn.failed":
		// The run failed. What it said before dying is already in the log.
		if res.StopReason == "" {
			res.StopReason = "failed"
		}
	}
	return nil
}
