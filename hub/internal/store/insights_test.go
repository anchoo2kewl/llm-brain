package store

import (
	"context"
	"path/filepath"
	"testing"
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
	for _, u := range []float64{10, 10, 10, 25} {
		if err := st.SaveGatewayAccounts(ctx, acct(u)); err != nil {
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
}
