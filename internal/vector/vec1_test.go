package vector

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/wamra/specs-index/internal/store"
)

func newStore(t *testing.T) Store {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return NewVec1(st.DB())
}

func ids(ms []Match) []int64 {
	var out []int64
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func eq(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestVec1UpsertDeleteKNN(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)

	// KNN on an empty store is empty, not an error.
	ms, err := vs.KNN(ctx, []float32{1, 0, 0, 0}, 3, Filter{})
	if err != nil || len(ms) != 0 {
		t.Fatalf("empty KNN = %v, %v", ms, err)
	}

	for id, v := range map[int64][]float32{
		1: {1, 0, 0, 0},
		2: {0.9, 0.1, 0, 0},
		3: {0, 1, 0, 0},
		4: {0, 0, 1, 0},
	} {
		if err := vs.Upsert(ctx, id, v, "requirement", "draft"); err != nil {
			t.Fatalf("upsert %d: %v", id, err)
		}
	}
	ms, err = vs.KNN(ctx, []float32{1, 0, 0, 0}, 3, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(ms); !eq(got, []int64{1, 2, 3}) {
		t.Fatalf("KNN order = %v, want [1 2 3]", got)
	}
	if ms[0].Distance > 1e-6 {
		t.Errorf("distance to itself = %v, want ~0", ms[0].Distance)
	}
	if math.Abs(ms[2].Distance-1) > 1e-3 {
		t.Errorf("distance to orthogonal = %v, want ~1 (cosine)", ms[2].Distance)
	}

	// Upsert replaces the vector.
	if err := vs.Upsert(ctx, 4, []float32{1, 0.01, 0, 0}, "requirement", "draft"); err != nil {
		t.Fatal(err)
	}
	ms, _ = vs.KNN(ctx, []float32{1, 0, 0, 0}, 2, Filter{})
	if got := ids(ms); !eq(got, []int64{1, 4}) && !eq(got, []int64{4, 1}) {
		t.Fatalf("after replace KNN = %v, want 1 and 4", got)
	}
	got, ok, err := vs.Get(ctx, 4)
	if err != nil || !ok || got.Vec[1] != 0.01 || got.Type != "requirement" || got.Status != "draft" {
		t.Fatalf("Get(4) = %+v ok=%v err=%v", got, ok, err)
	}

	// Delete removes it; deleting a missing id is fine.
	if err := vs.Delete(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := vs.Delete(ctx, 999); err != nil {
		t.Fatal(err)
	}
	ms, _ = vs.KNN(ctx, []float32{1, 0, 0, 0}, 10, Filter{})
	for _, m := range ms {
		if m.ID == 1 {
			t.Fatalf("deleted id 1 still returned: %v", ids(ms))
		}
	}
	if _, ok, _ := vs.Get(ctx, 1); ok {
		t.Error("Get(1) found a deleted vector")
	}
	if len(ms) != 3 {
		t.Errorf("K larger than the table returned %d rows, want 3", len(ms))
	}
}

func TestKNNFilter(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	rows := []struct {
		id        int64
		v         []float32
		typ, stat string
	}{
		{1, []float32{1, 0, 0}, "requirement", "approved"},
		{2, []float32{0.99, 0.1, 0}, "design", "approved"},
		{3, []float32{0.98, 0.2, 0}, "requirement", "draft"},
		{4, []float32{0.97, 0.3, 0}, "design", "draft"},
	}
	for _, r := range rows {
		if err := vs.Upsert(ctx, r.id, r.v, r.typ, r.stat); err != nil {
			t.Fatal(err)
		}
	}
	q := []float32{1, 0, 0}
	cases := []struct {
		name string
		f    Filter
		want []int64
	}{
		{"type", Filter{Type: "design"}, []int64{2, 4}},
		{"status", Filter{Status: "draft"}, []int64{3, 4}},
		{"both", Filter{Type: "design", Status: "draft"}, []int64{4}},
		{"none", Filter{Type: "scenario"}, nil},
	}
	// K=1: the nearest row overall (1) is a requirement, so the filter must act
	// inside the search, not on the top K afterwards.
	if ms, err := vs.KNN(ctx, q, 1, Filter{Type: "design"}); err != nil || !eq(ids(ms), []int64{2}) {
		t.Fatalf("K=1 with type design = %v, %v; want [2]", ms, err)
	}
	for _, c := range cases {
		ms, err := vs.KNN(ctx, q, 10, c.f)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := ids(ms); !eq(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMetaRefreshAndReset(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	if err := vs.Upsert(ctx, 1, []float32{1, 0}, "requirement", "draft"); err != nil {
		t.Fatal(err)
	}
	if err := vs.SetMeta(ctx, 1, "requirement", "approved"); err != nil {
		t.Fatal(err)
	}
	ms, _ := vs.KNN(ctx, []float32{1, 0}, 5, Filter{Status: "approved"})
	if got := ids(ms); !eq(got, []int64{1}) {
		t.Fatalf("after SetMeta KNN(approved) = %v", got)
	}
	ms, _ = vs.KNN(ctx, []float32{1, 0}, 5, Filter{Status: "draft"})
	if len(ms) != 0 {
		t.Fatalf("stale status still matches: %v", ids(ms))
	}
	metas, err := vs.Metas(ctx)
	if err != nil || metas[1] != [2]string{"requirement", "approved"} {
		t.Fatalf("Metas = %v, %v", metas, err)
	}

	// Reset empties the table and frees the dimension.
	if err := vs.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if metas, _ := vs.Metas(ctx); len(metas) != 0 {
		t.Fatalf("Metas after Reset = %v", metas)
	}
	if err := vs.Upsert(ctx, 7, []float32{1, 0, 0, 0, 0}, "design", ""); err != nil {
		t.Fatalf("upsert with a new dimension after Reset: %v", err)
	}
	ms, err = vs.KNN(ctx, []float32{1, 0, 0, 0, 0}, 1, Filter{})
	if err != nil || !eq(ids(ms), []int64{7}) {
		t.Fatalf("KNN after Reset = %v, %v", ms, err)
	}
}

// TestKNNUsesCosine: with unnormalised vectors the Euclidean and cosine
// nearest neighbours differ, so K=1 shows which metric the table uses.
func TestKNNUsesCosine(t *testing.T) {
	ctx := context.Background()
	vs := newStore(t)
	if err := vs.Upsert(ctx, 1, []float32{10, 1}, "requirement", ""); err != nil {
		t.Fatal(err)
	}
	if err := vs.Upsert(ctx, 2, []float32{0.5, 0.5}, "requirement", ""); err != nil {
		t.Fatal(err)
	}
	ms, err := vs.KNN(ctx, []float32{1, 0}, 1, Filter{})
	if err != nil || !eq(ids(ms), []int64{1}) {
		t.Fatalf("K=1 neighbour = %v, %v; want 1 (cosine), 2 would be Euclidean", ms, err)
	}
	// rows added after the first ones are searched too
	if err := vs.Upsert(ctx, 3, []float32{100, 0.1}, "requirement", ""); err != nil {
		t.Fatal(err)
	}
	ms, _ = vs.KNN(ctx, []float32{1, 0}, 1, Filter{})
	if !eq(ids(ms), []int64{3}) {
		t.Fatalf("after a later insert K=1 = %v, want 3", ids(ms))
	}
}
