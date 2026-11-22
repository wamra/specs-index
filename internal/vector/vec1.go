package vector

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// rebuildSQL puts chunk_vec into flat (exact) mode with cosine distance. The
// setting is stored in the table, so it is issued whenever the table is
// created or emptied.
const rebuildSQL = `INSERT INTO chunk_vec(cmd, arg) VALUES('rebuild', '{index:"flat", distance:"cos"}')`

// vec1 is a Store over the vec1 virtual table chunk_vec (rowid = chunk id).
type vec1 struct{ db *sql.DB }

// NewVec1 returns the vec1-backed store over db, which must have the schema
// of internal/store applied. Metadata is stored lower-cased, so filters match
// case-insensitively.
func NewVec1(db *sql.DB) Store { return &vec1{db: db} }

func encode(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func decode(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func (s *vec1) Upsert(ctx context.Context, id int64, vec []float32, typ, status string) error {
	if len(vec) == 0 {
		return fmt.Errorf("vector %d: empty vector", id)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A table that is empty has no index configuration yet (or lost it):
	// without it vec1 ranks by Euclidean distance.
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM chunk_vec`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := tx.ExecContext(ctx, rebuildSQL); err != nil {
			return fmt.Errorf("vector %d: %w", id, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunk_vec WHERE rowid = ?`, id); err != nil {
		return fmt.Errorf("vector %d: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chunk_vec(rowid, vector, type, status) VALUES (?, ?, ?, ?)`,
		id, encode(vec), norm(typ), norm(status)); err != nil {
		return fmt.Errorf("vector %d: %w", id, err)
	}
	return tx.Commit()
}

func (s *vec1) Delete(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM chunk_vec WHERE rowid = ?`, id)
	return err
}

func (s *vec1) count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM chunk_vec`).Scan(&n)
	return n, err
}

func (s *vec1) KNN(ctx context.Context, vec []float32, k int, f Filter) ([]Match, error) {
	if k <= 0 {
		return nil, nil
	}
	n, err := s.count(ctx)
	if err != nil || n == 0 {
		return nil, err
	}
	// Plain "col = ?" comparisons on metadata columns are evaluated inside the
	// vec1 search, so K applies after filtering.
	q := fmt.Sprintf(`SELECT rowid, vec1_cos_distance(?1, vector) AS d FROM chunk_vec(?1, '{K:%d}') WHERE 1 = 1`, k)
	args := []any{encode(vec)}
	if t := norm(f.Type); t != "" {
		args = append(args, t)
		q += fmt.Sprintf(` AND type = ?%d`, len(args))
	}
	if st := norm(f.Status); st != "" {
		args = append(args, st)
		q += fmt.Sprintf(` AND status = ?%d`, len(args))
	}
	q += ` ORDER BY d, rowid`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	defer rows.Close()
	var out []Match
	for rows.Next() {
		var m Match
		if err := rows.Scan(&m.ID, &m.Distance); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *vec1) Reset(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// DELETE keeps the fixed dimension; dropping the table frees it.
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS chunk_vec`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE VIRTUAL TABLE chunk_vec USING vec1(vector, type, status)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, rebuildSQL); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *vec1) Get(ctx context.Context, id int64) (Entry, bool, error) {
	var b []byte
	var e Entry
	var typ, status sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT vector, type, status FROM chunk_vec WHERE rowid = ?`, id).Scan(&b, &typ, &status)
	if err == sql.ErrNoRows {
		return e, false, nil
	}
	if err != nil {
		return e, false, err
	}
	e.Vec, e.Type, e.Status = decode(b), typ.String, status.String
	return e, true, nil
}

func (s *vec1) Metas(ctx context.Context) (map[int64][2]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT rowid, type, status FROM chunk_vec`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][2]string{}
	for rows.Next() {
		var id int64
		var typ, status sql.NullString
		if err := rows.Scan(&id, &typ, &status); err != nil {
			return nil, err
		}
		out[id] = [2]string{typ.String, status.String}
	}
	return out, rows.Err()
}

func (s *vec1) SetMeta(ctx context.Context, id int64, typ, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chunk_vec SET type = ?, status = ? WHERE rowid = ?`, norm(typ), norm(status), id)
	return err
}
