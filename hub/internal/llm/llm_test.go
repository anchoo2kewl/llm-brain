package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// FakeGateway serves /v1/models and a streaming /v1/chat/completions that echoes the last user message.
func FakeGateway(t *testing.T) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gpt-5","owned_by":"openai"},{"id":"claude-sonnet-4-5","owned_by":"anthropic"},
				{"id":"gemini-2.5-pro","owned_by":"google"},{"id":"qwen3-coder","owned_by":"iflow"}]}`)
		case "/v1/chat/completions":
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, string(b))
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			for _, piece := range []string{"Hello", ", **world**", "!"} {
				fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", piece)
				w.(http.Flusher).Flush()
			}
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

func TestModelsGrouped(t *testing.T) {
	gw, _ := FakeGateway(t)
	c := &Client{URL: gw.URL, Key: "test-key"}
	ms, err := c.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, g := range Grouped(ms) {
		got = append(got, g.Vendor+":"+g.Models[0].ID)
	}
	if strings.Join(got, " ") != "Claude:claude-sonnet-4-5 OpenAI:gpt-5 Gemini:gemini-2.5-pro Other:qwen3-coder" {
		t.Error(got)
	}
	if _, err := (&Client{URL: gw.URL, Key: "wrong"}).Models(context.Background()); err == nil {
		t.Error("bad key accepted")
	}
	if _, err := (&Client{URL: "http://127.0.0.1:1", Key: "k"}).Models(context.Background()); err == nil {
		t.Error("unreachable gateway gave models")
	}
}

func TestChatStreamsAndSwitchesModels(t *testing.T) {
	gw, bodies := FakeGateway(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	ch := &Chats{S: st, C: &Client{URL: gw.URL, Key: "test-key"}}
	id, rid, err := ch.Start(ctx, "gpt-5", "hi there\nsecond line")
	if err != nil {
		t.Fatal(err)
	}
	ch.Wait()
	m, _ := st.LLMMessage(ctx, rid)
	if m.Status != "done" || m.Content != "Hello, **world**!" || m.Model != "gpt-5" {
		t.Fatalf("%+v", m)
	}
	if text, done, _, ok := ch.Snapshot(rid); !ok || !done || text != "Hello, **world**!" {
		t.Errorf("snapshot %q %v %v", text, done, ok)
	}
	if _, err := ch.Send(ctx, id, "claude-sonnet-4-5", "again"); err != nil {
		t.Fatal(err)
	}
	ch.Wait()
	c, _ := st.LLMChat(ctx, id)
	if c.Title != "hi there" || c.Model != "claude-sonnet-4-5" {
		t.Errorf("%+v", c)
	}
	ms, _ := st.LLMMessages(ctx, id)
	if len(ms) != 4 || ms[3].Model != "claude-sonnet-4-5" {
		t.Fatalf("%+v", ms)
	}
	// The second request carries the whole conversation to the new model.
	last := (*bodies)[1]
	for _, want := range []string{`"model":"claude-sonnet-4-5"`, `"stream":true`, `hi there`, `Hello, **world**!`, `"again"`} {
		if !strings.Contains(last, want) {
			t.Errorf("request %s missing %s", last, want)
		}
	}
	if found, _ := st.LLMChats(ctx, "world", 10, true); len(found) != 1 {
		t.Errorf("search: %v", found)
	}
}

func TestChatGatewayDown(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	defer st.Close()
	ch := &Chats{S: st, C: &Client{URL: "http://127.0.0.1:1", Key: "k"}}
	_, rid, err := ch.Start(context.Background(), "gpt-5", "hi")
	if err != nil {
		t.Fatal(err)
	}
	ch.Wait()
	m, _ := st.LLMMessage(context.Background(), rid)
	if m.Status != "error" || !strings.Contains(m.Error, "gateway isn't answering") {
		t.Fatalf("%+v", m)
	}
	if _, _, err := ch.Start(context.Background(), "", "hi"); err == nil {
		t.Error("no model accepted")
	}
}

func TestMarkdownIsSafe(t *testing.T) {
	got := string(Markdown("# Title\n\nSome **bold** and *it* with `<b>code</b>`.\n<script>alert(1)</script>\n\n- one\n- [link](https://example.com/a?b=1&c=2)\n- [bad](javascript:alert(1))\n\n```go\nif a < b {}\n```\n"))
	for _, want := range []string{"<h3>Title</h3>", "<strong>bold</strong>", "<em>it</em>", "<code>&lt;b&gt;code&lt;/b&gt;</code>",
		"&lt;script&gt;alert(1)&lt;/script&gt;", "<li>one</li>", `<a href="https://example.com/a?b=1&amp;c=2" rel="noreferrer noopener" target="_blank">link</a>`,
		`[bad](javascript:alert(1))`, `<pre><code class="lang-go">if a &lt; b {}</code></pre>`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	if strings.Contains(got, "<script>") || strings.Contains(got, `href="javascript`) {
		t.Errorf("unsafe output:\n%s", got)
	}
}

func TestStreamLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if r.URL.Query().Get("big") == "line" || strings.Contains(r.Header.Get("X-Test"), "line") {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", strings.Repeat("x", 4096))
			return
		}
		for i := 0; i < 10000; i++ { // an endless reply
			if _, err := fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", strings.Repeat("y", 100)); err != nil {
				return // the client hung up
			}
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL, Key: "k", MaxLine: 1024, MaxReply: 1000}
	got, err := c.Stream(context.Background(), "m", nil, nil)
	if !errors.Is(err, ErrTooLarge) || len(got) > 1000 || len(got) < 900 {
		t.Fatalf("reply cap: %d bytes, %v", len(got), err)
	}
	c.HTTP = &http.Client{Transport: headerRT{"X-Test", "line"}}
	if _, err := c.Stream(context.Background(), "m", nil, nil); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("line cap: %v", err)
	}
}

type headerRT struct{ k, v string }

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set(h.k, h.v)
	return http.DefaultTransport.RoundTrip(r)
}

// A reply that times out is finished as an error with a fresh context, so the chat isn't busy forever.
func TestTimedOutReplyIsFinished(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select { // stall
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	st, _ := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	defer st.Close()
	ch := &Chats{S: st, C: &Client{URL: srv.URL, Key: "k"}, Timeout: 200 * time.Millisecond}
	id, rid, err := ch.Start(context.Background(), "gpt-5", "hi")
	if err != nil {
		t.Fatal(err)
	}
	ch.Wait()
	m, _ := st.LLMMessage(context.Background(), rid)
	if m.Status != "error" || m.Content != "partial" || !strings.Contains(m.Error, "no complete reply") {
		t.Fatalf("%+v", m)
	}
	if _, err := ch.Send(context.Background(), id, "gpt-5", "again"); err != nil {
		t.Fatalf("chat still busy: %v", err)
	}
	ch.Wait() // the second reply times out too; let it finish before the store closes
}

func TestPickerOrderAndLabels(t *testing.T) {
	ms := []Model{{ID: "claude-sonnet-4-5-20250929"}, {ID: "claude-haiku-5-5"}, {ID: "claude-opus-5-5"}, {ID: "claude-sonnet-5-5"},
		{ID: "gpt-5.5"}, {ID: "gpt-6.1-sol"}, {ID: "gpt-6-luna"}, {ID: "gpt-image-2"}, {ID: "codex-auto-review"}}
	var got []string
	for _, g := range Grouped(ms) {
		for _, m := range g.Models {
			got = append(got, Label(m.ID))
		}
	}
	want := "Claude Opus 5.5|Claude Sonnet 5.5|Claude Haiku 5.5|Claude Sonnet 4.5 (2025-09-29)|GPT-6.1 Sol|GPT-6 Luna|GPT-5.5"
	if s := strings.Join(got, "|"); s != want {
		t.Fatalf("got  %s\nwant %s", s, want)
	}
}

func TestPickerOrderEdges(t *testing.T) {
	ms := []Model{{ID: "gpt-5-nano"}, {ID: "gpt-5-mini"}, {ID: "gpt-5"}, {ID: "claude-sonnet-4-5-20250929"}, {ID: "claude-sonnet-4-5-20251001"},
		{ID: "claude-sonnet-4-5"}, {ID: "gpt-4o-2024-08-06"}, {ID: "gpt-4o"}}
	var got []string
	for _, g := range Grouped(ms) {
		for _, m := range g.Models {
			got = append(got, m.ID)
		}
	}
	want := "claude-sonnet-4-5|claude-sonnet-4-5-20251001|claude-sonnet-4-5-20250929|gpt-5|gpt-5-mini|gpt-5-nano|gpt-4o|gpt-4o-2024-08-06"
	if s := strings.Join(got, "|"); s != want {
		t.Fatalf("got  %s\nwant %s", s, want)
	}
	if l := Label("gpt-4o-2024-08-06"); l != "GPT-4o (2024-08-06)" {
		t.Fatalf("label %q", l)
	}
}

func TestPartsFromAttachments(t *testing.T) {
	dir := t.TempDir()
	write := func(sum string, b []byte) { _ = os.WriteFile(filepath.Join(dir, sum), b, 0o600) }
	write("img", []byte("\x89PNG fake"))
	write("txt", []byte("a,b\n1,2\n"))
	write("bin", []byte{0, 1, 2})
	c := &Chats{AssetDir: dir}
	budget := 1 << 20
	ps := c.parts("what is this?", []store.Asset{{Name: "shot.png", SHA256: "img"}, {Name: "data.csv", SHA256: "txt"}, {Name: "blob.bin", SHA256: "bin"}, {Name: "gone.md", SHA256: "nope"}}, &budget)
	if len(ps) != 5 || ps[0].Text != "what is this?" {
		t.Fatalf("parts: %+v", ps)
	}
	if ps[1].Type != "image_url" || !strings.HasPrefix(ps[1].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("image: %+v", ps[1])
	}
	if !strings.Contains(ps[2].Text, "a,b\n1,2") || !strings.Contains(ps[3].Text, "can't be read") || !strings.Contains(ps[4].Text, "no longer") {
		t.Fatalf("files: %+v", ps[2:])
	}
	small := 5
	if ps := c.parts("", []store.Asset{{Name: "big.png", SHA256: "img", Size: 10}}, &small); !strings.Contains(ps[0].Text, "too big") {
		t.Fatalf("budget: %+v", ps)
	}
	b, _ := json.Marshal(Message{Role: "user", Content: ps[:2]})
	if !strings.Contains(string(b), `"type":"image_url","image_url":{"url":"data:image/png`) {
		t.Fatalf("wire: %s", b)
	}
}
