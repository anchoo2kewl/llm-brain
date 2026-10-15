package insights

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/ingest"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

func claudeLines(n int, pad string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `{"type":"user","sessionId":"s","cwd":"/w","timestamp":"2026-03-11T15:00:%02dZ","message":{"role":"user","content":"prompt %d"}}`+"\n", i, i)
		fmt.Fprintf(&b, `{"type":"assistant","sessionId":"s","cwd":"/w","timestamp":"2026-03-11T15:00:%02dZ","message":{"id":"m%d","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"answer%s"},{"type":"tool_use","id":"t%d","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":1,"output_tokens":10,"cache_read_input_tokens":100,"cache_creation_input_tokens":0}}}`+"\n", i, i, pad, i)
	}
	return b.String()
}

type counts struct{ usage, tools, prompts, blocks int }

func derived(t *testing.T, st *store.Store) counts {
	t.Helper()
	var c counts
	_ = st.DB.QueryRow(`SELECT COUNT(*) FROM usage_events`).Scan(&c.usage)
	_ = st.DB.QueryRow(`SELECT COUNT(*) FROM tool_calls`).Scan(&c.tools)
	_ = st.DB.QueryRow(`SELECT COALESCE(SUM(prompts), 0), COALESCE(SUM(blocks), 0) FROM activity`).Scan(&c.prompts, &c.blocks)
	return c
}

// A transcript that is truncated or replaced is re-read from the start: its derived rows must be rebuilt, not
// added to (item: reset double counting).
func TestResetRebuildsDerivedRows(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	eng := ingest.NewEngine(st, "acme", "acme/tracker")
	ru := &Rollup{Store: st}
	data := claudeLines(3, "")
	if _, err := eng.Chunk(ctx, ingest.Chunk{Host: "h", Kind: ingest.KindClaude, Path: "/p/x/s.jsonl", Mtime: time.Now(), Data: []byte(data)}); err != nil {
		t.Fatal(err)
	}
	if err := ru.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	before := derived(t, st)
	if before != (counts{usage: 3, tools: 3, prompts: 3, blocks: 9}) {
		t.Fatalf("first read: %+v", before)
	}
	// The file is replaced (same content re-read from offset 0). Its messages are the newest, so the re-read
	// ones reuse their ids; the rollup must still see them.
	if _, err := eng.Chunk(ctx, ingest.Chunk{Host: "h", Kind: ingest.KindClaude, Path: "/p/x/s.jsonl", Reset: true, Mtime: time.Now(), Data: []byte(data)}); err != nil {
		t.Fatal(err)
	}
	if err := ru.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if after := derived(t, st); after != before {
		t.Fatalf("after reset: %+v, want %+v (no double counting, nothing skipped)", after, before)
	}
	// Replaced by a shorter file: the derived rows shrink with it.
	if _, err := eng.Chunk(ctx, ingest.Chunk{Host: "h", Kind: ingest.KindClaude, Path: "/p/x/s.jsonl", Reset: true, Mtime: time.Now(), Data: []byte(claudeLines(1, ""))}); err != nil {
		t.Fatal(err)
	}
	if err := ru.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if got := derived(t, st); got != (counts{usage: 1, tools: 1, prompts: 1, blocks: 3}) {
		t.Fatalf("after shrinking: %+v", got)
	}
}

// Usage on an assistant line bigger than the stored raw cap is kept: ingestion reads it from the full line.
func TestUsageOfHugeLines(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	eng := ingest.NewEngine(st, "acme", "acme/tracker")
	data := claudeLines(1, strings.Repeat("x", 300<<10)) // > 256 KiB, so the stored raw is truncated
	if _, err := eng.Chunk(ctx, ingest.Chunk{Host: "h", Kind: ingest.KindClaude, Path: "/p/x/big.jsonl", Mtime: time.Now(), Data: []byte(data)}); err != nil {
		t.Fatal(err)
	}
	var rawLen int
	_ = st.DB.QueryRow(`SELECT MAX(length(raw)) FROM messages`).Scan(&rawLen)
	if rawLen >= 300<<10 {
		t.Fatalf("fixture should exceed the raw cap (raw %d bytes)", rawLen)
	}
	if err := (&Rollup{Store: st}).CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	var out, read int64
	if err := st.DB.QueryRow(`SELECT output, cache_read FROM usage_events WHERE api_id = 'm0'`).Scan(&out, &read); err != nil {
		t.Fatalf("usage of the huge line was lost: %v", err)
	}
	if out != 10 || read != 100 {
		t.Fatalf("usage %d/%d", out, read)
	}
	if c := derived(t, st); c.tools != 1 {
		t.Fatalf("tool call of the huge line: %+v", c)
	}
}
