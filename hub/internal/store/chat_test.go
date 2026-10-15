package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAddChatMessagesIdempotent(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs := []ChatMessage{
		{ConvID: "C0000000001", ConvName: "general", ConvKind: "channel", TS: "1792012200.000100", UserID: "U0000000002", UserName: "teammate-a", Text: "hello"},
		{ConvID: "C0000000001", ConvName: "general", ConvKind: "channel", TS: "1792012260.000200", ThreadTS: "1792012200.000100", Text: "reply"},
	}
	if n, err := st.AddChatMessages(ctx, msgs); err != nil || n != 2 {
		t.Fatalf("first insert: %d %v", n, err)
	}
	if n, err := st.AddChatMessages(ctx, msgs); err != nil || n != 0 {
		t.Fatalf("second insert: %d %v", n, err)
	}
	// The same message found again as a mention: still one row, now flagged, and queued for re-indexing.
	again := msgs[0]
	again.MentionsMe, again.Text = true, "changed text is ignored"
	if n, err := st.AddChatMessages(ctx, []ChatMessage{again}); err != nil || n != 0 {
		t.Fatalf("mention insert: %d %v", n, err)
	}
	got, err := st.ChatMessagesAfter(ctx, 0, 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("rows: %v %v", got, err)
	}
	if !got[0].MentionsMe || got[0].Text != "hello" || got[0].Source != "slack" || got[0].At != "2026-10-14T21:10:00.000Z" || got[1].MentionsMe {
		t.Fatalf("rows: %+v", got)
	}
	ds, _ := st.DirtyRows(ctx, 10)
	if len(ds) != 1 || ds[0].Kind != "c" || ds[0].ID != got[0].ID {
		t.Fatalf("dirty: %+v", ds)
	}
	if !got[1].IsReply() || got[0].IsReply() {
		t.Fatal("IsReply")
	}
}

func TestChatSettingsAndCursor(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveChatWatch(ctx, []ChatChannel{{ID: "C0000000001", Name: "general"}}, true, false); err != nil {
		t.Fatal(err)
	}
	set, err := st.ChatSettings(ctx)
	if err != nil || !set.WatchDMs || set.WatchMentions || !set.Watches("C0000000001") || set.Connected() {
		t.Fatalf("%+v %v", set, err)
	}
	c, _ := st.ChatCursor(ctx, "C0000000001")
	c.Oldest, c.Threads["1792012200.000100"] = "1792012300.000000", "1792012260.000200"
	if err := st.SaveChatCursor(ctx, c); err != nil {
		t.Fatal(err)
	}
	c2, _ := st.ChatCursor(ctx, "C0000000001")
	if c2.Oldest != c.Oldest || c2.Threads["1792012200.000100"] != "1792012260.000200" {
		t.Fatalf("%+v", c2)
	}
}
