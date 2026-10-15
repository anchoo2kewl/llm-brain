package chat

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anchoo2kewl/llm-brain/hub/internal/store"
)

// Poller reads Slack into the store every interval: the watched channels, and (if switched on) every DM and
// group DM, and mentions of the token's user anywhere. Settings and the token come from the store each
// cycle, so a change on the settings page applies on the next one.
type Poller struct {
	Store *store.Store
	Log   *log.Logger
	API   string       // Slack Web API base URL; DefaultAPI if ""
	HTTP  *http.Client // nil: a client with a 30 s timeout
	Sleep func(ctx context.Context, d time.Duration) error

	Backfill       time.Duration // how far back a conversation is read the first time (default 24h)
	ThreadWindow   time.Duration // threads active this recently are re-read for new replies (default 7 days)
	MaxDMCalls     int           // history calls per cycle for DMs; the rest wait for the next cycle (default 40)
	MaxThreadCalls int           // conversations.replies calls per cycle (default 50)
	Now            func() time.Time

	// OnWrite runs after a cycle stored new messages (the search index syncs).
	OnWrite func(ctx context.Context)

	kickOnce sync.Once
	kick     chan struct{}
	names    map[string]string // user id -> display name
	rr       int               // where the DM round-robin resumes
}

// mentionsCursor is the chat_cursors row that remembers how far search.messages has been read.
const mentionsCursor = "search:mentions"

// mentionOverlap re-reads a little before the last mention seen: search indexes messages with a delay.
const mentionOverlap = 15 * time.Minute

// TokenFromEnvFile reads SLACK_TOKEN from a KEY=VALUE file such as ~/.config/llm-brain/slack.env.
func TokenFromEnvFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimPrefix(strings.TrimSpace(sc.Text()), "export ")
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "SLACK_TOKEN" {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// SeedToken stores the token from envFile when the store has none. It reports whether it did.
func SeedToken(ctx context.Context, st *store.Store, envFile string) (bool, error) {
	cur, err := st.Setting(ctx, store.KeySlackToken)
	if err != nil || cur != "" {
		return false, err
	}
	tok := TokenFromEnvFile(envFile)
	if tok == "" {
		return false, nil
	}
	return true, st.SetSettings(ctx, map[string]string{store.KeySlackToken: tok})
}

func (p *Poller) defaults() {
	if p.Backfill <= 0 {
		p.Backfill = 24 * time.Hour
	}
	if p.ThreadWindow <= 0 {
		p.ThreadWindow = 7 * 24 * time.Hour
	}
	if p.MaxDMCalls <= 0 {
		p.MaxDMCalls = 40
	}
	if p.MaxThreadCalls <= 0 {
		p.MaxThreadCalls = 50
	}
	if p.Now == nil {
		p.Now = time.Now
	}
	if p.Log == nil {
		p.Log = log.New(os.Stderr, "", log.LstdFlags)
	}
}

func (p *Poller) client(token string) *Client {
	return &Client{Token: token, API: p.API, HTTP: p.HTTP, Sleep: p.Sleep}
}

func (p *Poller) kicks() chan struct{} {
	p.kickOnce.Do(func() { p.kick = make(chan struct{}, 1) })
	return p.kick
}

// Kick starts the next cycle now (the settings changed) instead of at the end of the interval.
func (p *Poller) Kick() {
	select {
	case p.kicks() <- struct{}{}:
	default:
	}
}

func (p *Poller) Run(ctx context.Context, every time.Duration) {
	kick := p.kicks()
	for {
		if err := p.Once(ctx); err != nil && ctx.Err() == nil {
			p.Log.Printf("chat poll: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		case <-kick:
		}
	}
}

// Connect checks a token with auth.test and, if it works, stores it with the account it belongs to.
func (p *Poller) Connect(ctx context.Context, token string) (Identity, error) {
	id, err := p.client(token).AuthTest(ctx)
	if err != nil {
		return id, err
	}
	err = p.Store.SetSettings(ctx, map[string]string{store.KeySlackToken: token, store.KeyChatLastError: ""})
	if err == nil {
		err = p.saveIdentity(ctx, id)
	}
	return id, err
}

func (p *Poller) saveIdentity(ctx context.Context, id Identity) error {
	return p.Store.SetSettings(ctx, map[string]string{store.KeySlackUserID: id.UserID, store.KeySlackUserName: id.User,
		store.KeySlackTeam: id.Team, store.KeySlackURL: id.URL, store.KeySlackScopes: strings.Join(id.Scopes, ",")})
}

// Channels lists the channels the stored token's user can pick from.
func (p *Poller) Channels(ctx context.Context) ([]Conversation, error) {
	tok, err := p.Store.Setting(ctx, store.KeySlackToken)
	if err != nil {
		return nil, err
	}
	cs, err := p.client(tok).Channels(ctx)
	sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
	return cs, err
}

// conv is a conversation being read.
type conv struct {
	ID, Name, Kind string
}

// cycle is one Once: the client, who "me" is, and what's left of the call budgets.
type cycle struct {
	c          *Client
	me, url    string
	threadLeft int
	added      int
	errs       []error
}

// Once runs one cycle. Errors reading one conversation don't stop the others; they're logged and the last
// one is shown on the settings page.
func (p *Poller) Once(ctx context.Context) error {
	p.defaults()
	set, err := p.Store.ChatSettings(ctx)
	if err != nil || set.Token == "" {
		return err
	}
	cy := &cycle{c: p.client(set.Token), threadLeft: p.MaxThreadCalls}
	id, err := cy.c.AuthTest(ctx)
	if err != nil {
		_ = p.Store.SetSettings(ctx, map[string]string{store.KeyChatLastError: err.Error(), store.KeyChatLastPoll: store.Now()})
		return err
	}
	if err := p.saveIdentity(ctx, id); err != nil {
		return err
	}
	cy.me, cy.url = id.UserID, id.URL
	if p.names == nil {
		if p.names, err = p.Store.ChatUsers(ctx); err != nil {
			return err
		}
	}
	for _, ch := range set.Channels {
		kind := ch.Kind
		if kind == "" {
			kind = "channel"
		}
		if err := p.readConv(ctx, cy, conv{ID: ch.ID, Name: ch.Name, Kind: kind}, nil); err != nil {
			cy.errs = append(cy.errs, fmt.Errorf("#%s: %w", ch.Name, err))
		}
	}
	if set.WatchDMs {
		if err := p.readDMs(ctx, cy, set); err != nil {
			cy.errs = append(cy.errs, fmt.Errorf("DMs: %w", err))
		}
	}
	if set.WatchMentions {
		if err := p.readMentions(ctx, cy); err != nil {
			cy.errs = append(cy.errs, fmt.Errorf("mentions: %w", err))
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	last := ""
	for _, e := range cy.errs {
		p.Log.Printf("chat poll: %v", e)
		last = e.Error()
	}
	if err := p.Store.SetSettings(ctx, map[string]string{store.KeyChatLastPoll: store.Now(), store.KeyChatLastError: last}); err != nil {
		return err
	}
	if cy.added > 0 && p.OnWrite != nil {
		p.OnWrite(ctx)
	}
	return nil
}

func slackTS(t time.Time) string {
	return fmt.Sprintf("%d.%06d", t.Unix(), t.Nanosecond()/1000)
}

// tsAfter: a is a later Slack ts than b ("" is the earliest).
func tsAfter(a, b string) bool {
	if b == "" {
		return a != ""
	}
	return store.SlackTime(a).After(store.SlackTime(b))
}

func isParent(m Message) bool { return m.ReplyCount > 0 && (m.ThreadTS == "" || m.ThreadTS == m.TS) }

// readConv reads a conversation's new messages and its recently active threads. budget, if not nil, counts
// down the history calls it makes.
func (p *Poller) readConv(ctx context.Context, cy *cycle, cv conv, budget *int) error {
	cur, err := p.Store.ChatCursor(ctx, cv.ID)
	if err != nil {
		return err
	}
	oldest := cur.Oldest
	if oldest == "" {
		oldest = slackTS(p.Now().Add(-p.Backfill))
	}
	var msgs []Message
	page := ""
	for n := 0; ; n++ {
		ms, next, err := cy.c.History(ctx, cv.ID, oldest, page)
		if budget != nil {
			*budget--
		}
		if err != nil {
			return err
		}
		msgs = append(msgs, ms...)
		if next == "" {
			break
		}
		if n == 19 {
			p.Log.Printf("chat: %s has more than 20 pages of new messages; older ones skipped", cv.Name)
			break
		}
		page = next
	}
	newest := cur.Oldest
	rows := make([]store.ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		if tsAfter(m.TS, newest) {
			newest = m.TS
		}
		if _, ok := cur.Threads[m.TS]; !ok && isParent(m) {
			cur.Threads[m.TS] = "" // a new thread: read all of it
		}
		if r, ok := p.row(ctx, cy, cv, m); ok {
			rows = append(rows, r)
		}
	}
	// Threads: the ones active within the window, most recent first, as far as the budget goes.
	type thread struct{ parent, last string }
	var ths []thread
	horizon := p.Now().Add(-p.ThreadWindow)
	for parent, last := range cur.Threads {
		act := parent
		if tsAfter(last, act) {
			act = last
		}
		if store.SlackTime(act).Before(horizon) {
			delete(cur.Threads, parent)
			continue
		}
		ths = append(ths, thread{parent, act})
	}
	sort.Slice(ths, func(i, j int) bool { return tsAfter(ths[i].last, ths[j].last) })
	for _, th := range ths {
		if cy.threadLeft <= 0 {
			break
		}
		cy.threadLeft--
		reps, err := cy.c.Replies(ctx, cv.ID, th.parent, cur.Threads[th.parent])
		if err != nil {
			return err
		}
		for _, m := range reps {
			if m.TS != th.parent && tsAfter(m.TS, cur.Threads[th.parent]) {
				cur.Threads[th.parent] = m.TS
			}
			if r, ok := p.row(ctx, cy, cv, m); ok {
				rows = append(rows, r)
			}
		}
	}
	n, err := p.Store.AddChatMessages(ctx, rows)
	if err != nil {
		return err
	}
	cy.added += n
	cur.Oldest = newest
	if cur.Oldest == "" {
		cur.Oldest = oldest // nothing yet: next time, read from where this one started
	}
	return p.Store.SaveChatCursor(ctx, cur)
}

// readDMs reads the user's DMs and group DMs within the history budget: first the ones Slack says changed
// since they were last read, then the rest in turn, picking up next cycle where this one stopped.
func (p *Poller) readDMs(ctx context.Context, cy *cycle, set store.ChatSettings) error {
	dms, err := cy.c.DMs(ctx)
	if err != nil {
		return err
	}
	if len(dms) == 0 {
		return nil
	}
	type cand struct {
		Conversation
		changed bool
	}
	var changed, rest []cand
	for _, d := range dms {
		if set.Watches(d.ID) {
			continue
		}
		cur, err := p.Store.ChatCursor(ctx, d.ID)
		if err != nil {
			return err
		}
		if d.Updated > 0 && cur.Oldest != "" && d.Updated > store.SlackTime(cur.Oldest).UnixMilli() {
			changed = append(changed, cand{d, true})
		} else {
			rest = append(rest, cand{d, false})
		}
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].Updated > changed[j].Updated })
	order := changed
	start := 0
	if len(rest) > 0 {
		start = p.rr % len(rest)
	}
	for i := range rest {
		order = append(order, rest[(start+i)%len(rest)])
	}
	budget := p.MaxDMCalls
	read := 0
	for _, d := range order {
		if budget <= 0 || ctx.Err() != nil {
			break
		}
		if !d.changed {
			read++
		}
		cv := conv{ID: d.ID, Kind: d.Kind(), Name: d.Name}
		if d.IsIM {
			cv.Name = p.name(ctx, cy, d.User)
		} else if d.IsMPIM {
			cv.Name = groupName(d.Name)
		}
		if err := p.readConv(ctx, cy, cv, &budget); err != nil {
			cy.errs = append(cy.errs, fmt.Errorf("DM %s: %w", cv.Name, err))
		}
	}
	p.rr = start + read
	return nil
}

// groupName turns a group DM's "mpdm-a--b--c-1" into "a, b, c".
func groupName(n string) string {
	s := strings.TrimPrefix(n, "mpdm-")
	if i := strings.LastIndex(s, "-"); i > 0 {
		if _, err := strconv.Atoi(s[i+1:]); err == nil {
			s = s[:i]
		}
	}
	return strings.ReplaceAll(s, "--", ", ")
}

// readMentions reads search.messages for <@me>, newest first, back to a little before the last one seen.
func (p *Poller) readMentions(ctx context.Context, cy *cycle) error {
	cur, err := p.Store.ChatCursor(ctx, mentionsCursor)
	if err != nil {
		return err
	}
	stop := p.Now().Add(-p.Backfill)
	if cur.Oldest != "" {
		stop = store.SlackTime(cur.Oldest).Add(-mentionOverlap)
	}
	newest := cur.Oldest
	var rows []store.ChatMessage
	done := false
	for page := 1; page <= 5 && !done; page++ {
		ms, pages, err := cy.c.Search(ctx, "<@"+cy.me+">", page)
		if err != nil {
			return err
		}
		for _, m := range ms {
			if m.Channel == nil || m.TS == "" {
				continue
			}
			if store.SlackTime(m.TS).Before(stop) {
				done = true
				break
			}
			if tsAfter(m.TS, newest) {
				newest = m.TS
			}
			ch := Conversation{ID: m.Channel.ID, Name: m.Channel.Name, IsChannel: m.Channel.IsChannel, IsGroup: m.Channel.IsGroup,
				IsPrivate: m.Channel.IsPrivate, IsIM: m.Channel.IsIM, IsMPIM: m.Channel.IsMPIM}
			cv := conv{ID: ch.ID, Name: ch.Name, Kind: ch.Kind()}
			if ch.IsIM {
				cv.Name = p.name(ctx, cy, firstNonEmpty(m.Channel.User, m.User))
			} else if ch.IsMPIM {
				cv.Name = groupName(ch.Name)
			}
			if m.ThreadTS == "" {
				m.ThreadTS = threadFromPermalink(m.Permalink)
			}
			if r, ok := p.row(ctx, cy, cv, m); ok {
				r.MentionsMe = true
				rows = append(rows, r)
			}
		}
		if page >= pages {
			break
		}
	}
	n, err := p.Store.AddChatMessages(ctx, rows)
	if err != nil {
		return err
	}
	cy.added += n
	cur.Oldest = newest
	return p.Store.SaveChatCursor(ctx, cur)
}

func threadFromPermalink(link string) string {
	if u, err := url.Parse(link); err == nil {
		return u.Query().Get("thread_ts")
	}
	return ""
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// skipSubtypes are housekeeping messages, not conversation.
var skipSubtypes = map[string]bool{"channel_join": true, "channel_leave": true, "group_join": true, "group_leave": true}

// row turns a Slack message into a stored one; ok is false for messages not worth keeping.
func (p *Poller) row(ctx context.Context, cy *cycle, cv conv, m Message) (store.ChatMessage, bool) {
	if m.TS == "" || skipSubtypes[m.Subtype] || strings.TrimSpace(m.Text) == "" {
		return store.ChatMessage{}, false
	}
	r := store.ChatMessage{Source: "slack", ConvID: cv.ID, ConvName: cv.Name, ConvKind: cv.Kind, TS: m.TS, ThreadTS: m.ThreadTS,
		UserID: m.User, MentionsMe: mentions(m.Text, cy.me), Permalink: m.Permalink, At: store.T(store.SlackTime(m.TS))}
	switch {
	case m.User != "":
		r.UserName = p.name(ctx, cy, m.User)
	case m.Username != "":
		r.UserName = m.Username
	case m.BotProfile != nil:
		r.UserName = m.BotProfile.Name
	}
	r.Text = Clean(m.Text, func(id string) string { return p.name(ctx, cy, id) })
	if r.Permalink == "" {
		r.Permalink = permalink(cy.url, cv.ID, m.TS, m.ThreadTS)
	}
	return r, true
}

// permalink builds a message link from the workspace URL (no chat.getPermalink call per message).
func permalink(base, convID, ts, threadTS string) string {
	if base == "" || ts == "" {
		return ""
	}
	link := strings.TrimRight(base, "/") + "/archives/" + convID + "/p" + strings.Replace(ts, ".", "", 1)
	if threadTS != "" && threadTS != ts {
		link += "?thread_ts=" + threadTS + "&cid=" + convID
	}
	return link
}

// name is a user's display name, from memory, the store, or users.info (then remembered in both). A user
// that can't be looked up shows as their id, and isn't asked about again until the hub restarts.
func (p *Poller) name(ctx context.Context, cy *cycle, id string) string {
	if id == "" {
		return ""
	}
	if n, ok := p.names[id]; ok {
		return n
	}
	n, err := cy.c.UserName(ctx, id)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			p.Log.Printf("chat: users.info %s: %v", id, err)
		}
		p.names[id] = id
		return id
	}
	p.names[id] = n
	if err := p.Store.SaveChatUser(ctx, id, n); err != nil {
		p.Log.Printf("chat: save user: %v", err)
	}
	return n
}
