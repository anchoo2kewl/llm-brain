package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type reviewTransport func(*http.Request) (*http.Response, error)

func (f reviewTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReviewAlertUsesPairedConnectorAndFixedMessages(t *testing.T) {
	rig := newSessRig(t)
	if err := rig.st.SetSettings(context.Background(), map[string]string{"telegram_token": "fake-secret", "telegram_chat": "10", "telegram_user": "20"}); err != nil {
		t.Fatal(err)
	}
	var sent []string
	rig.srv.ReviewHTTP = &http.Client{Transport: reviewTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.telegram.org" || r.URL.Path != "/botfake-secret/sendMessage" {
			t.Fatal("unexpected destination")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["chat_id"] != "10" {
			t.Fatal("did not use paired chat")
		}
		sent = append(sent, body["text"])
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	})}
	for _, active := range []string{"true", "false"} {
		req := httptest.NewRequest("POST", "/api/reviews/alert", strings.NewReader(`{"incident":"worker_stuck","counts":{"eligible":3,"completed":1,"missing":2},"active":`+active+`}`))
		req.RemoteAddr = "127.0.0.1:1234"
		w := httptest.NewRecorder()
		rig.srv.Handler().ServeHTTP(w, req)
		if w.Code != 200 || strings.Contains(w.Body.String(), "fake-secret") {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	if len(sent) != 2 || !strings.Contains(sent[0], "alert") || !strings.Contains(sent[1], "recovered") || !strings.Contains(sent[0], "completed=1") {
		t.Fatalf("messages: %v", sent)
	}
}

func TestReviewAlertRejectsUnpairedForeignAndArbitraryInput(t *testing.T) {
	rig := newSessRig(t)
	cases := []struct {
		body, remote, origin string
		code                 int
	}{
		{`{"incident":"worker_stuck","active":true}`, "127.0.0.1:1", "", 409},
		{`{"incident":"arbitrary message","active":true}`, "127.0.0.1:1", "", 400},
		{`{"incident":"worker_stuck","active":true,"token":"x"}`, "127.0.0.1:1", "", 400},
		{`{"incident":"worker_stuck"}`, "127.0.0.1:1", "", 400},
		{`{"incident":"worker_stuck","active":true} {}`, "127.0.0.1:1", "", 400},
		{`{"incident":"worker_stuck","active":true}`, "203.0.113.1:1", "", 403},
		{`{"incident":"worker_stuck","active":true}`, "127.0.0.1:1", "https://foreign.example", 403},
	}
	for _, test := range cases {
		req := httptest.NewRequest("POST", "/api/reviews/alert", strings.NewReader(test.body))
		req.RemoteAddr = test.remote
		if test.origin != "" {
			req.Header.Set("Origin", test.origin)
		}
		w := httptest.NewRecorder()
		rig.srv.Handler().ServeHTTP(w, req)
		if w.Code != test.code {
			t.Fatalf("%d want%d: %s", w.Code, test.code, w.Body.String())
		}
	}
}

func TestReviewAlertDeliveryErrorIsNotReportedAsSuccess(t *testing.T) {
	rig := newSessRig(t)
	if err := rig.st.SetSettings(context.Background(), map[string]string{"telegram_token": "fake-secret", "telegram_chat": "10", "telegram_user": "20"}); err != nil {
		t.Fatal(err)
	}
	rig.srv.ReviewHTTP = &http.Client{Transport: reviewTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(`{"ok":false}`))}, nil
	})}
	req := httptest.NewRequest("POST", "/api/reviews/alert", strings.NewReader(`{"incident":"discovery_failed","active":true}`))
	req.RemoteAddr = "127.0.0.1:1"
	w := httptest.NewRecorder()
	rig.srv.Handler().ServeHTTP(w, req)
	if w.Code != 502 || strings.Contains(w.Body.String(), "fake-secret") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
