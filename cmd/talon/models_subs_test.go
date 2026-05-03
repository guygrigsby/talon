package main

import (
	"reflect"
	"testing"
)

func TestReadFallbacksReader_FromMergedJSON(t *testing.T) {
	// readFallbacks reads via resolvePaths() which is hard to stub
	// without rebuilding the global flag wiring; the gjson logic is
	// the meaningful unit, so test it via a parallel helper that
	// takes raw bytes. (If readFallbacks is later refactored to
	// accept paths, swap to that.)
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"present", `{"agents":{"defaults":{"model":{"fallbacks":["a","b","c"]}}}}`, []string{"a", "b", "c"}},
		{"empty array", `{"agents":{"defaults":{"model":{"fallbacks":[]}}}}`, []string{}},
		{"missing path", `{}`, nil},
		{"trims empty entries", `{"agents":{"defaults":{"model":{"fallbacks":["a","",  "  ","b"]}}}}`, []string{"a", "b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseFallbacksFromBytes([]byte(c.raw))
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("parseFallbacksFromBytes:\n got: %#v\nwant: %#v", got, c.want)
			}
		})
	}
}

func TestReadAliases_FromMergedJSON(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []aliasPair
	}{
		{
			"present",
			`{"agents":{"defaults":{"models":{"openai/gpt-5":{"alias":"smart"},"deepseek/r1":{"alias":"fast"}}}}}`,
			[]aliasPair{{Alias: "smart", Model: "openai/gpt-5"}, {Alias: "fast", Model: "deepseek/r1"}},
		},
		{
			"skip entries without alias",
			`{"agents":{"defaults":{"models":{"openai/gpt-5":{},"deepseek/r1":{"alias":"fast"}}}}}`,
			[]aliasPair{{Alias: "fast", Model: "deepseek/r1"}},
		},
		{"missing path", `{}`, []aliasPair{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseAliasesFromBytes([]byte(c.raw))
			// Order from gjson.ForEach over an object isn't
			// guaranteed; sort by alias before compare.
			sortAliases(got)
			sortAliases(c.want)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("parseAliasesFromBytes:\n got: %#v\nwant: %#v", got, c.want)
			}
		})
	}
}
