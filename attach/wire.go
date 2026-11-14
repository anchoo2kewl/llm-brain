package main

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// The terminal websocket protocol (hub/internal/web/terminals.go): binary frames are terminal bytes both ways, text
// frames are JSON control, and close code 4000 carries a reason to show. The same rules as app-mac's Wire.swift.

const reasonCode = 4000

// status is `GET /terminals/{id}/status`.
type status struct {
	Status   string `json:"status"`
	Error    string `json:"error"`
	HostLive *bool  `json:"host_live"`
}

func resizeFrame(cols, rows int) []byte {
	return []byte(fmt.Sprintf(`{"t":"resize","cols":%d,"rows":%d}`, cols, rows))
}

// hostAllowed is the client allow-list (app-mac HubURL.swift, app/src-tauri/src/allow.rs): loopback, Tailscale
// CGNAT 100.64.0.0/10, Tailscale IPv6 fd7a:115c:a1e0::/48 and *.ts.net names.
func hostAllowed(host string) bool {
	host = strings.ToLower(strings.Trim(host, "[]"))
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4[0] == 127 || (v4[0] == 100 && v4[1]&0xC0 == 64)
		}
		_, tail, _ := net.ParseCIDR("fd7a:115c:a1e0::/48")
		return ip.IsLoopback() || tail.Contains(ip)
	}
	host = strings.TrimRight(host, ".")
	for _, c := range host {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
			return false
		}
	}
	return len(host) > len(".ts.net") && strings.HasSuffix(host, ".ts.net") && !strings.HasPrefix(host, ".") &&
		!strings.Contains(host, "..")
}

// hubBase checks a hub URL and returns its origin, for example http://127.0.0.1:8787.
func hubBase(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%q is not an http or https hub URL", raw)
	}
	if u.User != nil {
		return nil, fmt.Errorf("a hub URL must not contain a user name or password")
	}
	if !hostAllowed(u.Hostname()) {
		return nil, fmt.Errorf("host %s is not allowed: the hub must stay on loopback or the tailnet "+
			"(127.0.0.1, a 100.64.0.0/10 address or a *.ts.net name)", u.Hostname())
	}
	return &url.URL{Scheme: u.Scheme, Host: strings.ToLower(u.Host)}, nil
}

func socketURL(base *url.URL, id int64, cols, rows int) string {
	u := *base
	u.Scheme = map[string]string{"http": "ws", "https": "wss"}[base.Scheme]
	u.Path = fmt.Sprintf("/terminals/%d/ws", id)
	u.RawQuery = fmt.Sprintf("cols=%d&rows=%d", cols, rows)
	return u.String()
}

func statusURL(base *url.URL, id int64) string {
	u := *base
	u.Path = fmt.Sprintf("/terminals/%d/status", id)
	return u.String()
}

// afterClose decides what to do after the socket closed, from the close reason and the terminal's status (nil when
// the hub did not answer). ended is true when the terminal is gone; otherwise wait and reconnect.
func afterClose(code int, reason string, st *status, tries int) (msg string, wait time.Duration, ended bool) {
	why := ""
	if code == reasonCode {
		why = reason
	}
	backoff := time.Duration(min(max(tries, 1), 10)) * time.Second
	switch {
	case st == nil:
		return "The hub is unreachable. Retrying…", backoff, false
	case st.Status == "starting":
		return "Starting…", 1500 * time.Millisecond, false
	case st.Status != "live":
		msg = why
		if msg == "" {
			msg = "The terminal has " + st.Status + "."
		}
		if st.Error != "" {
			msg += " " + st.Error
		}
		return msg, 0, true
	case st.HostLive != nil && !*st.HostLive:
		return "The machine is offline. Retrying…", backoff, false
	case why == "" || why == "detached":
		if tries == 0 {
			backoff = 200 * time.Millisecond
		}
		return "Reconnecting…", backoff, false
	}
	return why + ". Retrying…", backoff, false
}
