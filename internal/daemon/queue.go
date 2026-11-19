package daemon

import "sync"

// peerQueue orders everything one client receives. A connection that has just
// attached holds its frames until the attach reply is out: the reply carries the
// history a window folds before any live line, and a live byte that overtook it
// would be drawn before the work it belongs after.
//
// No write ever runs under the queue's lock. A frame is the transport's own
// blocking write, and holding the lock across it would stall every caller of
// bind and release — the whole relay — on one client that stopped reading.
type peerQueue struct {
	mu       sync.Mutex
	holding  bool
	draining bool
	held     []func() error
	onError  func(error)
}

// newPeerQueue builds a queue that reports a failed write through report. The
// report is what makes a dropped frame visible in a log; the client it belonged
// to learns from its own connection closing.
func newPeerQueue(report func(error)) *peerQueue {
	return &peerQueue{onError: report}
}

// hold stops the queue from writing, so what arrives while a client is still
// attaching waits for release.
func (q *peerQueue) hold() {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.holding = true
}

// enqueue writes a frame in arrival order. While the queue is held or draining,
// the write is queued for release; otherwise it runs here, outside the lock.
func (q *peerQueue) enqueue(write func() error) error {
	q.mu.Lock()
	if q.holding || q.draining {
		q.held = append(q.held, write)
		q.mu.Unlock()
		return nil
	}
	q.mu.Unlock()

	return write()
}

// release lets the queue through: the held frames go first, in arrival order,
// and everything that arrives while they are being written follows behind them
// rather than overtaking the batch.
func (q *peerQueue) release() {
	q.mu.Lock()
	q.holding = false
	if q.draining {
		// Another release is already draining; it will pick these frames up.
		q.mu.Unlock()
		return
	}
	q.draining = true
	q.mu.Unlock()

	for {
		q.mu.Lock()
		batch := q.held
		q.held = nil
		if len(batch) == 0 {
			q.draining = false
			q.mu.Unlock()
			return
		}
		q.mu.Unlock()

		for _, write := range batch {
			if err := write(); err != nil && q.onError != nil {
				q.onError(err)
			}
		}
	}
}
