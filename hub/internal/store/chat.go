package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------- settings

// Setting returns a setting's value, or "" when it isn't set.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSettings writes several settings in one transaction.
func (s *Store) SetSettings(ctx context.Context, kv map[string]string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range kv {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Setting keys of the chat connector.
const (
	KeySlackToken     = "slack_token"
	KeySlackUserID    = "slack_user_id"
	KeySlackUserName  = "slack_user_name"
	KeySlackTeam      = "slack_team"
	KeySlackURL       = "slack_url"    // workspace URL from auth.test, for permalinks
	KeySlackScopes    = "slack_scopes" // the token's x-oauth-scopes
	KeyChatChannels   = "chat_channels"
	KeyChatWatchDMs   = "chat_watch_dms"
	KeyChatMentions   = "chat_watch_mentions"
	KeyChatLastPoll   = "chat_last_poll"
	KeyChatLastError  = "chat_last_error"
	chatSettingsQuery = `SELECT key, value FROM settings WHERE key LIKE 'slack_%' OR key LIKE 'chat_%'`
)

type ChatChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"` // channel | private
}

// ChatSettings is the chat connector's configuration. Token is for the poller only: pages and APIs show
// the account fields, never it.
type ChatSettings struct {
	Token                   string
	UserID, UserName, Team  string
	URL, Scopes             string
	Channels                []ChatChannel
	WatchDMs, WatchMentions bool
	LastPoll, LastError     string
}

func (c ChatSettings) Connected() bool { return c.Token != "" && c.UserID != "" }

func (c ChatSettings) Watches(convID string) bool {
	for _, ch := range c.Channels {
		if ch.ID == convID {
			return true
		}
	}
	return false
}

func (s *Store) ChatSettings(ctx context.Context) (ChatSettings, error) {
	var c ChatSettings
	rows, err := s.DB.QueryContext(ctx, chatSettingsQuery)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return c, err
		}
		switch k {
		case KeySlackToken:
			c.Token = v
		case KeySlackUserID:
			c.UserID = v
		case KeySlackUserName:
			c.UserName = v
		case KeySlackTeam:
			c.Team = v
		case KeySlackURL:
			c.URL = v
		case KeySlackScopes:
			c.Scopes = v
		case KeyChatChannels:
			_ = json.Unmarshal([]byte(v), &c.Channels)
		case KeyChatWatchDMs:
			c.WatchDMs = v == "1"
		case KeyChatMentions:
			c.WatchMentions = v == "1"
		case KeyChatLastPoll:
			c.LastPoll = v
		case KeyChatLastError:
			c.LastError = v
		}
	}
	return c, rows.Err()
}

// SaveChatWatch saves what to watch.
func (s *Store) SaveChatWatch(ctx context.Context, channels []ChatChannel, dms, mentions bool) error {
	if channels == nil {
		channels = []ChatChannel{}
	}
	b, _ := json.Marshal(channels)
	return s.SetSettings(ctx, map[string]string{KeyChatChannels: string(b), KeyChatWatchDMs: bit(dms), KeyChatMentions: bit(mentions)})
}

func bit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// ---------------------------------------------------------------- messages

// ChatMessage is one stored chat message. Its JSON is the /api/chat/messages shape.
type ChatMessage struct {
	ID         int64  `json:"id"`
	Source     string `json:"source"`
	ConvID     string `json:"conv_id"`
	ConvName   string `json:"conv_name"`
	ConvKind   string `json:"conv_kind"`
	MentionsMe bool   `json:"mentions_me"`
	TS         string `json:"ts"`
	ThreadTS   string `json:"thread_ts"`
	UserID     string `json:"user_id"`
	UserName   string `json:"user_name"`
	Text       string `json:"text"`
	Permalink  string `json:"permalink"`
	At         string `json:"at"`
}

// IsReply: a message in a thread that isn't the thread's parent.
func (m ChatMessage) IsReply() bool { return m.ThreadTS != "" && m.ThreadTS != m.TS }

// SlackTime turns a Slack ts ("1792012200.000100") into a time.
func SlackTime(ts string) time.Time {
	sec, frac, _ := strings.Cut(ts, ".")
	n, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return time.Time{}
	}
	var us int64
	if frac != "" {
		frac = (frac + "000000")[:6]
		us, _ = strconv.ParseInt(frac, 10, 64)
	}
	return time.Unix(n, us*1000).UTC()
}

// AddChatMessages stores messages; a message already stored (same conversation and ts) is kept as it is,
// except that mentions_me is set when the new copy says so. It returns how many rows were new.
func (s *Store) AddChatMessages(ctx context.Context, ms []ChatMessage) (int, error) {
	if len(ms) == 0 {
		return 0, nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	added := 0
	for _, m := range ms {
		if m.Source == "" {
			m.Source = "slack"
		}
		if m.At == "" {
			m.At = T(SlackTime(m.TS))
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO chat_messages
			(source, conv_id, conv_name, conv_kind, mentions_me, ts, thread_ts, user_id, user_name, text, permalink, at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT (conv_id, ts) DO NOTHING`,
			m.Source, m.ConvID, m.ConvName, m.ConvKind, b2i(m.MentionsMe), m.TS, m.ThreadTS, m.UserID, m.UserName, m.Text, m.Permalink, m.At)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
			continue
		}
		if m.MentionsMe {
			// The search index filters on mentions:me, so a row that gains it is re-indexed.
			var id int64
			err := tx.QueryRowContext(ctx, `SELECT id FROM chat_messages WHERE conv_id = ? AND ts = ? AND mentions_me = 0`, m.ConvID, m.TS).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE chat_messages SET mentions_me = 1 WHERE id = ?`, id); err != nil {
				return 0, err
			}
			if err := MarkDirty(ctx, tx, "c", id); err != nil {
				return 0, err
			}
		}
	}
	return added, tx.Commit()
}

const chatCols = `id, source, conv_id, conv_name, conv_kind, mentions_me, ts, thread_ts, user_id, user_name, text, permalink, at`

func (s *Store) queryChat(ctx context.Context, q string, args ...any) ([]ChatMessage, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChatMessage{}
	for rows.Next() {
		var m ChatMessage
		var mm int
		if err := rows.Scan(&m.ID, &m.Source, &m.ConvID, &m.ConvName, &m.ConvKind, &mm, &m.TS, &m.ThreadTS, &m.UserID,
			&m.UserName, &m.Text, &m.Permalink, &m.At); err != nil {
			return nil, err
		}
		m.MentionsMe = mm != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// ChatMessagesAfter pages chat messages in id order (the fleet API and the search index).
func (s *Store) ChatMessagesAfter(ctx context.Context, after int64, limit int) ([]ChatMessage, error) {
	return s.queryChat(ctx, `SELECT `+chatCols+` FROM chat_messages WHERE id > ? ORDER BY id LIMIT ?`, after, limit)
}

func (s *Store) ChatMessagesByID(ctx context.Context, ids []int64) ([]ChatMessage, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q, args := inList(`SELECT `+chatCols+` FROM chat_messages WHERE id IN `, ids)
	return s.queryChat(ctx, q, args...)
}

// Chat sources the chat page shows besides single conversations.
const (
	ChatAll      = "all"
	ChatDMs      = "dm"
	ChatMentions = "mentions"
)

// ChatMessages returns a source's messages at or before until (a store time; "" for now), newest first.
// src is ChatAll, ChatDMs, ChatMentions or a conversation id.
func (s *Store) ChatMessages(ctx context.Context, src, until string, limit int) ([]ChatMessage, error) {
	where, args := "1=1", []any{}
	switch src {
	case ChatAll, "":
	case ChatDMs:
		where = `conv_kind IN ('dm', 'group_dm')`
	case ChatMentions:
		where = `mentions_me = 1`
	default:
		where, args = `conv_id = ?`, append(args, src)
	}
	if until != "" {
		where += ` AND at <= ?`
		args = append(args, until)
	}
	args = append(args, limit)
	return s.queryChat(ctx, `SELECT `+chatCols+` FROM chat_messages WHERE `+where+` ORDER BY at DESC, id DESC LIMIT ?`, args...)
}

// ChatCounts are message counts since a time: per conversation, for DMs and for mentions of me.
type ChatCounts struct {
	ByConv        map[string]int
	DMs, Mentions int
	Total         int
}

func (s *Store) ChatCounts(ctx context.Context, since time.Time) (ChatCounts, error) {
	c := ChatCounts{ByConv: map[string]int{}}
	rows, err := s.DB.QueryContext(ctx, `SELECT conv_id, conv_kind, mentions_me, count(*) FROM chat_messages WHERE at >= ?
		GROUP BY conv_id, conv_kind, mentions_me`, T(since))
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind string
		var mm, n int
		if err := rows.Scan(&id, &kind, &mm, &n); err != nil {
			return c, err
		}
		c.ByConv[id] += n
		c.Total += n
		if kind == "dm" || kind == "group_dm" {
			c.DMs += n
		}
		if mm != 0 {
			c.Mentions += n
		}
	}
	return c, rows.Err()
}

// ---------------------------------------------------------------- cursors and users

// ChatCursor is how far a conversation has been read.
type ChatCursor struct {
	ConvID  string
	Oldest  string            // newest ts read: the next history call's oldest
	Threads map[string]string // thread parent ts -> newest reply ts read
}

func (s *Store) ChatCursor(ctx context.Context, convID string) (ChatCursor, error) {
	c := ChatCursor{ConvID: convID, Threads: map[string]string{}}
	var threads string
	err := s.DB.QueryRowContext(ctx, `SELECT oldest, threads FROM chat_cursors WHERE conv_id = ?`, convID).Scan(&c.Oldest, &threads)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	_ = json.Unmarshal([]byte(threads), &c.Threads)
	if c.Threads == nil {
		c.Threads = map[string]string{}
	}
	return c, nil
}

func (s *Store) SaveChatCursor(ctx context.Context, c ChatCursor) error {
	if c.Threads == nil {
		c.Threads = map[string]string{}
	}
	b, _ := json.Marshal(c.Threads)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO chat_cursors (conv_id, oldest, threads) VALUES (?, ?, ?)
		ON CONFLICT (conv_id) DO UPDATE SET oldest = excluded.oldest, threads = excluded.threads`, c.ConvID, c.Oldest, string(b))
	return err
}

func (s *Store) ChatUsers(ctx context.Context) (map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT user_id, name FROM chat_users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, n string
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func (s *Store) SaveChatUser(ctx context.Context, id, name string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO chat_users (user_id, name) VALUES (?, ?)
		ON CONFLICT (user_id) DO UPDATE SET name = excluded.name`, id, name)
	return err
}
