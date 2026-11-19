// brain-attach connects the terminal it runs in to a hub terminal: the same websocket the apps use
// (`/terminals/{id}/ws`). The Mac app runs it inside each Ghostty pane; it also works from any terminal
// (Ghostty, iTerm, Terminal, Linux terminals, Windows Terminal).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/term"
)

func main() {
	hub := flag.String("hub", "", "hub URL, for example https://hub.<tailnet>.ts.net or http://127.0.0.1:8787")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: brain-attach --hub <url> <terminal-id>")
		flag.PrintDefaults()
	}
	flag.Parse()
	id, err := strconv.ParseInt(flag.Arg(0), 10, 64)
	if *hub == "" || flag.NArg() != 1 || err != nil || id <= 0 {
		flag.Usage()
		os.Exit(2)
	}
	base, err := hubBase(*hub)
	if err != nil {
		fmt.Fprintln(os.Stderr, "brain-attach:", err)
		os.Exit(2)
	}
	os.Exit(run(base, id))
}

// The hub is on loopback or the tailnet: no proxy, no redirects to another host.
var client = &http.Client{
	Transport:     &http.Transport{Proxy: nil},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// run attaches until the terminal ends (0) or the first connection fails (1). Later drops reconnect: the hub
// terminal keeps running, and tmux redraws the screen on reattach.
func run(base *url.URL, id int64) int {
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		if old, err := term.MakeRaw(fd); err == nil {
			defer term.Restore(fd, old)
		}
	}
	prepareConsole()

	input := make(chan []byte, 64)
	go func() {
		for {
			buf := make([]byte, 32<<10)
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				input <- buf[:n]
			}
			if err != nil {
				close(input)
				return
			}
		}
	}()
	resized := watchResize()

	tries, connected, shown := 0, false, ""
	for {
		cols, rows := size()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		c, _, err := websocket.Dial(ctx, socketURL(base, id, cols, rows), &websocket.DialOptions{HTTPClient: client})
		cancel()
		code, reason := 0, ""
		if err != nil {
			if !connected {
				note(fmt.Sprintf("cannot connect to the hub: %v", err))
				return 1
			}
		} else {
			connected = true
			var got, quit bool
			code, reason, got, quit = session(c, input, resized)
			if quit {
				return 0
			}
			if got {
				tries, shown = 0, ""
			}
		}
		msg, wait, ended := afterClose(code, reason, terminalStatus(base, id), tries)
		if msg != shown { // once per change, not on every retry
			note(msg)
			shown = msg
		}
		if ended {
			return 0
		}
		tries++
		// Keys typed while offline are dropped: they were meant for a screen that is gone.
		for t := time.After(wait); ; {
			select {
			case _, ok := <-input:
				if !ok {
					return 0
				}
				continue
			case <-t:
			}
			break
		}
	}
}

// session copies frames until the socket closes. got: a frame came (the attach worked). quit: local input ended.
func session(c *websocket.Conn, input <-chan []byte, resized <-chan struct{}) (code int, reason string, got, quit bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer c.CloseNow()
	c.SetReadLimit(32 << 20)
	var seen atomic.Bool
	done := make(chan error, 1)
	go func() {
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				done <- err
				return
			}
			seen.Store(true)
			if typ == websocket.MessageBinary { // text frames are control: ignored
				os.Stdout.Write(data)
			}
		}
	}()
	// A dropped network (or a Mac that slept) often leaves the socket open with nothing on it.
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case b, ok := <-input:
			if !ok {
				c.Close(websocket.StatusNormalClosure, "")
				return 0, "", seen.Load(), true
			}
			_ = c.Write(ctx, websocket.MessageBinary, b)
		case <-resized:
			cols, rows := size()
			_ = c.Write(ctx, websocket.MessageText, resizeFrame(cols, rows))
		case <-ping.C:
			go func() {
				pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
				defer pcancel()
				if c.Ping(pctx) != nil {
					c.CloseNow()
				}
			}()
		case err := <-done:
			var ce websocket.CloseError
			if errors.As(err, &ce) {
				return int(ce.Code), ce.Reason, seen.Load(), false
			}
			return 0, "", seen.Load(), false
		}
	}
}

func terminalStatus(base *url.URL, id int64) *status {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", statusURL(base, id), nil)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var st status
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&st) != nil {
		return nil
	}
	return &st
}

func size() (int, int) {
	if c, r, err := term.GetSize(int(os.Stdout.Fd())); err == nil && c > 1 && r > 1 {
		return c, r
	}
	return 120, 40
}

// note prints a status line between the remote screen's output; the terminal is in raw mode, hence \r\n.
func note(msg string) { fmt.Fprintf(os.Stderr, "\r\n[brain-attach] %s\r\n", msg) }
