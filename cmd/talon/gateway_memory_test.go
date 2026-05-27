package main

import "testing"

func TestReadMemorySettingsReadsNativeConfig(t *testing.T) {
	paths := writeFixture(t, `{
		"memory": {
			"enabled": true,
			"path": "/tmp/talon-memory",
			"model": "sentence-transformers/all-MiniLM-L6-v2"
		}
	}`)

	got, err := readMemorySettings(paths)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.Path != "/tmp/talon-memory" || got.Model != "sentence-transformers/all-MiniLM-L6-v2" {
		t.Fatalf("memory settings = %+v", got)
	}
}

func TestReadMemorySettingsReadsRecallMinScore(t *testing.T) {
	paths := writeFixture(t, `{
		"memory": {
			"enabled": true,
			"recall": {"min_score": 0.42}
		}
	}`)

	got, err := readMemorySettings(paths)
	if err != nil {
		t.Fatal(err)
	}
	if got.RecallMinScore != 0.42 {
		t.Fatalf("recall min score = %v, want 0.42", got.RecallMinScore)
	}
}

func TestResolveRecallMinScore(t *testing.T) {
	// Unset (zero) -> code default floor.
	if got := resolveRecallMinScore(memorySettings{}); got != defaultRecallMinScore {
		t.Fatalf("unset min score = %v, want default %v", got, defaultRecallMinScore)
	}
	// Configured positive value wins.
	for _, want := range []float64{0.25, 0.42, 0.9} {
		s := memorySettings{RecallMinScore: want}
		if got := resolveRecallMinScore(s); got != want {
			t.Fatalf("configured min score = %v, want %v", got, want)
		}
	}
}
