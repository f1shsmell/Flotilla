// Package outbox runs the effects the store has queued.
//
// An effect is work owed as a consequence of an accepted fact: the thing the
// daemon promised to do once it recorded what happened. It is written in the
// same transaction as its event, so a fact never exists without the work it
// owes, and the receipt it leaves behind is what stops the work being done
// twice after a crash.
//
// The worker is separate from the code that produces effects because their
// failure modes differ. Producing one is part of a decision and must be atomic
// with it; running one is I/O that can fail, be retried, or be abandoned.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/f1shsmell/flotilla/internal/store"
)

// Handler runs one effect. It receives the payload the producer queued and must
// be safe to call twice: a crash between doing the work and recording the
// receipt means it will be called again.
type Handler func(ctx context.Context, payload json.RawMessage) error

// ErrNoHandler reports an effect this build does not know how to run. It is
// permanent: retrying cannot teach the worker a handler it does not have.
var ErrNoHandler = errors.New("outbox: no handler for this effect")

// DefaultMaxAttempts bounds how many times one effect is retried before it is
// abandoned. An effect is meant to be idempotent, so a few attempts cover a
// transient failure; past that the effect is a bug, and leaving it pending
// would make the queue look busy forever.
const DefaultMaxAttempts = 5

// Worker drains the outbox.
type Worker struct {
	store    *store.Store
	handlers map[string]Handler
	log      *slog.Logger
	maxTries int
	// wake carries one token per request to look at the queue again. A nil
	// channel (a worker nobody woken) parks the loop, which is what makes an
	// idle daemon idle rather than busy.
	wake chan struct{}
}

// Config is what a worker needs.
type Config struct {
	// Store is the queue. It is required.
	Store *store.Store
	// Log receives what the worker did. Nil discards.
	Log *slog.Logger
	// MaxAttempts bounds retries per effect. Zero uses DefaultMaxAttempts.
	MaxAttempts int
}

// New builds a worker with no handlers. A worker that meets an effect it has no
// handler for abandons it, which is the honest outcome: the work cannot be done
// by this build, and pretending it is pending would hide that.
func New(cfg Config) (*Worker, error) {
	if cfg.Store == nil {
		return nil, errors.New("outbox: no store")
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
	}
	return &Worker{
		store:    cfg.Store,
		handlers: make(map[string]Handler),
		log:      cfg.Log,
		maxTries: cfg.MaxAttempts,
		wake:     make(chan struct{}, 1),
	}, nil
}

// discard drops log output for a worker built without a logger.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Register adds the handler for one effect name. Registering twice for the same
// name replaces the first, which is what lets a test substitute one.
func (w *Worker) Register(name string, h Handler) {
	w.handlers[name] = h
}

// Wake asks the loop to look at the queue. It never blocks: a second wake-up
// while one is pending would tell the loop nothing it does not already know.
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Drain runs every effect that is pending right now and returns how many it
// settled. It is the deterministic entry point: a test calls it and knows the
// queue is empty afterwards, without waiting on a timer.
//
// Draining stops at the first store failure, because a store that cannot be
// written will not be written on the next iteration either.
func (w *Worker) Drain(ctx context.Context) (int, error) {
	settled := 0

	for {
		// A done context ends the drain quietly: the caller asked for a stop,
		// and the effects left queued are still owed, so they are not lost —
		// they are simply not this call's work.
		select {
		case <-ctx.Done():
			return settled, nil
		default:
		}

		pending, err := w.store.Pending(ctx, 0)
		if err != nil {
			return settled, fmt.Errorf("read the outbox: %w", err)
		}
		if len(pending) == 0 {
			return settled, nil
		}

		// One row per iteration, oldest first. Re-reading after each settle is
		// what makes the count honest: a crash in the middle leaves the rest
		// queued, and the next drain picks them up without a cursor to trust.
		if err := w.run(ctx, pending[0]); err != nil {
			return settled, err
		}
		settled++
	}
}

// Run drains the queue and then waits for a wake-up, until ctx ends. It does not
// poll: an idle daemon with an empty outbox sleeps in the select below, and a
// producer that queues work calls Wake.
func (w *Worker) Run(ctx context.Context) error {
	for {
		if _, err := w.Drain(ctx); err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return nil
		case <-w.wake:
		}
	}
}

// run executes one effect and settles it.
func (w *Worker) run(ctx context.Context, p store.PendingEffect) error {
	// A receipt means the work was done and the row was already settled — or
	// that a crash landed between the two. Either way the work is not repeated;
	// settling again is what closes the row.
	applied, err := w.store.Applied(ctx, p.EventSeq, p.Name)
	if err != nil {
		return fmt.Errorf("check the receipt for %s: %w", p.Name, err)
	}
	if applied {
		if err := w.store.Complete(ctx, p, "already applied"); err != nil {
			return fmt.Errorf("settle the applied effect %s: %w", p.Name, err)
		}
		w.log.Debug("effect already applied", "effect", p.Name, "event", p.EventSeq)
		return nil
	}

	handler, ok := w.handlers[p.Name]
	if !ok {
		// The work cannot be done by this build. Abandoning is permanent and
		// visible; leaving it pending would make the queue look owed forever
		// and would retry a certainty.
		cause := fmt.Errorf("%s: %w", p.Name, ErrNoHandler)
		if err := w.store.Abandon(ctx, p, cause); err != nil {
			return fmt.Errorf("abandon the unknown effect %s: %w", p.Name, err)
		}
		w.log.Warn("abandoned an effect no handler claims", "effect", p.Name, "event", p.EventSeq)
		return nil
	}

	if err := handler(ctx, p.Payload); err != nil {
		if p.Attempts+1 >= w.maxTries {
			if err := w.store.Abandon(ctx, p, err); err != nil {
				return fmt.Errorf("abandon the failing effect %s: %w", p.Name, err)
			}
			w.log.Warn("abandoned an effect that kept failing", "effect", p.Name,
				"event", p.EventSeq, "attempts", p.Attempts+1, "err", err.Error())
			return nil
		}
		if err := w.store.Fail(ctx, p, err); err != nil {
			return fmt.Errorf("record a failed attempt at %s: %w", p.Name, err)
		}
		w.log.Warn("an effect failed and will be retried", "effect", p.Name,
			"event", p.EventSeq, "attempts", p.Attempts+1, "err", err.Error())
		return nil
	}

	if err := w.store.Complete(ctx, p, ""); err != nil {
		return fmt.Errorf("settle the effect %s: %w", p.Name, err)
	}
	w.log.Debug("effect applied", "effect", p.Name, "event", p.EventSeq)
	return nil
}
