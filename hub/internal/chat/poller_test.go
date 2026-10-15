package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

const (
	testToken = "xoxp-0000-test-token"
	me        = "U0000000001"
	general   = "C0000000001"
	other     = "C0000000009"
	dm        = "D0000000001"
)

var now = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func tsAt(d time.Duration, us int) string { return fmt.Sprintf("%d.%06d", now.Add(-d).Unix(), us) }

// fakeSlack is a Slack Web API stand-in: it records every call, rejects a wrong token, and answers the
// first conversations.history call with a 429.
type fakeSlack struct {
	mu       sync.Mutex
	calls    []string // "method?query"
	limited  bool
	parent   string
	reply    string
	plain    string
	dmMsg    string
	mention  string
	userInfo map[string]int
}

func newFake() *fakeSlack {
	return &fakeSlack{parent: tsAt(time.Hour, 100), reply: tsAt(30*time.Minute, 200), plain: tsAt(20*time.Minute, 300),
		dmMsg: tsAt(10*time.Minute, 400), mention: tsAt(5*time.Minute, 500), userInfo: map[string]int{}}
}

func (f *fakeSlack) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeSlack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, "/api/")
	q := r.URL.Query()
	f.mu.Lock()
	f.calls = append(f.calls, method+"?"+r.URL.RawQuery)
	f.mu.Unlock()
	if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+testToken {
		writeAPI(w, map[string]any{"ok": false, "error": "invalid_auth"})
		return
	}
	switch method {
	case "auth.test":
		w.Header().Set("X-OAuth-Scopes", "channels:history,channels:read,users:read")
		writeAPI(w, map[string]any{"ok": true, "user_id": me, "user": "me-user", "team": "acme", "url": "https://acme.slack.com/"})
	case "conversations.history":
		f.mu.Lock()
		first := !f.limited
		f.limited = true
		f.mu.Unlock()
		if first {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		var msgs []map[string]any
		switch q.Get("channel") {
		case general:
			msgs = []map[string]any{
				{"type": "message", "ts": f.plain, "user": "U0000000003", "text": "see <#C0000000003|random> &amp; <https://example.com|docs>"},
				{"type": "message", "subtype": "channel_join", "ts": tsAt(50*time.Minute, 1), "user": "U0000000003", "text": "<@U0000000003> has joined"},
				{"type": "message", "ts": f.parent, "thread_ts": f.parent, "reply_count": 1, "latest_reply": f.reply, "user": "U0000000002", "text": "deploy please <@" + me + ">"},
			}
		case dm:
			msgs = []map[string]any{{"type": "message", "ts": f.dmMsg, "user": "U0000000002", "text": "quick question"}}
		}
		var keep []map[string]any // honour oldest, as Slack does
		for _, m := range msgs {
			if tsAfter(m["ts"].(string), q.Get("oldest")) {
				keep = append(keep, m)
			}
		}
		writeAPI(w, map[string]any{"ok": true, "messages": keep, "has_more": false})
	case "conversations.replies":
		msgs := []map[string]any{{"type": "message", "ts": f.parent, "thread_ts": f.parent, "reply_count": 1, "user": "U0000000002", "text": "deploy please <@" + me + ">"}}
		if tsAfter(f.reply, q.Get("oldest")) {
			msgs = append(msgs, map[string]any{"type": "message", "ts": f.reply, "thread_ts": f.parent, "user": "U0000000003", "text": "on it\n*Sent using* Claude"})
		}
		writeAPI(w, map[string]any{"ok": true, "messages": msgs, "has_more": false})
	case "users.conversations":
		writeAPI(w, map[string]any{"ok": true, "channels": []map[string]any{
			{"id": dm, "is_im": true, "user": "U0000000002"},
		}})
	case "search.messages":
		writeAPI(w, map[string]any{"ok": true, "messages": map[string]any{"paging": map[string]any{"pages": 1}, "matches": []map[string]any{
			{"ts": f.mention, "user": "U0000000003", "text": "<@" + me + "> can you look", "permalink": "https://acme.slack.com/archives/" + other + "/p1",
				"channel": map[string]any{"id": other, "name": "other", "is_channel": true}},
			// the thread parent again, as search finds it: one row, not two
			{"ts": f.parent, "user": "U0000000002", "text": "deploy please <@" + me + ">", "permalink": "https://acme.slack.com/archives/" + general + "/p2",
				"channel": map[string]any{"id": general, "name": "general", "is_channel": true}},
		}}})
	case "users.info":
		id := q.Get("user")
		f.mu.Lock()
		f.userInfo[id]++
		f.mu.Unlock()
		names := map[string]map[string]any{
			"U0000000002": {"name": "ta", "profile": map[string]any{"display_name": "teammate-a"}},
			"U0000000003": {"name": "tb", "profile": map[string]any{"display_name": "", "real_name": "teammate-b"}},
			me:            {"name": "me-user", "profile": map[string]any{}},
		}
		if u, ok := names[id]; ok {
			writeAPI(w, map[string]any{"ok": true, "user": u})
		} else {
			writeAPI(w, map[string]any{"ok": false, "error": "user_not_found"})
		}
	default:
		writeAPI(w, map[string]any{"ok": false, "error": "unknown_method"})
	}
}

func writeAPI(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestPoller(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	if err := st.SetSettings(ctx, map[string]string{store.KeySlackToken: testToken}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveChatWatch(ctx, []store.ChatChannel{{ID: general, Name: "general", Kind: "channel"}}, true, true); err != nil {
		t.Fatal(err)
	}
	var slept []time.Duration
	writes := 0
	p := &Poller{Store: st, Log: log.New(io.Discard, "", 0), API: srv.URL + "/api", Now: func() time.Time { return now },
		Sleep:   func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil },
		OnWrite: func(context.Context) { writes++ }}
	if err := p.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 1 || slept[0] != time.Second {
		t.Fatalf("429 not waited out: %v", slept)
	}
	ms, err := st.ChatMessagesAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]store.ChatMessage{}
	for _, m := range ms {
		got[m.ConvID+"/"+m.TS] = m
	}
	if len(ms) != 5 { // general: plain, parent, reply; DM; mention in #other (the join is skipped, the parent is stored once)
		t.Fatalf("%d rows: %+v", len(ms), ms)
	}
	parent := got[general+"/"+fake.parent]
	if !parent.MentionsMe || parent.UserName != "teammate-a" || parent.Text != "deploy please @me-user" || parent.ConvKind != "channel" ||
		parent.Permalink != "https://acme.slack.com/archives/"+general+"/p"+strings.Replace(fake.parent, ".", "", 1) {
		t.Errorf("parent: %+v", parent)
	}
	reply := got[general+"/"+fake.reply]
	if reply.Text != "on it" || reply.UserName != "teammate-b" || reply.ThreadTS != fake.parent || !strings.Contains(reply.Permalink, "thread_ts="+fake.parent) {
		t.Errorf("reply: %+v", reply)
	}
	if plain := got[general+"/"+fake.plain]; plain.Text != "see #random & docs (https://example.com)" || plain.MentionsMe {
		t.Errorf("plain: %+v", plain)
	}
	if d := got[dm+"/"+fake.dmMsg]; d.ConvKind != "dm" || d.ConvName != "teammate-a" || d.MentionsMe {
		t.Errorf("dm: %+v", d)
	}
	if m := got[other+"/"+fake.mention]; !m.MentionsMe || m.ConvName != "other" || m.ConvKind != "channel" || !strings.HasSuffix(m.Permalink, "/p1") {
		t.Errorf("mention: %+v", m)
	}
	if writes != 1 {
		t.Errorf("OnWrite ran %d times", writes)
	}
	set, _ := st.ChatSettings(ctx)
	if set.UserID != me || set.UserName != "me-user" || set.Team != "acme" || set.LastError != "" || set.LastPoll == "" {
		t.Errorf("settings: %+v", set)
	}
	// The first history call for a conversation reads the backfill window.
	if !strings.Contains(fake.calls[slicesIndex(fake.calls, "conversations.history")], "oldest="+fmt.Sprint(now.Add(-24*time.Hour).Unix())) {
		t.Errorf("first history call: %v", fake.calls)
	}

	// A second cycle: nothing new, history from the newest ts read, replies after the last reply, and no
	// user looked up twice.
	before := len(fake.calls)
	if err := p.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.ChatMessagesAfter(ctx, 0, 100); len(n) != 5 {
		t.Fatalf("second cycle added rows: %d", len(n))
	}
	if writes != 1 {
		t.Errorf("OnWrite ran on a cycle with nothing new")
	}
	second := strings.Join(fake.calls[before:], "\n")
	if !strings.Contains(second, "channel="+general+"&limit=200&oldest="+fake.plain) {
		t.Errorf("history cursor not used:\n%s", second)
	}
	if !strings.Contains(second, "oldest="+fake.reply) {
		t.Errorf("replies cursor not used:\n%s", second)
	}
	for id, n := range fake.userInfo {
		if n != 1 {
			t.Errorf("users.info %s called %d times", id, n)
		}
	}
	for _, c := range fake.calls {
		m, _, _ := strings.Cut(c, "?")
		if !readMethods[m] {
			t.Errorf("called %s", m)
		}
	}
}

func slicesIndex(calls []string, prefix string) int {
	for i, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

func TestPollerBadToken(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := httptest.NewServer(newFake())
	defer srv.Close()
	_ = st.SetSettings(ctx, map[string]string{store.KeySlackToken: "xoxp-wrong"})
	var logs strings.Builder
	p := &Poller{Store: st, Log: log.New(&logs, "", 0), API: srv.URL + "/api"}
	err = p.Once(ctx)
	if !IsAuthError(err) {
		t.Fatalf("got %v", err)
	}
	set, _ := st.ChatSettings(ctx)
	if !strings.Contains(set.LastError, "invalid_auth") || strings.Contains(set.LastError, "xoxp") || strings.Contains(logs.String(), "xoxp") {
		t.Fatalf("last error %q, logs %q", set.LastError, logs.String())
	}
}

func TestDMBudgetRoundRobin(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var mu sync.Mutex
	read := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/api/") {
		case "auth.test":
			writeAPI(w, map[string]any{"ok": true, "user_id": me, "user": "me-user", "team": "acme"})
		case "users.conversations":
			var cs []map[string]any
			for i := range 5 {
				cs = append(cs, map[string]any{"id": fmt.Sprintf("D000000000%d", i), "is_mpim": true, "name": "mpdm-a--b-1"})
			}
			writeAPI(w, map[string]any{"ok": true, "channels": cs})
		case "conversations.history":
			mu.Lock()
			read[r.URL.Query().Get("channel")]++
			mu.Unlock()
			writeAPI(w, map[string]any{"ok": true, "messages": []any{}})
		default:
			writeAPI(w, map[string]any{"ok": false, "error": "unknown_method"})
		}
	}))
	defer srv.Close()
	_ = st.SetSettings(ctx, map[string]string{store.KeySlackToken: testToken})
	_ = st.SaveChatWatch(ctx, nil, true, false)
	p := &Poller{Store: st, Log: log.New(io.Discard, "", 0), API: srv.URL + "/api", MaxDMCalls: 2}
	for range 3 {
		if err := p.Once(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// 3 cycles x 2 calls over 5 DMs: every DM read at least once, none more than twice.
	if len(read) != 5 {
		t.Fatalf("read %v", read)
	}
	for id, n := range read {
		if n > 2 {
			t.Errorf("%s read %d times", id, n)
		}
	}
}
