package chat

import (
	"encoding/json"
	"testing"
)

func TestClean(t *testing.T) {
	names := map[string]string{"U0000000002": "teammate-a"}
	lookup := func(id string) string { return names[id] }
	cases := []struct{ name, in, want string }{
		{"plain", "hello there", "hello there"},
		{"user with label", "hi <@U0000000002|Teammate A>", "hi @Teammate A"},
		{"user looked up", "hi <@U0000000002>", "hi @teammate-a"},
		{"unknown user", "hi <@U0000000009>", "hi @U0000000009"},
		{"channel", "see <#C0000000001|general>", "see #general"},
		{"channel no label", "see <#C0000000001>", "see #C0000000001"},
		{"link with label", "read <https://example.com/a?b=1&amp;c=2|the docs>", "read the docs (https://example.com/a?b=1&c=2)"},
		{"bare link", "at <https://example.com/x>", "at https://example.com/x"},
		{"mailto", "mail <mailto:a@example.com|a@example.com>", "mail a@example.com"},
		{"broadcast", "<!here> deploy", "@here deploy"},
		{"subteam", "<!subteam^S0000000001|@oncall> ping", "@oncall ping"},
		{"html entities", "a &lt; b &amp;&amp; c &gt; d", "a < b && c > d"},
		{"escaped markup stays text", "&lt;@U0000000002&gt;", "<@U0000000002>"},
		{"sent using footer", "on it\n*Sent using* Claude", "on it"},
		{"sent using italic", "done\n\n_Sent using_ <@U0000000002|bot>", "done"},
		{"sent using mid-text kept", "*Sent using* is a footer\nmore", "*Sent using* is a footer\nmore"},
	}
	for _, c := range cases {
		if got := Clean(c.in, lookup); got != c.want {
			t.Errorf("%s: Clean(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestGroupName(t *testing.T) {
	if got := groupName("mpdm-teammate-a--teammate-b--me-1"); got != "teammate-a, teammate-b, me" {
		t.Fatalf("got %q", got)
	}
}

func TestMissingScopes(t *testing.T) {
	m := MissingScopes([]string{"channels:history", "channels:read", "users:read", "search:read", "im:history", "im:read", "mpim:history", "mpim:read"})
	if len(m) != 2 || m[0] != "groups:history" || m[1] != "groups:read" {
		t.Fatalf("got %v", m)
	}
}

func TestMessageChannelStringOrObject(t *testing.T) {
	var ms []Message
	if err := json.Unmarshal([]byte(`[{"ts":"1","channel":"G0000000001"},{"ts":"2","channel":{"id":"C0000000001","name":"general","is_channel":true}}]`), &ms); err != nil {
		t.Fatal(err)
	}
	if ms[0].Channel.ID != "G0000000001" || ms[1].Channel.Name != "general" || !ms[1].Channel.IsChannel {
		t.Fatalf("got %+v %+v", ms[0].Channel, ms[1].Channel)
	}
}
