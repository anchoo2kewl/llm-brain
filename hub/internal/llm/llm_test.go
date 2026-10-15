package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
	if found, _ := st.LLMChats(ctx, "world", 10); len(found) != 1 {
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
