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

// Edge is a directed link between two entity keys.
// Origin is one of: field, table, tag, mention, inferred.
type Edge struct {
	Src, Dst, Kind string
	Line           int
	Explicit       bool
	Origin         string
}

// Doc is one parsed source file.
type Doc struct {
	Path, Type, Version string
	Entities            []Entity
	Edges               []Edge
}
