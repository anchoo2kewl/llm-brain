package store

import (
	"context"
	"encoding/json"
	"time"
)

// Tool updates: each host agent reports the CLIs it can update (inventory), and the hub queues
// tool_update commands on a schedule or by hand. The command row holds the result, so there's no other log.

// KindToolUpdate is the command kind (the rules are in package agent).
const KindToolUpdate = "tool_update"

// ToolInfo is one CLI on a host.
type ToolInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Method  string `json:"method,omitempty"` // cask:<name> | formula:<name> | npm:<pkg> | self | brew | '' (can't update)
	Path    string `json:"path,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Updatable: the agent knows how to update it.
func (t ToolInfo) Updatable() bool { return t.Method != "" }

// SetAgentTools records a host's tools.
func (s *Store) SetAgentTools(ctx context.Context, host string, tools []ToolInfo) error {
	if tools == nil {
		tools = []ToolInfo{}
	}
	b, _ := json.Marshal(tools)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO agent_hosts (host, tools) VALUES (?, ?)
		ON CONFLICT (host) DO UPDATE SET tools = excluded.tools`, host, string(b))
	return err
}

// AgentTools is every host's tools, by host.
func (s *Store) AgentTools(ctx context.Context) (map[string][]ToolInfo, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT host, tools FROM agent_hosts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]ToolInfo{}
	for rows.Next() {
		var h, t string
		if err := rows.Scan(&h, &t); err != nil {
			return nil, err
		}
		var ts []ToolInfo
		_ = json.Unmarshal([]byte(t), &ts)
		out[h] = ts
	}
	return out, rows.Err()
}

// ToolUpdates lists recent tool_update commands, newest first.
func (s *Store) ToolUpdates(ctx context.Context, limit int) ([]Command, error) {
	return s.cmdList(ctx, `SELECT `+cmdCols+` FROM commands WHERE kind = ? ORDER BY id DESC LIMIT ?`, KindToolUpdate, limit)
}

// ToolUpdateActive: a tool_update for host is queued or running.
func (s *Store) ToolUpdateActive(ctx context.Context, host string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM commands WHERE kind = ? AND host = ?
		AND status IN ('queued', 'claimed', 'running')`, KindToolUpdate, host).Scan(&n)
	return n > 0, err
}

// FinishToolUpdate applies a report to a tool_update command (a late or repeated report is a no-op).
func (s *Store) FinishToolUpdate(ctx context.Context, id int64, status, body string) (bool, error) {
	now := Now()
	fin := ""
	if FinalStatus(status) {
		fin = now
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE commands SET status = ?, result = ?, updated_at = ?,
		finished_at = CASE WHEN ? != '' THEN ? ELSE finished_at END
		WHERE id = ? AND kind = ? AND status NOT IN ('done', 'failed', 'stopped', 'cancelled')`, status, body, now, fin, fin, id, KindToolUpdate)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Settings keys of tool updates.
const (
	KeyUpdateAuto  = "update_auto"  // "1": update every live host once a day
	KeyUpdateHour  = "update_hour"  // local hour (0-23) on the hub after which the daily update runs; default 4
	KeyUpdateTools = "update_tools" // comma-separated tool names; default all
	KeyUpdateSkip  = "update_skip"  // comma-separated hosts the daily update leaves alone
	keyUpdateLast  = "update_last:" // + host: the local date it was last auto-queued
)

// UpdateLast is the local date (2006-01-02) host was last auto-updated, ” if never.
func (s *Store) UpdateLast(ctx context.Context, host string) (string, error) {
	return s.Setting(ctx, keyUpdateLast+host)
}

func (s *Store) SetUpdateLast(ctx context.Context, host string, day time.Time) error {
	return s.SetSettings(ctx, map[string]string{keyUpdateLast + host: day.Format("2006-01-02")})
}
