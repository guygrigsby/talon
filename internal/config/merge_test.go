package config

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/tidwall/gjson"
)

// asMap parses a JSON byte slice into a map for comparison. Test helper.
func asMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, b)
	}
	return m
}

// --- mergeJSON: structural cases ---------------------------------------------

func TestMergeJSON_DeepNestedObjectMerge(t *testing.T) {
	base := []byte(`{"a":{"b":{"c":1,"d":2},"e":3}}`)
	overlay := []byte(`{"a":{"b":{"c":99},"f":4}}`)
	got, err := mergeJSON(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	want := asMap(t, []byte(`{"a":{"b":{"c":99,"d":2},"e":3,"f":4}}`))
	if !reflect.DeepEqual(asMap(t, got), want) {
		t.Errorf("got %s, want %v", got, want)
	}
}

func TestMergeJSON_TypeMismatch_OverlayWins(t *testing.T) {
	cases := []struct {
		name        string
		base        string
		overlay     string
		wantPath    string
		wantRawJSON string
	}{
		{
			name:        "map_replaced_by_scalar",
			base:        `{"x":{"a":1,"b":2}}`,
			overlay:     `{"x":"replaced"}`,
			wantPath:    "x",
			wantRawJSON: `"replaced"`,
		},
		{
			name:        "scalar_replaced_by_map",
			base:        `{"x":1}`,
			overlay:     `{"x":{"a":1}}`,
			wantPath:    "x",
			wantRawJSON: `{"a":1}`,
		},
		{
			name:        "array_replaced_by_object_when_no_id_keying",
			base:        `{"x":[1,2,3]}`,
			overlay:     `{"x":{"a":1}}`,
			wantPath:    "x",
			wantRawJSON: `{"a":1}`,
		},
		{
			name:        "object_replaced_by_array",
			base:        `{"x":{"a":1}}`,
			overlay:     `{"x":[1,2]}`,
			wantPath:    "x",
			wantRawJSON: `[1,2]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mergeJSON([]byte(tc.base), []byte(tc.overlay))
			if err != nil {
				t.Fatal(err)
			}
			gotRaw := gjson.GetBytes(got, tc.wantPath).Raw
			// Normalize both via re-encode for whitespace-insensitive compare.
			var gotV, wantV any
			if err := json.Unmarshal([]byte(gotRaw), &gotV); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.wantRawJSON), &wantV); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotV, wantV) {
				t.Errorf("got %v, want %v", gotV, wantV)
			}
		})
	}
}

func TestMergeJSON_NullOverlayDoesNotEraseBase(t *testing.T) {
	// JSON null in the overlay is currently treated as "no value" by
	// mergeValues so the base wins. This is intentional: tombstones
	// (talon-9ic) will be the explicit way to delete openclaw-layer
	// entries from the merged view; a literal null in the talon overlay
	// must NOT silently erase openclaw config.
	base := []byte(`{"x":1}`)
	overlay := []byte(`{"x":null}`)
	got, err := mergeJSON(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if v := gjson.GetBytes(got, "x").Int(); v != 1 {
		t.Errorf("x = %d, want base value 1 (null overlay must not erase)", v)
	}
}

func TestMergeJSON_EmptyOverlay(t *testing.T) {
	got, err := mergeJSON([]byte(`{"a":1}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if v := gjson.GetBytes(got, "a").Int(); v != 1 {
		t.Errorf("a = %d, want 1", v)
	}
}

func TestMergeJSON_EmptyBase(t *testing.T) {
	got, err := mergeJSON([]byte(`{}`), []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if v := gjson.GetBytes(got, "a").Int(); v != 1 {
		t.Errorf("a = %d, want 1", v)
	}
}

func TestMergeJSON_InvalidJSONReturnsError(t *testing.T) {
	if _, err := mergeJSON([]byte(`{`), []byte(`{}`)); err == nil {
		t.Errorf("expected parse error for invalid base")
	}
	if _, err := mergeJSON([]byte(`{}`), []byte(`}`)); err == nil {
		t.Errorf("expected parse error for invalid overlay")
	}
}

// --- mergeJSON: id-keyed arrays --------------------------------------------

func TestMergeJSON_IDArray_OverlayUpdatesAndAppends(t *testing.T) {
	base := []byte(`{"agents":{"list":[{"id":"main","model":"a"},{"id":"coding","model":"b"}]}}`)
	overlay := []byte(`{"agents":{"list":[{"id":"coding","model":"c"},{"id":"research","model":"d"}]}}`)
	got, err := mergeJSON(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{
		`agents.list.#(id=="main").model`:     "a",
		`agents.list.#(id=="coding").model`:   "c",
		`agents.list.#(id=="research").model`: "d",
	}
	for path, want := range checks {
		if got := gjson.GetBytes(got, path).Str; got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
}

func TestMergeJSON_IDArray_OverlayDeepMergesByID(t *testing.T) {
	// When an overlay entry has the same id, recursive merge applies to
	// the entry's fields — base fields not mentioned by the overlay
	// should survive.
	base := []byte(`{"list":[{"id":"a","name":"alpha","note":"keep me"}]}`)
	overlay := []byte(`{"list":[{"id":"a","name":"updated"}]}`)
	got, err := mergeJSON(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if v := gjson.GetBytes(got, `list.#(id=="a").name`).Str; v != "updated" {
		t.Errorf("name = %q, want %q", v, "updated")
	}
	if v := gjson.GetBytes(got, `list.#(id=="a").note`).Str; v != "keep me" {
		t.Errorf("note = %q, want preserved %q", v, "keep me")
	}
}

func TestMergeJSON_IDArray_MixedEntriesFallsBackToReplace(t *testing.T) {
	// If any entry lacks a string id, the array isn't id-keyed and the
	// overlay replaces the array wholesale.
	base := []byte(`{"list":[{"id":"a","x":1},{"y":2}]}`)
	overlay := []byte(`{"list":[{"id":"a","x":99}]}`)
	got, err := mergeJSON(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	arr := gjson.GetBytes(got, "list").Array()
	if len(arr) != 1 {
		t.Errorf("len(list) = %d, want 1 (wholesale replace), got %s", len(arr), gjson.GetBytes(got, "list").Raw)
	}
}

func TestMergeJSON_EmptyArrayDoesNotIDKey(t *testing.T) {
	// allHaveStringID returns false for empty arrays. An empty overlay
	// array should still replace.
	base := []byte(`{"list":[{"id":"a"}]}`)
	overlay := []byte(`{"list":[]}`)
	got, err := mergeJSON(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if arr := gjson.GetBytes(got, "list").Array(); len(arr) != 0 {
		t.Errorf("expected empty list (overlay wins), got %s", gjson.GetBytes(got, "list").Raw)
	}
}

func TestMergeJSON_NestedIDKeyedArray(t *testing.T) {
	// models.providers.<id>.models is also id-keyed; merging at the
	// parent level should recurse into it.
	base := []byte(`{"models":{"providers":{"deepseek":{"models":[{"id":"v4","cost":1},{"id":"chat","cost":2}]}}}}`)
	overlay := []byte(`{"models":{"providers":{"deepseek":{"models":[{"id":"v4","cost":99}]}}}}`)
	got, err := mergeJSON(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if v := gjson.GetBytes(got, `models.providers.deepseek.models.#(id=="v4").cost`).Int(); v != 99 {
		t.Errorf("v4.cost = %d, want overlay 99", v)
	}
	if v := gjson.GetBytes(got, `models.providers.deepseek.models.#(id=="chat").cost`).Int(); v != 2 {
		t.Errorf("chat.cost = %d, want preserved 2", v)
	}
}

// --- write-time merge (SetMerge mode → mergeAtPath / mergeRecursive) -------

func TestSet_Merge_DeepObjectPreservesSiblings(t *testing.T) {
	// SetMerge into a sub-tree with siblings already in the talon overlay
	// should preserve the siblings (path-by-path sjson set, not subtree
	// replace).
	src := `{"models":{"providers":{"openai":{"api":"x"},"anthropic":{"api":"y"}}}}`
	p := fixture(t, "", src)
	patch := map[string]any{"openai": map[string]any{"api": "z"}}
	if _, err := Set(p, mustParse(t, "models.providers"), patch, SetOpts{Mode: SetMerge}); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, p.Talon.Config)
	if v := gjson.Get(got, "models.providers.openai.api").Str; v != "z" {
		t.Errorf("openai.api = %q, want %q", v, "z")
	}
	if v := gjson.Get(got, "models.providers.anthropic.api").Str; v != "y" {
		t.Errorf("anthropic.api = %q, want preserved %q", v, "y")
	}
}

func TestSet_Merge_NestedPatch(t *testing.T) {
	// A nested merge patch should leave unspecified sibling fields alone
	// at every level.
	src := `{"a":{"b":{"c":1,"d":2},"e":3}}`
	p := fixture(t, "", src)
	patch := map[string]any{"b": map[string]any{"c": 99}}
	if _, err := Set(p, mustParse(t, "a"), patch, SetOpts{Mode: SetMerge}); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, p.Talon.Config)
	checks := map[string]int64{
		"a.b.c": 99,
		"a.b.d": 2,
		"a.e":   3,
	}
	for path, want := range checks {
		if v := gjson.Get(got, path).Int(); v != want {
			t.Errorf("%s = %d, want %d", path, v, want)
		}
	}
}

func TestSet_Merge_AgentsListMergesByID(t *testing.T) {
	// SetMerge on agents.list should merge by id, not replace.
	src := `{"agents":{"list":[{"id":"main","model":"a"},{"id":"coding","model":"b"}]}}`
	p := fixture(t, "", src)
	patch := []any{
		map[string]any{"id": "coding", "model": "c"},
		map[string]any{"id": "research", "model": "d"},
	}
	if _, err := Set(p, mustParse(t, "agents.list"), patch, SetOpts{Mode: SetMerge}); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, p.Talon.Config)
	checks := map[string]string{
		`agents.list.#(id=="main").model`:     "a",
		`agents.list.#(id=="coding").model`:   "c",
		`agents.list.#(id=="research").model`: "d",
	}
	for path, want := range checks {
		if v := gjson.Get(got, path).Str; v != want {
			t.Errorf("%s = %q, want %q", path, v, want)
		}
	}
}

func TestSet_Merge_RefusesScalarOverObject(t *testing.T) {
	// Merging a scalar into a map path is ambiguous; mergeRecursive
	// should error rather than silently replace.
	src := `{"x":{"a":1}}`
	p := fixture(t, "", src)
	_, err := Set(p, mustParse(t, "x"), map[string]any{"b": map[string]any{"c": 1}}, SetOpts{Mode: SetMerge})
	if err != nil {
		t.Fatal(err)
	}
	// Now try to merge a map at "x.b" but the existing "x.b" is already
	// a map, so this should succeed. The error case is when target is
	// not an object but patch is — try x.a (scalar) with map patch.
	_, err = Set(p, mustParse(t, "x.a"), map[string]any{"sub": 1}, SetOpts{Mode: SetMerge})
	if err == nil {
		t.Errorf("expected merge error when target is scalar but patch is map")
	}
}

func TestSet_Merge_BootstrapsTalonOverlayFromNothing(t *testing.T) {
	// SetMerge with no talon overlay yet should still produce a sparse
	// overlay containing only the merged patch.
	p := fixture(t, `{"openclaw":"only"}`, "")
	patch := map[string]any{"new": map[string]any{"key": "val"}}
	if _, err := Set(p, mustParse(t, "talon"), patch, SetOpts{Mode: SetMerge}); err != nil {
		t.Fatal(err)
	}
	overlay := readFile(t, p.Talon.Config)
	if v := gjson.Get(overlay, "talon.new.key").Str; v != "val" {
		t.Errorf("overlay.talon.new.key = %q, want %q", v, "val")
	}
	if gjson.Get(overlay, "openclaw").Exists() {
		t.Errorf("openclaw key leaked into talon overlay: %s", overlay)
	}
}

func TestSet_Merge_LeavesUnrelatedOverlayKeys(t *testing.T) {
	// SetMerge edits a sub-tree; unrelated talon-overlay keys must
	// survive (the openclaw-side fixture is irrelevant here).
	overlay := `{"gateway":{"port":19000},"unrelated":{"keep":true}}`
	p := fixture(t, "", overlay)
	patch := map[string]any{"port": 19001}
	if _, err := Set(p, mustParse(t, "gateway"), patch, SetOpts{Mode: SetMerge}); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, p.Talon.Config)
	if v := gjson.Get(got, "gateway.port").Int(); v != 19001 {
		t.Errorf("gateway.port = %d, want 19001", v)
	}
	if v := gjson.Get(got, "unrelated.keep").Bool(); !v {
		t.Errorf("unrelated.keep should survive: %s", got)
	}
}
