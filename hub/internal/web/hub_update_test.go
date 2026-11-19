package web

import (
	"context"
	"encoding/json"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHubUpdateAPI(t *testing.T) {
	r := newSessRig(t)
	r.srv.HubUpdateVersion = "0.1.0"
	helper := filepath.Join(t.TempDir(), "updater")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s' '{\"state\":\"up_to_date\",\"auto_enabled\":true}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r.srv.HubUpdateCommand = helper
	code, body, _ := r.get(t, "/api/hub-update")
	if code != 200 || !strings.Contains(body, "auto_enabled") {
		t.Fatalf("status %d %s", code, body)
	}
	code, body, _ = r.get(t, "/api/hub-update/health")
	var health map[string]any
	_ = json.Unmarshal([]byte(body), &health)
	if code != 200 || health["version"] != "0.1.0" {
		t.Fatalf("health %d %s", code, body)
	}
	for _, tc := range []struct {
		body string
		code int
	}{
		{"{\"action\":\"check\"}", 200},
		{"{\"action\":\"install\"}", 200},
		{"{\"action\":\"settings\",\"auto_enabled\":false}", 200},
		{"{\"action\":\"settings\",\"interval_hours\":0}", 400},
		{"{\"action\":\"shell\"}", 400},
		{"{\"action\":\"settings\"}", 400},
		{"{\"action\":\"install\",\"auto_enabled\":true}", 400},
	} {
		resp, err := r.cl.Post(r.ts.URL+"/api/hub-update", "application/json", strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.code {
			t.Fatalf("%s: %d", tc.body, resp.StatusCode)
		}
	}
	if ready, _ := r.srv.hubRestartReady(context.Background()); !ready {
		t.Fatal("idle test hub not ready")
	}
	id, err := r.st.QueueCommand(context.Background(), store.Command{Host: "laptop", Kind: "turn"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.DB.Exec("UPDATE commands SET status='running' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if ready, _ := r.srv.hubRestartReady(context.Background()); ready {
		t.Fatal("active command allowed restart")
	}
	if _, err := r.st.DB.Exec("UPDATE commands SET kind='term_attach' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if ready, _ := r.srv.hubRestartReady(context.Background()); !ready {
		t.Fatal("tmux attachment unnecessarily blocked update")
	}
}
