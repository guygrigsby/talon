package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBluebubblesServerInfo_Success — happy path: server returns the
// shape the wizard's verify step expects, password makes it onto the
// query string, parsed fields surface back.
func TestBluebubblesServerInfo_Success(t *testing.T) {
	var sawPassword string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPassword = r.URL.Query().Get("password")
		if r.URL.Path != "/api/v1/server/info" {
			t.Errorf("path = %q, want /api/v1/server/info", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": 200,
			"data": {
				"server_version": "1.9.0",
				"os_version": "macOS 14.5",
				"local_ipv4": "192.168.1.10"
			}
		}`))
	}))
	defer srv.Close()

	info, err := bluebubblesServerInfo(t.Context(), srv.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if sawPassword != "secret" {
		t.Errorf("password not forwarded: got %q", sawPassword)
	}
	if info.ServerVersion != "1.9.0" || info.OSVersion != "macOS 14.5" || info.LocalIPv4 != "192.168.1.10" {
		t.Errorf("info = %+v", info)
	}
}

func TestBluebubblesServerInfo_PasswordEscaped(t *testing.T) {
	// Password containing url-reserved chars must round-trip through
	// the query string without being misinterpreted as a separator.
	var sawPassword string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPassword = r.URL.Query().Get("password")
		_, _ = w.Write([]byte(`{"status":200,"data":{}}`))
	}))
	defer srv.Close()

	pw := "p@ss w&rd?#1+"
	if _, err := bluebubblesServerInfo(t.Context(), srv.URL, pw); err != nil {
		t.Fatal(err)
	}
	if sawPassword != pw {
		t.Errorf("password = %q, want %q", sawPassword, pw)
	}
}

func TestBluebubblesServerInfo_AuthFailureSurfaces(t *testing.T) {
	// BlueBubbles returns HTTP 200 with a 401 status field on bad
	// password. The verify helper must surface the message instead
	// of treating it as success.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":401,"message":"Unauthorized — bad password"}`))
	}))
	defer srv.Close()

	_, err := bluebubblesServerInfo(t.Context(), srv.URL, "wrong")
	if err == nil {
		t.Fatal("expected error on status=401")
	}
	if !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("error should surface server message; got %v", err)
	}
}

func TestBluebubblesServerInfo_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := bluebubblesServerInfo(t.Context(), srv.URL, "x")
	if err == nil {
		t.Fatal("expected error on HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status code; got %v", err)
	}
}

func TestBluebubblesServerInfo_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not-json`))
	}))
	defer srv.Close()

	_, err := bluebubblesServerInfo(t.Context(), srv.URL, "x")
	if err == nil {
		t.Fatal("expected decode error")
	}
}
