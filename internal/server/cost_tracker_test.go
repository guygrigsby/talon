package server

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/provider"
)

func TestCostTracker_NoCapAllowsAlways(t *testing.T) {
	paths := readFixture(t, "{}")
	c := NewCostTracker(paths)
	for i := 0; i < 100; i++ {
		if err := c.Allow("main"); err != nil {
			t.Fatalf("expected unset cap to allow, got %v", err)
		}
	}
}

func TestCostTracker_AllowRefusesOverCap(t *testing.T) {
	paths := readFixture(t, "{}")
	if err := os.WriteFile(paths.Talon.Config, []byte(`{"agents":{"defaults":{"dailyUsdCap":1.00}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCostTracker(paths)

	// Push past the cap with a model whose builtin price applies.
	// 1M input @ $15 = $15 — well past $1.
	c.Record("main", "anthropic/claude-opus-4-7", provider.Usage{InputTokens: 1_000_000, OutputTokens: 0})

	err := c.Allow("main")
	if err == nil {
		t.Fatal("expected cap-exceeded error")
	}
	var capErr *costCapError
	if !errors.As(err, &capErr) {
		t.Fatalf("expected *costCapError, got %T: %v", err, err)
	}
	if capErr.AgentID != "main" {
		t.Errorf("AgentID = %q, want main", capErr.AgentID)
	}
	if !strings.Contains(err.Error(), "main") || !strings.Contains(err.Error(), "$15") {
		t.Errorf("error message should include agent + spent: %q", err.Error())
	}
}

func TestCostTracker_OtherAgentNotAffected(t *testing.T) {
	paths := readFixture(t, "{}")
	if err := os.WriteFile(paths.Talon.Config, []byte(`{"agents":{"defaults":{"dailyUsdCap":1.00}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCostTracker(paths)
	// main hits cap; research shouldn't.
	c.Record("main", "anthropic/claude-opus-4-7", provider.Usage{InputTokens: 1_000_000})
	if err := c.Allow("main"); err == nil {
		t.Fatal("main should be capped")
	}
	if err := c.Allow("research"); err != nil {
		t.Errorf("research should still be allowed, got %v", err)
	}
}

func TestCostTracker_RecordHonorsConfigPriceOverride(t *testing.T) {
	paths := readFixture(t, "{}")
	// Override deepseek-chat at $100/1M in to trip the cap fast.
	cfg := `{
		"agents":{"defaults":{"dailyUsdCap":1.00}},
		"models":{"deepseek/deepseek-chat":{"priceUsdPer1M":{"in":100.0,"out":100.0}}}
	}`
	if err := os.WriteFile(paths.Talon.Config, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCostTracker(paths)
	// 50K input + 50K output @ $100/1M each = $5 + $5 = $10
	c.Record("main", "deepseek/deepseek-chat", provider.Usage{InputTokens: 50_000, OutputTokens: 50_000})
	if err := c.Allow("main"); err == nil {
		t.Fatal("expected config-override price to trip cap")
	}
}

func TestCostTracker_RecordHonorsDottedModelConfigPriceOverride(t *testing.T) {
	paths := readFixture(t, "{}")
	cfg := `{
		"agents":{"defaults":{"dailyUsdCap":1.00}},
		"models":{"openai/gpt-5.4-mini":{"priceUsdPer1M":{"in":100.0,"out":100.0}}}
	}`
	if err := os.WriteFile(paths.Talon.Config, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCostTracker(paths)
	c.Record("main", "openai/gpt-5.4-mini", provider.Usage{InputTokens: 50_000, OutputTokens: 50_000})
	if err := c.Allow("main"); err == nil {
		t.Fatal("expected dotted config-override price to trip cap")
	}
}

func TestCostTracker_UnknownModelIsZeroCost(t *testing.T) {
	paths := readFixture(t, "{}")
	if err := os.WriteFile(paths.Talon.Config, []byte(`{"agents":{"defaults":{"dailyUsdCap":1.00}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCostTracker(paths)
	// Unknown model with no config price = 0 cost = never trips cap.
	for i := 0; i < 10; i++ {
		c.Record("main", "fictional/no-such-model", provider.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	}
	if err := c.Allow("main"); err != nil {
		t.Errorf("untracked model should not contribute to cap, got %v", err)
	}
}

func TestCostTracker_NilSafe(t *testing.T) {
	// Defensive: chat handler may have a nil tracker (when paths
	// aren't configured). Both methods must no-op.
	var c *CostTracker
	if err := c.Allow("anything"); err != nil {
		t.Errorf("nil tracker Allow should return nil, got %v", err)
	}
	c.Record("anything", "x/y", provider.Usage{InputTokens: 100})
}

func TestFtoa_FormatsCommonValues(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0.00"},
		{1, "1.00"},
		{15, "15.00"},
		{0.5, "0.50"},
		{0.27, "0.27"},
		{1.105, "1.105"},
		{0.0015, "0.0015"},
		{12345.50, "12345.50"},
	}
	for _, c := range cases {
		if got := ftoa(c.in); got != c.want {
			t.Errorf("ftoa(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
