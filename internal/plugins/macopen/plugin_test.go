package macopen

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildOpenArgs(t *testing.T) {
	cases := []struct {
		name string
		in   openArgs
		want []string
	}{
		{
			name: "app by name, no extras",
			in:   openArgs{App: "Safari"},
			want: []string{"-a", "Safari"},
		},
		{
			name: "app by path",
			in:   openArgs{App: "/Applications/Safari.app"},
			want: []string{"-a", "/Applications/Safari.app"},
		},
		{
			name: "bundle ID",
			in:   openArgs{BundleID: "com.apple.Safari"},
			want: []string{"-b", "com.apple.Safari"},
		},
		{
			name: "url in app",
			in:   openArgs{App: "Safari", URL: "https://example.com"},
			want: []string{"-a", "Safari", "https://example.com"},
		},
		{
			name: "args forwarded after --args",
			in:   openArgs{App: "Safari", Args: []string{"--incognito", "--profile=work"}},
			want: []string{"-a", "Safari", "--args", "--incognito", "--profile=work"},
		},
		{
			name: "new instance + background flags precede -a",
			in:   openArgs{App: "Finder", NewInstance: true, Background: true},
			want: []string{"-n", "-g", "-a", "Finder"},
		},
		{
			name: "everything combined, with bundle id",
			in: openArgs{
				BundleID:    "com.apple.Safari",
				URL:         "https://example.com",
				Args:        []string{"--kiosk"},
				NewInstance: true,
				Background:  true,
			},
			want: []string{"-n", "-g", "-b", "com.apple.Safari", "https://example.com", "--args", "--kiosk"},
		},
		{
			name: "whitespace in fields gets trimmed",
			in:   openArgs{App: "  Safari  "},
			want: []string{"-a", "Safari"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := buildOpenArgs(c.in)
			if err != nil {
				t.Fatalf("buildOpenArgs errored: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("buildOpenArgs(%+v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestBuildOpenArgs_RejectsMissingAppAndBundle(t *testing.T) {
	_, err := buildOpenArgs(openArgs{URL: "https://example.com"})
	if err == nil {
		t.Fatal("expected error when neither app nor bundle_id provided")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error should mention required fields: %v", err)
	}
}

func TestBuildOpenArgs_RejectsBothAppAndBundle(t *testing.T) {
	_, err := buildOpenArgs(openArgs{App: "Safari", BundleID: "com.apple.Safari"})
	if err == nil {
		t.Fatal("expected error when both app and bundle_id provided")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention mutual exclusion: %v", err)
	}
}

// Whitespace-only fields count as missing — agents that pass empty
// strings shouldn't accidentally trigger the "open with default
// handler" path with no target.
func TestBuildOpenArgs_WhitespaceOnlyTreatedAsMissing(t *testing.T) {
	_, err := buildOpenArgs(openArgs{App: "   ", BundleID: "\t"})
	if err == nil {
		t.Fatal("expected error when both fields are whitespace-only")
	}
}
