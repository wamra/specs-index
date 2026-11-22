package embed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/wamra/specs-index/internal/vector"
)

// InferMaxDistance is the cosine distance below which the nearest design or
// scenario is suggested as a link.
const InferMaxDistance = 0.25

type suggestion struct {
	src, dst, kind string
	confidence     float64
}

// Infer suggests links from vector similarity and stores them as inferred
// edges (explicit=0, origin='inferred', confidence = 1 - distance), after
// clearing every earlier inferred edge. For each requirement with no explicit
// implements edge it suggests the nearest design (design implements
// requirement); for each design with no explicit verified_by edge, the nearest
// scenario. Suggestions never count toward coverage. It returns the number of
// edges stored.
func Infer(ctx context.Context, st Store, vs vector.Store) (int, error) {
	db := st.DB()
	var sugg []suggestion

	nearest := func(chunkID int64, typ string) (key string, dist float64, ok bool, err error) {
		en, found, err := vs.Get(ctx, chunkID)
		if err != nil || !found {
			return "", 0, false, err
		}
		ms, err := vs.KNN(ctx, en.Vec, 1, vector.Filter{Type: typ})
		if err != nil || len(ms) == 0 || ms[0].Distance >= InferMaxDistance {
			return "", 0, false, err
		}
		err = db.QueryRowContext(ctx, `SELECT e.key FROM chunks c JOIN entities e ON e.id = c.entity_id WHERE c.id = ?`, ms[0].ID).Scan(&key)
		if errors.Is(err, sql.ErrNoRows) { // a vector whose chunk is gone: skip it
			return "", 0, false, nil
		}
		return key, ms[0].Distance, err == nil, err
	}
	type source struct {
		key string
		id  int64
	}
	collect := func(q string) ([]source, error) {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []source
		for rows.Next() {
			var s source
			if err := rows.Scan(&s.key, &s.id); err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, rows.Err()
	}
	reqs, err := collect(`SELECT e.key, c.id FROM entities e JOIN chunks c ON c.entity_id = e.id
		WHERE e.type = 'requirement' AND NOT EXISTS
		(SELECT 1 FROM edges x WHERE x.kind = 'implements' AND x.explicit = 1 AND x.dst_key = e.key) ORDER BY e.key`)
	if err != nil {
		return 0, err
	}
	for _, r := range reqs {
		key, d, ok, err := nearest(r.id, "design")
		if err != nil {
			return 0, err
		}
		if ok {
			sugg = append(sugg, suggestion{src: key, dst: r.key, kind: "implements", confidence: 1 - d})
		}
	}
	designs, err := collect(`SELECT e.key, c.id FROM entities e JOIN chunks c ON c.entity_id = e.id
		WHERE e.type = 'design' AND NOT EXISTS
		(SELECT 1 FROM edges x WHERE x.kind = 'verified_by' AND x.explicit = 1 AND x.src_key = e.key) ORDER BY e.key`)
	if err != nil {
		return 0, err
	}
	for _, d := range designs {
		key, dist, ok, err := nearest(d.id, "scenario")
		if err != nil {
			return 0, err
		}
		if ok {
			sugg = append(sugg, suggestion{src: d.key, dst: key, kind: "verified_by", confidence: 1 - dist})
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM edges WHERE explicit = 0 AND origin = 'inferred'`); err != nil {
		return 0, err
	}
	for _, s := range sugg {
		if _, err := tx.ExecContext(ctx, `INSERT INTO edges(src_key, dst_key, kind, explicit, confidence, origin) VALUES (?, ?, ?, 0, ?, 'inferred')`,
			s.src, s.dst, s.kind, s.confidence); err != nil {
			return 0, fmt.Errorf("store suggestion %s -> %s: %w", s.src, s.dst, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(sugg), nil
}
