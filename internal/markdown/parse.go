// Package markdown is a line-based parser for requirement and design
// documents: ID headings, pipe tables keyed by ID, field lines under a
// heading, fenced code blocks and front matter. It does no file I/O.
package markdown

import (
	"regexp"
	"strings"

	"github.com/wamra/specs-index/internal/model"
)

// DefaultTypes maps an ID prefix to an entity type.
var DefaultTypes = map[string]string{"BRD": "requirement", "FDS": "design", "AC": "acceptance"}

var (
	idRe        = regexp.MustCompile(`[A-Z][A-Z0-9]*-[0-9]+`)
	fullIDRe    = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[0-9]+$`)
	headingRe   = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.*?)[ \t]*$`)
	closingHash = regexp.MustCompile(`[ \t]+#+$`)
	fenceOpenRe = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")
	fieldSplit  = regexp.MustCompile(`\t+| {2,}`)
	sepCellRe   = regexp.MustCompile(`^:?-+:?$`)
)

// fieldNames are the keys recognised as field lines under a heading.
var fieldNames = map[string]bool{
	"status": true, "priority": true, "owner": true, "parent": true,
	"implements": true, "mapped brds": true, "brd": true, "brds": true,
	"verified by": true, "scenarios": true, "fds": true,
	"depends on": true, "conflicts with": true,
}

// work is an entity under construction.
type work struct {
	model.Entity
	level       int // heading level; 0 for table rows
	fromHeading bool
	statusHead  bool // Status came from a heading's field line
	priorityHd  bool // Priority came from a heading's field line
	lines       []string
	edges       []model.Edge
	owner       *work // set on the shadow of a repeated heading
}

type parser struct {
	prefixTypes map[string]string
	lines       []string
	out         []*work
	shadows     []*work
	byKey       map[string]*work
	stack       []*work // open heading entities, outermost first
}

// Parse parses one Markdown file. path is stored in Doc.Path. types maps ID
// prefix to entity type; nil or empty selects DefaultTypes, and an unknown
// prefix is a requirement.
func Parse(path string, src []byte, types map[string]string) (model.Doc, error) {
	if len(types) == 0 {
		types = DefaultTypes
	}
	text := strings.TrimPrefix(string(src), "\xEF\xBB\xBF")
	text = strings.TrimSuffix(text, "\n")
	var lines []string
	if len(src) > 0 {
		lines = strings.Split(text, "\n")
		for i, l := range lines {
			lines[i] = strings.TrimSuffix(l, "\r")
		}
	}
	p := &parser{prefixTypes: types, lines: lines, byKey: map[string]*work{}}
	doc := model.Doc{Path: path}
	start := 0
	if n := frontMatterEnd(lines); n > 0 {
		doc.Type, doc.Version = frontMatterFields(lines[1 : n-1])
		start = n
	}
	p.run(start)
	for _, s := range p.shadows {
		s.owner.edges = append(s.owner.edges, s.edges...)
	}
	for _, w := range p.out {
		w.Entity.Body = strings.TrimSpace(strings.Join(w.lines, "\n"))
		doc.Entities = append(doc.Entities, w.Entity)
		doc.Edges = append(doc.Edges, w.edges...)
	}
	doc.Edges = dedupe(doc.Edges)
	return doc, nil
}

// frontMatterEnd returns the number of lines the front matter occupies
// (both fences included), or 0 when there is none.
func frontMatterEnd(lines []string) int {
	if len(lines) == 0 || strings.TrimRight(lines[0], " \t") != "---" {
		return 0
	}
	for i := 1; i < len(lines); i++ {
		t := strings.TrimRight(lines[i], " \t")
		if t == "---" || t == "..." {
			return i + 1
		}
	}
	return 0
}

func frontMatterFields(lines []string) (typ, version string) {
	for _, l := range lines {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "type":
			typ = v
		case "version":
			version = v
		}
	}
	return
}

func (p *parser) typeOf(key string) string {
	if t, ok := p.prefixTypes[prefixOf(key)]; ok {
		return t
	}
	return "requirement"
}

func prefixOf(key string) string {
	prefix, _, _ := strings.Cut(key, "-")
	return prefix
}

func (p *parser) run(start int) {
	var (
		inFence    bool
		fenceCh    byte
		fenceLen   int
		fieldOwner *work
	)
	for i := start; i < len(p.lines); i++ {
		line, no := p.lines[i], i+1
		if inFence {
			p.addBody(line, no, false)
			if closesFence(line, fenceCh, fenceLen) {
				inFence = false
			}
			continue
		}
		if m := fenceOpenRe.FindStringSubmatch(line); m != nil &&
			!(m[1][0] == '`' && strings.Contains(m[2], "`")) {
			inFence, fenceCh, fenceLen = true, m[1][0], len(m[1])
			fieldOwner = nil
			p.addBody(line, no, false)
			continue
		}
		if m := headingRe.FindStringSubmatch(line); m != nil {
			fieldOwner = p.heading(len(m[1]), m[2], no)
			continue
		}
		if fieldOwner != nil {
			if p.fieldLine(fieldOwner, line, no) {
				continue
			}
			fieldOwner = nil
		}
		if n := p.table(i); n > 0 {
			i += n - 1
			continue
		}
		p.addBody(line, no, true)
	}
	p.closeTo(0, len(p.lines))
}

func closesFence(line string, ch byte, n int) bool {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 {
		return false
	}
	t = strings.TrimRight(t, " \t")
	return len(t) >= n && strings.Trim(t, string(ch)) == ""
}

// heading handles an ATX heading. It returns the new entity when the heading
// starts with an ID, so field lines directly below it can be read.
func (p *parser) heading(level int, text string, no int) *work {
	p.closeTo(level, no-1)
	text = closingHash.ReplaceAllString(text, "")
	loc := idRe.FindStringIndex(text)
	if loc == nil || loc[0] != 0 || !boundaryAfter(text, loc[1]) {
		return nil
	}
	key := text[:loc[1]]
	title := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(text[loc[1]:]), ":-–—"))

	w, ok := p.byKey[key]
	switch {
	case !ok:
		w = p.newWork(key, title, no, no)
		w.fromHeading = true
	case !w.fromHeading:
		// A table row came first: the heading wins span, title and body.
		w.Title, w.StartLine, w.EndLine = title, no, no
		w.fromHeading = true
		w.lines = nil
	default:
		// The same ID heads two sections: the first keeps the entity, the
		// second only contributes its edges.
		s := &work{owner: w}
		s.Key = key
		p.shadows = append(p.shadows, s)
		w = s
	}
	w.level = level
	if parent := p.top(); parent != nil && prefixOf(parent.Key) == prefixOf(key) {
		w.edges = append(w.edges, model.Edge{Src: parent.Key, Dst: key,
			Kind: "parent_of", Line: no, Explicit: true, Origin: "field"})
	}
	p.stack = append(p.stack, w)
	return w
}

func (p *parser) newWork(key, title string, start, end int) *work {
	w := &work{}
	w.Key, w.Type = key, p.typeOf(key)
	w.Title, w.StartLine, w.EndLine = title, start, end
	p.byKey[key] = w
	p.out = append(p.out, w)
	return w
}

func (p *parser) top() *work {
	if len(p.stack) == 0 {
		return nil
	}
	return p.stack[len(p.stack)-1]
}

// closeTo ends every open heading entity of level >= level at line end,
// dropping trailing blank lines from the span.
func (p *parser) closeTo(level, end int) {
	for len(p.stack) > 0 && p.top().level >= level {
		w := p.top()
		p.stack = p.stack[:len(p.stack)-1]
		for end > w.StartLine && strings.TrimSpace(p.lines[end-1]) == "" {
			end--
		}
		if w.owner == nil {
			w.EndLine = end
		}
	}
}

// addBody appends a line to the innermost open entity and, for ordinary text,
// records ID mentions as references edges.
func (p *parser) addBody(line string, no int, scan bool) {
	w := p.top()
	if w == nil {
		return
	}
	if w.owner == nil {
		w.lines = append(w.lines, line)
	}
	if scan {
		p.mentions(w, line, no)
	}
}

func (p *parser) mentions(w *work, text string, no int) {
	for _, id := range findIDs(text) {
		if id != w.Key {
			w.edges = append(w.edges, model.Edge{Src: w.Key, Dst: id,
				Kind: "references", Line: no, Explicit: true, Origin: "mention"})
		}
	}
}

// findIDs returns every ID in s that is not part of a longer word.
func findIDs(s string) []string {
	var ids []string
	for _, loc := range idRe.FindAllStringIndex(s, -1) {
		if loc[0] > 0 && isWordByte(s[loc[0]-1]) {
			continue
		}
		if !boundaryAfter(s, loc[1]) {
			continue
		}
		ids = append(ids, s[loc[0]:loc[1]])
	}
	return ids
}

func isWordByte(b byte) bool {
	return b == '_' || b == '-' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func boundaryAfter(s string, end int) bool { return end >= len(s) || !isWordByte(s[end]) }

// fieldLine reads "Key: value" segments (several per line are separated by
// two or more spaces or a tab). It reports whether the line was a field line.
func (p *parser) fieldLine(w *work, line string, no int) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	type kv struct{ k, v string }
	var pairs []kv
	for _, seg := range fieldSplit.Split(strings.TrimSpace(line), -1) {
		k, v, ok := strings.Cut(seg, ":")
		k = strings.ToLower(strings.TrimSpace(k))
		if !ok || !fieldNames[k] {
			return false
		}
		pairs = append(pairs, kv{k, strings.TrimSpace(v)})
	}
	for _, f := range pairs {
		p.relation(w, f.k, f.v, no, "field")
	}
	return true
}

// relation applies one field-line key or table column to w. It reports
// whether the name is a known field.
func (p *parser) relation(w *work, name, value string, no int, origin string) bool {
	add := func(src, dst, kind string) {
		if src != dst {
			w.edges = append(w.edges, model.Edge{Src: src, Dst: dst, Kind: kind,
				Line: no, Explicit: true, Origin: origin})
		}
	}
	fromHeading := origin == "field"
	switch name {
	case "status":
		if w.owner == nil && (w.Status == "" || fromHeading && !w.statusHead) {
			w.Status, w.statusHead = value, fromHeading
		}
	case "priority":
		if w.owner == nil && (w.Priority == "" || fromHeading && !w.priorityHd) {
			w.Priority, w.priorityHd = value, fromHeading
		}
	case "owner":
	case "parent":
		for _, id := range findIDs(value) {
			add(id, w.Key, "parent_of")
		}
	case "implements", "mapped brds", "brd", "brds":
		for _, id := range findIDs(value) {
			add(w.Key, id, "implements")
		}
	case "verified by", "scenarios":
		for _, id := range findIDs(value) {
			add(w.Key, id, "verified_by")
		}
	case "fds":
		if w.Type != "acceptance" {
			return false
		}
		for _, id := range findIDs(value) {
			add(id, w.Key, "verified_by")
		}
	case "depends on":
		for _, id := range findIDs(value) {
			add(w.Key, id, "depends_on")
		}
	case "conflicts with":
		for _, id := range findIDs(value) {
			add(w.Key, id, "conflicts_with")
		}
	default:
		return false
	}
	return true
}

// splitRow splits a pipe-table row into trimmed cells.
func splitRow(line string) (cells []string, ok bool) {
	t := strings.TrimSpace(line)
	if !strings.Contains(t, "|") {
		return nil, false
	}
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")
	var cur strings.Builder
	for i := 0; i < len(t); i++ {
		switch {
		case t[i] == '\\' && i+1 < len(t) && t[i+1] == '|':
			cur.WriteByte('|')
			i++
		case t[i] == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(t[i])
		}
	}
	return append(cells, strings.TrimSpace(cur.String())), true
}

func isSeparator(cells []string) bool {
	for _, c := range cells {
		if !sepCellRe.MatchString(c) {
			return false
		}
	}
	return len(cells) > 0
}

func stripMarks(s string) string { return strings.Trim(strings.TrimSpace(s), "`*_") }

// table handles a pipe table whose header row is line index i. It returns
// the number of lines consumed, or 0 when the lines are not a table with at
// least one row whose first column holds an ID.
func (p *parser) table(i int) int {
	header, ok := splitRow(p.lines[i])
	if !ok || i+1 >= len(p.lines) {
		return 0
	}
	if sep, ok := splitRow(p.lines[i+1]); !ok || !isSeparator(sep) {
		return 0
	}
	end := i + 2
	idRows := 0
	for ; end < len(p.lines); end++ {
		cells, ok := splitRow(p.lines[end])
		if !ok {
			break
		}
		if fullIDRe.MatchString(stripMarks(cells[0])) {
			idRows++
		}
	}
	if idRows == 0 {
		return 0
	}
	cols := make([]string, len(header))
	for c, h := range header {
		cols[c] = strings.ToLower(stripMarks(h))
	}
	for j := i + 2; j < end; j++ {
		cells, _ := splitRow(p.lines[j])
		if !fullIDRe.MatchString(stripMarks(cells[0])) {
			p.addBody(p.lines[j], j+1, true)
			continue
		}
		p.row(cols, header, cells, j+1)
	}
	return end - i
}

// row turns one table row into an entity (or merges it into the entity of the
// same ID) and applies its columns by header name.
func (p *parser) row(cols, header, cells []string, no int) {
	key := stripMarks(cells[0])
	w, existed := p.byKey[key]
	if !existed {
		w = p.newWork(key, "", no, no)
	}
	owns := !existed // this row supplies the title and extra text
	for c := 1; c < len(cells) && c < len(cols); c++ {
		val := cells[c]
		if p.relation(w, cols[c], val, no, "table") {
			continue
		}
		if c == 1 && owns {
			w.Title = val
			continue
		}
		if val == "" {
			continue
		}
		if owns {
			w.lines = append(w.lines, header[c]+": "+val)
		}
		p.mentions(w, val, no)
	}
}

// dedupe removes repeated (src, dst, kind) edges, keeping the first.
func dedupe(in []model.Edge) []model.Edge {
	type k struct{ s, d, kind string }
	seen := map[k]bool{}
	var out []model.Edge
	for _, e := range in {
		key := k{e.Src, e.Dst, e.Kind}
		if !seen[key] {
			seen[key] = true
			out = append(out, e)
		}
	}
	return out
}
