// Package vector stores embeddings and answers nearest-neighbour queries.
package vector

import "context"

// Filter restricts a KNN query by the metadata stored beside each vector.
// An empty field matches everything.
type Filter struct{ Type, Status string }

// Match is one KNN result: ID is the chunk id the vector was stored under and
// Distance the cosine distance to the query (0 = same direction).
type Match struct {
	ID       int64
	Distance float64
}

// Entry is a stored vector with its metadata.
type Entry struct {
	Vec          []float32
	Type, Status string
}

// Store keeps one vector per chunk id.
type Store interface {
	// Upsert stores vec under id with its type/status metadata, replacing any
	// earlier vector with that id.
	Upsert(ctx context.Context, id int64, vec []float32, typ, status string) error
	// Delete removes the vector stored under id; a missing id is not an error.
	Delete(ctx context.Context, id int64) error
	// KNN returns up to k nearest vectors, nearest first. An empty store
	// returns no matches.
	KNN(ctx context.Context, vec []float32, k int, f Filter) ([]Match, error)
	// Reset removes every vector and the fixed dimension.
	Reset(ctx context.Context) error
	// Get returns the vector stored under id.
	Get(ctx context.Context, id int64) (Entry, bool, error)
	// Metas returns the {type, status} metadata of every stored vector.
	Metas(ctx context.Context) (map[int64][2]string, error)
	// SetMeta rewrites the metadata of the vector stored under id.
	SetMeta(ctx context.Context, id int64, typ, status string) error
}
