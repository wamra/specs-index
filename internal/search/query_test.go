package search

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseQuery(t *testing.T) {
	cases := []struct {
		in   string
		want Query
	}{
		{"resume interrupted upload", Query{Text: "resume interrupted upload"}},
		{`"legal hold" retention`, Query{Text: `"legal hold" retention`}},
		{"type:requirement", Query{Filters: []Filter{{Field: "type", Value: "requirement"}}}},
		{"legal hold -tag:wip", Query{Text: "legal hold", Filters: []Filter{{Field: "tag", Value: "wip", Negate: true}}}},
		{`upload -"dark mode"`, Query{Text: "upload", Exclude: []string{"dark mode"}}},
		{"oauth AND token OR NOT expired", Query{Text: "oauth AND token OR NOT expired"}},
		{"has:scenario", Query{Has: []string{"scenario"}}},
		{"type:requirement -has:fds", Query{Filters: []Filter{{Field: "type", Value: "requirement"}}, HasNot: []string{"fds"}}},
		{"BRD-004", Query{IDs: []string{"BRD-004"}}},
		{"BRD-004 upload FDS-002", Query{Text: "upload", IDs: []string{"BRD-004", "FDS-002"}}},
		{`path:"docs/my dir" status:approved`, Query{Filters: []Filter{{Field: "path", Value: "docs/my dir"}, {Field: "status", Value: "approved"}}}},
		{"oauth status:approved type:requirement -has:scenario", Query{Text: "oauth", Filters: []Filter{{Field: "status", Value: "approved"}, {Field: "type", Value: "requirement"}}, HasNot: []string{"scenario"}}},
		{"single sign-on", Query{Text: `single "sign-on"`}},
		{"note:this", Query{Text: `"note:this"`}},
		{"", Query{}},
	}
	for _, c := range cases {
		got, err := ParseQuery(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q:\n got  %+v\n want %+v", c.in, got, c.want)
		}
	}
}

func TestParseQueryErrors(t *testing.T) {
	for _, in := range []string{`"unbalanced phrase`, "has:nothing", "-has:bogus", `-"open`} {
		_, err := ParseQuery(in)
		if err == nil {
			t.Errorf("%q: want error", in)
			continue
		}
		if !strings.Contains(err.Error(), in) {
			t.Errorf("%q: error %q does not name the query", in, err)
		}
	}
}
