package gateway

import "testing"

func TestWindowsClaude(t *testing.T) {
	w := Windows(map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Utilization": "0.26", "Anthropic-Ratelimit-Unified-5h-Reset": "1792012200",
		"Anthropic-Ratelimit-Unified-5h-Status": "allowed", "Anthropic-Ratelimit-Unified-7d-Utilization": "0.44",
	})
	if len(w) != 2 || w[0].Name != "5h" || w[0].UsedPct < 25.9 || w[0].UsedPct > 26.1 || w[0].ResetAt != 1792012200 || w[1].Name != "7d" {
		t.Fatalf("got %+v", w)
	}
}

func TestWindowsCodex(t *testing.T) {
	w := Windows(map[string]string{"x-codex-primary-used-percent": "12", "x-codex-primary-window-minutes": "300",
		"x-codex-secondary-used-percent": "40", "x-codex-secondary-window-minutes": "10080"})
	if len(w) != 2 || w[0].Name != "5h" || w[0].UsedPct != 12 || w[1].Name != "7d" {
		t.Fatalf("got %+v", w)
	}
}

func TestRequestDropsAPIKeyAndNormalizesTime(t *testing.T) {
	r := usageRecord{RequestID: "r1", Timestamp: "2026-10-14T17:24:55.315735-04:00", Source: "me@x", Model: "claude-opus-5-5"}.request()
	if r.TS != "2026-10-14T21:24:55.315Z" || r.RequestID != "r1" {
		t.Fatalf("got %+v", r)
	}
}
