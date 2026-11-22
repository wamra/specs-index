// Package gherkin is a hand-written parser for English-keyword Gherkin
// .feature files. It turns one file into a model.Doc: a feature entity, one
// entity per scenario (with its steps) and verified_by edges from ID tags.
package gherkin

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/wamra/specs-index/internal/model"
)

var (
	idTagRe   = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[0-9]+$`)
	langRe    = regexp.MustCompile(`^#\s*language\s*:\s*(\S+)`)
	stepWords = []string{"Given", "When", "Then", "And", "But", "*"}
)

const idTagPrefix = "id:"

// tag is one tag token (without the leading "@") and its 1-based line.
type tag struct {
	name string
	line int
}

// scenario collects one Scenario / Scenario Outline while it is being read.
type scenario struct {
	title      string
	start, end int
	tags       []tag // scenario-level tags, then Examples tags
	rule, feat []tag // inherited tags
	steps      []model.Step
	body       []string
}

type parser struct {
	path     string
	doc      model.Doc
	feature  int // index into doc.Entities, -1 before the Feature line
	featTags []tag
	ruleTags []tag
	pending  []tag
	cur      *scenario
	seen     map[string]int
	featDesc bool
}

// Parse reads one feature file. path is the root-relative, forward-slash
// path; it is the feature's key and the prefix of scenario keys. Parse does
// no file I/O. A "# language:" header other than "en" is an error.
func Parse(path string, src []byte) (model.Doc, error) {
	p := &parser{path: path, feature: -1, seen: map[string]int{}}
	p.doc = model.Doc{Path: path, Type: "feature"}

	text := strings.TrimPrefix(string(src), "\xef\xbb\xbf")
	lines := strings.Split(text, "\n")
	lastContent := 0
	docDelim, docLine := "", 0
	for i, raw := range lines {
		no := i + 1
		line := strings.TrimRight(raw, "\r")
		t := strings.TrimSpace(line)
		if t != "" {
			lastContent = no
		}

		if docDelim != "" { // inside a doc string: nothing is parsed
			if strings.HasPrefix(t, docDelim) {
				docDelim, docLine = "", 0
			}
			p.addBody(t, no)
			continue
		}
		switch {
		case t == "":
		case strings.HasPrefix(t, "#"):
			if p.feature < 0 {
				if m := langRe.FindStringSubmatch(t); m != nil && m[1] != "en" {
					return model.Doc{}, fmt.Errorf("%s:%d: unsupported # language: %q (only en is supported)", path, no, m[1])
				}
			}
		case t[0] == '@':
			p.pending = append(p.pending, parseTags(t, no)...)
		case t[0] == '|':
			p.addBody(t, no)
		case strings.HasPrefix(t, `"""`) || strings.HasPrefix(t, "```"):
			docDelim, docLine = t[:3], no
			p.addBody(t, no)
		default:
			p.keywordLine(t, no)
		}
	}
	if docDelim != "" {
		// Everything after an unclosed delimiter would be swallowed silently.
		return model.Doc{}, fmt.Errorf("%s:%d: doc string opened with %s is never closed", path, docLine, docDelim)
	}
	p.finishScenario()
	if p.feature >= 0 {
		p.doc.Entities[p.feature].EndLine = lastContent
	}
	return p.doc, nil
}

func parseTags(t string, no int) []tag {
	var out []tag
	for _, f := range strings.Fields(t) {
		if strings.HasPrefix(f, "#") {
			break
		}
		if name := strings.TrimPrefix(f, "@"); strings.HasPrefix(f, "@") && name != "" {
			out = append(out, tag{name, no})
		}
	}
	return out
}

// afterKeyword returns the text after "<word>:" when t starts with it.
func afterKeyword(t, word string) (string, bool) {
	if !strings.HasPrefix(t, word+":") {
		return "", false
	}
	return strings.TrimSpace(t[len(word)+1:]), true
}

func (p *parser) keywordLine(t string, no int) {
	if name, ok := afterKeyword(t, "Feature"); ok {
		p.finishScenario()
		if p.feature >= 0 {
			return // a second Feature line is not valid Gherkin; ignore it
		}
		start := no
		if len(p.pending) > 0 {
			start = p.pending[0].line
		}
		p.featTags = p.pending
		p.pending = nil
		p.doc.Entities = append(p.doc.Entities, model.Entity{
			Key: p.path, Type: "feature", Title: name,
			Tags: plain(p.featTags), StartLine: start,
		})
		p.feature = len(p.doc.Entities) - 1
		p.featDesc = true
		return
	}
	if _, ok := afterKeyword(t, "Rule"); ok {
		p.finishScenario()
		p.ruleTags, p.pending = p.pending, nil
		p.featDesc = p.feature >= 0
		p.addFeatureBody(t)
		return
	}
	if _, ok := afterKeyword(t, "Background"); ok {
		p.finishScenario()
		p.pending = nil
		p.featDesc = p.feature >= 0
		p.addFeatureBody(t)
		return
	}
	for _, w := range []string{"Scenario Outline", "Scenario Template", "Scenario", "Example"} {
		if name, ok := afterKeyword(t, w); ok {
			p.finishScenario()
			start := no
			if len(p.pending) > 0 {
				start = p.pending[0].line
			}
			p.cur = &scenario{
				title: name, start: start, end: no, tags: p.pending,
				rule: p.ruleTags, feat: p.featTags,
			}
			p.pending = nil
			p.featDesc = false
			return
		}
	}
	for _, w := range []string{"Examples", "Scenarios"} {
		if _, ok := afterKeyword(t, w); ok {
			if p.cur != nil {
				p.cur.tags = append(p.cur.tags, p.pending...)
				p.addBody(t, no)
			}
			p.pending = nil
			return
		}
	}
	for _, w := range stepWords {
		if rest, ok := strings.CutPrefix(t, w+" "); ok {
			p.addStep(w, strings.TrimSpace(rest), no)
			return
		}
	}
	// Free description text under Feature, Rule, Background or a scenario.
	switch {
	case p.cur != nil:
		p.addBody(t, no)
	default:
		p.addFeatureBody(t)
	}
}

// addFeatureBody appends a line to the feature entity's body. It collects the
// feature description plus Background and Rule text, which have no entity of
// their own.
func (p *parser) addFeatureBody(t string) {
	if !p.featDesc || p.feature < 0 {
		return
	}
	e := &p.doc.Entities[p.feature]
	if e.Body != "" {
		e.Body += "\n"
	}
	e.Body += t
}

func (p *parser) addStep(word, text string, no int) {
	if p.cur == nil { // Background steps belong to no scenario
		p.addFeatureBody(word + " " + text)
		return
	}
	p.cur.steps = append(p.cur.steps, model.Step{
		Seq: len(p.cur.steps) + 1, Keyword: word, Text: text, Line: no,
	})
	p.addBody(word+" "+text, no)
}

// addBody appends a line of scenario content and extends the scenario span.
func (p *parser) addBody(t string, no int) {
	if p.cur == nil {
		p.addFeatureBody(t)
		return
	}
	p.cur.body = append(p.cur.body, t)
	p.cur.end = no
}

// plain returns the tag names that are neither ID tags nor @id: overrides.
func plain(ts []tag) []string {
	var out []string
	for _, t := range ts {
		if !idTagRe.MatchString(t.name) && !strings.HasPrefix(t.name, idTagPrefix) {
			out = append(out, t.name)
		}
	}
	return out
}

func (p *parser) finishScenario() {
	s := p.cur
	if s == nil {
		return
	}
	p.cur = nil

	key := p.path + "#" + s.title
	for _, t := range s.tags {
		if id, ok := strings.CutPrefix(t.name, idTagPrefix); ok && id != "" {
			key = id
		}
	}
	base := key
	p.seen[base]++
	if n := p.seen[base]; n > 1 {
		key = fmt.Sprintf("%s~%d", base, n)
	}

	all := append(append(append([]tag{}, s.feat...), s.rule...), s.tags...)
	var tags []string
	for _, n := range plain(all) {
		dup := false
		for _, x := range tags {
			dup = dup || x == n
		}
		if !dup {
			tags = append(tags, n)
		}
	}
	seenSrc := map[string]bool{}
	for _, t := range all {
		if idTagRe.MatchString(t.name) && !seenSrc[t.name] {
			seenSrc[t.name] = true
			p.doc.Edges = append(p.doc.Edges, model.Edge{
				Src: t.name, Dst: key, Kind: "verified_by",
				Line: t.line, Explicit: true, Origin: "tag",
			})
		}
	}
	p.doc.Entities = append(p.doc.Entities, model.Entity{
		Key: key, Type: "scenario", Title: s.title,
		Body: strings.Join(s.body, "\n"), Tags: tags,
		StartLine: s.start, EndLine: s.end, Steps: s.steps,
	})
}
