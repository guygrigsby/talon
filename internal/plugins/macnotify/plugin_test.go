package macnotify

import (
	"context"
	"strings"
	"testing"

	pb "github.com/guygrigsby/talon/internal/plugin/pb"
)

func TestRunTool_RejectsUnknownTool(t *testing.T) {
	p := &macNotifyPlugin{}
	resp, err := p.RunTool(context.Background(), &pb.RunToolRequest{
		ToolName:      "something-else",
		ArgumentsJson: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetIsError() || !strings.Contains(resp.GetOutput(), "unknown tool") {
		t.Errorf("expected unknown-tool error; got %+v", resp)
	}
}

func TestRunTool_RejectsMissingTitle(t *testing.T) {
	p := &macNotifyPlugin{}
	resp, _ := p.RunTool(context.Background(), &pb.RunToolRequest{
		ToolName:      "mac_notify",
		ArgumentsJson: `{"body": "x"}`,
	})
	if !resp.GetIsError() || !strings.Contains(resp.GetOutput(), "title is required") {
		t.Errorf("expected title-required error; got %+v", resp)
	}
}

func TestRunTool_RejectsMissingBody(t *testing.T) {
	p := &macNotifyPlugin{}
	resp, _ := p.RunTool(context.Background(), &pb.RunToolRequest{
		ToolName:      "mac_notify",
		ArgumentsJson: `{"title": "x"}`,
	})
	if !resp.GetIsError() || !strings.Contains(resp.GetOutput(), "body is required") {
		t.Errorf("expected body-required error; got %+v", resp)
	}
}

func TestRunTool_RejectsBlankFields(t *testing.T) {
	// Whitespace-only must fail too — the AppleScript script would
	// otherwise post a banner with empty text.
	p := &macNotifyPlugin{}
	resp, _ := p.RunTool(context.Background(), &pb.RunToolRequest{
		ToolName:      "mac_notify",
		ArgumentsJson: `{"title": "  ", "body": "ok"}`,
	})
	if !resp.GetIsError() || !strings.Contains(resp.GetOutput(), "title is required") {
		t.Errorf("blank title should fail; got %+v", resp)
	}
}

func TestRunTool_BadJSON(t *testing.T) {
	p := &macNotifyPlugin{}
	resp, _ := p.RunTool(context.Background(), &pb.RunToolRequest{
		ToolName:      "mac_notify",
		ArgumentsJson: `{not-json`,
	})
	if !resp.GetIsError() || !strings.Contains(resp.GetOutput(), "invalid arguments JSON") {
		t.Errorf("bad JSON should fail clearly; got %+v", resp)
	}
}

func TestInitialize_AdvertisesMacNotifyTool(t *testing.T) {
	p := &macNotifyPlugin{}
	resp, err := p.Initialize(context.Background(), &pb.InitializeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	tools := resp.GetManifest().GetOffersTools()
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	if tools[0].GetName() != "mac_notify" {
		t.Errorf("tool name = %q", tools[0].GetName())
	}
	// Spot-check schema requires title+body — protects against a
	// future edit accidentally relaxing the contract.
	schema := string(tools[0].GetParametersSchema())
	if !strings.Contains(schema, `"required": ["title", "body"]`) {
		t.Errorf("schema missing required[title,body]: %s", schema)
	}
}
