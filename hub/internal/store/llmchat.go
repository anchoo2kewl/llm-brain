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

// LLMChats lists chats, newest activity first; q filters on the title and the messages.
func (s *Store) LLMChats(ctx context.Context, q string, limit int) ([]LLMChat, error) {
	query := `SELECT id, title, model, created_at, updated_at FROM llm_chats`
	var args []any
	if q = strings.TrimSpace(q); q != "" {
		like := "%" + strings.NewReplacer("%", "", "_", "").Replace(strings.ToLower(q)) + "%"
		query += ` WHERE lower(title) LIKE ? OR lower(model) LIKE ? OR id IN (SELECT chat_id FROM llm_messages WHERE lower(content) LIKE ?)`
		args = append(args, like, like, like)
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
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
func (s *Store) AddLLMTurn(ctx context.Context, chatID int64, model, text string) (int64, error) {
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
	if _, err := insertID(ctx, tx, `INSERT INTO llm_messages (chat_id, role, model, content, status, created_at, updated_at)
		VALUES (?, 'user', '', ?, 'done', ?, ?)`, chatID, text, now, now); err != nil {
		return 0, err
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

// SetLLMReply records a reply's text and status.
func (s *Store) SetLLMReply(ctx context.Context, id int64, content, status, errMsg string) error {
	now := Now()
	if _, err := s.DB.ExecContext(ctx, `UPDATE llm_messages SET content = ?, status = ?, error = ?, updated_at = ? WHERE id = ?`,
		content, status, errMsg, now, id); err != nil {
		return err
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
