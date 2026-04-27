package config

import (
	"encoding/json"
	"fmt"
	"maps"
)

// mergeJSON deep-merges two JSON byte streams with the second (overlay)
// taking priority. Maps are merged recursively. Arrays of {id: string, ...}
// objects are merged by id (so an overlay can override a single agent
// without re-listing all of them); other arrays are replaced wholesale.
// Scalars in the overlay always replace the base.
func mergeJSON(base, overlay []byte) ([]byte, error) {
	var b, o any
	if err := json.Unmarshal(base, &b); err != nil {
		return nil, fmt.Errorf("merge: parse base: %w", err)
	}
	if err := json.Unmarshal(overlay, &o); err != nil {
		return nil, fmt.Errorf("merge: parse overlay: %w", err)
	}
	merged := mergeValues(b, o)
	return json.Marshal(merged)
}

func mergeValues(base, overlay any) any {
	if overlay == nil {
		return base
	}
	if base == nil {
		return overlay
	}
	bm, baseIsMap := base.(map[string]any)
	om, overlayIsMap := overlay.(map[string]any)
	if baseIsMap && overlayIsMap {
		out := make(map[string]any, len(bm)+len(om))
		maps.Copy(out, bm)
		for k, ov := range om {
			if existing, ok := out[k]; ok {
				out[k] = mergeValues(existing, ov)
			} else {
				out[k] = ov
			}
		}
		return out
	}
	ba, baseIsArr := base.([]any)
	oa, overlayIsArr := overlay.([]any)
	if baseIsArr && overlayIsArr && allHaveStringID(ba) && allHaveStringID(oa) {
		return mergeArraysByID(ba, oa)
	}
	return overlay
}

func allHaveStringID(arr []any) bool {
	if len(arr) == 0 {
		return false
	}
	for _, entry := range arr {
		m, ok := entry.(map[string]any)
		if !ok {
			return false
		}
		id, ok := m["id"].(string)
		if !ok || id == "" {
			return false
		}
	}
	return true
}

func mergeArraysByID(base, overlay []any) []any {
	merged := append([]any{}, base...)
	indexByID := make(map[string]int, len(merged))
	for i, entry := range merged {
		if m, ok := entry.(map[string]any); ok {
			if id, ok := m["id"].(string); ok && id != "" {
				indexByID[id] = i
			}
		}
	}
	for _, entry := range overlay {
		m, ok := entry.(map[string]any)
		if !ok {
			merged = append(merged, entry)
			continue
		}
		id, ok := m["id"].(string)
		if !ok || id == "" {
			merged = append(merged, entry)
			continue
		}
		if idx, present := indexByID[id]; present {
			existing, _ := merged[idx].(map[string]any)
			merged[idx] = mergeValues(existing, m)
			continue
		}
		indexByID[id] = len(merged)
		merged = append(merged, entry)
	}
	return merged
}
