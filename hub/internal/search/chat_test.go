package search

import (
	"slices"
	"strings"
	"testing"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

func TestSearchChat(t *testing.T) {
	f := newFixture(t, "")
	f.seed(t) // transcripts, a run and an event: chat filters must leave them out
	msgs := []store.ChatMessage{
		{ConvID: "C0000000001", ConvName: "general", ConvKind: "channel", TS: "1792012200.000100", UserName: "teammate-a",
			Text: "the canary deploy for web-app-api is green"},
		{ConvID: "C0000000002", ConvName: "release-train", ConvKind: "channel", TS: "1792012260.000100", UserName: "teammate-b",
			Text: "@me-user can you check the canary"},
		{ConvID: "D0000000001", ConvName: "teammate-a", ConvKind: "dm", TS: "1792012320.000100", UserName: "teammate-a", Text: "lunch?"},
	}
	if _, err := f.st.AddChatMessages(f.ctx, msgs); err != nil {
		t.Fatal(err)
	}
	if err := f.idx.Sync(f.ctx); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"canary":                            {"c1", "c2"},
		"canary type:chat":                  {"c1", "c2"},
		"type:chat":                         {"c1", "c2", "c3"},
		"type:slack channel:#general":       {"c1"},
		"channel:release-train":             {"c2"},
		"from:@teammate-a":                  {"c1", "c3"},
		"from:teammate-a type:chat kind:dm": {"c3"},
		"mentions:me":                       nil,
		"deploy type:chat":                  {"c1"},
		"type:message canary":               nil,
	}
	check := func() {
		for q, want := range cases {
			if got := f.search(t, q); !slices.Equal(got, want) {
				t.Errorf("%q: got %v want %v", q, got, want)
			}
		}
	}
	check()
	// Found again as a mention: the same row gains mentions_me and is re-indexed.
	m := msgs[1]
	m.MentionsMe = true
	if _, err := f.st.AddChatMessages(f.ctx, []store.ChatMessage{m}); err != nil {
		t.Fatal(err)
	}
	if err := f.idx.Sync(f.ctx); err != nil {
		t.Fatal(err)
	}
	cases["mentions:me"] = []string{"c2"}
	check()
	res, err := f.idx.Search(f.ctx, Request{Q: "lunch"})
	if err != nil || len(res.Hits) != 1 {
		t.Fatalf("lunch: %+v %v", res, err)
	}
	if h := res.Hits[0]; h.Type != "chat" || h.Title != "teammate-a" || !strings.HasPrefix(h.Link, "/chat?src=D0000000001&until=") || !strings.HasSuffix(h.Link, "#c3") {
		t.Fatalf("hit: %+v", h)
	}
}
