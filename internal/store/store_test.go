package store

import (
	"encoding/binary"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", ".Wammy", "specs.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestOpenCreatesSchema(t *testing.T) {
	s, _ := openTemp(t)
	for _, name := range []string{
		"sources", "documents", "entities", "steps", "edges", "chunks",
		"meta", "findings", "chunk_fts", "id_fts", "chunk_vec",
	} {
		var got string
		err := s.DB().QueryRow(`SELECT name FROM sqlite_master WHERE name = ?`, name).Scan(&got)
		if err != nil {
			t.Errorf("table %s missing: %v", name, err)
		}
	}
	for key, want := range map[string]string{"schema_version": "1", "contract_version": "1"} {
		got, err := s.Meta(key)
		if err != nil || got != want {
			t.Errorf("Meta(%s) = %q, %v; want %q", key, got, err, want)
		}
	}
	var mode string
	if err := s.DB().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v; want wal", mode, err)
	}
}

func TestEntityKeyUnique(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.DB().Exec(`INSERT INTO documents(id, path, type) VALUES (1, 'a.md', 'brd')`); err != nil {
		t.Fatal(err)
	}
	ins := `INSERT INTO entities(doc_id, key, type, title) VALUES (1, 'BRD-001', 'requirement', 't')`
	if _, err := s.DB().Exec(ins); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(ins); err == nil {
		t.Fatal("duplicate entities.key accepted")
	}
}

func TestMetaRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	if v, err := s.Meta("nope"); err != nil || v != "" {
		t.Errorf("Meta(missing) = %q, %v; want empty, nil", v, err)
	}
	if err := s.SetMeta("k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta("k", "v2"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Meta("k"); v != "v2" {
		t.Errorf("Meta(k) = %q; want v2", v)
	}
}

func ftsCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	err := s.DB().QueryRow(`SELECT count(*) FROM chunk_fts WHERE chunk_fts MATCH 'retrieval NOT gherkin'`).Scan(&n)
	if err != nil {
		t.Fatalf("fts query: %v", err)
	}
	return n
}

func TestFTSAndVec1Work(t *testing.T) {
	s, _ := openTemp(t)
	db := s.DB()
	if _, err := db.Exec(`INSERT INTO chunk_fts(rowid, title, body, steps, tags) VALUES
		(1, 'hybrid', 'hybrid retrieval with exact id precedence', '', ''),
		(2, 'gherkin', 'retrieval of gherkin scenario steps', '', '')`); err != nil {
		t.Fatal(err)
	}
	if n := ftsCount(t, s); n != 1 {
		t.Errorf("fts count = %d; want 1", n)
	}
	if _, err := db.Exec(`INSERT INTO id_fts(key, path) VALUES ('BRD-001', 'docs/a.md')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM id_fts WHERE id_fts MATCH '"BRD-001"'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("trigram match = %d, %v; want 1", n, err)
	}

	r := rand.New(rand.NewSource(1))
	vecs := make([][]byte, 2000)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range vecs {
		vecs[i] = randVec(r, 768)
		if _, err := tx.Exec(`INSERT INTO chunk_vec(rowid, vector, type, status) VALUES (?, ?, ?, ?)`,
			i+1, vecs[i], i%3, i%2); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO chunk_vec(cmd, arg) VALUES ('rebuild', '{index:"flat", distance:"cos"}')`); err != nil {
		t.Fatal(err)
	}
	top := func(q []byte) (int64, bool) {
		var id int64
		err := db.QueryRow(`SELECT rowid FROM chunk_vec(?, '{K:1}')`, q).Scan(&id)
		return id, err == nil
	}
	if id, ok := top(vecs[76]); !ok || id != 77 {
		t.Errorf("top-1 for stored row 77 = %d, %v", id, ok)
	}
	if _, err := db.Exec(`DELETE FROM chunk_vec WHERE rowid = 77`); err != nil {
		t.Fatal(err)
	}
	if id, ok := top(vecs[76]); ok && id == 77 {
		t.Error("row 77 still returned after DELETE")
	}
	rows, err := db.Query(`SELECT rowid, type FROM chunk_vec(?, '{K:10}') WHERE type = 1`, vecs[0])
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := 0
	for rows.Next() {
		var id, typ int64
		if err := rows.Scan(&id, &typ); err != nil {
			t.Fatal(err)
		}
		if typ != 1 {
			t.Errorf("row %d has type %d; want 1", id, typ)
		}
		got++
	}
	if got == 0 {
		t.Error("filtered query returned no rows")
	}
}

func randVec(r *rand.Rand, dims int) []byte {
	b := make([]byte, dims*4)
	for i := 0; i < dims; i++ {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(float32(r.NormFloat64())))
	}
	return b
}

func TestEveryPooledConnectionHasExtensions(t *testing.T) {
	s, _ := openTemp(t)
	db := s.DB()
	db.SetMaxOpenConns(4)
	var wg, acquired sync.WaitGroup
	errs := make(chan error, 8)
	ready := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		acquired.Add(1)
		go func() {
			defer wg.Done()
			conn, err := db.Conn(t.Context())
			acquired.Done()
			if err != nil {
				errs <- err
				return
			}
			defer conn.Close()
			<-ready // hold all four connections open at once
			var n int
			if err := conn.QueryRowContext(t.Context(),
				`SELECT count(*) FROM chunk_fts WHERE chunk_fts MATCH 'retrieval NOT gherkin'`).Scan(&n); err != nil {
				errs <- err
				return
			}
			var c int
			if err := conn.QueryRowContext(t.Context(),
				`SELECT count(*) FROM chunk_vec`).Scan(&c); err != nil {
				errs <- err
			}
		}()
	}
	acquired.Wait() // every goroutine now holds its own connection (pool max is 4)
	if st := db.Stats(); st.InUse != 4 && len(errs) == 0 {
		t.Errorf("InUse = %d; want 4 distinct connections held at once", st.InUse)
	}
	close(ready)
	wg.Wait()
	close(errs)
	for err := range errs {
		if strings.Contains(err.Error(), "no such module") {
			t.Errorf("extension missing on a pooled connection: %v", err)
		} else {
			t.Errorf("unexpected: %v", err)
		}
	}
}

func TestCloseCheckpoints(t *testing.T) {
	s, path := openTemp(t)
	if err := s.SetMeta("x", "y"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() != 0 {
		t.Errorf("-wal has %d bytes after Close", fi.Size())
	}
}

func TestOpenReadOnly(t *testing.T) {
	s, path := openTemp(t)
	if err := s.SetMeta("k", "v"); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if v, err := ro.Meta("k"); err != nil || v != "v" {
		t.Errorf("Meta = %q, %v", v, err)
	}
	if err := ro.SetMeta("k", "w"); err == nil {
		t.Error("write succeeded on read-only store")
	}
	var n int
	if err := ro.DB().QueryRow(`SELECT count(*) FROM chunk_fts WHERE chunk_fts MATCH 'x'`).Scan(&n); err != nil {
		t.Errorf("fts on read-only: %v", err)
	}
	if _, err := OpenReadOnly(filepath.Join(t.TempDir(), "missing.sqlite")); err == nil {
		t.Error("OpenReadOnly on missing file succeeded")
	}
}

func TestPathWithSpecialChars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a b#c%d?e", "specs.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.Close()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("db not at expected path: %v", err)
	}
}
