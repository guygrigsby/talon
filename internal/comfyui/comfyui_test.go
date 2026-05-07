package comfyui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestSubmit_PostsJSONAndReturnsPromptID(t *testing.T) {
	var got struct {
		Prompt   json.RawMessage `json:"prompt"`
		ClientID string          `json:"client_id"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/prompt" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"prompt_id":"abc-123","number":7,"node_errors":{}}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	res, err := c.Submit(t.Context(), json.RawMessage(`{"3":{"class_type":"KSampler"}}`), "client-xyz")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.PromptID != "abc-123" || res.Number != 7 {
		t.Fatalf("got %+v", res)
	}
	if got.ClientID != "client-xyz" {
		t.Errorf("client_id passthrough wrong: %q", got.ClientID)
	}
	if !strings.Contains(string(got.Prompt), `"KSampler"`) {
		t.Errorf("workflow body not propagated: %s", got.Prompt)
	}
}

func TestSubmit_SurfacesNon2xxBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`workflow rejected: missing node 5`))
	}))
	defer srv.Close()

	_, err := New(srv.URL).Submit(t.Context(), json.RawMessage(`{}`), "x")
	if err == nil || !strings.Contains(err.Error(), "http 400") || !strings.Contains(err.Error(), "missing node 5") {
		t.Fatalf("expected 400 with body, got: %v", err)
	}
}

func TestHistory_ReturnsEntryWhenPresent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/history/p1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"p1":{"outputs":{"9":{"images":[{"filename":"out.png","subfolder":"","type":"output"}]}},"status":{"status_str":"success","completed":true}}}`))
	}))
	defer srv.Close()

	entry, err := New(srv.URL).History(t.Context(), "p1")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if entry == nil || !entry.Status.Completed || entry.Status.StatusStr != "success" {
		t.Fatalf("status not parsed: %+v", entry)
	}
	imgs := entry.Outputs["9"].Images
	if len(imgs) != 1 || imgs[0].Filename != "out.png" || imgs[0].Type != "output" {
		t.Fatalf("images not parsed: %+v", imgs)
	}
}

func TestHistory_NilWhenAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	entry, err := New(srv.URL).History(t.Context(), "p1")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if entry != nil {
		t.Fatalf("expected nil, got %+v", entry)
	}
}

func TestFetch_ReturnsBytesAndContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/view" {
			t.Errorf("path = %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("filename") != "out.png" || q.Get("type") != "output" || q.Get("subfolder") != "sub" {
			t.Errorf("bad query: %v", q)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG..."))
	}))
	defer srv.Close()

	body, ctype, err := New(srv.URL).Fetch(t.Context(), ImageRef{Filename: "out.png", Subfolder: "sub", Type: "output"}, "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(body) != "\x89PNG..." || ctype != "image/png" {
		t.Fatalf("got body=%q ctype=%q", body, ctype)
	}
}

func TestFetch_RejectsEmptyFilename(t *testing.T) {
	_, _, err := New("http://example.invalid").Fetch(t.Context(), ImageRef{}, "")
	if err == nil || !strings.Contains(err.Error(), "filename is required") {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func TestEvents_StreamsServerFrames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" || r.URL.Query().Get("clientId") != "cid-1" {
			t.Errorf("bad ws request: %s %s", r.URL.Path, r.URL.RawQuery)
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"executing","data":{"node":"3","prompt_id":"p1"}}`))
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"execution_success","data":{"prompt_id":"p1"}}`))
		// Hold until client cancels so the test controls shutdown.
		<-ctx.Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	events, errs, err := New(srv.URL).Events(ctx, "cid-1")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	got := make([]Event, 0, 2)
	for len(got) < 2 {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("channel closed early; got %d", len(got))
			}
			got = append(got, ev)
		case e := <-errs:
			t.Fatalf("err: %v", e)
		case <-ctx.Done():
			t.Fatalf("timeout; got %d frames", len(got))
		}
	}
	if got[0].Type != "executing" || got[1].Type != "execution_success" {
		t.Fatalf("unexpected types: %v / %v", got[0].Type, got[1].Type)
	}
}

func TestUpload_PostsMultipartAndReturnsResolvedFilename(t *testing.T) {
	var (
		gotMethod, gotPath, gotContentType string
		gotImageBytes                      []byte
		gotImageFilename                   string
		gotImagePartCT                     string
		gotSubfolder, gotType, gotOverwrite string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		// Parse the multipart form to mirror what ComfyUI does.
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		gotSubfolder = r.FormValue("subfolder")
		gotType = r.FormValue("type")
		gotOverwrite = r.FormValue("overwrite")
		fhs := r.MultipartForm.File["image"]
		if len(fhs) != 1 {
			t.Fatalf("expected one image file, got %d", len(fhs))
		}
		gotImageFilename = fhs[0].Filename
		gotImagePartCT = fhs[0].Header.Get("Content-Type")
		f, err := fhs[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		buf := make([]byte, fhs[0].Size)
		if _, err := f.Read(buf); err != nil && err.Error() != "EOF" {
			t.Fatal(err)
		}
		gotImageBytes = buf

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"src (1).png","subfolder":"uploads","type":"input"}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	body := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 'p', 'a', 'y'}
	res, err := c.Upload(t.Context(), "src.png", body, UploadOptions{
		Subfolder:   "uploads",
		Type:        "input",
		ContentType: "image/png",
		Overwrite:   true,
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if res.Filename != "src (1).png" {
		t.Errorf("Filename: %q", res.Filename)
	}
	if res.Subfolder != "uploads" || res.Type != "input" {
		t.Errorf("response fields: %+v", res)
	}

	if gotMethod != http.MethodPost || gotPath != "/upload/image" {
		t.Errorf("request: %s %s", gotMethod, gotPath)
	}
	if !strings.HasPrefix(gotContentType, "multipart/form-data; boundary=") {
		t.Errorf("content-type: %q", gotContentType)
	}
	if gotImageFilename != "src.png" {
		t.Errorf("part filename: %q", gotImageFilename)
	}
	if gotImagePartCT != "image/png" {
		t.Errorf("part content-type: %q", gotImagePartCT)
	}
	if string(gotImageBytes) != string(body) {
		t.Errorf("part body bytes mismatch")
	}
	if gotSubfolder != "uploads" || gotType != "input" || gotOverwrite != "true" {
		t.Errorf("form fields: subfolder=%q type=%q overwrite=%q", gotSubfolder, gotType, gotOverwrite)
	}
}

func TestUpload_RejectsEmptyFilenameOrBody(t *testing.T) {
	c := New("http://unused")
	if _, err := c.Upload(t.Context(), "", []byte("x"), UploadOptions{}); err == nil {
		t.Error("expected error for empty filename")
	}
	if _, err := c.Upload(t.Context(), "x.png", nil, UploadOptions{}); err == nil {
		t.Error("expected error for empty body")
	}
}

func TestObjectInfo_DecodesNodeMap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/object_info" {
			t.Errorf("path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"LoraLoader":{"input":{"required":{"lora_name":[["a.safetensors","b.safetensors"]]}}}}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	info, err := c.ObjectInfo(t.Context())
	if err != nil {
		t.Fatalf("ObjectInfo: %v", err)
	}
	if _, ok := info["LoraLoader"]; !ok {
		t.Errorf("expected LoraLoader entry, got keys %v", keys(info))
	}
}

func keys(m ObjectInfo) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestHTTPToWS(t *testing.T) {
	cases := map[string]string{
		"http://x:1/p":  "ws://x:1/p",
		"https://x:1/p": "wss://x:1/p",
	}
	for in, want := range cases {
		got, err := httpToWS(in)
		if err != nil || got != want {
			t.Errorf("%q → %q (err=%v), want %q", in, got, err, want)
		}
	}
	if _, err := httpToWS("ftp://x"); err == nil {
		t.Errorf("expected error for non-http scheme")
	}
}

// TestLive_SystemStats is opt-in: set TALON_COMFYUI_URL to actually hit
// a running ComfyUI and verify reachability. Useful for sanity-checking
// the BaseURL that a future config-driven resolver will hand in.
func TestLive_SystemStats(t *testing.T) {
	url := os.Getenv("TALON_COMFYUI_URL")
	if url == "" {
		t.Skip("TALON_COMFYUI_URL not set")
	}
	c := New(url)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, c.BaseURL+"/system_stats", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("system_stats http %d", resp.StatusCode)
	}
}
