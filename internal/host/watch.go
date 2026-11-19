package host

import (
	"context"

	"github.com/charmbracelet/x/xpty"

	"github.com/f1shsmell/flotilla/internal/session"
)

// watchAgent reaps the agent and closes the host when it exits. xpty.WaitProcess
// is the supported wait on Windows, where exec.Cmd.Wait does not work on ConPTY.
func (h *Host) watchAgent(ctx context.Context) {
	err := xpty.WaitProcess(ctx, h.cmd)
	h.exitc <- err
	h.log.Info("agent exited", "session", h.cfg.ID, "err", errText(err))
	_ = h.Close()
}

// watchACP notices the ACP agent ending without being asked to. An agent that
// advertised resume is relaunched inside this host, bounded by the crash cap;
// anything else closes the host: a prompt after the death would fail at the
// transport while the session still looks alive. An involuntary exit reads as
// unknown — this build never saw the agent decide to stop.
func (h *Host) watchACP(ctx context.Context) {
	for {
		sess := h.acpAgent()
		select {
		case <-sess.Exited():
		case <-h.donec:
			return
		case <-ctx.Done():
			return
		}

		// The agent is gone either way; only the reason differs. A host that was
		// already asked to stop must not report its own kill as an agent that died
		// on its own, so the closing state is what decides the wording.
		if h.closing.Load() {
			h.log.Debug("the acp agent stopped with its host", "session", h.cfg.ID)
			_ = h.Close()
			return
		}

		if sess.ExitErr() != nil {
			h.log.Warn("the acp agent exited on its own", "session", h.cfg.ID,
				"err", errText(sess.ExitErr()))
		}
		h.setState(session.StateUnknown)

		if sess.SupportsResume() && h.resumeAllowed() {
			err := h.resumeACP(ctx, sess.ID())
			if err == nil {
				h.log.Info("the acp agent was resumed", "session", h.cfg.ID)
				continue
			}
			h.log.Warn("resume the acp agent failed", "session", h.cfg.ID, "err", errText(err))
		}
		_ = h.Close()
		return
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
