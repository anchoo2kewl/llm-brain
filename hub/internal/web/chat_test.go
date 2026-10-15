package web

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchoo2kewl/llm-brain/hub/internal/chat"
	"github.com/anchoo2kewl/llm-brain/hub/internal/ingest"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

const secretToken = "xoxp-0000-secret-token-value"

// fakeSlack answers auth.test and conversations.list for secretToken (or a second token, "xoxp-new").
func fakeSlack(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+secretToken && auth != "Bearer xoxp-new" {
			_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
			return
		}
		switch strings.TrimPrefix(r.URL.Path, "/api/") {
		case "auth.test":
			w.Header().Set("X-OAuth-Scopes", "channels:history,channels:read,users:read")
			_, _ = w.Write([]byte(`{"ok":true,"user_id":"U0000000001","user":"me-user","team":"acme","url":"https://acme.slack.com/"}`))
		case "conversations.list":
			_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C0000000001","name":"general","is_channel":true,"is_member":true},
				{"id":"C0000000002","name":"not-mine","is_channel":true,"is_member":false},
				{"id":"C0000000003","name":"secret-room","is_private":true,"is_member":true}]}`))
		default:
			_, _ = w.Write([]byte(`{"ok":false,"error":"unknown_method"}`))
		}
	}))
}

func chatServer(t *testing.T) (*Server, *store.Store, *httptest.Server) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	slack := fakeSlack(t)
	t.Cleanup(slack.Close)
	srv, err := New(st, ingest.NewEngine(st, "acme", "acme/web-app"), t.TempDir(), "h", log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	srv.Chat = &chat.Poller{Store: st, Log: log.New(io.Discard, "", 0), API: slack.URL + "/api"}
	if _, err := srv.Chat.Connect(context.Background(), secretToken); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddChatMessages(context.Background(), []store.ChatMessage{
		{ConvID: "C0000000001", ConvName: "general", ConvKind: "channel", TS: "1792012200.000100", UserName: "teammate-a",
			Text: "parent <script>x</script>", Permalink: "https://acme.slack.com/archives/C0000000001/p1792012200000100"},
		{ConvID: "C0000000001", ConvName: "general", ConvKind: "channel", TS: "1792012260.000100", ThreadTS: "1792012200.000100",
			UserName: "teammate-b", Text: "a reply", MentionsMe: true},
	}); err != nil {
		t.Fatal(err)
	}
	_ = st.SaveChatWatch(context.Background(), []store.ChatChannel{{ID: "C0000000001", Name: "general", Kind: "channel"}}, true, false)
	return srv, st, httptest.NewServer(srv.Handler())
}

func TestChatPagesNeverShowToken(t *testing.T) {
	_, _, ts := chatServer(t)
	defer ts.Close()
	for _, p := range []string{"/settings/slack", "/chat", "/chat?src=C0000000001", "/chat?src=mentions", "/", "/api/chat/me",
		"/api/chat/messages", "/settings/slack?err=x"} {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		body := string(b)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d %s", p, resp.StatusCode, body)
		}
		if strings.Contains(body, secretToken) || strings.Contains(body, "secret-token") {
			t.Fatalf("%s shows the token", p)
		}
		if strings.Contains(body, "<script>x") {
			t.Fatalf("%s: unescaped message", p)
		}
	}
	b := get(t, ts.URL+"/settings/slack")
	for _, want := range []string{"Connected as <strong>@me-user</strong> in <strong>acme</strong>", "groups:history", "search:read",
		`value="C0000000001|channel|general" checked`, `value="C0000000003|private|secret-room" >`, "Replace token"} {
		if !strings.Contains(b, want) {
			t.Errorf("settings page missing %q", want)
		}
	}
	if strings.Contains(b, "not-mine") || strings.Contains(b, "channels:history</code>") {
		t.Error("settings page lists a channel I'm not in, or a scope I have")
	}
	b = get(t, ts.URL+"/chat?src=C0000000001")
	if !strings.Contains(b, "chat-replies") || !strings.Contains(b, "teammate-b") || !strings.Contains(b, `id="c2"`) ||
		!strings.Contains(b, "acme.slack.com/archives/C0000000001/p1792012200000100") || !strings.Contains(b, "mentions you") {
		t.Errorf("chat page: %s", b)
	}
	if b := get(t, ts.URL+"/"); !strings.Contains(b, "From chat: 2 messages in 24h") && !strings.Contains(b, "From chat: 0 messages in 24h") {
		t.Errorf("overview has no chat count")
	}
}

func get(t *testing.T, u string) string {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestChatAPI(t *testing.T) {
	_, _, ts := chatServer(t)
	defer ts.Close()
	var me map[string]any
	if err := json.Unmarshal([]byte(get(t, ts.URL+"/api/chat/me")), &me); err != nil {
		t.Fatal(err)
	}
	if me["user_id"] != "U0000000001" || me["user_name"] != "me-user" || me["team"] != "acme" {
		t.Fatalf("me: %v", me)
	}
	var ms []map[string]any
	if err := json.Unmarshal([]byte(get(t, ts.URL+"/api/chat/messages?after=1&limit=500")), &ms); err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0]["id"] != float64(2) || ms[0]["mentions_me"] != true || ms[0]["thread_ts"] != "1792012200.000100" {
		t.Fatalf("messages: %v", ms)
	}
	keys := []string{"id", "source", "conv_id", "conv_name", "conv_kind", "mentions_me", "ts", "thread_ts", "user_id", "user_name", "text", "permalink", "at"}
	if len(ms[0]) != len(keys) {
		t.Fatalf("fields: %v", ms[0])
	}
	for _, k := range keys {
		if _, ok := ms[0][k]; !ok {
			t.Errorf("missing %s", k)
		}
	}
	if b := get(t, ts.URL+"/api/chat/messages?after=99"); strings.TrimSpace(b) != "[]" {
		t.Fatalf("empty page: %q", b)
	}
}

func TestSlackSettingsPosts(t *testing.T) {
	_, st, ts := chatServer(t)
	defer ts.Close()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(path string, form url.Values, origin string) *http.Response {
		req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	ctx := context.Background()
	// Another site can't change what's watched, or swap the token.
	if r := post("/settings/slack", url.Values{"dms": {"1"}}, "https://evil.example"); r.StatusCode != 403 {
		t.Fatalf("cross-site save: %d", r.StatusCode)
	}
	r := post("/settings/slack", url.Values{"ch": {"C0000000003|private|secret-room"}, "mentions": {"1"}}, ts.URL)
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("save: %d", r.StatusCode)
	}
	set, _ := st.ChatSettings(ctx)
	if len(set.Channels) != 1 || set.Channels[0] != (store.ChatChannel{ID: "C0000000003", Name: "secret-room", Kind: "private"}) || set.WatchDMs || !set.WatchMentions {
		t.Fatalf("saved: %+v", set)
	}
	// A bad token is refused and the old one kept; a good one replaces it. Neither appears in the redirect.
	r = post("/settings/slack/token", url.Values{"token": {"xoxp-bad"}}, "")
	if loc := r.Header.Get("Location"); !strings.Contains(loc, "invalid_auth") || strings.Contains(loc, "xoxp") {
		t.Fatalf("bad token redirect: %q", loc)
	}
	if set, _ := st.ChatSettings(ctx); set.Token != secretToken {
		t.Fatal("bad token replaced the good one")
	}
	r = post("/settings/slack/token", url.Values{"token": {"xoxp-new"}}, "")
	if loc := r.Header.Get("Location"); loc != "/settings/slack?saved=token" {
		t.Fatalf("token redirect: %q", loc)
	}
	if set, _ := st.ChatSettings(ctx); set.Token != "xoxp-new" || set.UserName != "me-user" {
		t.Fatalf("token not replaced: user %q", set.UserName)
	}
}
