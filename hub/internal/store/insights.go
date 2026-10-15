package store

import (
	"context"
	"database/sql"
	"time"
)

// quotaHistoryEvery is how often an unchanged quota window is recorded anyway, so a chart shows it held.
const quotaHistoryEvery = 10 * time.Minute

// recordQuotaHistory appends each account's quota windows to gateway_quota_history when they changed since
// the last row for that window (or that row is older than quotaHistoryEvery).
func recordQuotaHistory(ctx context.Context, tx *sql.Tx, accts []GatewayAccount, now time.Time) error {
	at := T(now)
	for _, a := range accts {
		for _, w := range a.Windows {
			var last string
			var used float64
			var reset int64
			err := tx.QueryRowContext(ctx, `SELECT at, used_pct, reset_at FROM gateway_quota_history
				WHERE account_id = ? AND win = ? ORDER BY at DESC LIMIT 1`, a.ID, w.Name).Scan(&last, &used, &reset)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if err == nil && used == w.UsedPct && reset == w.ResetAt && now.Sub(ParseT(last)) < quotaHistoryEvery {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO gateway_quota_history (account_id, win, at, used_pct, reset_at)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (account_id, win, at) DO UPDATE SET used_pct = excluded.used_pct, reset_at = excluded.reset_at`, a.ID, w.Name, at, w.UsedPct, w.ResetAt); err != nil {
				return err
			}
		}
	}
	return nil
}

// Usage is the token usage of one model response (Claude transcripts repeat it on every line of the response,
// so (session, API message id) identifies it).
type Usage struct {
	SessionID                            int64
	APIID, TS, Model                     string
	Input, Output, CacheRead, CacheWrite int64
}

// UpsertUsage records a response's usage. Later lines of a response can carry larger counts (the final output
// tally), so each figure keeps its maximum; recording the same line twice changes nothing.
func UpsertUsage(ctx context.Context, tx *sql.Tx, u Usage) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO usage_events (session_id, api_id, ts, model, input, output, cache_read, cache_write)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (session_id, api_id) DO UPDATE SET
		input = max(usage_events.input, excluded.input), output = max(usage_events.output, excluded.output),
		cache_read = max(usage_events.cache_read, excluded.cache_read), cache_write = max(usage_events.cache_write, excluded.cache_write)`,
		u.SessionID, u.APIID, u.TS, u.Model, u.Input, u.Output, u.CacheRead, u.CacheWrite)
	return err
}

// InsightsCursor names the rollup's position in messages (insights_cursor.name).
const InsightsCursor = "messages"

// ResetSessionInsights drops the derived rows of the transcript at host/path, inside the transaction that
// deletes its messages (a truncated or replaced file is re-read from the start). Re-ingested messages then
// roll up again without double counting.
//
// Message ids are rowids: when the deleted messages were the newest, the re-read ones reuse their ids. The
// rollup cursor is pulled back to the newest surviving id so those are not skipped (nothing between that id
// and the old cursor exists any more, so nothing is counted twice).
func ResetSessionInsights(ctx context.Context, tx *sql.Tx, host, path string) error {
	for _, q := range []string{
		`DELETE FROM usage_events WHERE session_id IN (SELECT id FROM sessions WHERE host = ? AND path = ?)`,
		`DELETE FROM tool_calls WHERE session_id IN (SELECT id FROM sessions WHERE host = ? AND path = ?)`,
		`DELETE FROM activity WHERE session_id IN (SELECT id FROM sessions WHERE host = ? AND path = ?)`,
	} {
		if _, err := tx.ExecContext(ctx, q, host, path); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE insights_cursor SET last_id = min(last_id,
		COALESCE((SELECT id FROM messages ORDER BY id DESC LIMIT 1), 0)) WHERE name = ?`, InsightsCursor)
	return err
}
