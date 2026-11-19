package daemon

import (
	"context"
	"encoding/json"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// readSurface asks the host what the agent currently exposes, so a window that
// opens a session renders its controls without a second round trip. A terminal
// session answers unknown method, which means no controls, not a failed attach.
func (d *Daemon) readSurface(id session.ID, conn *ipc.Conn) []session.Control {
	ctx, cancel := context.WithTimeout(context.Background(), replayTimeout)
	defer cancel()

	raw, err := conn.Call(ctx, session.MethodSurface, struct{}{})
	if err != nil {
		if ipc.CodeOf(err) != ipc.CodeUnknownMethod {
			d.cfg.Log.Warn("cannot read a session surface", "session", id, "err", err.Error())
		}
		return nil
	}
	var resp session.SurfaceResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		d.cfg.Log.Warn("cannot decode a session surface", "session", id, "err", err.Error())
		return nil
	}
	return resp.Controls
}
