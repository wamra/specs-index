package api

import (
	"context"

	"github.com/wamra/specs-index/internal/graph"
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
// otherwise "<kind> forward" or "<kind> reversed". Repeat nodes are not
// expanded; Missing nodes are edge ends no entity defines.
type TraceNode struct {
	Key      string      `json:"key"`
	Type     string      `json:"type"`
	Title    string      `json:"title"`
	Path     string      `json:"path"`
	Line     int         `json:"line"`
	Via      string      `json:"via"`
	Explicit bool        `json:"explicit"`
	Repeat   bool        `json:"repeat"`
	Missing  bool        `json:"missing"`
	Children []TraceNode `json:"children"`
}

// Trace walks the links from key. An unknown key returns
// *graph.UnknownKeyError (with near matches).
func Trace(ctx context.Context, st *store.Store, key string, o TraceOptions) (TraceNode, error) {
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
		Key: n.Key, Type: n.Type, Title: n.Title, Path: n.Path, Line: n.Line, Via: n.Via,
		Explicit: n.Explicit, Repeat: n.Repeat, Missing: n.Missing, Children: make([]TraceNode, 0, len(n.Children)),
	}
	for _, c := range n.Children {
		t.Children = append(t.Children, convertNode(c))
	}
	return t
}
