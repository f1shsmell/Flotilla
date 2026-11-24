package store

import (
	"context"
	"fmt"
	"time"
)

// SetProtected pins or unpins one session against idle reclamation.
func (s *Store) SetProtected(ctx context.Context, session string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !on {
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM protected WHERE session_id = ?`, session); err != nil {
			return fmt.Errorf("unpin session %q: %w", session, err)
		}
		return nil
	}

	if _, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO protected (session_id, at) VALUES (?, ?)`,
		session, time.Now().UnixNano()); err != nil {
		return fmt.Errorf("pin session %q: %w", session, err)
	}
	return nil
}

// ProtectedIDs lists the sessions a previous daemon was told never to reclaim.
func (s *Store) ProtectedIDs(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.QueryContext(ctx,
		`SELECT session_id FROM protected ORDER BY session_id`)
	if err != nil {
		return nil, fmt.Errorf("read the pinned sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read a pinned session: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the pinned sessions: %w", err)
	}
	return out, nil
}
