package config

import (
	"reflect"
	"testing"
)

func TestParsePath(t *testing.T) {
	cases := []struct {
		in      string
		want    []string
		wantErr bool
	}{
		{"", nil, false},
		{"a", []string{"a"}, false},
		{"a.b.c", []string{"a", "b", "c"}, false},
		{"a.b[0].c", []string{"a", "b", "0", "c"}, false},
		{`a["b.c"].d`, []string{"a", "b.c", "d"}, false},
		{`a.b\.c`, []string{"a", "b.c"}, false},
		{`agents.list[1].model`, []string{"agents", "list", "1", "model"}, false},
		{`channels.telegram.groups["*"].requireMention`, []string{"channels", "telegram", "groups", "*", "requireMention"}, false},
		{`a.b[].c`, nil, true},
		{`a.b[`, nil, true},
		{`a.__proto__.b`, nil, true},
		{`a['b'].c`, nil, true},
	}
	for _, tc := range cases {
		got, err := ParsePath(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParsePath(%q): expected error, got %v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParsePath(%q): unexpected error: %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParsePath(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestToSjsonPath(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"a", "b"}, "a.b"},
		{[]string{"agents", "list", "1", "model"}, "agents.list.1.model"},
		{[]string{"channels", "telegram", "groups", "*", "requireMention"}, `channels.telegram.groups.\*.requireMention`},
		{[]string{"a", "b.c"}, `a.b\.c`},
		{[]string{"weird", `with#hash`, `with?q`}, `weird.with\#hash.with\?q`},
	}
	for _, tc := range cases {
		got := ToSjsonPath(tc.in)
		if got != tc.want {
			t.Errorf("ToSjsonPath(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
