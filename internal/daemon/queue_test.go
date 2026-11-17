package daemon

// heldCount reports how many frames a queue is holding for release. It lives in
// the test package because the difference it shows — held rather than dropped —
// is one the client's silence cannot demonstrate.
func (q *peerQueue) heldCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()

	return len(q.held)
}
