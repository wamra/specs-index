// Package search parses the query grammar and runs exact-ID and full-text
// search over the index.
package search

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Query is a parsed search query.
//
// Text is the FTS5 part: bare words, quoted phrases and AND/OR/NOT, with
// every other word quoted so punctuation cannot be read as FTS5 syntax.
// Exclude lists phrases from -"phrase" and -word.
type Query struct {
	Text    string
	IDs     []string
	Filters []Filter
	Has     []string
	HasNot  []string
	Exclude []string
}

// Filter is a field:value metadata filter. Field is one of type, status,
// priority, tag, path.
type Filter struct {
	Field, Value string
	Negate       bool
}

var (
	idRe       = regexp.MustCompile(`^[A-Z][A-Z0-9]*-\d+$`)
	plainWord  = regexp.MustCompile(`^[A-Za-z0-9_]+\*?$`)
	filterKeys = map[string]bool{"type": true, "status": true, "priority": true, "tag": true, "path": true}
	hasKeys    = map[string]bool{"scenario": true, "fds": true}
)

func badQuery(s, why string) error {
	return fmt.Errorf("invalid search query (%s): %s", s, why)
}

type token struct {
	neg    bool
	text   string // word, or phrase content
	quoted bool   // text is a quoted phrase
	key    string // for field:"value" forms, the key including the colon
}

// tokenize splits s into words and quoted phrases.
func tokenize(s string) ([]token, error) {
	rs := []rune(s)
	var out []token
	i := 0
	for i < len(rs) {
		if unicode.IsSpace(rs[i]) {
			i++
			continue
		}
		var t token
		if rs[i] == '-' && i+1 < len(rs) && !unicode.IsSpace(rs[i+1]) {
			t.neg = true
			i++
		}
		if rs[i] == '"' {
			end := indexRune(rs, i+1, '"')
			if end < 0 {
				return nil, badQuery(s, "unbalanced quote")
			}
			t.text, t.quoted = string(rs[i+1:end]), true
			i = end + 1
			out = append(out, t)
			continue
		}
		start := i
		for i < len(rs) && !unicode.IsSpace(rs[i]) {
			if rs[i] == '"' && i > start && rs[i-1] == ':' {
				end := indexRune(rs, i+1, '"')
				if end < 0 {
					return nil, badQuery(s, "unbalanced quote")
				}
				t.key = string(rs[start:i])
				t.text, t.quoted = string(rs[i+1:end]), true
				i = end + 1
				break
			}
			i++
		}
		if t.key == "" {
			t.text = string(rs[start:i])
		}
		out = append(out, t)
	}
	return out, nil
}

func indexRune(rs []rune, from int, r rune) int {
	for i := from; i < len(rs); i++ {
		if rs[i] == r {
			return i
		}
	}
	return -1
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// ParseQuery parses the search grammar: free text, "phrases", AND/OR/NOT,
// -"phrase" exclusions, type:/status:/priority:/tag:/path: filters (each
// negated with a leading minus), has:scenario / has:fds (and -has:...), and
// bare IDs such as BRD-004.
func ParseQuery(s string) (Query, error) {
	var q Query
	toks, err := tokenize(s)
	if err != nil {
		return q, err
	}
	var text []string
	for _, t := range toks {
		field, value, isField := t.key, t.text, t.key != ""
		if isField {
			field = strings.TrimSuffix(field, ":")
		} else if !t.quoted {
			field, value, isField = strings.Cut(t.text, ":")
			isField = isField && value != ""
		}
		lower := strings.ToLower(field)
		switch {
		case isField && lower == "has":
			v := strings.ToLower(value)
			if !hasKeys[v] {
				return q, badQuery(s, fmt.Sprintf("unknown has: value %q (use scenario or fds)", value))
			}
			if t.neg {
				q.HasNot = append(q.HasNot, v)
			} else {
				q.Has = append(q.Has, v)
			}
		case isField && filterKeys[lower]:
			q.Filters = append(q.Filters, Filter{Field: lower, Value: value, Negate: t.neg})
		case t.neg:
			q.Exclude = append(q.Exclude, t.key+t.text)
		case t.quoted && !isField:
			text = append(text, quote(t.text))
		case t.quoted:
			text = append(text, quote(t.key+":"+t.text))
		case idRe.MatchString(t.text):
			q.IDs = append(q.IDs, t.text)
		case t.text == "AND" || t.text == "OR" || t.text == "NOT" || plainWord.MatchString(t.text):
			text = append(text, t.text)
		default:
			text = append(text, quote(t.text))
		}
	}
	q.Text = strings.Join(text, " ")
	return q, nil
}
