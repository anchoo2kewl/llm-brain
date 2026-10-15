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
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO gateway_quota_history (account_id, win, at, used_pct, reset_at)
				VALUES (?, ?, ?, ?, ?)`, a.ID, w.Name, at, w.UsedPct, w.ResetAt); err != nil {
				return err
			}
		}
	}
	return nil
}
