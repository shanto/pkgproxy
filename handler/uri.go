//go:build windows

package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	SchemeScoop  = "scoop"
	SchemeWinget = "winget"

	ActionInstall = "install"
)

// Request is a parsed scoop:// or winget:// URI.
type Request struct {
	Scheme string // SchemeScoop | SchemeWinget
	Action string // always ActionInstall for now
	Name   string // percent-decoded package name / winget id
	Repo   string // scoop only: bucket name or repository URL, "" if absent
	Raw    string // the URI as received
}

// ParseRequest turns a URI into a Request.
//
// It accepts both the authority form and the opaque form, because different
// callers normalise custom schemes differently:
//
//	scoop://install/ripgrep?repo=main   -> Host="install", Path="/ripgrep"
//	scoop:install/ripgrep               -> Opaque="install/ripgrep"
//
// Note that url.Parse leaves the path percent-decoded, which is what makes
// winget ids containing "+" (Microsoft.VCRedist.2015%2B.x64) work.
func ParseRequest(raw string) (*Request, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("empty URI")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("cannot parse URI %q: %w", raw, err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != SchemeScoop && scheme != SchemeWinget {
		return nil, fmt.Errorf("unsupported scheme %q (expected scoop or winget)", u.Scheme)
	}

	action, rest := strings.ToLower(u.Host), u.Path
	if action == "" && u.Opaque != "" {
		// Opaque is not percent-decoded by url.Parse, so do it ourselves.
		decoded, derr := url.PathUnescape(u.Opaque)
		if derr != nil {
			decoded = u.Opaque
		}
		parts := strings.SplitN(decoded, "/", 2)
		action = strings.ToLower(parts[0])
		if len(parts) == 2 {
			rest = "/" + parts[1]
		}
	}

	if action != ActionInstall {
		return nil, fmt.Errorf("unsupported action %q in %q (only %q is implemented)", action, raw, ActionInstall)
	}

	name := strings.Trim(rest, "/")
	if err := validatePackageName(name); err != nil {
		return nil, err
	}

	req := &Request{Scheme: scheme, Action: action, Name: name, Raw: raw}

	if scheme == SchemeScoop {
		q := u.Query()
		// "repo" is the documented parameter; "bucket" is what the pkgproxy
		// search proxy actually emits, so accept it as an alias.
		repo := strings.TrimSpace(q.Get("repo"))
		if repo == "" {
			repo = strings.TrimSpace(q.Get("bucket"))
		}
		req.Repo = repo
	}

	return req, nil
}

// validatePackageName rejects anything that could be read as an option by the
// package manager, or that smuggles in a path.
func validatePackageName(name string) error {
	switch {
	case name == "":
		return errors.New("the URI has no package name")
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("invalid package name %q: must not start with '-'", name)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("invalid package name %q: must not contain a path separator (use ?repo=<bucket> for scoop buckets)", name)
	case name == "." || name == "..":
		return fmt.Errorf("invalid package name %q", name)
	}
	return nil
}
