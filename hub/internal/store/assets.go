package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// Asset is the hub's copy of a file from a host (migration 0017).
type Asset struct {
	ID        int64
	Host      string
	Path      string
	Name      string
	Size      int64
	SHA256    string
	Source    string
	CreatedAt string
}

const assetCols = `id, host, path, name, size, sha256, source, created_at`

func scanAsset(sc interface{ Scan(...any) error }) (Asset, error) {
	var a Asset
	err := sc.Scan(&a.ID, &a.Host, &a.Path, &a.Name, &a.Size, &a.SHA256, &a.Source, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return a, err
}

// AddAsset records a copy; the same bytes from the same place again just return the existing row.
func (s *Store) AddAsset(ctx context.Context, a Asset) (int64, error) {
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO assets (host, path, name, size, sha256, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (host, path, sha256) DO NOTHING`, a.Host, a.Path, a.Name, a.Size, a.SHA256, a.Source, Now()); err != nil {
		return 0, err
	}
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM assets WHERE host = ? AND path = ? AND sha256 = ?`, a.Host, a.Path, a.SHA256).Scan(&id)
	return id, err
}

func (s *Store) Asset(ctx context.Context, id int64) (Asset, error) {
	return scanAsset(s.DB.QueryRowContext(ctx, `SELECT `+assetCols+` FROM assets WHERE id = ?`, id))
}

// Assets lists copies, newest first; q matches name or path.
func (s *Store) Assets(ctx context.Context, q string, limit int) ([]Asset, error) {
	query, args := `SELECT `+assetCols+` FROM assets`, []any{}
	if q = strings.TrimSpace(q); q != "" {
		like := "%" + strings.ToLower(q) + "%"
		query += ` WHERE lower(name) LIKE ? OR lower(path) LIKE ?`
		args = append(args, like, like)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Asset
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SessionFile is a file a session wrote or edited, from its tool calls.
type SessionFile struct {
	Path    string
	Created bool // written whole (Write / Add File), not only edited
	LastMsg int64
}

// fileTools: tool calls that write files, and where their path is.
var fileTools = map[string]string{"Write": "file_path", "Edit": "file_path", "MultiEdit": "file_path", "NotebookEdit": "notebook_path",
	"write_file": "file_path", "replace": "file_path", "edit_file": "path", "create_file": "path"}

// SessionFiles lists the files a session (and its subagents) wrote or edited, most recent first.
func (s *Store) SessionFiles(ctx context.Context, sessionID int64) ([]SessionFile, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, tool_name, tool_input FROM messages WHERE kind = 'tool_use' AND session_id = ?
		ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]int{}
	var out []SessionFile
	note := func(p string, created bool, id int64) {
		p = strings.TrimSpace(p)
		if p == "" || !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~") {
			return
		}
		if i, ok := seen[p]; ok {
			out[i].LastMsg, out[i].Created = id, out[i].Created || created
			return
		}
		seen[p] = len(out)
		out = append(out, SessionFile{Path: p, Created: created, LastMsg: id})
	}
	for rows.Next() {
		var id int64
		var tool, input string
		if err := rows.Scan(&id, &tool, &input); err != nil {
			return nil, err
		}
		if key, ok := fileTools[tool]; ok {
			var m map[string]any
			if json.Unmarshal([]byte(input), &m) == nil {
				if p, _ := m[key].(string); p != "" {
					note(p, tool == "Write" || tool == "write_file" || tool == "create_file", id)
				}
			}
			continue
		}
		if strings.Contains(input, "*** Begin Patch") { // codex apply_patch
			for _, l := range strings.Split(strings.ReplaceAll(input, `\n`, "\n"), "\n") {
				l = strings.TrimSpace(l)
				if p, ok := strings.CutPrefix(l, "*** Add File: "); ok {
					note(p, true, id)
				} else if p, ok := strings.CutPrefix(l, "*** Update File: "); ok {
					note(p, false, id)
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastMsg > out[j].LastMsg })
	return out, rows.Err()
}

// KeptAt is the newest kept copy of host:path, if any.
func (s *Store) KeptAt(ctx context.Context, host, path string) (Asset, error) {
	return scanAsset(s.DB.QueryRowContext(ctx, `SELECT `+assetCols+` FROM assets WHERE host = ? AND path = ? ORDER BY id DESC LIMIT 1`, host, path))
}

// WrittenDocs returns Write tool calls after a message id whose file looks like a document, with the host,
// for the hub to keep a copy of (oldest first), and the last message id scanned.
func (s *Store) WrittenDocs(ctx context.Context, after int64, limit int) ([][2]string, int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT m.id, se.host, m.tool_name, m.tool_input FROM messages m JOIN sessions se ON se.id = m.session_id
		WHERE m.id > ? AND m.kind = 'tool_use' AND m.tool_name IN ('Write', 'write_file', 'create_file') ORDER BY m.id LIMIT ?`, after, limit)
	if err != nil {
		return nil, after, err
	}
	defer rows.Close()
	var out [][2]string
	last := after
	for rows.Next() {
		var id int64
		var host, tool, input string
		if err := rows.Scan(&id, &host, &tool, &input); err != nil {
			return nil, after, err
		}
		last = id
		var m map[string]any
		if json.Unmarshal([]byte(input), &m) != nil {
			continue
		}
		p, _ := m[fileTools[tool]].(string)
		if DocLike(p) {
			out = append(out, [2]string{host, p})
		}
	}
	return out, last, rows.Err()
}

// DocLike: a file worth keeping a copy of on its own (a deliverable), not source code.
func DocLike(p string) bool {
	i := strings.LastIndex(p, ".")
	if i < 0 || !strings.HasPrefix(p, "/") {
		return false
	}
	switch strings.ToLower(p[i+1:]) {
	case "md", "pdf", "csv", "tsv", "txt", "html", "htm", "png", "jpg", "jpeg", "gif", "webp", "svg", "docx", "xlsx", "pptx", "doc", "xls", "ppt", "json", "zip", "log":
		return true
	}
	return false
}
