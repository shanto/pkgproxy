//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// holdTerminal keeps the console window alive after an install so the user can
// read scoop/winget output. A URI handler launched by Explorer gets its own
// console, and without this it would vanish the instant we exit.
//
// It is a no-op when stdin is not a console (piped or redirected input), so it
// never blocks scripted use, and when --no-pause was passed.
func holdTerminal(opt options, lead string) {
	if opt.noPause || !stdinIsConsole() {
		return
	}
	fmt.Fprintf(os.Stderr, "\n%s Press any key to terminate.\n", lead)
	waitForKey()
}

func stdinIsConsole() bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(os.Stdin.Fd()), &mode) == nil
}

// waitForKey reads a single keypress without requiring Enter, by clearing
// ENABLE_LINE_INPUT and ENABLE_ECHO_INPUT for the duration.
func waitForKey() {
	h := windows.Handle(os.Stdin.Fd())

	var old uint32
	restore := false
	if err := windows.GetConsoleMode(h, &old); err == nil {
		raw := old &^ (windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT)
		if windows.SetConsoleMode(h, raw) == nil {
			restore = true
		}
	}

	var b [1]byte
	_, _ = os.Stdin.Read(b[:])

	if restore {
		_ = windows.SetConsoleMode(h, old)
	}
}
