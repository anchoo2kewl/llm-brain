package store

import (
	"context"
	"time"
)

// The inbox's reads: sessions that ended their turn and wait for a reply, chat that waits for an answer, and
// what the person has already seen. Deciding what's an inbox item is in package web (inbox.go).

// Seen returns the seen-at time of each key that has one.
func (s *Store) Seen(ctx context.Context) (map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT key, at FROM inbox_seen`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, at string
		if err := rows.Scan(&k, &at); err != nil {
			return nil, err
		}
		out[k] = at
	}
	return out, rows.Err()
}

// MarkSeen records that key was seen up to at (never moving it back).
func (s *Store) MarkSeen(ctx context.Context, key, at string) error {
	if at == "" {
		at = Now()
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO inbox_seen (key, at) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET at = CASE WHEN excluded.at > inbox_seen.at THEN excluded.at ELSE inbox_seen.at END`, key, at)
	return err
}

// PruneSeen drops seen markers older than d, so the table stays small.
func (s *Store) PruneSeen(ctx context.Context, d time.Duration) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM inbox_seen WHERE at < ?`, ago(d))
	return err
}

// LastTurn is a session with its newest message that isn't injected context.
type LastTurn struct {
	Session
	Role, Kind, Text, TS string
}

// RecentLastTurns are the top-level sessions active since `since`, newest first, each with its last message.
func (s *Store) RecentLastTurns(ctx context.Context, since time.Duration, limit int) ([]LastTurn, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+sessCols+`, m.role, m.kind, substr(m.text, 1, 600), m.ts FROM sessions s
		JOIN messages m ON m.id = (SELECT max(id) FROM messages WHERE session_id = s.id AND meta = 0)
		WHERE s.parent_uuid = '' AND s.file_mtime >= ? ORDER BY s.file_mtime DESC LIMIT ?`, ago(since), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LastTurn
	for rows.Next() {
		var x LastTurn
		err := rows.Scan(&x.ID, &x.Host, &x.Path, &x.Provider, &x.UUID, &x.ParentUUID, &x.Project, &x.Cwd, &x.Branch, &x.Model,
			&x.Originator, &x.Title, &x.AutoTitle, &x.AgentName, &x.FirstAt, &x.LastAt, &x.FileMtime, &x.MessageCount, &x.Name,
			&x.Role, &x.Kind, &x.Text, &x.TS)
		if err != nil {
			return nil, err
		}
		x.Live = x.FileMtime >= ago(LiveWindow)
		out = append(out, x)
	}
	return out, rows.Err()
}

// ChatAwaiting is the newest message in each conversation since `since` that mentions me or is a DM, from
// someone else, with no message from me after it.
func (s *Store) ChatAwaiting(ctx context.Context, since time.Time, me string, limit int) ([]ChatMessage, error) {
	ms, err := s.queryChat(ctx, `SELECT `+chatCols+` FROM chat_messages c WHERE at >= ? AND user_id != ?
		AND (mentions_me = 1 OR conv_kind IN ('im', 'mpim'))
		AND NOT EXISTS (SELECT 1 FROM chat_messages r WHERE r.conv_id = c.conv_id AND r.user_id = ? AND r.at > c.at)
		ORDER BY at DESC LIMIT ?`, T(since), me, me, limit*4)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []ChatMessage
	for _, m := range ms {
		if seen[m.ConvID] {
			continue
		}
		seen[m.ConvID] = true
		out = append(out, m)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}
