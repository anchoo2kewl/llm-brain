// Package store is the hub's database: Turso (the Rust rewrite of SQLite) through its pure-Go database/sql
// driver. One file, one connection; the usual Turso single-connection pattern.
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "turso.tech/database/tursogo" // registers the "turso" driver (pure Go via purego, no CGO)
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	DB *sql.DB
}

// Open opens (or creates) the database at path (":memory:" for tests) and applies the embedded migrations.
//
// Turso caveats this code works around (tursogo v0.7.2):
//   - no FTS5 module, and the native FTS index method is not compiled into the driver even with
//     ?experimental=index_method, so search is LIKE over the text columns;
//   - one connection only (SetMaxOpenConns(1)): every write is serialized, and readers queue behind a long
//     ingest transaction, so ingestion commits per chunk rather than per file.
func Open(path string) (*Store, error) {
	db, err := sql.Open("turso", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate() error {
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	for _, n := range names {
		var x int
		if err := s.DB.QueryRow(`SELECT 1 FROM schema_migrations WHERE name = ?`, n).Scan(&x); err == nil {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + n)
		if err != nil {
			return err
		}
		for _, stmt := range splitSQL(string(body)) {
			if _, err := s.DB.Exec(stmt); err != nil {
				return fmt.Errorf("migration %s: %w\n%s", n, err, stmt)
			}
		}
		if _, err := s.DB.Exec(`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, n, Now()); err != nil {
			return err
		}
	}
	return nil
}

// splitSQL splits a migration on ';' at line ends; the driver runs one statement per Exec.
func splitSQL(src string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "--") {
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
		if strings.HasSuffix(t, ";") {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
	}
	if rest := strings.TrimSpace(cur.String()); rest != "" {
		out = append(out, rest)
	}
	return out
}

const TimeFormat = "2006-01-02T15:04:05.000Z"

func Now() string { return time.Now().UTC().Format(TimeFormat) }

func T(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(TimeFormat)
}

func ParseT(s string) time.Time {
	t, _ := time.Parse(TimeFormat, s)
	return t
}
