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
