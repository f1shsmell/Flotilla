package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/f1shsmell/flotilla/internal/ipc"
	"github.com/f1shsmell/flotilla/internal/session"
)

// Attachment is one live connection from this daemon to a session host.
type Attachment struct {
	// Record is what the host published about itself.
	Record session.Record
	// Info is what the host reported when asked.
	Info session.Info
	// Conn is the authenticated connection; its Done closes when either side
	// stops.
	Conn *ipc.Conn
}

// Fleet is the set of sessions one scan could reach.
type Fleet struct {
	Attachments []Attachment
	// Failures names the hosts that could not be reached. A failure is not
	// fatal: a daemon that starts while one host is wedged still serves the
	// rest, and that host's record stays on disk for the next attempt.
	Failures []error
}

// ErrNoHosts reports a scan that found nothing reachable.
var ErrNoHosts = errors.New("daemon: no session hosts")

// Attach dials every published host and asks what it holds.
func Attach(ctx context.Context, reg session.Registry, token, stateDir string, log *slog.Logger) (Fleet, error) {
	records, err := reg.List()
	if err != nil {
		return Fleet{}, fmt.Errorf("list host records: %w", err)
	}

	fleet := Fleet{}
	for _, rec := range records {
		att, err := AttachOne(ctx, reg, rec.ID, token, stateDir)
		if err != nil {
			log.Warn("session host unreachable", "session", rec.ID, "pid", rec.PID, "err", err.Error())
			fleet.Failures = append(fleet.Failures, fmt.Errorf("attach to session %q: %w", rec.ID, err))
			continue
		}

		fleet.Attachments = append(fleet.Attachments, att)
		log.Info("session reattached",
			"session", rec.ID, "host_pid", att.Info.PID, "command", att.Info.Command)
	}

	return fleet, nil
}

// AttachOne dials the host that published a record for id and asks it what it
// holds. The record is the only source of the endpoint: no side guesses a name.
func AttachOne(ctx context.Context, reg session.Registry, id session.ID, token, stateDir string) (Attachment, error) {
	rec, err := reg.Read(id)
	if err != nil {
		return Attachment{}, fmt.Errorf("read the record for session %q: %w", id, err)
	}
	if rec.Endpoint.Name == "" {
		return Attachment{}, fmt.Errorf("session %q published no endpoint: %w", id, session.ErrNoRecord)
	}

	conn, err := ipc.Dial(ctx, rec.Endpoint, stateDir, ipc.Config{Token: token, Role: "daemon"})
	if err != nil {
		return Attachment{}, fmt.Errorf("dial session %q: %w", id, err)
	}

	raw, err := conn.Call(ctx, session.MethodInfo, struct{}{})
	if err != nil {
		conn.Close()
		return Attachment{}, fmt.Errorf("ask session %q for its info: %w", id, err)
	}

	var info session.Info
	if err := json.Unmarshal(raw, &info); err != nil {
		conn.Close()
		return Attachment{}, fmt.Errorf("decode session info: %w", err)
	}

	return Attachment{Record: rec, Info: info, Conn: conn}, nil
}

// Close detaches from every host. The hosts keep running: that is the whole
// point of the two-process model.
func (f Fleet) Close() {
	for _, att := range f.Attachments {
		att.Conn.Close()
	}
}
