package indexer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wamra/specs-index/internal/gherkin"
	"github.com/wamra/specs-index/internal/markdown"
	"github.com/wamra/specs-index/internal/model"
	"github.com/wamra/specs-index/internal/store"
)

// Failure is a file that could not be indexed.
type Failure struct {
	Path   string
	Reason string
}

// Report summarises one Run. Scanned counts the files found under the
// sources; Parsed, Unchanged and Failed partition them (failed entries that
// were never read, such as links leaving the root, are not in Scanned).
// Entities and Edges are the totals in the index after the run.
type Report struct {
	Scanned, Parsed, Unchanged, Removed, Failed int
	Entities, Edges                             int
	Failures                                    []Failure
}

// afterDoc, when set by a test, runs after each document of the main pass.
var afterDoc func()

const (
	statusOK     = "ok"
	statusFailed = "failed"
	sniffBytes   = 8000
)

// Run brings the index in step with the files under the registered sources.
func Run(ctx context.Context, st *store.Store, root string) (Report, error) {
	return run(ctx, st, root, false)
}

type docRow struct {
	id          int64
	size, mtime int64
	sha, status string
}

type runner struct {
	ctx       context.Context
	db        *sql.DB
	root      string
	cands     map[string]bool
	pending   map[string]bool // documents to re-parse regardless of their file state
	parsed    map[string]bool
	failed    map[string]bool
	removed   map[string]bool
	failures  []Failure
	pruneOnly bool
}

func run(ctx context.Context, st *store.Store, root string, pruneOnly bool) (Report, error) {
	files, discFails, err := discover(ctx, st, root)
	if err != nil {
		return Report{}, err
	}
	r := &runner{
		ctx: ctx, db: st.DB(), root: root, pruneOnly: pruneOnly,
		cands: map[string]bool{}, pending: map[string]bool{},
		parsed: map[string]bool{}, failed: map[string]bool{}, removed: map[string]bool{},
		failures: discFails,
	}
	for _, f := range files {
		r.cands[f] = true
	}
	known, err := r.knownPaths()
	if err != nil {
		return Report{}, err
	}
	all := map[string]bool{}
	for _, f := range files {
		all[f] = true
	}
	for _, k := range known {
		all[k] = true
	}
	order := make([]string, 0, len(all))
	for p := range all {
		order = append(order, p)
	}
	sort.Strings(order)
	for _, p := range order {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		if err := r.process(p, false); err != nil {
			return Report{}, err
		}
		if afterDoc != nil {
			afterDoc()
		}
	}
	// Documents whose duplicate-ID ownership may have changed.
	for len(r.pending) > 0 {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		next := ""
		for p := range r.pending {
			if next == "" || p < next {
				next = p
			}
		}
		delete(r.pending, next)
		if err := r.process(next, true); err != nil {
			return Report{}, err
		}
	}

	rep := Report{Scanned: len(files), Removed: len(r.removed), Failures: r.failures}
	rep.Failed = len(r.failures)
	touched := 0
	for _, f := range files {
		switch {
		case r.failed[f]:
			touched++
		case r.parsed[f]:
			rep.Parsed++
			touched++
		}
	}
	rep.Unchanged = len(files) - touched
	if err := r.db.QueryRow(`SELECT count(*) FROM entities`).Scan(&rep.Entities); err != nil {
		return rep, err
	}
	if err := r.db.QueryRow(`SELECT count(*) FROM edges`).Scan(&rep.Edges); err != nil {
		return rep, err
	}
	if !pruneOnly {
		if err := st.SetMeta("indexed_at", time.Now().UTC().Format(time.RFC3339)); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

func (r *runner) knownPaths() ([]string, error) {
	rows, err := r.db.Query(`SELECT path FROM documents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *runner) lookup(rel string) (docRow, bool, error) {
	var d docRow
	var sha, status sql.NullString
	var size, mtime sql.NullInt64
	err := r.db.QueryRow(`SELECT id, size, mtime, sha256, status FROM documents WHERE path = ?`, rel).
		Scan(&d.id, &size, &mtime, &sha, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return d, false, nil
	}
	d.size, d.mtime, d.sha, d.status = size.Int64, mtime.Int64, sha.String, status.String
	return d, err == nil, err
}

func (r *runner) inTx(fn func(tx *sql.Tx) error) error {
	tx, err := r.db.BeginTx(r.ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func docKind(rel string) string {
	if strings.EqualFold(filepath.Ext(rel), ".feature") {
		return "feature"
	}
	return "document"
}

// process brings one document in step with its file. force skips the
// unchanged shortcuts.
func (r *runner) process(rel string, force bool) error {
	row, exists, err := r.lookup(rel)
	if err != nil {
		return err
	}
	if !r.cands[rel] {
		if exists {
			return r.remove(rel, row)
		}
		return nil
	}
	if r.pruneOnly && !force {
		return nil
	}
	abs := filepath.Join(r.root, filepath.FromSlash(rel))
	fi, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && exists {
			return r.remove(rel, row)
		}
		return r.fail(rel, exists, row, nil, err.Error())
	}
	if !force && exists && row.status == statusOK && row.size == fi.Size() && row.mtime == fi.ModTime().UnixNano() {
		return nil
	}
	src, err := os.ReadFile(abs)
	if err != nil {
		return r.fail(rel, exists, row, fi, "unreadable: "+err.Error())
	}
	if isBinary(src) {
		return r.fail(rel, exists, row, fi, "binary or non-UTF-8 file")
	}
	sum := sha256.Sum256(src)
	sha := hex.EncodeToString(sum[:])
	if !force && exists && row.status == statusOK && row.sha == sha {
		_, err := r.db.Exec(`UPDATE documents SET size = ?, mtime = ? WHERE id = ?`, fi.Size(), fi.ModTime().UnixNano(), row.id)
		return err
	}
	var doc model.Doc
	if docKind(rel) == "feature" {
		doc, err = gherkin.Parse(rel, src)
	} else {
		doc, err = markdown.Parse(rel, src, nil)
	}
	if err != nil {
		return r.fail(rel, exists, row, fi, "parse: "+err.Error())
	}
	err = r.inTx(func(tx *sql.Tx) error {
		id, err := upsertDoc(tx, rel, doc, fi, sha, statusOK, "")
		if err != nil {
			return err
		}
		return r.storeDoc(tx, id, rel, doc)
	})
	if err != nil {
		return fmt.Errorf("index %s: %w", rel, err)
	}
	r.parsed[rel] = true
	return nil
}

func isBinary(b []byte) bool {
	head := b
	if len(head) > sniffBytes {
		head = head[:sniffBytes]
	}
	return bytes.IndexByte(head, 0) >= 0 || !utf8.Valid(b)
}

func upsertDoc(tx *sql.Tx, rel string, doc model.Doc, fi os.FileInfo, sha, status, reason string) (int64, error) {
	typ := doc.Type
	if typ == "" {
		typ = docKind(rel)
	}
	var size, mtime any
	if fi != nil {
		size, mtime = fi.Size(), fi.ModTime().UnixNano()
	}
	_, err := tx.Exec(`INSERT INTO documents(path, type, version, sha256, mtime, size, indexed_at, status, fail_reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET type=excluded.type, version=excluded.version, sha256=excluded.sha256,
		  mtime=excluded.mtime, size=excluded.size, indexed_at=excluded.indexed_at,
		  status=excluded.status, fail_reason=excluded.fail_reason`,
		rel, typ, doc.Version, sha, mtime, size, time.Now().UTC().Format(time.RFC3339), status, reason)
	if err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRow(`SELECT id FROM documents WHERE path = ?`, rel).Scan(&id)
	return id, err
}

// fail records rel as failed, dropping any rows from an earlier good parse.
func (r *runner) fail(rel string, exists bool, row docRow, fi os.FileInfo, reason string) error {
	err := r.inTx(func(tx *sql.Tx) error {
		id, err := upsertDoc(tx, rel, model.Doc{}, fi, "", statusFailed, reason)
		if err != nil {
			return err
		}
		keys, err := r.clearDoc(tx, id)
		if err != nil {
			return err
		}
		return r.queueDuplicates(tx, keys, id)
	})
	if err != nil {
		return fmt.Errorf("record failure %s: %w", rel, err)
	}
	r.failed[rel] = true
	r.failures = append(r.failures, Failure{Path: rel, Reason: reason})
	return nil
}

func (r *runner) remove(rel string, row docRow) error {
	err := r.inTx(func(tx *sql.Tx) error {
		keys, err := r.clearDoc(tx, row.id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM documents WHERE id = ?`, row.id); err != nil {
			return err
		}
		return r.queueDuplicates(tx, keys, row.id)
	})
	if err != nil {
		return fmt.Errorf("remove %s: %w", rel, err)
	}
	r.removed[rel] = true
	return nil
}

// clearDoc deletes everything a document contributed except its documents
// row, and returns the keys it owned.
func (r *runner) clearDoc(tx *sql.Tx, docID int64) ([]string, error) {
	rows, err := tx.Query(`SELECT id, key FROM entities WHERE doc_id = ?`, docID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	var keys []string
	for rows.Next() {
		var id int64
		var k string
		if err := rows.Scan(&id, &k); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
		keys = append(keys, k)
	}
	rows.Close()
	for _, id := range ids {
		if err := deleteEntity(tx, id); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`DELETE FROM edges WHERE doc_id = ?`, docID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM findings WHERE doc_id = ?`, docID); err != nil {
		return nil, err
	}
	return keys, nil
}

func deleteEntity(tx *sql.Tx, id int64) error {
	for _, q := range []string{
		`DELETE FROM chunk_vec WHERE rowid IN (SELECT id FROM chunks WHERE entity_id = ?1)`,
		`DELETE FROM chunks WHERE entity_id = ?1`,
		`DELETE FROM steps WHERE entity_id = ?1`,
		`DELETE FROM chunk_fts WHERE rowid = ?1`,
		`DELETE FROM id_fts WHERE rowid = ?1`,
		`DELETE FROM entities WHERE id = ?1`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return nil
}

// queue schedules path for a forced re-parse. The document's file state is
// cleared in the same transaction, so if the run stops before the re-parse the
// next run still sees the document as changed.
func (r *runner) queue(tx *sql.Tx, path string) error {
	r.pending[path] = true
	_, err := tx.Exec(`UPDATE documents SET sha256 = NULL, mtime = NULL, size = NULL WHERE path = ?`, path)
	return err
}

// queueDuplicates schedules a re-parse of every other document that defines
// one of keys as a duplicate, so one of them can take the key over.
func (r *runner) queueDuplicates(tx *sql.Tx, keys []string, exceptDoc int64) error {
	for _, k := range keys {
		rows, err := tx.Query(`SELECT d.path FROM findings f JOIN documents d ON d.id = f.doc_id
			WHERE f.kind = 'duplicate' AND f.key = ? AND f.doc_id <> ?`, k, exceptDoc)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return err
			}
			if err := r.queue(tx, p); err != nil {
				rows.Close()
				return err
			}
		}
		rows.Close()
	}
	return nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func chunkText(e model.Entity) string {
	parts := []string{e.Key, e.Title, e.Body}
	for _, s := range e.Steps {
		parts = append(parts, s.Keyword+" "+s.Text)
	}
	if len(e.Tags) > 0 {
		parts = append(parts, strings.Join(e.Tags, " "))
	}
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}

type oldEntity struct {
	id      int64
	chunkID sql.NullInt64
	sha     sql.NullString
}

// storeDoc replaces a document's rows. Entities that keep their key keep
// their row, and their chunk when the chunk text is unchanged.
func (r *runner) storeDoc(tx *sql.Tx, docID int64, rel string, doc model.Doc) error {
	old := map[string]oldEntity{}
	rows, err := tx.Query(`SELECT e.key, e.id, c.id, c.text_sha256 FROM entities e
		LEFT JOIN chunks c ON c.entity_id = e.id WHERE e.doc_id = ?`, docID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k string
		var o oldEntity
		if err := rows.Scan(&k, &o.id, &o.chunkID, &o.sha); err != nil {
			rows.Close()
			return err
		}
		old[k] = o
	}
	rows.Close()
	if _, err := tx.Exec(`DELETE FROM edges WHERE doc_id = ?`, docID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM findings WHERE doc_id = ?`, docID); err != nil {
		return err
	}

	owned := map[string]bool{}
	for _, e := range doc.Entities {
		if owned[e.Key] {
			continue
		}
		var ownerID, ownerDoc int64
		var ownerPath string
		err := tx.QueryRow(`SELECT e.id, e.doc_id, d.path FROM entities e JOIN documents d ON d.id = e.doc_id
			WHERE e.key = ?`, e.Key).Scan(&ownerID, &ownerDoc, &ownerPath)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		case ownerDoc == docID:
		case ownerPath < rel:
			if _, err := tx.Exec(`INSERT INTO findings(kind, key, doc_id, line, detail) VALUES ('duplicate', ?, ?, ?, ?)`,
				e.Key, docID, e.StartLine, "duplicate of "+e.Key+" defined in "+ownerPath); err != nil {
				return err
			}
			continue
		default: // this document sorts first and takes the key over
			if err := deleteEntity(tx, ownerID); err != nil {
				return err
			}
			if err := r.queue(tx, ownerPath); err != nil {
				return err
			}
		}
		owned[e.Key] = true
		if err := storeEntity(tx, docID, rel, e, old); err != nil {
			return err
		}
	}
	var gone []string
	for k, o := range old {
		if owned[k] {
			continue
		}
		if err := deleteEntity(tx, o.id); err != nil {
			return err
		}
		gone = append(gone, k)
	}
	sort.Strings(gone)
	if err := r.queueDuplicates(tx, gone, docID); err != nil {
		return err
	}
	for _, e := range doc.Edges {
		explicit := 0
		if e.Explicit {
			explicit = 1
		}
		if _, err := tx.Exec(`INSERT INTO edges(src_key, dst_key, kind, doc_id, line, explicit, origin) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			e.Src, e.Dst, e.Kind, docID, e.Line, explicit, e.Origin); err != nil {
			return err
		}
	}
	return nil
}

func storeEntity(tx *sql.Tx, docID int64, rel string, e model.Entity, old map[string]oldEntity) error {
	tags := strings.Join(e.Tags, ",")
	o, had := old[e.Key]
	var id int64
	if had {
		id = o.id
		if _, err := tx.Exec(`UPDATE entities SET type=?, title=?, body=?, status=?, priority=?, tags=?,
			start_line=?, end_line=?, body_sha256=? WHERE id=?`,
			e.Type, e.Title, e.Body, e.Status, e.Priority, tags, e.StartLine, e.EndLine, sha256Hex(e.Body), id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM steps WHERE entity_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM chunk_fts WHERE rowid = ?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM id_fts WHERE rowid = ?`, id); err != nil {
			return err
		}
	} else {
		res, err := tx.Exec(`INSERT INTO entities(doc_id, key, type, title, body, status, priority, tags,
			start_line, end_line, body_sha256) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			docID, e.Key, e.Type, e.Title, e.Body, e.Status, e.Priority, tags, e.StartLine, e.EndLine, sha256Hex(e.Body))
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
	}
	var stepText []string
	for _, s := range e.Steps {
		if _, err := tx.Exec(`INSERT INTO steps(entity_id, seq, keyword, text, line) VALUES (?, ?, ?, ?, ?)`,
			id, s.Seq, s.Keyword, s.Text, s.Line); err != nil {
			return err
		}
		stepText = append(stepText, s.Keyword+" "+s.Text)
	}
	text := chunkText(e)
	sum := sha256Hex(text)
	switch {
	case had && o.chunkID.Valid && o.sha.String == sum:
		// chunk unchanged: keep its id and embedding state
	case had && o.chunkID.Valid:
		if _, err := tx.Exec(`DELETE FROM chunk_vec WHERE rowid = ?`, o.chunkID.Int64); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE chunks SET text=?, text_sha256=?, embed_fingerprint='' WHERE id=?`, text, sum, o.chunkID.Int64); err != nil {
			return err
		}
	default:
		if _, err := tx.Exec(`INSERT INTO chunks(entity_id, text, text_sha256, embed_fingerprint) VALUES (?, ?, ?, '')`, id, text, sum); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO chunk_fts(rowid, title, body, steps, tags) VALUES (?, ?, ?, ?, ?)`,
		id, e.Title, e.Body, strings.Join(stepText, "\n"), strings.Join(e.Tags, " ")); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO id_fts(rowid, key, path) VALUES (?, ?, ?)`, id, e.Key, rel)
	return err
}
