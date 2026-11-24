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
	"time"

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
	// retry is how long Run waits before another pass when effects are still
	// owed. It is a field so a test can shrink it and still exercise the loop.
	retry time.Duration
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
	// Retry is how long the loop waits before another pass when effects are
	// still owed. Zero uses retryDelay. It is configurable because a test that
	// exercises the loop must not spend seconds of wall clock doing it.
	Retry time.Duration
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
	if cfg.Retry <= 0 {
		cfg.Retry = retryDelay
	}
	return &Worker{
		store:    cfg.Store,
		handlers: make(map[string]Handler),
		log:      cfg.Log,
		maxTries: cfg.MaxAttempts,
		retry:    cfg.Retry,
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

// Drain runs one pass over the effects that are pending right now and returns
// how many it attempted. Each effect is attempted at most once per pass: a row
// that fails is left pending for a later pass rather than retried in a tight
// loop that would burn every attempt in milliseconds and abandon a transient
// failure.
//
// Draining stops at the first store failure, because a store that cannot be
// written will not be written on the next iteration either. A handler's failure
// is not a store failure: it is recorded and the pass moves on.
func (w *Worker) Drain(ctx context.Context) (int, error) {
	attempted := 0
	// seen is the rows this pass has already tried. Re-reading the queue after
	// each settle is what makes the count honest, and this is what keeps that
	// re-read from handing back the row that just failed.
	seen := make(map[int64]struct{})

	for {
		// A done context ends the drain quietly: the caller asked for a stop,
		// and the effects left queued are still owed, so they are not lost —
		// they are simply not this call's work.
		select {
		case <-ctx.Done():
			return attempted, nil
		default:
		}

		pending, err := w.store.Pending(ctx, 0)
		if err != nil {
			return attempted, fmt.Errorf("read the outbox: %w", err)
		}

		next := -1
		for i := range pending {
			if _, done := seen[pending[i].ID]; !done {
				next = i
				break
			}
		}
		if next < 0 {
			// Everything pending has been tried this pass. Whatever is left is
			// owed to a later pass; Run schedules one.
			return attempted, nil
		}

		seen[pending[next].ID] = struct{}{}
		if err := w.run(ctx, pending[next]); err != nil {
			return attempted, err
		}
		attempted++
	}
}

// retryDelay is how long Run waits before another pass when effects are still
// owed after one. It is the backoff that keeps a failing effect from being
// retried in a tight loop while still letting it be retried at all: without a
// pass, a failed row would wait for a wake-up that its own failure never sent.
//
// It is short because the failures this covers are meant to be transient — a
// locked file, a database busy moment — and the attempt cap already bounds how
// long a permanent failure can keep the queue busy.
const retryDelay = 2 * time.Second

// Run drains the queue and then waits, until ctx ends. It does not poll when the
// queue is empty: an idle daemon sleeps in the select below, and a producer that
// queues work calls Wake.
//
// When the queue is not empty after a pass, the next pass is scheduled here
// rather than left to a wake-up: an effect that failed is owed now, and nothing
// else is going to ring.
func (w *Worker) Run(ctx context.Context) error {
	for {
		if _, err := w.Drain(ctx); err != nil {
			return err
		}

		pending, err := w.store.Pending(ctx, 0)
		if err != nil {
			return fmt.Errorf("read the outbox: %w", err)
		}

		wait := w.retry
		if len(pending) == 0 {
			// Nothing owed. The only thing that can make work appear is a
			// producer calling Wake, so waiting on a timer would be a poll.
			wait = 0
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
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
