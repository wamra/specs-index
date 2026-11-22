// Package model holds the types shared by the parsers, the indexer and the
// query layer.
package model

// Step is one Gherkin step of a scenario.
type Step struct {
	Seq     int
	Keyword string
	Text    string
	Line    int
}

// Entity is anything with a key that can be searched and linked.
// Type is one of: requirement, design, acceptance, feature, scenario.
type Entity struct {
	Key, Type, Title, Body, Status, Priority string
	Tags                                     []string
	StartLine, EndLine                       int
	Steps                                    []Step
}

// EdgeKinds are the explicit link kinds, in the order they are documented.
var EdgeKinds = []string{"parent_of", "implements", "verified_by", "depends_on", "conflicts_with", "references"}

// IsEdgeKind reports whether k is one of EdgeKinds.
func IsEdgeKind(k string) bool {
	for _, e := range EdgeKinds {
		if e == k {
			return true
		}
	}
	return false
}

// Edge is a directed link between two entity keys.
// Origin is one of: field, table, tag, mention, inferred.
type Edge struct {
	Src, Dst, Kind string
	Line           int
	Explicit       bool
	Origin         string
}

// Repeat is a later definition of a key already defined earlier in the same
// file (Line), which the parser folded into the first one (FirstLine).
type Repeat struct {
	Key             string
	Line, FirstLine int
}

// Doc is one parsed source file. Repeats lists keys defined more than once in
// it.
type Doc struct {
	Path, Type, Version string
	Entities            []Entity
	Edges               []Edge
	Repeats             []Repeat
}
