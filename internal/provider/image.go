package provider

import (
	"context"
	"encoding/json"
)

// ImageProvider is a streaming image-generation backend. Implementations
// wrap a concrete backend (ComfyUI, DALL-E, Stability, etc.) and expose
// a uniform channel-based API so ImagesHandler can route without knowing
// which backend is active.
//
// Implementations must be safe for concurrent use.
type ImageProvider interface {
	// Name returns the stable provider key that matches the
	// images.provider config entry (e.g. "comfyui", "dalle").
	Name() string

	// StreamImageGeneration submits a generation request and returns a
	// channel of ImageDelta events. The channel is closed when the
	// stream terminates: one ImageDeltaResult on success, one
	// ImageDeltaError on failure. Progress events may precede either.
	//
	// Setup errors (backend unreachable, missing config) are returned
	// synchronously; in that case ch will be nil.
	StreamImageGeneration(ctx context.Context, req ImageRequest) (<-chan ImageDelta, error)
}

// ImageRequest is the provider-agnostic generation request.
type ImageRequest struct {
	Prompt         string
	NegativePrompt string
	// Model is the provider-specific model id (without the provider
	// prefix). Empty means "provider default".
	Model ModelID
	// WorkflowID selects a named builtin workflow on the provider side.
	// Empty falls back to the provider's default workflow.
	WorkflowID string
	// Workflow is a raw JSON workflow override. Non-nil overrides the
	// provider's on-disk workflow (after prompt/seed patching).
	Workflow json.RawMessage
	// InputImage carries raw bytes for img2img / inpainting. Nil means
	// text-to-image.
	InputImage []byte
	Seed       *int64
	Width      int
	Height     int
	Steps      int
	// NodeOverrides passes provider-specific node/layer patches. Shape:
	// map[nodeID]map[inputKey]value. Providers that don't support
	// node-level overrides may ignore this field.
	NodeOverrides map[string]map[string]any
}

// ImageDeltaKind discriminates an ImageDelta variant.
type ImageDeltaKind int

const (
	// ImageDeltaProgress is a generation-progress tick.
	ImageDeltaProgress ImageDeltaKind = iota
	// ImageDeltaResult is the terminal success event carrying the
	// generated image reference (and optionally inline bytes).
	ImageDeltaResult
	// ImageDeltaError is the terminal failure event.
	ImageDeltaError
)

// ImageDelta is one event in a StreamImageGeneration response.
type ImageDelta struct {
	Kind     ImageDeltaKind
	Progress *ImageProgress
	Result   *ImageResult
	Err      error
}

// ImageProgress is the payload of an ImageDeltaProgress event.
type ImageProgress struct {
	Step  int
	Total int
	// Node is an optional provider-specific stage identifier (e.g. a
	// ComfyUI node id). Empty when not meaningful.
	Node string
}

// ImageResult is the payload of an ImageDeltaResult event.
type ImageResult struct {
	// Ref is the provider-specific reference used to fetch the image:
	// a file path, ComfyUI /view query string, URL, etc.
	Ref      string
	// Data carries inline image bytes when the provider can supply them
	// without an extra fetch round-trip. May be nil.
	Data     []byte
	MimeType string
}
