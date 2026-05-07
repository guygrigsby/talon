package comfyui

// ComfyUI-Manager is a popular custom-node extension that adds model
// (checkpoint, LoRA, embedding, etc.) install + custom-node management
// over HTTP. It's the standard fix for "I want to install a LoRA on a
// remote ComfyUI box without SSH access". When present, talon exposes
// a thin proxy over its install API so the Studio UI can offer
// click-to-install for missing style LoRAs.
//
// API stability note: ComfyUI-Manager's HTTP surface evolves
// across releases. The detection probe here is the most stable
// endpoint (/customnode/getmappings has existed for many versions);
// the install endpoint shape (/model/install) covers a recent baseline.
// If the user's manager rejects the install body, we surface the
// underlying HTTP error verbatim so the Studio UI can offer a
// "fall back to manual download" prompt.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ManagerStatus is the result of probing ComfyUI for the Manager
// extension. Present=false isn't an error — it just means the user's
// ComfyUI doesn't have the manager installed and auto-install isn't
// available. Endpoint records which detection path matched, so
// debug output ("manager detected at /customnode/getmappings") is
// actionable.
type ManagerStatus struct {
	Present  bool   `json:"present"`
	Endpoint string `json:"endpoint,omitempty"`
}

// ManagerStatus probes ComfyUI for the Manager extension. The probe
// is best-effort: a non-2xx response (or a connection error) means
// "not present", not "request failed". Network or DNS errors
// genuinely failing surface as ctx errors via the HTTP client.
//
// Detection paths, in order of stability:
//  1. /customnode/getmappings — listed since the earliest manager
//     versions; returns JSON when present, 404 when absent.
//  2. /manager/version — newer manager builds expose this.
//
// Any 200 response on either path is enough; we don't validate the
// body shape since manager versions vary.
func (c *Client) ManagerStatus(ctx context.Context) (*ManagerStatus, error) {
	for _, path := range []string{"/customnode/getmappings", "/manager/version"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
		if err != nil {
			return nil, fmt.Errorf("comfyui manager probe: build request: %w", err)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			// Treat dial-level failures as "manager probe inconclusive";
			// the next path may succeed, and if both fail the caller
			// gets Present=false plus the dial error context via
			// callers that combine ManagerStatus with a wider health
			// probe.
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode/100 == 2 {
			return &ManagerStatus{Present: true, Endpoint: path}, nil
		}
	}
	return &ManagerStatus{Present: false}, nil
}

// ManagerInstallRequest is the install body. Type is the model
// category ("loras", "checkpoints", "vae", "embeddings", etc.) and
// must match a directory ComfyUI knows about under models/. URL is
// the source download (typically a CivitAI or HuggingFace URL).
// SavePath is the manager's destination subdir; when empty the
// manager defaults to the type-named dir.
//
// Filename is what the file lands as on disk; if empty the manager
// derives it from the URL (often by Content-Disposition or the URL
// path tail). Setting it explicitly is recommended so style preset
// metadata can refer to a stable name.
type ManagerInstallRequest struct {
	Type     string `json:"type"`
	URL      string `json:"url"`
	Filename string `json:"filename,omitempty"`
	SavePath string `json:"save_path,omitempty"`
}

// ManagerInstallResult mirrors the manager's accept envelope. The
// manager may queue installs asynchronously; OK=true confirms the
// request was accepted, not that the file is present yet. Callers
// that need to know "is the file there now?" should follow up with
// ObjectInfo and look for the filename in LoraLoader.input.required
// (or whichever loader matches Type).
type ManagerInstallResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

// ManagerInstall posts an install request to ComfyUI-Manager. The
// endpoint shape follows the older /model/install path which has
// the broadest version compatibility. A non-2xx response surfaces
// verbatim with the body for diagnostics — manager versions
// occasionally tighten their input validation and the error text
// is the fastest path to a fix.
func (c *Client) ManagerInstall(ctx context.Context, req ManagerInstallRequest) (*ManagerInstallResult, error) {
	if req.Type == "" {
		return nil, fmt.Errorf("comfyui manager install: type is required")
	}
	if req.URL == "" {
		return nil, fmt.Errorf("comfyui manager install: url is required")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("comfyui manager install: marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/model/install", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("comfyui manager install: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("comfyui manager install: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("comfyui manager install: read body: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("comfyui manager install: http %d: %s", resp.StatusCode, truncate(string(raw), 256))
	}
	// The manager's response body shape varies — sometimes empty,
	// sometimes JSON with a status field. Decode opportunistically
	// and otherwise return OK=true since the 2xx already says so.
	var out ManagerInstallResult
	out.OK = true
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
		if !out.OK {
			out.OK = true // 2xx wins regardless of decoded shape
		}
	}
	return &out, nil
}
