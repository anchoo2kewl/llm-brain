package store

import "context"

// KindSessionClose is the command that ends a CLI session's process on its Mac (the rules are in package agent).
const KindSessionClose = "session_close"

// SetSessionClose records the session_close command just queued for a session.
func (s *Store) SetSessionClose(ctx context.Context, id, cmd int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE sessions SET close_cmd = ? WHERE id = ?`, cmd, id)
	return err
}

// MarkSessionClosed records that a session's process was ended now.
func (s *Store) MarkSessionClosed(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE sessions SET closed_at = ? WHERE id = ?`, Now(), id)
	return err
}
