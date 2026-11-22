// Package store owns the per-project SQLite index: connection setup and schema.
package store

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
	"github.com/ncruces/go-sqlite3/ext/vec1"
)

//go:embed schema.sql
var schemaSQL string

// Store wraps the index database handle.
type Store struct {
	db *sql.DB

	closeOnce sync.Once
	closeErr  error
}

// dsn builds the "file:" URI for path. The path is percent-encoded and written
// without a "//" authority, so a Windows path (C:\...) and a path containing
// '%', '#' or '?' reach SQLite unchanged.
func dsn(path string, q url.Values) string {
	return "file:" + (&url.URL{Path: path}).EscapedPath() + "?" + q.Encode()
}

// initConn runs on every new pooled connection: it registers the FTS5 and
// vec1 extensions, which are per-connection in this driver.
func initConn(c *sqlite3.Conn) error {
	if err := fts5.Register(c); err != nil {
		return fmt.Errorf("register fts5: %w", err)
	}
	if err := vec1.Register(c); err != nil {
		return fmt.Errorf("register vec1: %w", err)
	}
	return nil
}

func openDB(path string, q url.Values) (*sql.DB, error) {
	db, err := driver.Open(dsn(path, q), initConn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Open opens (creating if needed) the index at path, creating parent
// directories, and applies the schema.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(30000)")
	q.Add("_pragma", "journal_mode(wal)")
	db, err := openDB(path, q)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// DB returns the underlying database handle.
func (s *Store) DB() *sql.DB { return s.db }

// Close checkpoints the WAL (TRUNCATE), then closes the database. It is safe
// to call more than once.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		if _, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			s.closeErr = fmt.Errorf("checkpoint: %w", err)
		}
		s.closeErr = errors.Join(s.closeErr, s.db.Close())
	})
	return s.closeErr
}

// Meta returns the value stored under key, or "" if the key is absent.
func (s *Store) Meta(key string) (string, error) {
	var v sql.NullString
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v.String, err
}

// SetMeta inserts or replaces the value stored under key.
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO meta(key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
