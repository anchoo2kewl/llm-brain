package web

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/agent"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// The inbox: one list of what needs you, from every source the hub has. An agent that finished its turn and
// waits for an answer, a task that failed, an issue blocked on a person, a fleet PR that needs a review, a
// Slack message nobody answered, an update that broke. Opening an item (or dismissing it) marks it seen; it
// comes back when something newer happens there.

// Item is one thing that needs you.
type Item struct {
	Key    string // for seen/dismiss: s:<session> a:<task> t:<terminal> i:<issue url> p:<pr url> c:<conv> r:<run> u:<host> k:<ask>
	State  string // waiting | blocked | failed | review
	Kind   string // terminal | task | session | issue | pr | chat | run | update | ask
	Title  string
	Detail string // the last thing said, a reason
	Where  string // host · folder, a channel
	Link   string
	At     string // when it happened (and what seen is compared with)
	Ext    bool   // a link off the hub (GitHub, Slack)
	// How to answer from the inbox: a task or session takes a message, a terminal takes text or keys.
	ReplyTo  string // POST target for a prompt (tasks, sessions with a live agent)
	TermID   int64  // a terminal: quick replies go to /api/terminals/{id}/send
	Provider string
	Close    string // POST here to end its process (a terminal, a session)
}

// Inbox is the whole list plus counts by state, for the badge and the stream.
type Inbox struct {
	Items  []Item
	Counts map[string]int
	Total  int
	Hash   string // changes when the list does
}

// inboxWindow: how far back sessions, chat and failures count.
const inboxWindow = 36 * time.Hour

// quietFor: a session's last answer counts once its file has been quiet this long (it isn't mid-turn).
const quietFor = 15 * time.Second

// sessionWindow: a conversation outside the hub that ended on the agent's answer is waiting for you for this
// long; after that it was probably finished, not forgotten. An open terminal's session counts for the whole
// inbox window.
const sessionWindow = 6 * time.Hour

func (s *Server) buildInbox(ctx context.Context) (Inbox, error) {
	var items []Item
	var errs []error
	add := func(it Item) { items = append(items, it) }
	seen, err := s.Store.Seen(ctx)
	if err != nil {
		return Inbox{}, err
	}

	// Hub tasks: an answer is ready, or the turn failed.
	tasks, err := s.Store.AgentSessions(ctx, 200)
	errs = append(errs, err)
	taskOf := map[string]bool{} // host|provider session: the transcript is shown as the task
	cutoff := store.T(time.Now().Add(-inboxWindow))
	for _, a := range tasks {
		if a.ProviderSession != "" {
			taskOf[a.Host+"|"+a.ProviderSession] = true
		}
		if a.UpdatedAt < cutoff || a.Turns == 0 {
			continue
		}
		it := Item{Key: "a:" + itoa(a.ID), Kind: "task", Title: orUntitled(a.Title), Where: a.Host + " · " + folderName(a.Cwd),
			Link: "/agents/" + itoa(a.ID), At: a.UpdatedAt, Provider: a.Provider}
		switch a.Status {
		case "idle":
			it.State, it.Detail, it.ReplyTo = "waiting", firstLine(a.LastResult, 280), "/agents/"+itoa(a.ID)+"/send"
		case "failed":
			it.State, it.Detail = "failed", firstLine(a.Error, 280)
		default:
			continue
		}
		add(it)
	}

	// Terminals and sessions (hub or not) whose last word is the agent's: it's waiting for you.
	terms, err := s.Store.Terminals(ctx, "", 200)
	errs = append(errs, err)
	termBy := map[string]store.Terminal{}
	for _, t := range terms {
		if t.Open() && t.SessionUUID != "" {
			termBy[t.Host+"|"+t.SessionUUID] = t
		}
	}
	turns, err := s.Store.RecentLastTurns(ctx, inboxWindow, 150)
	errs = append(errs, err)
	quiet := store.T(time.Now().Add(-quietFor))
	recent := store.T(time.Now().Add(-sessionWindow))
	for _, x := range turns {
		if strings.HasPrefix(x.AgentName, "fleet-") || x.RunID > 0 || taskOf[x.Host+"|"+x.UUID] || headless(x.Originator) {
			continue
		}
		if x.Role != "assistant" || x.Kind != "text" || x.FileMtime > quiet {
			continue
		}
		it := Item{State: "waiting", Kind: "session", Key: "s:" + itoa(x.ID), Title: x.DisplayTitle(), Detail: firstLine(x.Text, 280),
			Where: x.Host + " · " + folderName(x.Cwd), Link: "/sessions/" + itoa(x.ID), At: x.FileMtime, Provider: x.Provider}
		if t, ok := termBy[x.Host+"|"+x.UUID]; ok {
			it.Kind, it.Key, it.Link, it.TermID = "terminal", "t:"+itoa(t.ID), "/terminals/"+itoa(t.ID), t.ID
			it.Close = "/terminals/" + itoa(t.ID) + "/kill"
			if t.Title != "" {
				it.Title = t.Title
			}
		} else if x.FileMtime < recent {
			continue
		} else if s.Agents != nil && x.Live {
			it.ReplyTo = "/sessions/" + itoa(x.ID) + "/send"
		}
		if it.TermID == 0 && closable(x.Session) {
			it.Close = "/sessions/" + itoa(x.ID) + "/close"
		} else if it.TermID == 0 && x.Closed() {
			continue // closed from the hub: nothing is waiting any more
		}
		add(it)
	}

	// The fleet: issues blocked on a person, fleet PRs waiting for a review, asks of you from team chat.
	if st, err := readStatus(filepath.Join(s.StateDir, "status.json")); err == nil {
		for _, is := range st.Owned {
			for _, l := range is.Labels {
				if strings.Contains(l.Name, "blocked") {
					add(Item{Key: "i:" + is.URL, State: "blocked", Kind: "issue", Title: "#" + itoa(int64(is.Number)) + " " + is.Title,
						Detail: "labelled " + l.Name, Where: who(is.Author.Login), Link: is.URL, At: is.UpdatedAt, Ext: true})
					break
				}
			}
		}
		for _, p := range st.PRs {
			if p.ReviewDecision == "APPROVED" || strings.EqualFold(p.CI, "failing") {
				continue
			}
			add(Item{Key: "p:" + p.URL, State: "review", Kind: "pr", Title: p.Title, Detail: prDetail(p), Where: short(p.URL),
				Link: p.URL, At: p.UpdatedAt, Ext: true})
		}
		if st.Chat != nil {
			for _, ask := range st.Chat.Asks {
				add(Item{Key: "k:" + hash(ask), State: "waiting", Kind: "ask", Title: ask, Detail: "from team chat", Where: "Slack digest",
					Link: "/#chat", At: st.Chat.At})
			}
		}
	}

	// Slack: DMs and mentions with no answer from you.
	if set, err := s.Store.ChatSettings(ctx); err == nil && set.UserID != "" {
		ms, err := s.Store.ChatAwaiting(ctx, time.Now().Add(-inboxWindow), set.UserID, 30)
		errs = append(errs, err)
		for _, m := range ms {
			where := "#" + m.ConvName
			if m.ConvKind == "im" || m.ConvKind == "mpim" {
				where = "direct message"
			}
			link := "/chat?src=" + m.ConvID + "#c" + itoa(m.ID)
			add(Item{Key: "c:" + m.ConvID, State: "waiting", Kind: "chat", Title: orUnknown(m.UserName), Detail: firstLine(m.Text, 280),
				Where: where, Link: link, At: m.At})
		}
	}

	// Fleet runs that failed.
	runs, err := s.Store.Runs(ctx, store.RunFilter{Status: "failed", Limit: 20})
	errs = append(errs, err)
	for _, r := range runs {
		if r.StartedAt < cutoff {
			continue
		}
		add(Item{Key: "r:" + itoa(r.ID), State: "failed", Kind: "run", Title: r.Key, Detail: firstLine(r.Summary, 200),
			Where: r.Host, Link: "/runs/" + itoa(r.ID), At: firstNonEmpty(r.EndedAt, r.StartedAt)})
	}

	// Tool updates that failed (the newest per host).
	ups, err := s.Store.ToolUpdates(ctx, 20)
	errs = append(errs, err)
	doneHost := map[string]bool{}
	for _, c := range ups {
		if doneHost[c.Host] || !store.FinalStatus(c.Status) {
			continue
		}
		doneHost[c.Host] = true
		if c.Status != "failed" || c.FinishedAt < cutoff {
			continue
		}
		_, e := agent.ParseUpdateResult(c.Result)
		add(Item{Key: "u:" + c.Host, State: "failed", Kind: "update", Title: "Update failed on " + c.Host, Detail: e,
			Where: c.Host, Link: "/settings/updates", At: c.FinishedAt})
	}

	// Drop what was seen since it happened, then newest first within each state.
	keep := items[:0]
	for _, it := range items {
		if at, ok := seen[it.Key]; ok && !parseAny(at).Before(parseAny(it.At)) {
			continue
		}
		keep = append(keep, it)
	}
	items = keep
	rank := map[string]int{"waiting": 0, "review": 1, "blocked": 2, "failed": 3}
	sort.SliceStable(items, func(i, j int) bool {
		if rank[items[i].State] != rank[items[j].State] {
			return rank[items[i].State] < rank[items[j].State]
		}
		return parseAny(items[i].At).After(parseAny(items[j].At))
	})
	in := Inbox{Items: items, Counts: map[string]int{}, Total: len(items)}
	h := sha1.New()
	for _, it := range items {
		in.Counts[it.State]++
		h.Write([]byte(it.Key + "\x00" + it.At + "\x00"))
	}
	in.Hash = hex.EncodeToString(h.Sum(nil))[:12]
	// A missing source (no Slack, no status.json) is normal; a database error isn't.
	return in, errors.Join(errs...)
}

// headless: a one-shot run (claude -p, codex exec, an SDK script). Nobody waits on its last answer.
func headless(originator string) bool { return agent.HeadlessOriginator(originator) }

func prDetail(p PR) string {
	var parts []string
	if p.CI != "" {
		parts = append(parts, "CI "+strings.ToLower(p.CI))
	}
	switch p.ReviewDecision {
	case "CHANGES_REQUESTED":
		parts = append(parts, "changes requested")
	case "REVIEW_REQUIRED", "":
		parts = append(parts, "needs a review")
	}
	return strings.Join(parts, " · ")
}

func hash(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:6])
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func orUntitled(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(untitled)"
	}
	return s
}

func orUnknown(s string) string {
	if s == "" {
		return "someone"
	}
	return s
}

func firstNonEmpty(a ...string) string {
	for _, x := range a {
		if x != "" {
			return x
		}
	}
	return ""
}

// folderName is the last part of a folder, or "scratch".
func folderName(p string) string {
	if p == "" {
		return "scratch"
	}
	return filepath.Base(p)
}

// firstLine is the start of s up to n characters, on one line.
func firstLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		cut := n
		for cut > 0 && (s[cut]&0xC0) == 0x80 {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

// ---------------------------------------------------------------- pages

func (s *Server) inboxRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /inbox", s.inboxPage)
	mux.HandleFunc("GET /api/inbox", s.tailnetOnly(s.inboxAPI))
	mux.HandleFunc("POST /inbox/seen", s.tailnetOnly(s.inboxSeen))
}

func (s *Server) inboxPage(w http.ResponseWriter, r *http.Request) {
	in, err := s.buildInbox(r.Context())
	if err != nil {
		s.Log.Printf("inbox: %v", err) // show what could be built
	}
	filter := r.URL.Query().Get("state")
	all := in.Items
	if filter != "" {
		var f []Item
		for _, it := range in.Items {
			if it.State == filter {
				f = append(f, it)
			}
		}
		in.Items = f
	}
	running, _ := s.nowCounts(r.Context())
	tmpl := "layout"
	if r.URL.Query().Get("fragment") == "1" {
		tmpl = "content"
	}
	s.render(w, "inbox", tmpl, struct {
		page
		In      Inbox
		All     int
		Filter  string
		Running nowCount
		Replies []string
	}{page{Title: "Inbox", Nav: "inbox"}, in, len(all), filter, running, quickReplies})
}

// quickReplies are one-tap answers for an agent that's waiting.
var quickReplies = []string{"yes", "continue", "1", "2", "no"}

func (s *Server) inboxAPI(w http.ResponseWriter, r *http.Request) {
	in, err := s.buildInbox(r.Context())
	if err != nil && in.Counts == nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

// inboxSeen: POST key (and optionally at): mark an item seen. Dismiss uses it; so does opening an item.
func (s *Server) inboxSeen(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	key := r.FormValue("key")
	if key == "" || len(key) > 600 {
		http.Error(w, "bad key", http.StatusBadRequest)
		return
	}
	at := r.FormValue("at")
	if at == "" || parseAny(at).IsZero() {
		at = store.Now()
	}
	if err := s.Store.MarkSeen(r.Context(), key, store.T(parseAny(at))); err != nil {
		s.fail(w, err)
		return
	}
	s.Events.Kick()
	if r.FormValue("format") == "json" {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/inbox"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// markSeen notes that a page showing key was opened (best effort).
func (s *Server) markSeen(ctx context.Context, key string) {
	if err := s.Store.MarkSeen(ctx, key, store.Now()); err != nil {
		s.Log.Printf("inbox seen %s: %v", key, err)
		return
	}
	s.Events.Kick()
}
