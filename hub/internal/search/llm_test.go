package search

import (
	"slices"
	"testing"
)

func TestSearchPlainChats(t *testing.T) {
	f := newFixture(t, "")
	f.seed(t)
	id, err := f.st.CreateLLMChat(f.ctx, "pager math", "gpt-5")
	if err != nil {
		t.Fatal(err)
	}
	rid, err := f.st.AddLLMTurn(f.ctx, id, "gpt-5", "how do I count pages for a zeppelin catalogue?")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.idx.Sync(f.ctx); err != nil {
		t.Fatal(err)
	}
	// The question is indexed; the reply is still streaming, so it isn't yet.
	if got := f.search(t, "zeppelin"); !slices.Equal(got, []string{"l1"}) {
		t.Fatalf("question: %v", got)
	}
	if got := f.search(t, "ceil"); got != nil {
		t.Fatalf("streaming reply indexed: %v", got)
	}
	if err := f.st.SetLLMReply(f.ctx, rid, "Use ceil division: (total + size - 1) / size.", "done", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.idx.Sync(f.ctx); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string][]string{
		"ceil":                     {"l2"},
		"type:plain ceil":          {"l2"},
		"type:llm":                 {"l1", "l2"},
		"type:message zeppelin":    nil,
		"provider:gpt-5 type:llm":  {"l2"},
		"role:user type:plainchat": {"l1"},
	} {
		if got := f.search(t, q); !slices.Equal(got, want) {
			t.Errorf("%q: got %v want %v", q, got, want)
		}
	}
	res, err := f.idx.Search(f.ctx, Request{Q: "ceil"})
	if err != nil || len(res.Hits) != 1 {
		t.Fatal(res, err)
	}
	if h := res.Hits[0]; h.Link != "/llms/1#l2" || h.Title != "pager math" || h.Label != "plain chat · gpt-5" {
		t.Errorf("%+v", h)
	}
}
