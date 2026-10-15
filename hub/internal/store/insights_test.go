package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestQuotaHistoryRecordsChanges(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	acct := func(used float64) []GatewayAccount {
		return []GatewayAccount{{ID: "a.json", AuthIndex: "ai", Provider: "claude", Email: "me@example.com", Priority: 10,
			Windows: []Window{{Name: "5h", UsedPct: used, ResetAt: 1700000000}}, SeenAt: Now()}}
	}
	// Polls 20 s apart: unchanged ones are skipped, a change is recorded. Two changes within the same
	// second keep the latest value instead of dropping it.
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i, u := range []float64{10, 10, 10, 25, 30} {
		at := t0.Add(time.Duration(i) * 20 * time.Second)
		if i == 4 {
			at = t0.Add(60 * time.Second) // same second as the 25% poll
		}
		tx, err := st.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := recordQuotaHistory(ctx, tx, acct(u), at); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM gateway_quota_history WHERE account_id = 'a.json' AND win = '5h'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("unchanged polls are not recorded again within 10 minutes: want 2 rows, got %d", n)
	}
	var last float64
	if err := st.DB.QueryRow(`SELECT used_pct FROM gateway_quota_history WHERE account_id = 'a.json' ORDER BY at DESC LIMIT 1`).Scan(&last); err != nil || last != 30 {
		t.Fatalf("latest value in a second wins: got %v (%v)", last, err)
	}
}
