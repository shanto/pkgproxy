//go:build windows

package main

import (
	"os/exec"
	"strings"
	"sync"
)

var (
	psOnce sync.Once
	psPath string
)

// powershellPath returns the PowerShell host used to drive scoop.
//
// Windows PowerShell (powershell.exe) wins over pwsh on purpose: it ships with
// every supported Windows version, and a URI handler that only works when the
// user happens to have installed PowerShell 7 is a handler that fails on a
// stock machine. pwsh is still accepted as a fallback for the case where the
// system directory is not on PATH. This is a deliberate difference from scoop's
// own .cmd shim, which prefers pwsh -- scoop is a tool the user installed and
// chose, while a link handler has to work uninvited.
//
// Nothing we parse depends on the host: both render host output as UTF-16LE
// (see decodeConsoleOutput), and both take the same -File / -Command forms.
func powershellPath() string {
	psOnce.Do(func() {
		if p, err := exec.LookPath("powershell"); err == nil {
			psPath = p
			return
		}
		if p, err := exec.LookPath("pwsh"); err == nil {
			psPath = p
			return
		}
		psPath = "powershell.exe"
	})
	return psPath
}

// psFileArgs builds the argv for running a .ps1 file.
//
// Everything after -File is passed to the script positionally, so PowerShell
// never parses it as code. This is exactly why scoop is driven this way: an
// argument like `a&b` or `Microsoft.VCRedist.2015+.x64` arrives byte-for-byte.
//
// This form is for installs, which must be able to prompt -- so no
// -NonInteractive. Calls whose output we read back use psCaptureArgs instead.
func psFileArgs(script string, args []string) []string {
	out := make([]string, 0, 8+len(args))
	out = append(out, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script)
	return append(out, args...)
}

// psCaptureArgs builds the argv for a scoop call whose output we read back.
//
// -File is deliberately not used here, even though it is the right form for an
// install. scoop's shims/scoop.ps1 is a two-line forwarder that ends in
// `exit $LASTEXITCODE`, and when it is run with -File the objects it forwards
// never reach the redirected stdout: `scoop bucket list` comes back empty, and
// the caller then concludes that no bucket tracks the URL it was asked about.
// Verified against the real shim -- -File yields 6 bytes (a BOM and a blank
// line), -Command yields the whole table. -Command is only ever used for calls
// with arguments we control, so it costs none of the quoting safety that -File
// buys on the install path.
//
// Out-String -Width fixes the render width. Format-Table sizes its columns to
// the host, so at the default width a long Source URL is truncated with an
// ellipsis -- which would silently defeat URL matching rather than fail.
func psCaptureArgs(script string, args []string) []string {
	var b strings.Builder
	b.WriteString("& ")
	b.WriteString(psQuote(script))
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(psQuote(a))
	}
	b.WriteString(" | Out-String -Width 400")

	return []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", b.String()}
}

// psQuote wraps a value in a PowerShell single-quoted string, inside which the
// only escape is a doubled quote. Nothing in a single-quoted string is expanded,
// so $ and backtick survive verbatim.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
