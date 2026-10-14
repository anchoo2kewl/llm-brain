// Package gateway polls the CLIProxyAPI account gateway's management API and stores
// per-account quota and every request it routed, so the hub can show which account and
// model served each call and how much allowance is left.
package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

type Poller struct {
	Store *store.Store
	URL   string // e.g. http://127.0.0.1:8317
	Key   string // management secret (CPA_MGMT_KEY)
	Log   *log.Logger
	HTTP  *http.Client
}

// KeyFromEnvFile reads CPA_MGMT_KEY from a KEY=VALUE file such as ~/.config/llm-brain/cpa.env.
func KeyFromEnvFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok && k == "CPA_MGMT_KEY" {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

func (p *Poller) Run(ctx context.Context, every time.Duration) {
	if p.HTTP == nil {
		p.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	for {
		if err := p.Once(ctx); err != nil {
			p.Log.Printf("gateway poll: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

func (p *Poller) Once(ctx context.Context) error {
	var files struct {
		Files []authFile `json:"files"`
	}
	if err := p.get(ctx, "/v0/management/auth-files", &files); err != nil {
		return err
	}
	now := store.Now()
	accts := make([]store.GatewayAccount, 0, len(files.Files))
	for _, f := range files.Files {
		accts = append(accts, f.account(now))
	}
	if err := p.Store.SaveGatewayAccounts(ctx, accts); err != nil {
		return err
	}
	// The usage queue is drained on read: store everything we get.
	var queue []usageRecord
	if err := p.get(ctx, "/v0/management/usage-queue", &queue); err != nil {
		return err
	}
	reqs := make([]store.GatewayRequest, 0, len(queue))
	for _, u := range queue {
		reqs = append(reqs, u.request())
	}
	return p.Store.AddGatewayRequests(ctx, reqs)
}

func (p *Poller) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.URL, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.Key)
	res, err := p.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", path, res.StatusCode)
	}
	return json.NewDecoder(res.Body).Decode(v)
}

type authFile struct {
	ID          string          `json:"id"`
	AuthIndex   string          `json:"auth_index"`
	Provider    string          `json:"provider"`
	Email       string          `json:"email"`
	Label       string          `json:"label"`
	Priority    int             `json:"priority"`
	Disabled    bool            `json:"disabled"`
	Failed      int             `json:"failed"`
	Cooldowns   json.RawMessage `json:"cooldowns"`
	ModelQuotas map[string]struct {
		ObservedAt string            `json:"observed_at"`
		Signals    map[string]string `json:"signals"`
	} `json:"model_quotas"`
}

func (f authFile) account(now string) store.GatewayAccount {
	a := store.GatewayAccount{ID: f.ID, AuthIndex: f.AuthIndex, Provider: f.Provider, Email: f.Email,
		Label: f.Label, Priority: f.Priority, Disabled: f.Disabled, Failed: f.Failed, Cooldowns: f.Cooldowns, SeenAt: now}
	if a.Label == f.Email { // the gateway defaults label to the email; only keep a real tag
		a.Label = ""
	}
	// Quota is per account, reported on every model's responses: use the freshest observation.
	var latest string
	var signals map[string]string
	for _, q := range f.ModelQuotas {
		if q.ObservedAt > latest {
			latest, signals = q.ObservedAt, q.Signals
		}
	}
	if latest != "" {
		a.QuotaAt = store.ParseT(latest).UTC().Format(store.TimeFormat)
		if t, err := time.Parse(time.RFC3339Nano, latest); err == nil {
			a.QuotaAt = t.UTC().Format(store.TimeFormat)
		}
	}
	a.Windows = Windows(signals)
	return a
}

// Windows normalizes quota headers into windows. Claude reports
// Anthropic-Ratelimit-Unified-{5h,7d}-{Utilization (0..1),Reset (unix),Status};
// Codex reports X-Codex-{Primary,Secondary}-{Used-Percent (0..100),Reset-At|Reset-After-Seconds,Window-Minutes}.
func Windows(sig map[string]string) []store.Window {
	get := func(k string) string {
		for kk, v := range sig {
			if strings.EqualFold(kk, k) {
				return v
			}
		}
		return ""
	}
	var out []store.Window
	for _, n := range []string{"5h", "7d"} {
		p := "Anthropic-Ratelimit-Unified-" + n + "-"
		if u := get(p + "Utilization"); u != "" {
			f, _ := strconv.ParseFloat(u, 64)
			r, _ := strconv.ParseInt(get(p+"Reset"), 10, 64)
			out = append(out, store.Window{Name: n, UsedPct: f * 100, ResetAt: r, Status: get(p + "Status")})
		}
	}
	for _, n := range []string{"Primary", "Secondary"} {
		p := "X-Codex-" + n + "-"
		u := get(p + "Used-Percent")
		if u == "" {
			continue
		}
		f, _ := strconv.ParseFloat(u, 64)
		w := store.Window{Name: strings.ToLower(n), UsedPct: f}
		if r, err := strconv.ParseInt(get(p+"Reset-At"), 10, 64); err == nil {
			w.ResetAt = r
		} else if s, err := strconv.ParseInt(get(p+"Reset-After-Seconds"), 10, 64); err == nil {
			w.ResetAt = time.Now().Unix() + s
		}
		if m, err := strconv.Atoi(get(p + "Window-Minutes")); err == nil {
			switch {
			case m == 300:
				w.Name = "5h"
			case m == 10080:
				w.Name = "7d"
			case m > 0:
				w.Name = fmt.Sprintf("%dm", m)
			}
		}
		out = append(out, w)
	}
	return out
}

type usageRecord struct {
	RequestID string `json:"request_id"`
	Timestamp string `json:"timestamp"`
	AuthIndex string `json:"auth_index"`
	Source    string `json:"source"`
	Model     string `json:"model"`
	LatencyMS int64  `json:"latency_ms"`
	UserAgent string `json:"user_agent"`
	Failed    bool   `json:"failed"`
	Fail      struct {
		StatusCode int `json:"status_code"`
	} `json:"fail"`
	Tokens struct {
		Input  int64 `json:"input_tokens"`
		Output int64 `json:"output_tokens"`
		Cached int64 `json:"cached_tokens"`
		Total  int64 `json:"total_tokens"`
	} `json:"tokens"`
}

// request keeps what the hub shows; the record's plain-text client api_key is deliberately dropped.
func (u usageRecord) request() store.GatewayRequest {
	ts := u.Timestamp
	if t, err := time.Parse(time.RFC3339Nano, u.Timestamp); err == nil {
		ts = t.UTC().Format(store.TimeFormat)
	}
	id := u.RequestID
	if id == "" {
		id = u.Timestamp + "/" + u.AuthIndex
	}
	return store.GatewayRequest{RequestID: id, TS: ts, AuthIndex: u.AuthIndex, Account: u.Source, Model: u.Model,
		UserAgent: u.UserAgent, Input: u.Tokens.Input, Output: u.Tokens.Output, Cached: u.Tokens.Cached,
		Total: u.Tokens.Total, LatencyMS: u.LatencyMS, Failed: u.Failed, StatusCode: u.Fail.StatusCode}
}
