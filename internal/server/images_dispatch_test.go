package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/guygrigsby/talon/internal/comfyui"
)

// TestDispatchEvent_ExecutionErrorSurfacesException covers the
// "comfyui execution_error" black-box bug: the event from ComfyUI
// already carries the failing node id + exception message
// (typically "ckpt_name 'foo.safetensors' not found in [list]"),
// but the dispatcher was emitting a generic "comfyui execution_error"
// string and burying the detail in a sibling field that the UI never
// rendered. The fix surfaces node + exception in errorMessage.
func TestDispatchEvent_ExecutionErrorSurfacesException(t *testing.T) {
	captured := map[string]any{}
	emit := func(state string, data map[string]any) bool {
		if state == "error" {
			captured = data
		}
		return true
	}
	h := &ImagesHandler{}
	ev := comfyui.Event{
		Type: "execution_error",
		Data: json.RawMessage(`{
			"prompt_id": "p1",
			"node_id": "1",
			"node_type": "CheckpointLoaderSimple",
			"exception_type": "ValueError",
			"exception_message": "ckpt_name 'cyberrealisticPony_v170.safetensors' not found in available checkpoints"
		}`),
	}
	done, ok := h.dispatchEvent(emit, nil, "p1", ev)
	if !done {
		t.Fatal("execution_error should be terminal (done=true)")
	}
	if !ok {
		t.Fatal("ok should remain true (terminal handled cleanly)")
	}
	msg, _ := captured["errorMessage"].(string)
	if msg == "" {
		t.Fatalf("errorMessage missing from captured event: %#v", captured)
	}
	for _, want := range []string{"cyberrealisticPony_v170.safetensors", "CheckpointLoaderSimple"} {
		if !strings.Contains(msg, want) {
			t.Errorf("errorMessage should include %q, got %q", want, msg)
		}
	}
}

// TestDispatchEvent_ExecutionErrorWithMissingFields keeps the
// emit-something-useful guarantee even when ComfyUI hands us a
// payload missing the rich fields (older versions / partial events).
func TestDispatchEvent_ExecutionErrorWithMissingFields(t *testing.T) {
	captured := map[string]any{}
	emit := func(state string, data map[string]any) bool {
		if state == "error" {
			captured = data
		}
		return true
	}
	h := &ImagesHandler{}
	ev := comfyui.Event{
		Type: "execution_error",
		Data: json.RawMessage(`{}`),
	}
	if done, _ := h.dispatchEvent(emit, nil, "", ev); !done {
		t.Fatal("execution_error should be terminal")
	}
	msg, _ := captured["errorMessage"].(string)
	if msg == "" || !strings.Contains(strings.ToLower(msg), "execution_error") {
		t.Fatalf("expected a fallback errorMessage mentioning execution_error, got %q", msg)
	}
}
