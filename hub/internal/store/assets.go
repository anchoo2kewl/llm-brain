package store

import (
	"context"
	"database/sql"
	"errors"
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
