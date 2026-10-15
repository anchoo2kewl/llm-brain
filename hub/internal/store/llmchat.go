package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Plain chats (package llm): conversations with a model through the account gateway.

type LLMChat struct {
	ID        int64
	Title     string
	Model     string
	CreatedAt string
	UpdatedAt string
}

type LLMMessage struct {
	ID        int64
	ChatID    int64
	Role      string
	Model     string
	Content   string
	Status    string
	Error     string
	CreatedAt string
}

// KeyChatLast is the settings key for the last model picked on the New chat page ("plain:<model>" or
// "agent:<provider>").
const KeyChatLast = "chat_last_model"

// CreateLLMChat makes a chat and returns its id.
func (s *Store) CreateLLMChat(ctx context.Context, title, model string) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	now := Now()
	id, err := insertID(ctx, tx, `INSERT INTO llm_chats (title, model, created_at, updated_at) VALUES (?, ?, ?, ?)`, title, model, now, now)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) LLMChat(ctx context.Context, id int64) (LLMChat, error) {
	var c LLMChat
	err := s.DB.QueryRowContext(ctx, `SELECT id, title, model, created_at, updated_at FROM llm_chats WHERE id = ?`, id).
		Scan(&c.ID, &c.Title, &c.Model, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return c, err
}

// ScanRecent is how many of the newest plain-chat messages a search without the index looks through.
const ScanRecent = 5000

// LLMChats lists chats, newest activity first. q filters on the title and model, and, with scanContent,
// on the first 4000 characters of the newest ScanRecent messages, so the scan stays bounded (the fallback while the search index isn't ready; with it,
// message text is found through the index).
func (s *Store) LLMChats(ctx context.Context, q string, limit int, scanContent bool) ([]LLMChat, error) {
	query := `SELECT id, title, model, created_at, updated_at FROM llm_chats`
	var args []any
	if q = strings.TrimSpace(q); q != "" {
		like := "%" + strings.NewReplacer("%", "", "_", "").Replace(strings.ToLower(q)) + "%"
		query += ` WHERE lower(title) LIKE ? OR lower(model) LIKE ?`
		args = append(args, like, like)
		if scanContent {
			query += ` OR id IN (SELECT chat_id FROM llm_messages WHERE id > (SELECT COALESCE((SELECT id FROM llm_messages ORDER BY id DESC LIMIT 1), 0)) - ?
				AND lower(substr(content, 1, 4000)) LIKE ?)`
			args = append(args, ScanRecent, like)
		}
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	return s.llmChatList(ctx, query, args...)
}

// LLMChatsWithMessages lists the chats that hold these messages, newest activity first.
func (s *Store) LLMChatsWithMessages(ctx context.Context, msgIDs []int64) ([]LLMChat, error) {
	if len(msgIDs) == 0 {
		return nil, nil
	}
	q, args := inList(`SELECT id, title, model, created_at, updated_at FROM llm_chats
		WHERE id IN (SELECT chat_id FROM llm_messages WHERE id IN `, msgIDs)
	return s.llmChatList(ctx, q+`) ORDER BY updated_at DESC`, args...)
}

func (s *Store) llmChatList(ctx context.Context, query string, args ...any) ([]LLMChat, error) {
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMChat
	for rows.Next() {
		var c LLMChat
		if err := rows.Scan(&c.ID, &c.Title, &c.Model, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

const llmMsgCols = `id, chat_id, role, model, content, status, error, created_at`

func (s *Store) LLMMessages(ctx context.Context, chatID int64) ([]LLMMessage, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+llmMsgCols+` FROM llm_messages WHERE chat_id = ? ORDER BY id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMMessage
	for rows.Next() {
		var m LLMMessage
		if err := rows.Scan(&m.ID, &m.ChatID, &m.Role, &m.Model, &m.Content, &m.Status, &m.Error, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) LLMMessage(ctx context.Context, id int64) (LLMMessage, error) {
	var m LLMMessage
	err := s.DB.QueryRowContext(ctx, `SELECT `+llmMsgCols+` FROM llm_messages WHERE id = ?`, id).
		Scan(&m.ID, &m.ChatID, &m.Role, &m.Model, &m.Content, &m.Status, &m.Error, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return m, err
}

// AddLLMTurn stores the person's message and an empty streaming reply from model, and makes model the
// chat's current one. It returns the reply's id; ErrBusy when a reply is still streaming.
func (s *Store) AddLLMTurn(ctx context.Context, chatID int64, model, text string, attach ...int64) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM llm_messages WHERE chat_id = ? AND status = 'streaming'`, chatID).Scan(&n); err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, ErrBusy
	}
	now := Now()
	uid, err := insertID(ctx, tx, `INSERT INTO llm_messages (chat_id, role, model, content, status, created_at, updated_at)
		VALUES (?, 'user', '', ?, 'done', ?, ?)`, chatID, text, now, now)
	if err != nil {
		return 0, err
	}
	for _, a := range attach {
		if _, err := tx.ExecContext(ctx, `INSERT INTO llm_attachments (message_id, asset_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, uid, a); err != nil {
			return 0, err
		}
	}
	id, err := insertID(ctx, tx, `INSERT INTO llm_messages (chat_id, role, model, content, status, created_at, updated_at)
		VALUES (?, 'assistant', ?, '', 'streaming', ?, ?)`, chatID, model, now, now)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE llm_chats SET model = ?, updated_at = ? WHERE id = ?`, model, now, chatID); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// SetLLMReply records a reply's text and status. A finished reply is queued for the search index.
func (s *Store) SetLLMReply(ctx context.Context, id int64, content, status, errMsg string) error {
	now := Now()
	if _, err := s.DB.ExecContext(ctx, `UPDATE llm_messages SET content = ?, status = ?, error = ?, updated_at = ? WHERE id = ?`,
		content, status, errMsg, now, id); err != nil {
		return err
	}
	if status != "streaming" {
		if err := MarkDirty(ctx, s.DB, "l", id); err != nil {
			return err
		}
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE llm_chats SET updated_at = ? WHERE id = (SELECT chat_id FROM llm_messages WHERE id = ?)`, now, id)
	return err
}

// FailStaleReplies marks replies left streaming by a hub that stopped as interrupted.
func (s *Store) FailStaleReplies(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE llm_messages SET status = 'error', error = 'interrupted: the hub restarted', updated_at = ?
		WHERE status = 'streaming'`, Now())
	return err
}

// LLMIndexMessage is a plain-chat message with its chat's title, for the search index.
type LLMIndexMessage struct {
	LLMMessage
	Title string
}

const llmIdxCols = `m.id, m.chat_id, m.role, m.model, m.content, m.status, m.error, m.created_at, c.title`

func (s *Store) llmIdx(ctx context.Context, q string, args ...any) ([]LLMIndexMessage, error) {
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMIndexMessage
	for rows.Next() {
		var m LLMIndexMessage
		if err := rows.Scan(&m.ID, &m.ChatID, &m.Role, &m.Model, &m.Content, &m.Status, &m.Error, &m.CreatedAt, &m.Title); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// LLMMessagesAfter pages plain-chat messages in id order (the search index).
func (s *Store) LLMMessagesAfter(ctx context.Context, after int64, limit int) ([]LLMIndexMessage, error) {
	return s.llmIdx(ctx, `SELECT `+llmIdxCols+` FROM llm_messages m JOIN llm_chats c ON c.id = m.chat_id
		WHERE m.id > ? ORDER BY m.id LIMIT ?`, after, limit)
}

func (s *Store) LLMMessagesByID(ctx context.Context, ids []int64) ([]LLMIndexMessage, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q, args := inList(`SELECT `+llmIdxCols+` FROM llm_messages m JOIN llm_chats c ON c.id = m.chat_id WHERE m.id IN `, ids)
	return s.llmIdx(ctx, q, args...)
}

// LLMAttachments: the files attached to a chat's messages, by message id.
func (s *Store) LLMAttachments(ctx context.Context, chatID int64) (map[int64][]Asset, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT l.message_id, a.id, a.host, a.path, a.name, a.size, a.sha256, a.source, a.created_at
		FROM llm_attachments l JOIN assets a ON a.id = l.asset_id JOIN llm_messages m ON m.id = l.message_id
		WHERE m.chat_id = ? ORDER BY l.message_id, a.id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]Asset{}
	for rows.Next() {
		var mid int64
		var a Asset
		if err := rows.Scan(&mid, &a.ID, &a.Host, &a.Path, &a.Name, &a.Size, &a.SHA256, &a.Source, &a.CreatedAt); err != nil {
			return nil, err
		}
		out[mid] = append(out[mid], a)
	}
	return out, rows.Err()
}
