package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDiscoverLMStudioModels_HappyPath exercises a realistic
// LM Studio /v1/models response: a single loaded model with
// max_context_length set. The discovery should surface it as
// provider="lmstudio", id matching the server's id, contextWindow
// from max_context_length.
func TestDiscoverLMStudioModels_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{
			"object": "list",
			"data": [
				{"id": "dolphin-mistral-24b-venice-addition", "object": "model", "owned_by": "user", "max_context_length": 32768, "state": "loaded"}
			]
		}`))
	}))
	defer srv.Close()

	merged := []byte(`{"models":{"providers":{"lmstudio":{"baseUrl":"` + srv.URL + `"}}}}`)
	got, err := discoverLMStudioModels(context.Background(), merged, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d models, want 1", len(got))
	}
	if got[0]["id"] != "dolphin-mistral-24b-venice-addition" {
		t.Errorf("id = %v", got[0]["id"])
	}
	if got[0]["provider"] != "lmstudio" {
		t.Errorf("provider = %v", got[0]["provider"])
	}
	if got[0]["contextWindow"].(int64) != 32768 {
		t.Errorf("contextWindow = %v", got[0]["contextWindow"])
	}
}

// TestDiscoverLMStudioModels_FiltersUnloadedEntries: LM Studio
// reports both loaded and not-loaded models in /v1/models. We
// only want loaded ones in the picker so users don't accidentally
// pick a model the server can't serve.
func TestDiscoverLMStudioModels_FiltersUnloadedEntries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "loaded-one",   "state": "loaded"},
				{"id": "unloaded-two", "state": "not-loaded"}
			]
		}`))
	}))
	defer srv.Close()

	merged := []byte(`{"models":{"providers":{"lmstudio":{"baseUrl":"` + srv.URL + `"}}}}`)
	got, err := discoverLMStudioModels(context.Background(), merged, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["id"] != "loaded-one" {
		t.Errorf("expected only loaded-one, got %+v", got)
	}
}

// TestDiscoverLMStudioModels_ServerDownReturnsError: when the
// LM Studio process isn't running, models.list must NOT block —
// the helper returns an error, the caller swallows it and falls
// back to the static catalog.
func TestDiscoverLMStudioModels_ServerDownReturnsError(t *testing.T) {
	merged := []byte(`{"models":{"providers":{"lmstudio":{"baseUrl":"http://127.0.0.1:1"}}}}`)
	_, err := discoverLMStudioModels(context.Background(), merged, &http.Client{})
	if err == nil {
		t.Fatal("expected error when LM Studio is unreachable")
	}
}

// TestDiscoverLMStudioModels_NonOkPropagated: a 401 (e.g. a
// proxy in front of LM Studio rejecting the placeholder key)
// surfaces as an error so the operator gets a hint.
func TestDiscoverLMStudioModels_NonOkPropagated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "auth required", http.StatusUnauthorized)
	}))
	defer srv.Close()
	merged := []byte(`{"models":{"providers":{"lmstudio":{"baseUrl":"` + srv.URL + `"}}}}`)
	_, err := discoverLMStudioModels(context.Background(), merged, srv.Client())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 error, got %v", err)
	}
}
