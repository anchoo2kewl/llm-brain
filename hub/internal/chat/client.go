// Package chat reads team chat (Slack) into the hub: a small Web API client that only calls read methods,
// and a poller that stores watched channels, DMs and mentions of the token's user. It never posts.
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const DefaultAPI = "https://slack.com/api"

// NeededScopes are the user-token scopes the connector uses.
var NeededScopes = []string{"channels:history", "groups:history", "im:history", "mpim:history",
	"channels:read", "groups:read", "im:read", "mpim:read", "users:read", "search:read"}

// readMethods is every method the client may call: a guard against ever writing to Slack.
var readMethods = map[string]bool{
	"auth.test": true, "conversations.list": true, "conversations.history": true, "conversations.replies": true,
	"users.conversations": true, "users.info": true, "search.messages": true,
}

// Client calls the Slack Web API with a user token. The token goes only in the Authorization header; errors
// never include it.
type Client struct {
	Token string
	API   string // base URL, DefaultAPI if ""
	HTTP  *http.Client
	// Sleep waits out a rate limit; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
	// Calls counts requests per method (including retries), for tests and logs.
	Calls map[string]int
}

func NewClient(token, api string) *Client {
	return &Client{Token: token, API: api}
}

// APIError is Slack's `"ok": false` answer.
type APIError struct {
	Method, Code string
}

func (e *APIError) Error() string { return e.Method + ": " + e.Code }

// IsAuthError reports a token that no longer works.
func IsAuthError(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		switch ae.Code {
		case "invalid_auth", "not_authed", "token_revoked", "token_expired", "account_inactive":
			return true
		}
	}
	return false
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Call runs one read method and decodes the answer into out. On HTTP 429 it waits for Retry-After and tries
// again (up to 3 times). It returns the response headers (x-oauth-scopes).
func (c *Client) Call(ctx context.Context, method string, params url.Values, out any) (http.Header, error) {
	if !readMethods[method] {
		return nil, fmt.Errorf("chat: %s is not a read method", method)
	}
	if c.Token == "" {
		return nil, &APIError{Method: method, Code: "not_authed"}
	}
	api := c.API
	if api == "" {
		api = DefaultAPI
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	sleep := c.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	u := strings.TrimRight(api, "/") + "/" + method
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		if c.Calls != nil {
			c.Calls[method]++
		}
		res, err := hc.Do(req)
		if err != nil {
			return nil, err // a *url.Error: method URL and query, never the token
		}
		if res.StatusCode == http.StatusTooManyRequests && attempt < 3 {
			res.Body.Close()
			wait, _ := strconv.Atoi(res.Header.Get("Retry-After"))
			if err := sleep(ctx, time.Duration(max(wait, 0))*time.Second); err != nil {
				return nil, err
			}
			continue
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: HTTP %d", method, res.StatusCode)
		}
		var raw json.RawMessage
		if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
			return nil, fmt.Errorf("%s: %w", method, err)
		}
		var status struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &status)
		if !status.OK {
			if status.Error == "" {
				status.Error = "unknown_error"
			}
			return res.Header, &APIError{Method: method, Code: status.Error}
		}
		if out != nil {
			if err := json.Unmarshal(raw, out); err != nil {
				return nil, fmt.Errorf("%s: %w", method, err)
			}
		}
		return res.Header, nil
	}
}

type meta struct {
	ResponseMetadata struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}

// ---------------------------------------------------------------- methods

// Identity is auth.test: who the token is, and what it may do.
type Identity struct {
	UserID, User, Team, TeamID, URL string
	Scopes                          []string
}

func (c *Client) AuthTest(ctx context.Context) (Identity, error) {
	var r struct {
		URL    string `json:"url"`
		Team   string `json:"team"`
		User   string `json:"user"`
		TeamID string `json:"team_id"`
		UserID string `json:"user_id"`
	}
	h, err := c.Call(ctx, "auth.test", nil, &r)
	if err != nil {
		return Identity{}, err
	}
	id := Identity{UserID: r.UserID, User: r.User, Team: r.Team, TeamID: r.TeamID, URL: r.URL}
	for _, s := range strings.Split(h.Get("X-OAuth-Scopes"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			id.Scopes = append(id.Scopes, s)
		}
	}
	return id, nil
}

// MissingScopes is the needed scopes not in have.
func MissingScopes(have []string) []string {
	got := map[string]bool{}
	for _, s := range have {
		got[s] = true
	}
	var out []string
	for _, s := range NeededScopes {
		if !got[s] {
			out = append(out, s)
		}
	}
	return out
}

// Conversation is a channel, private channel, DM or group DM.
type Conversation struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IsChannel  bool   `json:"is_channel"`
	IsGroup    bool   `json:"is_group"`
	IsPrivate  bool   `json:"is_private"`
	IsIM       bool   `json:"is_im"`
	IsMPIM     bool   `json:"is_mpim"`
	IsArchived bool   `json:"is_archived"`
	IsMember   bool   `json:"is_member"`
	User       string `json:"user"`    // the other person, for a DM
	Updated    int64  `json:"updated"` // ms; last change Slack reports, 0 if not given
}

// Kind is the conv_kind stored for its messages.
func (c Conversation) Kind() string {
	switch {
	case c.IsIM:
		return "dm"
	case c.IsMPIM:
		return "group_dm"
	case c.IsPrivate || c.IsGroup:
		return "private"
	}
	return "channel"
}

func (c *Client) conversations(ctx context.Context, method string, p url.Values) ([]Conversation, error) {
	var out []Conversation
	for page := 0; page < 50; page++ {
		var r struct {
			meta
			Channels []Conversation `json:"channels"`
		}
		if _, err := c.Call(ctx, method, p, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Channels...)
		if r.ResponseMetadata.NextCursor == "" {
			break
		}
		p.Set("cursor", r.ResponseMetadata.NextCursor)
	}
	return out, nil
}

// Channels lists the public and private channels the user is a member of, without archived ones.
func (c *Client) Channels(ctx context.Context) ([]Conversation, error) {
	cs, err := c.conversations(ctx, "conversations.list", url.Values{
		"types": {"public_channel,private_channel"}, "exclude_archived": {"true"}, "limit": {"1000"}})
	if err != nil {
		return nil, err
	}
	out := cs[:0]
	for _, x := range cs {
		if x.IsMember && !x.IsArchived {
			out = append(out, x)
		}
	}
	return out, nil
}

// DMs lists the user's DMs and group DMs.
func (c *Client) DMs(ctx context.Context) ([]Conversation, error) {
	return c.conversations(ctx, "users.conversations", url.Values{"types": {"im,mpim"}, "exclude_archived": {"true"}, "limit": {"200"}})
}

// Message is a Slack message as history, replies and search return it.
type Message struct {
	Type       string `json:"type"`
	Subtype    string `json:"subtype"`
	TS         string `json:"ts"`
	ThreadTS   string `json:"thread_ts"`
	User       string `json:"user"`
	Username   string `json:"username"` // bots, and search results
	BotID      string `json:"bot_id"`
	Text       string `json:"text"`
	ReplyCount int    `json:"reply_count"`
	LatestRepl string `json:"latest_reply"`
	BotProfile *struct {
		Name string `json:"name"`
	} `json:"bot_profile"`
	// search.messages only:
	Permalink string      `json:"permalink"`
	Channel   *MsgChannel `json:"channel"`
}

// MsgChannel is a message's channel: an object in search.messages, a plain id string elsewhere
// (e.g. some messages in group DMs' history).
type MsgChannel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsChannel bool   `json:"is_channel"`
	IsGroup   bool   `json:"is_group"`
	IsPrivate bool   `json:"is_private"`
	IsIM      bool   `json:"is_im"`
	IsMPIM    bool   `json:"is_mpim"`
	User      string `json:"user"`
}

func (c *MsgChannel) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &c.ID)
	}
	type plain MsgChannel
	return json.Unmarshal(b, (*plain)(c))
}

// History returns one page of a conversation's messages newer than oldest (newest first) and the cursor of
// the next page ("" at the end).
func (c *Client) History(ctx context.Context, channel, oldest, cursor string) ([]Message, string, error) {
	p := url.Values{"channel": {channel}, "limit": {"200"}}
	if oldest != "" {
		p.Set("oldest", oldest)
	}
	if cursor != "" {
		p.Set("cursor", cursor)
	}
	var r struct {
		meta
		Messages []Message `json:"messages"`
		HasMore  bool      `json:"has_more"`
	}
	if _, err := c.Call(ctx, "conversations.history", p, &r); err != nil {
		return nil, "", err
	}
	next := r.ResponseMetadata.NextCursor
	if !r.HasMore {
		next = ""
	}
	return r.Messages, next, nil
}

// Replies returns a thread's parent and its replies newer than oldest, every page.
func (c *Client) Replies(ctx context.Context, channel, parent, oldest string) ([]Message, error) {
	p := url.Values{"channel": {channel}, "ts": {parent}, "limit": {"200"}}
	if oldest != "" {
		p.Set("oldest", oldest)
	}
	var out []Message
	for page := 0; page < 20; page++ {
		var r struct {
			meta
			Messages []Message `json:"messages"`
			HasMore  bool      `json:"has_more"`
		}
		if _, err := c.Call(ctx, "conversations.replies", p, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Messages...)
		if !r.HasMore || r.ResponseMetadata.NextCursor == "" {
			break
		}
		p.Set("cursor", r.ResponseMetadata.NextCursor)
	}
	return out, nil
}

// Search returns one page (1-based) of search.messages, newest first, and the number of pages.
func (c *Client) Search(ctx context.Context, query string, page int) ([]Message, int, error) {
	var r struct {
		Messages struct {
			Matches []Message `json:"matches"`
			Paging  struct {
				Pages int `json:"pages"`
			} `json:"paging"`
		} `json:"messages"`
	}
	_, err := c.Call(ctx, "search.messages", url.Values{"query": {query}, "sort": {"timestamp"}, "sort_dir": {"desc"},
		"count": {"100"}, "page": {strconv.Itoa(page)}}, &r)
	return r.Messages.Matches, r.Messages.Paging.Pages, err
}

// UserName is a user's display name (users.info): display name, else real name, else handle.
func (c *Client) UserName(ctx context.Context, id string) (string, error) {
	var r struct {
		User struct {
			Name    string `json:"name"`
			Profile struct {
				DisplayName string `json:"display_name"`
				RealName    string `json:"real_name"`
			} `json:"profile"`
		} `json:"user"`
	}
	if _, err := c.Call(ctx, "users.info", url.Values{"user": {id}}, &r); err != nil {
		return "", err
	}
	for _, n := range []string{r.User.Profile.DisplayName, r.User.Profile.RealName, r.User.Name} {
		if n = strings.TrimSpace(n); n != "" {
			return n, nil
		}
	}
	return id, nil
}
