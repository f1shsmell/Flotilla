package replay

import "github.com/f1shsmell/flotilla/internal/session"

// Builtin is the scenario a session runs when nobody names one. It exists so
// the replay preset works out of the box: a demo, a GIF recording or a GUI
// smoke run can start a session with no fixture to prepare and no token spent.
//
// The content is deliberately small and generic. It shows the three shapes a
// renderer has to draw — a thought, a tool call and a message — without
// pretending to be a real agent's work.
func Builtin() Scenario {
	return Scenario{
		Name:         "builtin",
		Capabilities: Capabilities{Resume: true},
		// The composer bar is part of what a session shows: a demo with no
		// surface would hide the controls a live agent offers.
		Surface: []session.Control{
			{
				ID: "mode", Name: "Permission mode", Kind: session.ControlMode, Current: "default",
				Choices: []session.Choice{
					{Value: "default", Name: "Ask first"},
					{Value: "acceptEdits", Name: "Accept edits"},
					{Value: "plan", Name: "Plan only"},
				},
			},
			{
				ID: "model", Name: "Model", Kind: session.ControlSelect, Current: "sonnet",
				Choices: []session.Choice{
					{Value: "sonnet", Name: "Sonnet"},
					{Value: "opus", Name: "Opus"},
				},
			},
			{
				ID: "effort", Name: "Thinking", Kind: session.ControlSelect, Current: "low",
				Choices: []session.Choice{
					{Value: "low", Name: "Low"},
					{Value: "high", Name: "High"},
				},
			},
			{ID: "fast", Name: "Fast answers", Kind: session.ControlBoolean, Current: "false"},
		},
		Turns: []Turn{
			{
				Updates: []Update{
					{Kind: "thought", Text: "This session is a replay: no model is behind it."},
					{Kind: "tool", ToolCallID: "replay-1", ToolTitle: "read the workspace"},
					{Kind: "tool", ToolCallID: "replay-1", ToolStatus: "completed"},
					{
						Kind: "message",
						Text: "Flotilla answered this turn from a recorded scenario. " +
							"Nothing was sent to a provider, so nothing was spent.",
					},
				},
				StopReason: StopEndTurn,
			},
			{
				Updates: []Update{
					{
						Kind: "message",
						Text: "The builtin scenario has one answer per turn and repeats its last " +
							"one, so a demo keeps working however many times you type.",
					},
				},
				StopReason: StopEndTurn,
			},
		},
	}
}
