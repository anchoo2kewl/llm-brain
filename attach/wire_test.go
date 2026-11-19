package main

import (
	"testing"
	"time"
)

func TestHubBase(t *testing.T) {
	ok := map[string]string{
		"http://127.0.0.1:8787":              "http://127.0.0.1:8787",
		"https://Hub.Tail1234.ts.net/x?y=1":  "https://hub.tail1234.ts.net",
		"http://100.64.0.1":                  "http://100.64.0.1",
		"http://[::1]:8787":                  "http://[::1]:8787",
		"http://[fd7a:115c:a1e0::1]:8787":    "http://[fd7a:115c:a1e0::1]:8787",
		" https://hub.tail1234.ts.net.:443 ": "https://hub.tail1234.ts.net.:443",
	}
	for in, want := range ok {
		u, err := hubBase(in)
		if err != nil || u.String() != want {
			t.Errorf("hubBase(%q) = %v, %v; want %s", in, u, err, want)
		}
	}
	for _, in := range []string{"ftp://127.0.0.1", "127.0.0.1:8787", "http://example.com", "http://100.128.0.1",
		"http://127.0.0.1.evil.com", "http://evil.com/.ts.net", "http://ts.net", "http://.ts.net", "http://localhost",
		"http://u:p@127.0.0.1", "http://[fd00::1]"} {
		if _, err := hubBase(in); err == nil {
			t.Errorf("hubBase(%q) accepted", in)
		}
	}
}

func TestURLsAndFrames(t *testing.T) {
	b, _ := hubBase("https://hub.tail1.ts.net")
	if got := socketURL(b, 42, 120, 40); got != "wss://hub.tail1.ts.net/terminals/42/ws?cols=120&rows=40" {
		t.Error(got)
	}
	b, _ = hubBase("http://127.0.0.1:8787")
	if got := socketURL(b, 7, 80, 24); got != "ws://127.0.0.1:8787/terminals/7/ws?cols=80&rows=24" {
		t.Error(got)
	}
	if got := statusURL(b, 7); got != "http://127.0.0.1:8787/terminals/7/status" {
		t.Error(got)
	}
	if got := string(resizeFrame(100, 30)); got != `{"t":"resize","cols":100,"rows":30}` {
		t.Error(got)
	}
}

func TestAfterClose(t *testing.T) {
	f, tr := false, true
	cases := []struct {
		code   int
		reason string
		st     *status
		tries  int
		msg    string
		wait   time.Duration
		ended  bool
	}{
		{1006, "", nil, 0, "The hub is unreachable. Retrying…", time.Second, false},
		{1006, "", nil, 30, "The hub is unreachable. Retrying…", 10 * time.Second, false},
		{4000, "x", &status{Status: "starting"}, 0, "Starting…", 1500 * time.Millisecond, false},
		{4000, "the terminal is ended", &status{Status: "ended"}, 0, "the terminal is ended", 0, true},
		{1000, "", &status{Status: "failed", Error: "boom"}, 0, "The terminal has failed. boom", 0, true},
		{4000, "the agent on m is offline", &status{Status: "live", HostLive: &f}, 2, "The machine is offline. Retrying…", 2 * time.Second, false},
		{4000, "detached", &status{Status: "live", HostLive: &tr}, 0, "Reconnecting…", 200 * time.Millisecond, false},
		{1000, "", &status{Status: "live"}, 3, "Reconnecting…", 3 * time.Second, false},
		{4000, "attach failed", &status{Status: "live", HostLive: &tr}, 1, "attach failed. Retrying…", time.Second, false},
	}
	for _, c := range cases {
		msg, wait, ended := afterClose(c.code, c.reason, c.st, c.tries)
		if msg != c.msg || wait != c.wait || ended != c.ended {
			t.Errorf("afterClose(%d, %q, %+v, %d) = %q, %v, %v", c.code, c.reason, c.st, c.tries, msg, wait, ended)
		}
	}
}
