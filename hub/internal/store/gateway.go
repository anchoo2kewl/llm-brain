package store

import (
	"context"
	"encoding/json"
	"time"
)

// Window is one quota window of a gateway account, e.g. Claude's 5h or 7d allowance.
type Window struct {
	Name    string  `json:"name"`     // "5h", "7d", "primary", "secondary"
	UsedPct float64 `json:"used_pct"` // 0..100
	ResetAt int64   `json:"reset_at"` // unix seconds, 0 if unknown
	Status  string  `json:"status"`   // allowed | allowed_warning | rejected | ''
}

type GatewayAccount struct {
	ID, AuthIndex, Provider, Email, Label string
	Priority                              int
	Disabled                              bool
	Failed                                int
	Cooldowns                             json.RawMessage
	Windows                               []Window
	QuotaAt, SeenAt                       string
	// Filled by GatewayAccounts:
	Serving       bool   // the account fill-first routing picks right now for its provider
	Requests24h   int    // requests it served in the last 24h
	Tokens24h     int64  // tokens it served in the last 24h
	LastModel     string // model of its most recent request
	LastRequestAt string
}

// Exhausted: the account is cooling down or a quota window is rejected.
func (a GatewayAccount) Exhausted() bool {
	if len(a.Cooldowns) > 0 && string(a.Cooldowns) != "[]" && string(a.Cooldowns) != "null" {
		return true
	}
	for _, w := range a.Windows {
		if w.Status == "rejected" || w.UsedPct >= 100 {
			return true
		}
	}
	return false
}

func (a GatewayAccount) Stale() bool { return time.Since(ParseT(a.SeenAt)) > 5*time.Minute }

type GatewayRequest struct {
	RequestID, TS, AuthIndex, Account, Model, UserAgent string
	Input, Output, Cached, Total, LatencyMS   int64
	Failed                                    bool
	StatusCode                                int
}

// SaveGatewayAccounts replaces the account list with the gateway's current view.
func (s *Store) SaveGatewayAccounts(ctx context.Context, accts []GatewayAccount) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM gateway_accounts`); err != nil {
		return err
	}
	for _, a := range accts {
		w, _ := json.Marshal(a.Windows)
		cd := string(a.Cooldowns)
		if cd == "" {
			cd = "[]"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO gateway_accounts
			(id, auth_index, provider, email, label, priority, disabled, failed, cooldowns, windows, quota_at, seen_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			a.ID, a.AuthIndex, a.Provider, a.Email, a.Label, a.Priority, b2i(a.Disabled), a.Failed, cd, string(w), a.QuotaAt, a.SeenAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) AddGatewayRequests(ctx context.Context, reqs []GatewayRequest) error {
	if len(reqs) == 0 {
		return nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range reqs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO gateway_requests
			(request_id, ts, auth_index, account, model, input_tokens, output_tokens, cached_tokens, total_tokens, latency_ms, failed, status_code, user_agent)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.RequestID, r.TS, r.AuthIndex, r.Account, r.Model, r.Input, r.Output, r.Cached, r.Total, r.LatencyMS, b2i(r.Failed), r.StatusCode, r.UserAgent); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GatewayAccounts returns accounts by provider then priority, with 24h usage and the account
// fill-first routing would pick now (highest priority that is enabled and not exhausted).
func (s *Store) GatewayAccounts(ctx context.Context) ([]GatewayAccount, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, auth_index, provider, email, label, priority, disabled, failed,
		cooldowns, windows, quota_at, seen_at FROM gateway_accounts ORDER BY provider, priority DESC, email`)
	if err != nil {
		return nil, err
	}
	var out []GatewayAccount
	for rows.Next() {
		var a GatewayAccount
		var dis int
		var cd, w string
		if err := rows.Scan(&a.ID, &a.AuthIndex, &a.Provider, &a.Email, &a.Label, &a.Priority, &dis, &a.Failed, &cd, &w, &a.QuotaAt, &a.SeenAt); err != nil {
			rows.Close()
			return nil, err
		}
		a.Disabled = dis != 0
		a.Cooldowns = json.RawMessage(cd)
		_ = json.Unmarshal([]byte(w), &a.Windows)
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	since := time.Now().Add(-24 * time.Hour).UTC().Format(TimeFormat)
	for i := range out {
		a := &out[i]
		_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(total_tokens),0) FROM gateway_requests WHERE auth_index = ? AND ts >= ?`,
			a.AuthIndex, since).Scan(&a.Requests24h, &a.Tokens24h)
		_ = s.DB.QueryRowContext(ctx, `SELECT model, ts FROM gateway_requests WHERE auth_index = ? ORDER BY ts DESC LIMIT 1`,
			a.AuthIndex).Scan(&a.LastModel, &a.LastRequestAt)
	}
	picked := map[string]bool{}
	for i := range out {
		a := &out[i]
		if !picked[a.Provider] && !a.Disabled && !a.Exhausted() {
			a.Serving, picked[a.Provider] = true, true
		}
	}
	return out, nil
}

func (s *Store) RecentGatewayRequests(ctx context.Context, limit int) ([]GatewayRequest, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT ts, auth_index, account, model, input_tokens, output_tokens, cached_tokens,
		total_tokens, latency_ms, failed, status_code, user_agent FROM gateway_requests ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GatewayRequest
	for rows.Next() {
		var r GatewayRequest
		var f int
		if err := rows.Scan(&r.TS, &r.AuthIndex, &r.Account, &r.Model, &r.Input, &r.Output, &r.Cached, &r.Total, &r.LatencyMS, &f, &r.StatusCode, &r.UserAgent); err != nil {
			return nil, err
		}
		r.Failed = f != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
