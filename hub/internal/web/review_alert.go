package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Reuse the existing paired Telegram connector's private settings. The endpoint
// accepts only fixed health conditions; it cannot send arbitrary text or reveal
// credentials. The watchdog persists transition receipts and retries failures.
var reviewAlertMu sync.Mutex

var reviewConditions = map[string]string{
	"scheduler_absent":    "review scheduler is not loaded",
	"scheduler_stale":     "review scheduler heartbeat is missing or stale",
	"discovery_failed":    "review discovery failed; candidate counts may be stale",
	"eligible_missing":    "eligible reviews are missing from processing",
	"worker_stuck":        "a review worker stopped making progress",
	"review_failed":       "a review failed or its worker disappeared",
	"verification_failed": "review completion or current-head verification failed",
}

func (s *Server) reviewAlert(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Counts   map[string]int `json:"counts"`
		Incident string         `json:"incident"`
		Active   *bool          `json:"active"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(&struct{}{}) != io.EOF || in.Active == nil || reviewConditions[in.Incident] == "" {
		http.Error(w, "invalid review health event", http.StatusBadRequest)
		return
	}
	for key, value := range in.Counts {
		switch key {
		case "discovered", "eligible", "queued", "running", "completed", "failed", "blocked", "missing":
		default:
			http.Error(w, "invalid review count", 400)
			return
		}
		if value < 0 || value > 100000000 {
			http.Error(w, "invalid review count", 400)
			return
		}
	}
	reviewAlertMu.Lock()
	defer reviewAlertMu.Unlock()
	// Check storage errors rather than interpreting an unavailable store as an
	// unpaired connector. No caller supplies a token, destination or API URL.
	values := map[string]string{}
	for _, key := range []string{"telegram_token", "telegram_chat", "telegram_user"} {
		value, err := s.Store.Setting(r.Context(), key)
		if err != nil {
			http.Error(w, "review notification settings unavailable", 503)
			return
		}
		values[key] = value
	}
	if values["telegram_token"] == "" || values["telegram_chat"] == "" || values["telegram_user"] == "" {
		http.Error(w, "pair the existing Telegram connector first", 409)
		return
	}
	text := "Review health alert: " + reviewConditions[in.Incident] + ". Check fleet review-status on the review host."
	if !*in.Active {
		text = "Review health recovered: " + reviewConditions[in.Incident] + "."
	}
	if in.Counts != nil {
		parts := []string{}
		for _, key := range []string{"discovered", "eligible", "queued", "running", "completed", "failed", "blocked", "missing"} {
			parts = append(parts, fmt.Sprintf("%s=%d", key, in.Counts[key]))
		}
		text += "\nLatest metrics: " + strings.Join(parts, ", ")
	}
	body, _ := json.Marshal(map[string]string{"chat_id": values["telegram_chat"], "text": text})
	request, err := http.NewRequestWithContext(r.Context(), "POST", "https://api.telegram.org/bot"+values["telegram_token"]+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		http.Error(w, "invalid Telegram configuration", 503)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	client := s.ReviewHTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		http.Error(w, "Telegram delivery failed", 502)
		return
	}
	defer response.Body.Close()
	var out struct {
		OK bool `json:"ok"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&out) != nil || !out.OK {
		http.Error(w, "Telegram delivery failed", 502)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"delivered": true})
}
