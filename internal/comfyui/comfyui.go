// Package comfyui is a minimal HTTP/WS client for a local or LAN ComfyUI
// server. It exposes only the surface talon's image-generation RPCs need:
// submit a workflow, watch progress events, fetch the resulting image
// bytes. Higher-level orchestration (default workflow templates,
// per-agent client IDs, talon RPC plumbing) lives one layer up.
//
// The package is deliberately stateless and base-URL-driven so the call
// site can resolve `images.providers.comfyui.baseUrl` from talon's
// merged config on every request without holding open connections.
package comfyui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"

	"github.com/coder/websocket"
)

// Client speaks to one ComfyUI instance. Construct with New; reuse across
// calls. The HTTP client is exposed so callers can swap in a custom
// transport (e.g. for tests or to thread a context-aware proxy).
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// New returns a Client that dials baseURL (e.g. "http://10.0.0.226:8188").
// The HTTP client uses sensible defaults; callers wanting custom
// timeouts can replace c.HTTP after construction. Per-call timeouts
// should come via context.
func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{},
	}
}

// SubmitResult mirrors the /prompt response envelope. NodeErrors is
// non-empty when the server rejects the workflow before queuing — the
// caller should surface those to the user since the prompt was not
// enqueued.
type SubmitResult struct {
	PromptID   string         `json:"prompt_id"`
	Number     int            `json:"number"`
	NodeErrors map[string]any `json:"node_errors,omitempty"`
}

// Submit queues workflow with ComfyUI for execution under clientID. The
// workflow is the API-format workflow JSON (a node-id-keyed object, NOT
// the editor's serialized format). clientID ties subsequent /ws events
// back to this submission; pass a fresh UUID per call.
func (c *Client) Submit(ctx context.Context, workflow json.RawMessage, clientID string) (*SubmitResult, error) {
	body, err := json.Marshal(map[string]any{
		"prompt":    workflow,
		"client_id": clientID,
	})
	if err != nil {
		return nil, fmt.Errorf("comfyui submit: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/prompt", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("comfyui submit: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("comfyui submit: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("comfyui submit: read body: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("comfyui submit: http %d: %s", resp.StatusCode, truncate(string(raw), 256))
	}
	var out SubmitResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("comfyui submit: decode: %w (body=%s)", err, truncate(string(raw), 256))
	}
	if out.PromptID == "" {
		return nil, fmt.Errorf("comfyui submit: empty prompt_id (body=%s)", truncate(string(raw), 256))
	}
	return &out, nil
}

// Event is one frame off the /ws stream. ComfyUI wraps every frame as
// {"type":"<kind>","data":{...}}; the kinds we care about are
// "executing", "progress", "executed", "execution_success",
// "execution_error", and "execution_interrupted". Callers decode Data
// according to Type. Unknown types are passed through verbatim so this
// package doesn't have to be revised for every server upgrade.
type Event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Events opens a websocket subscription as clientID and streams all
// frames the server sends until ctx is canceled or the connection
// drops. The returned channel is closed on shutdown; the returned err
// reports the cause if it wasn't a clean ctx cancel.
//
// Note: ComfyUI's /ws stream is per-clientID, not per-promptID. If you
// run multiple submissions on one clientID their events interleave —
// the caller filters by prompt_id in the data field. talon's
// orchestrator generates a fresh clientID per submission to keep each
// stream clean.
func (c *Client) Events(ctx context.Context, clientID string) (<-chan Event, <-chan error, error) {
	wsURL, err := httpToWS(c.BaseURL + "/ws")
	if err != nil {
		return nil, nil, fmt.Errorf("comfyui events: %w", err)
	}
	if clientID != "" {
		u, err := url.Parse(wsURL)
		if err != nil {
			return nil, nil, fmt.Errorf("comfyui events: parse url: %w", err)
		}
		q := u.Query()
		q.Set("clientId", clientID)
		u.RawQuery = q.Encode()
		wsURL = u.String()
	}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: c.HTTP})
	if err != nil {
		return nil, nil, fmt.Errorf("comfyui events: dial: %w", err)
	}
	conn.SetReadLimit(8 * 1024 * 1024) // image previews can be ~MBs

	events := make(chan Event, 16)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		defer conn.Close(websocket.StatusNormalClosure, "")
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return // clean shutdown
				}
				errs <- fmt.Errorf("comfyui events: read: %w", err)
				return
			}
			var ev Event
			if err := json.Unmarshal(raw, &ev); err != nil {
				// Binary preview frames arrive as MessageBinary and
				// don't match our envelope — skip silently. JSON
				// decode failures on text frames are noted via errs
				// only when the caller hasn't already drained.
				continue
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return events, errs, nil
}

// HistoryEntry is the per-prompt record at /history/<id>. Outputs is
// keyed by the workflow node id (string). For text-to-image workflows
// the SaveImage node's output carries Images; other outputs (latents,
// audio, etc.) ride in the same envelope but aren't decoded here.
type HistoryEntry struct {
	Outputs map[string]NodeOutput `json:"outputs"`
	Status  HistoryStatus         `json:"status"`
}

// NodeOutput is the per-node output payload. Add fields here as we add
// support for more output types (gifs, latents, audio).
type NodeOutput struct {
	Images []ImageRef `json:"images,omitempty"`
}

// ImageRef is an addressable image in ComfyUI's filesystem — what /view
// expects. The Type field is "output" for finalized renders, "temp"
// for in-progress previews, "input" for uploaded source images.
type ImageRef struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

// HistoryStatus is the run-level summary. Completed is true once the
// run reached a terminal state (success or error); StatusStr is
// "success", "error", or one of the in-progress states.
type HistoryStatus struct {
	StatusStr string `json:"status_str"`
	Completed bool   `json:"completed"`
}

// HistoryListEntry pairs a prompt id with its post-run record. Used by
// HistoryAll to surface gallery-style listings without losing the id
// (the keyed map at /history) the UI needs to associate refs back to
// the run that produced them.
type HistoryListEntry struct {
	PromptID string
	Entry    HistoryEntry
}

// HistoryAll fetches up to max recent records from /history. ComfyUI's
// history is in-memory and resets on server restart; pre-restart runs
// are not returned. The order matches ComfyUI's response order
// (newest-first in current versions, but callers should not rely on
// that and re-sort by their own criteria if needed).
func (c *Client) HistoryAll(ctx context.Context, max int) ([]HistoryListEntry, error) {
	u := c.BaseURL + "/history"
	if max > 0 {
		u += "?max_items=" + url.QueryEscape(itoa(max))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("comfyui history list: build request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("comfyui history list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("comfyui history list: http %d: %s", resp.StatusCode, truncate(string(raw), 256))
	}
	var envelope map[string]HistoryEntry
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("comfyui history list: decode: %w", err)
	}
	out := make([]HistoryListEntry, 0, len(envelope))
	for id, entry := range envelope {
		out = append(out, HistoryListEntry{PromptID: id, Entry: entry})
	}
	return out, nil
}

// History fetches the post-run record for promptID. While the run is
// in flight ComfyUI returns either an empty object or omits the entry;
// we return (nil, nil) in those cases so callers can poll without
// distinguishing pending-vs-not-found.
func (c *Client) History(ctx context.Context, promptID string) (*HistoryEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/history/"+url.PathEscape(promptID), nil)
	if err != nil {
		return nil, fmt.Errorf("comfyui history: build request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("comfyui history: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("comfyui history: http %d: %s", resp.StatusCode, truncate(string(raw), 256))
	}
	var envelope map[string]HistoryEntry
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("comfyui history: decode: %w", err)
	}
	entry, ok := envelope[promptID]
	if !ok {
		return nil, nil // run hasn't been recorded yet (or never existed)
	}
	return &entry, nil
}

// Fetch downloads the image bytes addressed by ref. Returns the bytes
// and the response Content-Type so the caller can pass it through to
// the browser without sniffing. When preview is non-empty it's passed
// through as ComfyUI's `preview=<format>;quality=<n>` query param —
// useful for thumbnail-sized variants in gallery views (e.g.
// "webp;quality=70" runs ~10× smaller than the full PNG).
func (c *Client) Fetch(ctx context.Context, ref ImageRef, preview string) ([]byte, string, error) {
	if ref.Filename == "" {
		return nil, "", fmt.Errorf("comfyui fetch: filename is required")
	}
	q := url.Values{}
	q.Set("filename", ref.Filename)
	if ref.Subfolder != "" {
		q.Set("subfolder", ref.Subfolder)
	}
	if ref.Type != "" {
		q.Set("type", ref.Type)
	}
	if preview != "" {
		q.Set("preview", preview)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/view?"+q.Encode(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("comfyui fetch: build request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("comfyui fetch: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("comfyui fetch: read body: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("comfyui fetch: http %d: %s", resp.StatusCode, truncate(string(body), 256))
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// UploadResult mirrors the /upload/image response. ComfyUI returns
// the resolved filename (which may differ from the requested name if a
// collision was detected and `overwrite` was false), the subfolder it
// was placed in, and the type ("input"/"temp"/"output"). The same
// triple is what LoadImage nodes consume in a workflow graph.
type UploadResult struct {
	Filename  string `json:"name"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

// Upload posts image bytes to ComfyUI's /upload/image endpoint.
// filename is the target name on the ComfyUI side; ComfyUI may add a
// numeric suffix to avoid clobbering a prior upload (the resolved
// name comes back in UploadResult.Filename). subfolder is optional;
// imageType selects "input" (default for img2img sources), "temp", or
// "output". contentType is forwarded to the multipart Content-Type
// header — pass "image/png" / "image/jpeg" / "image/webp" so ComfyUI
// stores the file with the right extension. Empty defaults to
// application/octet-stream which ComfyUI will accept but the file
// extension may be wrong.
func (c *Client) Upload(ctx context.Context, filename string, body []byte, opts UploadOptions) (*UploadResult, error) {
	if filename == "" {
		return nil, fmt.Errorf("comfyui upload: filename is required")
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("comfyui upload: body is empty")
	}
	imageType := opts.Type
	if imageType == "" {
		imageType = "input"
	}
	contentType := opts.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	// "image" is the field ComfyUI's /upload/image handler reads from
	// (named after the canonical web UI form). Set the per-part
	// Content-Type explicitly so a JPEG goes through as image/jpeg
	// rather than the generic application/octet-stream the default
	// CreateFormFile shorthand would produce.
	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image"; filename=%q`, filename))
	hdr.Set("Content-Type", contentType)
	part, err := w.CreatePart(hdr)
	if err != nil {
		return nil, fmt.Errorf("comfyui upload: build form: %w", err)
	}
	if _, err := part.Write(body); err != nil {
		return nil, fmt.Errorf("comfyui upload: write body: %w", err)
	}
	if opts.Subfolder != "" {
		if err := w.WriteField("subfolder", opts.Subfolder); err != nil {
			return nil, fmt.Errorf("comfyui upload: write subfolder: %w", err)
		}
	}
	if err := w.WriteField("type", imageType); err != nil {
		return nil, fmt.Errorf("comfyui upload: write type: %w", err)
	}
	if opts.Overwrite {
		if err := w.WriteField("overwrite", "true"); err != nil {
			return nil, fmt.Errorf("comfyui upload: write overwrite: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("comfyui upload: close form: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/upload/image", &buf)
	if err != nil {
		return nil, fmt.Errorf("comfyui upload: build request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("comfyui upload: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("comfyui upload: read body: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("comfyui upload: http %d: %s", resp.StatusCode, truncate(string(raw), 256))
	}
	var out UploadResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("comfyui upload: decode: %w (body=%s)", err, truncate(string(raw), 256))
	}
	if out.Filename == "" {
		return nil, fmt.Errorf("comfyui upload: empty filename in response (body=%s)", truncate(string(raw), 256))
	}
	return &out, nil
}

// UploadOptions is the parameter envelope for Client.Upload.
// Subfolder is optional ("" places the file at the root of the
// selected type's directory); Type defaults to "input" when empty;
// ContentType defaults to application/octet-stream; Overwrite=true
// asks ComfyUI to clobber a same-named file rather than auto-suffix.
type UploadOptions struct {
	Subfolder   string
	Type        string
	ContentType string
	Overwrite   bool
}

// ObjectInfo is the (huge) response shape from ComfyUI's /object_info
// endpoint, which describes every node class the server knows about
// plus the value enums for each input. Talon mainly uses it to
// enumerate installed LoRAs (LoraLoader.input.required.lora_name)
// and checkpoints (CheckpointLoaderSimple.input.required.ckpt_name)
// without parsing the underlying filesystem. The shape is loosely
// typed because ComfyUI extensions extend the node graph
// dynamically; consumers index by node class name and traverse the
// nested input descriptors as needed.
type ObjectInfo map[string]json.RawMessage

// ObjectInfo fetches /object_info. Used to detect installed
// loras/checkpoints/etc. Cheap to call but the payload is multiple
// MB on a fully-loaded ComfyUI; callers should cache the result if
// they need it more than once per minute.
func (c *Client) ObjectInfo(ctx context.Context) (ObjectInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/object_info", nil)
	if err != nil {
		return nil, fmt.Errorf("comfyui object_info: build request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("comfyui object_info: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("comfyui object_info: http %d: %s", resp.StatusCode, truncate(string(raw), 256))
	}
	var out ObjectInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("comfyui object_info: decode: %w", err)
	}
	return out, nil
}

// httpToWS rewrites http(s)://… to ws(s)://… so the WS dial reuses the
// caller's protocol-aware base URL without requiring a separate field.
func httpToWS(s string) (string, error) {
	switch {
	case strings.HasPrefix(s, "https://"):
		return "wss://" + strings.TrimPrefix(s, "https://"), nil
	case strings.HasPrefix(s, "http://"):
		return "ws://" + strings.TrimPrefix(s, "http://"), nil
	default:
		return "", fmt.Errorf("base URL must start with http:// or https://: %q", s)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
