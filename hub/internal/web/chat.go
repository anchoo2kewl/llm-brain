package web

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/chat"
	"github.com/anchoo2kewl/llm-brain/hub/internal/search"
	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// The chat pages and the chat API. The Slack token is in the store for the poller only: nothing here puts
// it in a page, a log line or a response. Pages get an account view without it.

// chatAccount is what pages and /api/chat/me may show about the connection.
type chatAccount struct {
	Connected bool   `json:"connected"`
	UserID    string `json:"user_id"`
	UserName  string `json:"user_name"`
	Team      string `json:"team"`
}

func account(set store.ChatSettings) chatAccount {
	return chatAccount{Connected: set.Connected(), UserID: set.UserID, UserName: set.UserName, Team: set.Team}
}

type chatSource struct {
	Key, Name string
	Count     int
	On        bool
}

// chatThread is a thread parent with its replies, or a message on its own. Parent is nil when the parent
// is older than what the page loaded.
type chatThread struct {
	Parent  *chatItem
	Replies []chatItem
}

type chatItem struct {
	store.ChatMessage
	Where string // #channel / DM name, on mixed views
}

const chatPageSize = 300

func (s *Server) chatPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	src := q.Get("src")
	if src == "" {
		src = store.ChatAll
	}
	until := q.Get("until")
	set, err := s.Store.ChatSettings(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	counts, err := s.Store.ChatCounts(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		s.fail(w, err)
		return
	}
	sources := []chatSource{{Key: store.ChatAll, Name: "Everything", Count: counts.Total}}
	for _, ch := range set.Channels {
		sources = append(sources, chatSource{Key: ch.ID, Name: "#" + ch.Name, Count: counts.ByConv[ch.ID]})
	}
	sources = append(sources, chatSource{Key: store.ChatDMs, Name: "Direct messages", Count: counts.DMs},
		chatSource{Key: store.ChatMentions, Name: "Mentions of me", Count: counts.Mentions})
	title := "Chat"
	for i := range sources {
		if sources[i].Key == src {
			sources[i].On = true
			title = sources[i].Name
		}
	}
	msgs, err := s.Store.ChatMessages(ctx, src, until, chatPageSize)
	if err != nil {
		s.fail(w, err)
		return
	}
	if title == "Chat" && len(msgs) > 0 { // a conversation that isn't watched (from a mention)
		title = search.ChatTitle(msgs[0])
	}
	mixed := src == store.ChatAll || src == store.ChatDMs || src == store.ChatMentions
	var older string
	if len(msgs) == chatPageSize {
		v := url.Values{"src": {src}, "until": {store.T(store.ParseT(msgs[len(msgs)-1].At).Add(-time.Millisecond))}}
		older = "/chat?" + v.Encode()
	}
	s.render(w, "chat", "layout", struct {
		page
		Account chatAccount
		Sources []chatSource
		Src     string
		Threads []chatThread
		Older   string
		Until   string
	}{page{Title: title, Nav: "chat"}, account(set), sources, src, threads(msgs, mixed), older, until})
}

// threads groups messages (newest first) into threads, ordered by their latest message, replies oldest
// first under their parent.
func threads(msgs []store.ChatMessage, mixed bool) []chatThread {
	idx := map[string]int{}
	var out []chatThread
	for _, m := range msgs {
		it := chatItem{ChatMessage: m}
		if mixed {
			it.Where = search.ChatTitle(m)
		}
		key := m.ConvID + "/" + m.TS
		if m.IsReply() {
			key = m.ConvID + "/" + m.ThreadTS
		}
		i, ok := idx[key]
		if !ok {
			i = len(out)
			idx[key] = i
			out = append(out, chatThread{})
		}
		if m.IsReply() {
			out[i].Replies = append(out[i].Replies, it)
		} else {
			out[i].Parent = &it
		}
	}
	for i := range out {
		rs := out[i].Replies
		sort.SliceStable(rs, func(a, b int) bool { return rs[a].At < rs[b].At })
	}
	return out
}

// ---------------------------------------------------------------- settings

func (s *Server) slackSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	set, err := s.Store.ChatSettings(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	type pick struct {
		ID, Name, Kind string
		On             bool
	}
	var picks []pick
	var listErr string
	seen := map[string]bool{}
	if set.Token != "" && s.Chat != nil {
		cs, err := s.Chat.Channels(ctx)
		if err != nil {
			listErr = err.Error()
		}
		for _, c := range cs {
			seen[c.ID] = true
			picks = append(picks, pick{ID: c.ID, Name: c.Name, Kind: c.Kind(), On: set.Watches(c.ID)})
		}
	}
	for _, ch := range set.Channels { // watched channels the list didn't return can still be unticked
		if !seen[ch.ID] {
			picks = append(picks, pick{ID: ch.ID, Name: ch.Name, Kind: ch.Kind, On: true})
		}
	}
	var scopes []string
	if set.Scopes != "" {
		scopes = strings.Split(set.Scopes, ",")
	}
	var missing []string
	if set.Connected() {
		missing = chat.MissingScopes(scopes)
	}
	s.render(w, "slack", "layout", struct {
		page
		Account                 chatAccount
		HasToken                bool
		Missing                 []string
		Channels                []pick
		ListErr                 string
		WatchDMs, WatchMentions bool
		LastPoll, LastError     string
		Saved, Err              string
		Polling                 bool
	}{page{Title: "Slack settings", Nav: "chat"}, account(set), set.Token != "", missing, picks, listErr,
		set.WatchDMs, set.WatchMentions, set.LastPoll, set.LastError, r.URL.Query().Get("saved"), r.URL.Query().Get("err"), s.Chat != nil})
}

// sameOrigin rejects a form posted from another site (the browser sending it is on the tailnet).
func sameOrigin(r *http.Request) bool {
	if sf := r.Header.Get("Sec-Fetch-Site"); sf != "" && sf != "same-origin" && sf != "none" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		u, err := url.Parse(o)
		return err == nil && u.Host == r.Host
	}
	return true
}

func (s *Server) slackToken(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	tok := strings.TrimSpace(r.PostFormValue("token"))
	if tok == "" {
		http.Redirect(w, r, "/settings/slack?err="+url.QueryEscape("no token given"), http.StatusSeeOther)
		return
	}
	if s.Chat == nil {
		http.Redirect(w, r, "/settings/slack?err="+url.QueryEscape("the chat poller is off on this hub"), http.StatusSeeOther)
		return
	}
	if _, err := s.Chat.Connect(r.Context(), tok); err != nil {
		http.Redirect(w, r, "/settings/slack?err="+url.QueryEscape("token not saved: "+err.Error()), http.StatusSeeOther)
		return
	}
	s.Log.Printf("chat: Slack token replaced")
	s.Chat.Kick()
	http.Redirect(w, r, "/settings/slack?saved=token", http.StatusSeeOther)
}

func (s *Server) slackSave(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var chs []store.ChatChannel
	for _, v := range r.PostForm["ch"] { // id|kind|name
		parts := strings.SplitN(v, "|", 3)
		if len(parts) == 3 && parts[0] != "" {
			chs = append(chs, store.ChatChannel{ID: parts[0], Kind: parts[1], Name: parts[2]})
		}
	}
	if err := s.Store.SaveChatWatch(r.Context(), chs, r.PostFormValue("dms") == "1", r.PostFormValue("mentions") == "1"); err != nil {
		s.fail(w, err)
		return
	}
	if s.Chat != nil {
		s.Chat.Kick()
	}
	http.Redirect(w, r, "/settings/slack?saved=watch", http.StatusSeeOther)
}

// ---------------------------------------------------------------- API

// chatMessagesAPI: GET /api/chat/messages?after=<id>&limit=500 — messages in id order, for fleet scripts.
func (s *Server) chatMessagesAPI(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 500
	}
	limit = min(limit, 5000)
	ms, err := s.Store.ChatMessagesAfter(r.Context(), after, limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ms)
}

// chatMeAPI: GET /api/chat/me — the connected account, without the token.
func (s *Server) chatMeAPI(w http.ResponseWriter, r *http.Request) {
	set, err := s.Store.ChatSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, account(set))
}
