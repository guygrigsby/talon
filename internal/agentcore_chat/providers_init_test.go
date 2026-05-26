package agentcore_chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/voocel/litellm"
)

func TestAnthropicShimDropsTopPWhenTemperatureSet(t *testing.T) {
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_1",
			"type": "message",
			"role": "assistant",
			"model": "claude-haiku-4-5",
			"content": [{"type": "text", "text": "ok"}],
			"usage": {"input_tokens": 1, "output_tokens": 1}
		}`))
	}))
	defer srv.Close()

	client, err := litellm.NewWithProvider("anthropic", litellm.ProviderConfig{
		APIKey:  "test",
		BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	temperature := 0.7
	_, err = client.Chat(context.Background(), &litellm.Request{
		Model:       "claude-haiku-4-5",
		Messages:    []litellm.Message{{Role: "user", Content: "hi"}},
		Temperature: &temperature,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}

	if _, ok := payload["top_p"]; ok {
		t.Fatalf("anthropic payload included top_p: %#v", payload)
	}
	if _, ok := payload["temperature"]; !ok {
		t.Fatalf("anthropic payload omitted temperature: %#v", payload)
	}
}

func TestOpenAIShimUsesResponsesForGPT5Tools(t *testing.T) {
	var path string
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "resp_1",
			"object": "response",
			"model": "gpt-5.4-mini",
			"status": "completed",
			"output_text": "ok",
			"output": [],
			"usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
		}`))
	}))
	defer srv.Close()

	client, err := litellm.NewWithProvider("openai", litellm.ProviderConfig{
		APIKey:  "test",
		BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	_, err = client.Chat(context.Background(), &litellm.Request{
		Model:    "gpt-5.4-mini",
		Messages: []litellm.Message{{Role: "user", Content: "hi"}},
		Tools: []litellm.Tool{{
			Type: "function",
			Function: litellm.FunctionDef{
				Name:        "lookup",
				Description: "lookup something",
				Parameters:  map[string]any{"type": "object"},
			},
		}},
		Thinking: litellm.NewThinkingWithLevel("low"),
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}

	if path != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", path)
	}
	if _, ok := payload["tools"]; !ok {
		t.Fatalf("responses payload omitted tools: %#v", payload)
	}
	tools := payload["tools"].([]any)
	tool := tools[0].(map[string]any)
	if got := tool["strict"]; got != false {
		t.Fatalf("responses tool strict = %#v, want false", got)
	}
	if _, ok := payload["reasoning"]; !ok {
		t.Fatalf("responses payload omitted reasoning: %#v", payload)
	}
	if _, ok := payload["reasoning_effort"]; ok {
		t.Fatalf("responses payload used chat-completions reasoning_effort: %#v", payload)
	}
}
