package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// One event stream for every page (GET /api/events, server-sent events): the inbox's counts and what's
// running, sent when they change. Pages use it for the badges and to re-render a live section only when
// something moved, instead of each polling on its own clock. It also drives push notifications (push.go):
// an inbox item that wasn't there before is announced once.

// Snapshot is what the stream sends.
type Snapshot struct {
	Inbox  int            `json:"inbox"`
	Counts map[string]int `json:"counts"`
	Hash   string         `json:"hash"`
	Now    nowCount       `json:"now"`
}

// Events fans the snapshot out to every open page.
type Events struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
	last []byte
	kick chan struct{}
}

func newEvents() *Events {
	return &Events{subs: map[chan []byte]struct{}{}, kick: make(chan struct{}, 1)}
}

// Kick asks for a fresh snapshot now (after something the person did).
func (e *Events) Kick() {
	if e == nil {
		return
	}
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

func (e *Events) subscribe() (chan []byte, []byte) {
	ch := make(chan []byte, 4)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.subs[ch] = struct{}{}
	return ch, e.last
}

func (e *Events) unsubscribe(ch chan []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.subs, ch)
}

// publish sends b to every subscriber when it differs from the last; a subscriber that's behind skips it.
func (e *Events) publish(b []byte) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if bytes.Equal(b, e.last) {
		return false
	}
	e.last = b
	for ch := range e.subs {
		select {
		case ch <- b:
		default:
		}
	}
	return true
}

// RunEvents builds the snapshot every `every` (and on Kick) until ctx ends.
func (s *Server) RunEvents(ctx context.Context, every time.Duration) {
	var prev map[string]bool // inbox keys last time; nil until the first snapshot, which is never announced
	for {
		in, err := s.buildInbox(ctx)
		if err != nil && ctx.Err() == nil {
			s.Log.Printf("events: %v", err)
		}
		now, _ := s.nowCounts(ctx)
		snap := Snapshot{Inbox: in.Total, Counts: in.Counts, Hash: in.Hash, Now: now}
		b, _ := json.Marshal(snap)
		if s.Events.publish(b) || prev == nil {
			cur := map[string]bool{}
			var fresh []Item
			for _, it := range in.Items {
				k := it.Key + "\x00" + it.At
				cur[k] = true
				if prev != nil && !prev[k] {
					fresh = append(fresh, it)
				}
			}
			prev = cur
			if len(fresh) > 0 && s.Push != nil {
				s.Push.Announce(ctx, fresh)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		case <-s.Events.kick:
		}
	}
}

// eventsSSE: GET /api/events.
func (s *Server) eventsSSE(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	ch, last := s.Events.subscribe()
	defer s.Events.unsubscribe(ch)
	fmt.Fprint(w, "retry: 5000\n\n")
	if last != nil {
		fmt.Fprintf(w, "data: %s\n\n", last)
	}
	fl.Flush()
	beat := time.NewTicker(25 * time.Second)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		case <-beat.C:
			fmt.Fprint(w, ": beat\n\n")
			fl.Flush()
		}
	}
}
