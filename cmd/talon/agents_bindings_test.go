package main

import (
	"reflect"
	"testing"
)

func TestScanChannelBindings_PicksUpChannelsWithAgentID(t *testing.T) {
	merged := []byte(`{
		"channels": {
			"telegram":  {"agentId": "main", "dmPolicy": "allowlist", "allowFrom": ["555","999"], "botToken": "xx"},
			"discord":   {"agentId": "research"},
			"signal":    {"port": 9000},
			"unbound":   {"agentId": ""}
		}
	}`)

	got := scanChannelBindings(merged)
	want := []binding{
		{Channel: "discord", AgentID: "research", Description: ""},
		{Channel: "telegram", AgentID: "main", Description: "dmPolicy=allowlist allowFrom=[555,999] token=set"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scanChannelBindings:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestScanChannelBindings_EmptyConfig(t *testing.T) {
	if got := scanChannelBindings([]byte(`{}`)); len(got) != 0 {
		t.Errorf("expected empty bindings for empty config, got %+v", got)
	}
	if got := scanChannelBindings([]byte(`{"channels": {}}`)); len(got) != 0 {
		t.Errorf("expected empty bindings for empty channels, got %+v", got)
	}
}

func TestScanChannelBindings_StableSort(t *testing.T) {
	// Channels object iteration is unordered in JSON; the function
	// must return a deterministic order for reproducible JSON output.
	merged := []byte(`{
		"channels": {
			"zeta":  {"agentId": "z1"},
			"alpha": {"agentId": "a1"},
			"mike":  {"agentId": "m1"}
		}
	}`)
	got := scanChannelBindings(merged)
	if len(got) != 3 {
		t.Fatalf("expected 3 bindings, got %d", len(got))
	}
	for i, want := range []string{"alpha", "mike", "zeta"} {
		if got[i].Channel != want {
			t.Errorf("position %d: got %q, want %q (full: %+v)", i, got[i].Channel, want, got)
		}
	}
}
