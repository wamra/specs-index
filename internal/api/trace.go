package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/wamra/specs-index/internal/graph"
	"github.com/wamra/specs-index/internal/model"
	"github.com/wamra/specs-index/internal/store"
)

// TraceOptions are the flags of trace.
type TraceOptions struct {
	Direction       string // down (default), up, both
	Depth           int    // 0 means the default (5)
	Kinds           []string
	IncludeInferred bool
}

// TraceNode is one entity in the trace tree. Via is empty on the root,
// otherwise "<kind> forward" or "<kind> reversed". Path and Line locate the
// entity's definition; LinkPath and LinkLine the line where the link that
// reached it is written (empty on the root and for inferred links). Repeat
// nodes are not expanded; Missing nodes are edge ends no entity defines.
type TraceNode struct {
	Key      string      `json:"key"`
	Type     string      `json:"type"`
	Title    string      `json:"title"`
	Path     string      `json:"path"`
	Line     int         `json:"line"`
	LinkPath string      `json:"link_path"`
	LinkLine int         `json:"link_line"`
	Via      string      `json:"via"`
	Explicit bool        `json:"explicit"`
	Repeat   bool        `json:"repeat"`
	Missing  bool        `json:"missing"`
	Children []TraceNode `json:"children"`
}

// Trace walks the links from key. An unknown key returns
// *graph.UnknownKeyError (with near matches).
func Trace(ctx context.Context, st *store.Store, key string, o TraceOptions) (TraceNode, error) {
	if err := ValidateTraceOptions(o); err != nil {
		return TraceNode{}, err
	}
	n, err := graph.Trace(ctx, st, key, graph.TraceOpts{
		Direction: o.Direction, Depth: o.Depth, Kinds: o.Kinds, IncludeInferred: o.IncludeInferred,
	})
	if err != nil {
		return TraceNode{}, err
	}
	return convertNode(n), nil
}

func convertNode(n *graph.Node) TraceNode {
	t := TraceNode{
		Key: n.Key, Type: n.Type, Title: n.Title, Path: n.Path, Line: n.Line, LinkPath: n.LinkPath, LinkLine: n.LinkLine, Via: n.Via,
		Explicit: n.Explicit, Repeat: n.Repeat, Missing: n.Missing, Children: make([]TraceNode, 0, len(n.Children)),
	}
	for _, c := range n.Children {
		t.Children = append(t.Children, convertNode(c))
	}
	return t
}

// ValidateTraceOptions rejects an unknown direction or edge kind with
// ErrInvalidArgument.
func ValidateTraceOptions(o TraceOptions) error {
	switch o.Direction {
	case "", "down", "up", "both":
	default:
		return fmt.Errorf("%w: invalid direction %q (want down, up or both)", ErrInvalidArgument, o.Direction)
	}
	for _, k := range o.Kinds {
		if !model.IsEdgeKind(k) {
			return fmt.Errorf("%w: unknown edge kind %q (valid: %s)", ErrInvalidArgument, k, strings.Join(model.EdgeKinds, ", "))
		}
	}
	return nil
}
