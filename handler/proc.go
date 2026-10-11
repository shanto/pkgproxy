//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"unicode/utf16"
)

// runInherit runs a command with the caller's stdio attached, so the package
// manager can print progress and prompt if it needs to. Returns the exit code.
func runInherit(name string, args []string) int {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "pkgproxy: cannot run %s: %v\n", name, err)
		return 1
	}
	return 0
}

// runCapture runs a command and returns its raw stdout. stderr is passed
// straight through so real errors stay visible instead of being swallowed.
//
// The bytes come back undecoded on purpose: a Windows PowerShell host writes
// UTF-16LE whenever its stdout is not a console, so decoding belongs to the
// caller that knows it invoked PowerShell (see decodeConsoleOutput).
func runCapture(name string, args []string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return out.Bytes(), err
	}
	return out.Bytes(), nil
}

// decodeConsoleOutput turns what a child process wrote to a pipe into UTF-8.
//
// PowerShell renders host output as UTF-16LE with a BOM whenever stdout is
// redirected -- both pwsh 7 and Windows PowerShell 5.1, whatever the locale. So
// text read back from a PowerShell child arrives NUL-interleaved and is
// unreadable until it is decoded; feeding it straight to a line parser yields
// no rows at all. Output that is already UTF-8, which is what a plain exe
// writes, passes through untouched.
func decodeConsoleOutput(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		return decodeUTF16(b[2:], binary.LittleEndian)
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return decodeUTF16(b[2:], binary.BigEndian)
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return string(b[3:])
	case looksUTF16LE(b):
		// No BOM. ASCII text in UTF-16LE carries a NUL in every second byte,
		// which UTF-8 text essentially never does.
		return decodeUTF16(b, binary.LittleEndian)
	}
	return string(b)
}

func decodeUTF16(b []byte, order binary.ByteOrder) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, order.Uint16(b[i:i+2]))
	}
	return string(utf16.Decode(u))
}

// looksUTF16LE reports whether b is plausible BOM-less UTF-16LE: at least half
// of its odd-indexed bytes in the first 64 are NUL.
func looksUTF16LE(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	n := len(b)
	if n > 64 {
		n = 64
	}
	nuls := 0
	for i := 1; i < n; i += 2 {
		if b[i] == 0 {
			nuls++
		}
	}
	return nuls >= n/4
}
