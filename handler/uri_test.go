//go:build windows

package main

import "testing"

func TestParseRequest(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantScheme string
		wantName   string
		wantRepo   string
	}{
		{
			name:       "scoop with bucket name",
			raw:        "scoop://install/ripgrep?repo=main",
			wantScheme: SchemeScoop,
			wantName:   "ripgrep",
			wantRepo:   "main",
		},
		{
			name:       "scoop with repo url",
			raw:        "scoop://install/7zip-zs?repo=https://github.com/hoilc/scoop-lemon",
			wantScheme: SchemeScoop,
			wantName:   "7zip-zs",
			wantRepo:   "https://github.com/hoilc/scoop-lemon",
		},
		{
			// server.mjs emits ?bucket=; it must keep working.
			name:       "bucket alias",
			raw:        "scoop://install/7zip-zs?bucket=https://github.com/hoilc/scoop-lemon",
			wantScheme: SchemeScoop,
			wantName:   "7zip-zs",
			wantRepo:   "https://github.com/hoilc/scoop-lemon",
		},
		{
			name:       "repo wins over bucket when both present",
			raw:        "scoop://install/x?repo=extras&bucket=main",
			wantScheme: SchemeScoop,
			wantName:   "x",
			wantRepo:   "extras",
		},
		{
			// The proxy percent-encodes & in a bucket value; decoding must undo it.
			name:       "percent-encoded ampersand in bucket",
			raw:        "scoop://install/x?bucket=https://example.com/a%26b",
			wantScheme: SchemeScoop,
			wantName:   "x",
			wantRepo:   "https://example.com/a&b",
		},
		{
			name:       "scoop without repo",
			raw:        "scoop://install/ripgrep",
			wantScheme: SchemeScoop,
			wantName:   "ripgrep",
			wantRepo:   "",
		},
		{
			name:       "scoop opaque form",
			raw:        "scoop:install/ripgrep?repo=main",
			wantScheme: SchemeScoop,
			wantName:   "ripgrep",
			wantRepo:   "main",
		},
		{
			name:       "winget id",
			raw:        "winget://install/Git.Git",
			wantScheme: SchemeWinget,
			wantName:   "Git.Git",
		},
		{
			// '+' is percent-encoded because a bare '+' in a query is a space;
			// in the path it must survive verbatim.
			name:       "winget id with encoded plus",
			raw:        "winget://install/Microsoft.VCRedist.2015%2B.x64",
			wantScheme: SchemeWinget,
			wantName:   "Microsoft.VCRedist.2015+.x64",
		},
		{
			name:       "winget name with space",
			raw:        "winget://install/Google%20Chrome",
			wantScheme: SchemeWinget,
			wantName:   "Google Chrome",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRequest(tt.raw)
			if err != nil {
				t.Fatalf("ParseRequest(%q) returned error: %v", tt.raw, err)
			}
			if got.Scheme != tt.wantScheme {
				t.Errorf("Scheme = %q, want %q", got.Scheme, tt.wantScheme)
			}
			if got.Action != ActionInstall {
				t.Errorf("Action = %q, want %q", got.Action, ActionInstall)
			}
			if got.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tt.wantName)
			}
			if got.Repo != tt.wantRepo {
				t.Errorf("Repo = %q, want %q", got.Repo, tt.wantRepo)
			}
		})
	}
}

func TestParseRequestErrors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"unsupported scheme", "https://install/Git.Git"},
		{"unknown scheme", "choco://install/foo"},
		{"unsupported action", "scoop://remove/ripgrep"},
		{"missing action", "scoop://ripgrep"},
		{"empty package name", "scoop://install/"},
		{"leading dash", "winget://install/-evil"},
		{"path separator", "winget://install/a/b"},
		{"backslash", "scoop://install/a%5Cb"},
		{"dot dot", "scoop://install/.."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := ParseRequest(tt.raw); err == nil {
				t.Fatalf("ParseRequest(%q) = %+v, want an error", tt.raw, got)
			}
		})
	}
}

func TestIsSchemeURI(t *testing.T) {
	for _, s := range []string{"scoop://install/x", "winget://install/x", "SCOOP://install/x", "scoop:install/x"} {
		if !isSchemeURI(s) {
			t.Errorf("isSchemeURI(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"register", "unregister", "https://x", "", "scoop"} {
		if isSchemeURI(s) {
			t.Errorf("isSchemeURI(%q) = true, want false", s)
		}
	}
}
