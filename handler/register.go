//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Registration is per-user (HKCU\Software\Classes). That needs no elevation,
// and it matches how scoop itself installs: per user, never machine wide.
const classesRoot = `Software\Classes`

var registeredSchemes = []string{SchemeScoop, SchemeWinget}

// selfPath resolves the running executable's absolute path, so the registry
// entry always points at wherever this binary actually lives.
func selfPath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot determine own path: %w", err)
	}
	return filepath.Abs(p)
}

// handlerCommand is the shell\open\command value Windows executes for a URI.
// %1 is substituted with the full URI; both it and the exe are quoted so a path
// containing spaces still works.
func handlerCommand(exe string) string {
	return `"` + exe + `" "%1"`
}

func cmdRegister(opt options) int {
	exe, err := selfPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pkgproxy: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "pkgproxy: registering %s\n", exe)

	failed := false
	for _, scheme := range registeredSchemes {
		if err := registerScheme(scheme, exe); err != nil {
			fmt.Fprintf(os.Stderr, "  %-7s FAILED: %v\n", scheme+":", err)
			failed = true
			continue
		}
		fmt.Fprintf(os.Stderr, "  %-7s -> %s\n", scheme+":", handlerCommand(exe))
	}
	if failed {
		return 1
	}

	fmt.Fprintf(os.Stderr, "\nDone. Registered for the current user under HKCU\\%s\n", classesRoot)
	fmt.Fprintf(os.Stderr, "Try it: pkgproxy \"scoop://install/ripgrep?repo=main\"\n")
	return 0
}

func registerScheme(scheme, exe string) error {
	base := classesRoot + `\` + scheme

	k, _, err := registry.CreateKey(registry.CURRENT_USER, base, registry.WRITE)
	if err != nil {
		return err
	}
	if err := k.SetStringValue("", "URL:"+scheme+" Protocol"); err != nil {
		k.Close()
		return err
	}
	// The presence of this value -- even empty -- is what makes Windows treat
	// the key as a URI scheme rather than a file extension.
	if err := k.SetStringValue("URL Protocol", ""); err != nil {
		k.Close()
		return err
	}
	k.Close()

	cmd, _, err := registry.CreateKey(registry.CURRENT_USER, base+`\shell\open\command`, registry.WRITE)
	if err != nil {
		return err
	}
	defer cmd.Close()
	return cmd.SetStringValue("", handlerCommand(exe))
}

func cmdUnregister(opt options) int {
	failed := false
	for _, scheme := range registeredSchemes {
		base := classesRoot + `\` + scheme

		current, err := existingCommand(base)
		switch {
		case errors.Is(err, registry.ErrNotExist):
			fmt.Fprintf(os.Stderr, "  %-7s not registered, skipping\n", scheme+":")
			continue
		case err != nil:
			fmt.Fprintf(os.Stderr, "  %-7s FAILED: %v\n", scheme+":", err)
			failed = true
			continue
		}

		// Refuse to delete a handler somebody else installed unless told to.
		if !opt.force && !isOurCommand(current) {
			fmt.Fprintf(os.Stderr, "  %-7s not registered by pkgproxy (command: %s)\n", scheme+":", current)
			fmt.Fprintf(os.Stderr, "          use --force to remove it anyway\n")
			failed = true
			continue
		}

		if err := deleteTree(registry.CURRENT_USER, base); err != nil {
			fmt.Fprintf(os.Stderr, "  %-7s FAILED: %v\n", scheme+":", err)
			failed = true
			continue
		}
		fmt.Fprintf(os.Stderr, "  %-7s removed (was: %s)\n", scheme+":", current)
	}

	if failed {
		return 1
	}
	fmt.Fprintln(os.Stderr, "\nDone. Both schemes are unregistered for the current user.")
	return 0
}

// existingCommand reads shell\open\command for a scheme, or returns
// registry.ErrNotExist when the scheme is not registered at all.
func existingCommand(base string) (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, base+`\shell\open\command`, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()

	v, _, err := k.GetStringValue("")
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	return v, err
}

func isOurCommand(cmd string) bool {
	return strings.Contains(strings.ToLower(cmd), "pkgproxy")
}

// deleteTree removes a registry key and everything beneath it. Subkeys are
// deleted depth first, because RegDeleteKey only removes a key once it is
// childless.
func deleteTree(root registry.Key, path string) error {
	k, err := registry.OpenKey(root, path, registry.ALL_ACCESS)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}

	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		k.Close()
		return err
	}
	for _, n := range names {
		if err := deleteTree(k, n); err != nil {
			k.Close()
			return err
		}
	}
	k.Close()

	return registry.DeleteKey(root, path)
}
