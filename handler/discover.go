//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type exeKind int

const (
	kindExe exeKind = iota // run directly
	kindPS1                // run through PowerShell with -File
)

var errNotFound = errors.New("not found")

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// findWinget locates winget.exe: first on PATH, then at the App Execution Alias
// location that ships with App Installer.
//
// Caveat worth knowing: if another winget build shadows the real one on PATH
// (UniGetUI bundles one, for instance) PATH wins by design. Both callers print
// the resolved path so the choice is never a mystery.
func findWinget() (string, error) {
	if p, err := exec.LookPath("winget"); err == nil {
		return p, nil
	}
	if p, err := exec.LookPath("winget.exe"); err == nil {
		return p, nil
	}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		p := filepath.Join(la, "Microsoft", "WindowsApps", "winget.exe")
		if isFile(p) {
			return p, nil
		}
	}
	return "", errors.New(`winget not found: not on PATH and %LOCALAPPDATA%\Microsoft\WindowsApps\winget.exe is missing`)
}

// findScoop locates scoop and reports how it must be invoked.
//
// The .ps1 is strongly preferred over the .cmd shim: Go escapes arguments for
// CreateProcess, cmd.exe then re-parses them, and the two disagree about
// & | ^ % < >. PowerShell's -File passes arguments positionally, so nothing is
// re-interpreted.
func findScoop() (scoopTool, error) {
	// 1. The real implementation, if the shim directory is on PATH.
	if p, err := exec.LookPath("scoop.ps1"); err == nil {
		return scoopTool{path: p, kind: kindPS1}, nil
	}

	// 2. Whatever "scoop" resolves to.
	if p, err := exec.LookPath("scoop"); err == nil {
		switch strings.ToLower(filepath.Ext(p)) {
		case ".ps1":
			return scoopTool{path: p, kind: kindPS1}, nil
		case ".exe", ".com":
			return scoopTool{path: p, kind: kindExe}, nil
		default:
			// A .cmd/.bat shim, or the extensionless sh shim (which Go cannot
			// run at all). Both just forward to a sibling scoop.ps1, so prefer
			// that; anything else falls through to the %SCOOP% fallback.
			base := strings.TrimSuffix(p, filepath.Ext(p))
			if isFile(base + ".ps1") {
				return scoopTool{path: base + ".ps1", kind: kindPS1}, nil
			}
		}
	}

	// 3. Documented fallback: %SCOOP%\apps\scoop\current\bin\scoop.ps1
	scoopHome := os.Getenv("SCOOP")
	if scoopHome == "" {
		if up := os.Getenv("USERPROFILE"); up != "" {
			scoopHome = filepath.Join(up, "scoop")
		}
	}
	if scoopHome != "" {
		p := filepath.Join(scoopHome, "apps", "scoop", "current", "bin", "scoop.ps1")
		if isFile(p) {
			return scoopTool{path: p, kind: kindPS1}, nil
		}
	}

	return scoopTool{}, fmt.Errorf(`scoop not found: not on PATH and no %%SCOOP%%\apps\scoop\current\bin\scoop.ps1 (SCOOP=%q)`, scoopHome)
}
