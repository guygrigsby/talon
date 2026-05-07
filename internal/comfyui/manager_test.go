package comfyui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManagerStatus_DetectsManagerOnPrimaryPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/customnode/getmappings":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	st, err := c.ManagerStatus(t.Context())
	if err != nil {
		t.Fatalf("ManagerStatus: %v", err)
	}
	if !st.Present {
		t.Errorf("expected Present=true, got %+v", st)
	}
	if st.Endpoint != "/customnode/getmappings" {
		t.Errorf("expected primary endpoint, got %q", st.Endpoint)
	}
}

func TestManagerStatus_FallsBackToVersionPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/customnode/getmappings":
			http.NotFound(w, r) // simulate older manager that doesn't expose this
		case "/manager/version":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("3.32.0"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	st, _ := c.ManagerStatus(t.Context())
	if !st.Present {
		t.Errorf("expected Present=true via fallback, got %+v", st)
	}
	if st.Endpoint != "/manager/version" {
		t.Errorf("expected fallback endpoint, got %q", st.Endpoint)
	}
}

func TestManagerStatus_NotPresent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Plain ComfyUI without manager: 404 on every manager path.
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	c := New(srv.URL)
	st, err := c.ManagerStatus(t.Context())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if st.Present {
		t.Errorf("expected Present=false, got %+v", st)
	}
}

func TestManagerInstall_PostsJSONAndAccepts200(t *testing.T) {
	var (
		gotMethod, gotPath, gotCT string
		gotBody                   ManagerInstallRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"message":"queued"}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	res, err := c.ManagerInstall(t.Context(), ManagerInstallRequest{
		Type:     "loras",
		URL:      "https://civitai.com/api/download/models/12345",
		Filename: "simpsons_xl.safetensors",
		SavePath: "loras",
	})
	if err != nil {
		t.Fatalf("ManagerInstall: %v", err)
	}
	if !res.OK {
		t.Errorf("expected OK=true, got %+v", res)
	}
	if res.Message != "queued" {
		t.Errorf("message: %q", res.Message)
	}
	if gotMethod != http.MethodPost || gotPath != "/model/install" {
		t.Errorf("request: %s %s", gotMethod, gotPath)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type: %q", gotCT)
	}
	if gotBody.Type != "loras" || gotBody.URL == "" || gotBody.Filename != "simpsons_xl.safetensors" {
		t.Errorf("body forwarded incorrectly: %+v", gotBody)
	}
}

func TestManagerInstall_AcceptsEmptyResponseBody(t *testing.T) {
	// Some manager versions accept and queue with a 200 + empty body.
	// The implementation should still report OK=true.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL)
	res, err := c.ManagerInstall(t.Context(), ManagerInstallRequest{
		Type: "loras",
		URL:  "https://example/lora.safetensors",
	})
	if err != nil {
		t.Fatalf("ManagerInstall: %v", err)
	}
	if !res.OK {
		t.Errorf("expected OK=true on empty 200, got %+v", res)
	}
}

func TestManagerInstall_SurfacesNon2xxBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"unsupported model type"}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.ManagerInstall(t.Context(), ManagerInstallRequest{
		Type: "loras",
		URL:  "https://example/lora.safetensors",
	})
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "unsupported model type") {
		t.Errorf("error should expose status + body: %v", err)
	}
}

func TestManagerInstall_RejectsMissingFields(t *testing.T) {
	c := New("http://unused")
	if _, err := c.ManagerInstall(t.Context(), ManagerInstallRequest{URL: "https://x"}); err == nil {
		t.Error("expected error for empty type")
	}
	if _, err := c.ManagerInstall(t.Context(), ManagerInstallRequest{Type: "loras"}); err == nil {
		t.Error("expected error for empty url")
	}
}
