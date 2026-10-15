package web

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// The installable app: a web manifest, a service worker (offline shell, push), and the settings that shape
// the app as a whole (home page, theme, notifications).

const keyHomePage = "home_page" // "inbox" (default) | "fleet"

func (s *Server) appRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /manifest.webmanifest", s.manifest)
	mux.HandleFunc("GET /sw.js", s.serviceWorker)
	mux.HandleFunc("GET /offline", func(w http.ResponseWriter, r *http.Request) {
		s.render(w, "offline", "layout", page{Title: "Offline", Nav: "inbox"})
	})
	mux.HandleFunc("GET /settings", s.settingsPage)
	mux.HandleFunc("POST /settings/app", s.tailnetOnly(s.sameSite(s.settingsSave)))
}

// home is the inbox, unless the person picked the fleet overview.
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if v, _ := s.Store.Setting(r.Context(), keyHomePage); v == "fleet" {
		s.overview(w, r)
		return
	}
	s.inboxPage(w, r)
}

func (s *Server) manifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-cache")
	writeRaw(w, `{
  "name": "llm-brain hub",
  "short_name": "llm-brain",
  "description": "Watch, steer and talk to every agent you run.",
  "id": "/",
  "start_url": "/",
  "scope": "/",
  "display": "standalone",
  "display_override": ["window-controls-overlay", "standalone"],
  "orientation": "any",
  "background_color": "#0f1522",
  "theme_color": "#4f46e5",
  "categories": ["developer", "productivity"],
  "icons": [
    {"src": "/static/icons/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
    {"src": "/static/icons/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
    {"src": "/static/icons/maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
    {"src": "/static/logo.svg", "sizes": "any", "type": "image/svg+xml", "purpose": "any"}
  ],
  "shortcuts": [
    {"name": "Inbox", "url": "/inbox", "icons": [{"src": "/static/icons/icon-192.png", "sizes": "192x192"}]},
    {"name": "Now", "url": "/now", "icons": [{"src": "/static/icons/icon-192.png", "sizes": "192x192"}]},
    {"name": "New terminal", "url": "/terminals/new", "icons": [{"src": "/static/icons/icon-192.png", "sizes": "192x192"}]},
    {"name": "Workspace", "url": "/workspace", "icons": [{"src": "/static/icons/icon-192.png", "sizes": "192x192"}]}
  ]
}
`)
}

// serviceWorker serves static/sw.js from the root, so its scope is the whole app.
func (s *Server) serviceWorker(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(staticFS, "static/sw.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Service-Worker-Allowed", "/")
	_, _ = w.Write(b)
}

func writeRaw(w http.ResponseWriter, s string) { _, _ = w.Write([]byte(s)) }

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	home, _ := s.Store.Setting(ctx, keyHomePage)
	if home == "" {
		home = "inbox"
	}
	slack, _ := s.Store.ChatSettings(ctx)
	var subs []pushRow
	if s.Push != nil {
		subs, _ = s.Push.subs(ctx)
	}
	upd := ""
	if v, _ := s.Store.Setting(ctx, store.KeyUpdateAuto); v == "1" {
		upd = "on"
	}
	s.render(w, "settings", "layout", struct {
		page
		Home      string
		Slack     store.ChatSettings
		PushOn    bool
		Subs      []pushRow
		AutoUpd   string
		Saved     bool
		HTTPS     bool
		HasAgents bool
	}{page{Title: "Settings", Nav: "settings"}, home, slack, s.Push != nil, subs, upd, r.URL.Query().Get("saved") == "1",
		r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"), s.Agents != nil})
}

func (s *Server) settingsSave(w http.ResponseWriter, r *http.Request) {
	home := r.FormValue("home")
	if home != "fleet" {
		home = "inbox"
	}
	if err := s.Store.SetSettings(r.Context(), map[string]string{keyHomePage: home}); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}
