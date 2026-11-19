package host

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/f1shsmell/flotilla/internal/acp"
	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

var errUnknownControl = ipc.Class(errors.New("host: unknown control"), ipc.CodeBadRequest)

var errBadControlValue = ipc.Class(errors.New("host: bad control value"), ipc.CodeBadRequest)

// legacyModeOffered reports whether the older mode state should become a
// control. An announced option speaks first: a mode-category option is the
// newer API for the same setting, and any option already named "mode" owns
// that ID — a second control with it would break rendering and writes.
func legacyModeOffered(modes *sdk.SessionModeState, options []sdk.SessionConfigOption) bool {
	if modes == nil {
		return false
	}
	for _, option := range options {
		if option.Select != nil {
			if option.Select.Category != nil && *option.Select.Category == sdk.SessionConfigOptionCategoryMode {
				return false
			}
			if option.Select.Id == "mode" {
				return false
			}
		}
		if option.Boolean != nil && option.Boolean.Id == "mode" {
			return false
		}
	}
	return true
}

// projectControls turns what the agent announced into the flat control list a
// client renders. A mode-category option speaks for the mode — it is the newer
// API — so the older mode state only becomes a control when no option does.
func projectControls(modes *sdk.SessionModeState, options []sdk.SessionConfigOption) []session.Control {
	controls := make([]session.Control, 0, len(options)+1)

	for _, option := range options {
		switch {
		case option.Select != nil:
			kind := session.ControlSelect
			if option.Select.Category != nil && *option.Select.Category == sdk.SessionConfigOptionCategoryMode {
				kind = session.ControlMode
			}
			controls = append(controls, session.Control{
				ID:      string(option.Select.Id),
				Name:    option.Select.Name,
				Kind:    kind,
				Current: string(option.Select.CurrentValue),
				Choices: selectChoices(option.Select.Options),
			})
		case option.Boolean != nil:
			// A boolean carries no choices: on and off are the renderer's words,
			// not values the agent named.
			controls = append(controls, session.Control{
				ID:      string(option.Boolean.Id),
				Name:    option.Boolean.Name,
				Kind:    session.ControlBoolean,
				Current: strconv.FormatBool(option.Boolean.CurrentValue),
			})
		}
	}

	if legacyModeOffered(modes, options) {
		choices := make([]session.Choice, 0, len(modes.AvailableModes))
		for _, mode := range modes.AvailableModes {
			choices = append(choices, session.Choice{Value: string(mode.Id), Name: mode.Name})
		}
		controls = append([]session.Control{{
			ID:      "mode",
			Name:    "Mode",
			Kind:    session.ControlMode,
			Current: string(modes.CurrentModeId),
			Choices: choices,
		}}, controls...)
	}

	return controls
}

// selectChoices flattens both choice shapes into one list. A grouped choice
// keeps its group's label so a renderer can rebuild the sections.
func selectChoices(options sdk.SessionConfigSelectOptions) []session.Choice {
	var choices []session.Choice
	if options.Ungrouped != nil {
		for _, choice := range *options.Ungrouped {
			choices = append(choices, session.Choice{Value: string(choice.Value), Name: choice.Name})
		}
	}
	if options.Grouped != nil {
		for _, group := range *options.Grouped {
			for _, choice := range group.Options {
				choices = append(choices, session.Choice{
					Value: string(choice.Value),
					Name:  choice.Name,
					Group: group.Name,
				})
			}
		}
	}
	return choices
}

// controls projects the agent's current surface. A PTY session has none.
func (h *Host) controls() []session.Control {
	if h.acp == nil {
		return nil
	}
	return projectControls(h.acp.sess.Modes(), h.acp.sess.ConfigOptions())
}

// handleSurface answers what the agent currently exposes. A terminal agent
// announces nothing structured, so the call is unknown rather than an empty
// list that would render as "this agent has no settings".
func (h *Host) handleSurface() (session.SurfaceResponse, error) {
	if h.acp == nil {
		return session.SurfaceResponse{}, ipc.ErrNoHandler
	}
	return session.SurfaceResponse{Controls: h.controls()}, nil
}

// handleSurfaceSet routes one control write to the ACP call that owns it: an
// announced option goes through set_config_option, the mode state through
// set_mode. The agent's own notification is what updates the surface, so this
// call only delivers the request.
func (h *Host) handleSurfaceSet(ctx context.Context, req session.SurfaceSetRequest) error {
	if h.acp == nil {
		return ipc.ErrNoHandler
	}

	for _, option := range h.acp.sess.ConfigOptions() {
		if option.Boolean != nil && string(option.Boolean.Id) == req.ID {
			if req.Value != "true" && req.Value != "false" {
				return fmt.Errorf("control %q takes true or false, got %q: %w", req.ID, req.Value, errBadControlValue)
			}
			if err := h.acp.sess.SetConfigBool(ctx, req.ID, req.Value == "true"); err != nil {
				return fmt.Errorf("set control %q: %w", req.ID, err)
			}
			return nil
		}
		if option.Select != nil && string(option.Select.Id) == req.ID {
			if err := h.acp.sess.SetConfigValue(ctx, req.ID, req.Value); err != nil {
				return fmt.Errorf("set control %q: %w", req.ID, err)
			}
			return nil
		}
	}

	if req.ID == "mode" && legacyModeOffered(h.acp.sess.Modes(), h.acp.sess.ConfigOptions()) {
		if err := h.acp.sess.SetMode(ctx, req.Value); err != nil {
			return fmt.Errorf("set mode: %w", err)
		}
		return nil
	}

	return fmt.Errorf("control %q: %w", req.ID, errUnknownControl)
}

// surfaceChanged tells the attached client the agent's surface moved. The
// session state was already updated before this callback runs, so the event
// carries the new values rather than the change itself: a client that missed
// earlier events still lands on the truth.
func (h *Host) surfaceChanged(_ acp.SurfaceChange) {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()

	if conn == nil {
		return
	}
	if err := conn.Notify(session.MethodSurfaceUpdate, session.SurfaceResponse{Controls: h.controls()}); err != nil {
		h.log.Debug("forward a surface change failed", "session", h.cfg.ID, "err", err.Error())
	}
}
