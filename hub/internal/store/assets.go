package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
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

var pathKeyRe = regexp.MustCompile(`"(file_path|notebook_path|path)"\s*:\s*"((?:[^"\\]|\\.)*)"`)

// toolPaths: the files one tool call writes, created or edited. Works on tool_input cut short by ingestion
// (a big Write's content is truncated, but its path comes first). Relative paths resolve against cwd.
func toolPaths(tool, input, cwd string) (paths []string, created []bool) {
	add := func(p string, c bool) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~") {
			if cwd == "" {
				return
			}
			p = filepath.Join(cwd, p)
		}
		paths, created = append(paths, filepath.Clean(p)), append(created, c)
	}
	if key, ok := fileTools[tool]; ok {
		var m map[string]any
		p := ""
		if json.Unmarshal([]byte(input), &m) == nil {
			p, _ = m[key].(string)
		} else {
			for _, sm := range pathKeyRe.FindAllStringSubmatch(input, -1) {
				if sm[1] == key {
					_ = json.Unmarshal([]byte(`"`+sm[2]+`"`), &p)
					break
				}
			}
		}
		add(p, tool == "Write" || tool == "write_file" || tool == "create_file")
		return
	}
	if strings.Contains(input, "*** Begin Patch") { // codex apply_patch
		for _, l := range strings.Split(strings.ReplaceAll(input, `\n`, "\n"), "\n") {
			l = strings.TrimSpace(l)
			if p, ok := strings.CutPrefix(l, "*** Add File: "); ok {
				add(p, true)
			} else if p, ok := strings.CutPrefix(l, "*** Update File: "); ok {
				add(p, false)
			}
		}
	}
	return
}

// SessionFiles lists the files a session and its subagents wrote or edited, most recent first.
func (s *Store) SessionFiles(ctx context.Context, sess Session) ([]SessionFile, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT m.id, m.tool_name, m.tool_input, se.cwd FROM messages m JOIN sessions se ON se.id = m.session_id
		WHERE m.kind = 'tool_use' AND (se.id = ? OR (se.host = ? AND se.parent_uuid = ? AND ? != '')) ORDER BY m.id`,
		sess.ID, sess.Host, sess.UUID, sess.UUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]int{}
	var out []SessionFile
	note := func(p string, created bool, id int64) {
		if i, ok := seen[p]; ok {
			out[i].LastMsg, out[i].Created = id, out[i].Created || created
			return
		}
		seen[p] = len(out)
		out = append(out, SessionFile{Path: p, Created: created, LastMsg: id})
	}
	for rows.Next() {
		var id int64
		var tool, input, cwd string
		if err := rows.Scan(&id, &tool, &input, &cwd); err != nil {
			return nil, err
		}
		ps, cs := toolPaths(tool, input, cwd)
		for i := range ps {
			note(ps[i], cs[i], id)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastMsg > out[j].LastMsg })
	return out, rows.Err()
}

// KeptAt is the newest kept copy of host:path, if any.
func (s *Store) KeptAt(ctx context.Context, host, path string) (Asset, error) {
	return scanAsset(s.DB.QueryRowContext(ctx, `SELECT `+assetCols+` FROM assets WHERE host = ? AND path = ? ORDER BY id DESC LIMIT 1`, host, path))
}

// WrittenDocs returns the documents created by tool calls after a message id, as (host, path), and the last
// message id scanned: Write calls and codex patches that add a file.
func (s *Store) WrittenDocs(ctx context.Context, after int64, limit int) ([][2]string, int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT m.id, se.host, se.cwd, m.tool_name, m.tool_input FROM messages m JOIN sessions se ON se.id = m.session_id
		WHERE m.id > ? AND m.kind = 'tool_use' AND (m.tool_name IN ('Write', 'write_file', 'create_file') OR m.tool_input LIKE '%*** Add File: %')
		ORDER BY m.id LIMIT ?`, after, limit)
	if err != nil {
		return nil, after, err
	}
	defer rows.Close()
	var out [][2]string
	last := after
	for rows.Next() {
		var id int64
		var host, cwd, tool, input string
		if err := rows.Scan(&id, &host, &cwd, &tool, &input); err != nil {
			return nil, after, err
		}
		last = id
		ps, cs := toolPaths(tool, input, cwd)
		for i, p := range ps {
			if cs[i] && DocLike(p) {
				out = append(out, [2]string{host, p})
			}
		}
	}
	return out, last, rows.Err()
}

// QueueAssets adds files for the harvester to copy (again, if already queued: tries start over).
func (s *Store) QueueAssets(ctx context.Context, items [][2]string) error {
	for _, it := range items {
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO asset_queue (host, path, tries, next_at) VALUES (?, ?, 0, '')
			ON CONFLICT (host, path) DO UPDATE SET tries = 0, next_at = ''`, it[0], it[1]); err != nil {
			return err
		}
	}
	return nil
}

// DueAssets: queued copies whose retry time has come, oldest attempts first.
func (s *Store) DueAssets(ctx context.Context, limit int) ([][2]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT host, path FROM asset_queue WHERE next_at <= ? ORDER BY tries, next_at LIMIT ?`, Now(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var h, p string
		if err := rows.Scan(&h, &p); err != nil {
			return nil, err
		}
		out = append(out, [2]string{h, p})
	}
	return out, rows.Err()
}

// DoneAsset drops a queued copy (copied, or never copyable).
func (s *Store) DoneAsset(ctx context.Context, host, path string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM asset_queue WHERE host = ? AND path = ?`, host, path)
	return err
}

// RetryAsset pushes a queued copy back with backoff (1, 2, 4… minutes, up to 6 h); after 30 tries it's dropped.
func (s *Store) RetryAsset(ctx context.Context, host, path string, tries int) error {
	if tries >= 30 {
		return s.DoneAsset(ctx, host, path)
	}
	wait := time.Minute << min(tries, 9)
	if wait > 6*time.Hour {
		wait = 6 * time.Hour
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE asset_queue SET tries = ?, next_at = ? WHERE host = ? AND path = ?`,
		tries+1, T(time.Now().Add(wait)), host, path)
	return err
}

// AssetTries is how many times a queued copy has failed.
func (s *Store) AssetTries(ctx context.Context, host, path string) int {
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT tries FROM asset_queue WHERE host = ? AND path = ?`, host, path).Scan(&n)
	return n
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
