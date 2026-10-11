//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
)

func installWinget(req *Request, opt options) int {
	path, err := findWinget()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pkgproxy: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "pkgproxy: using %s\n", path)

	args := []string{"install"}
	if looksLikeWingetID(req.Name) {
		// Exact id match. -e is deliberate: without it winget falls back to
		// substring matching and can install the wrong package.
		args = append(args, "--id", req.Name, "-e")
	} else {
		// Not shaped like an id, so treat it as a name. --exact still refuses
		// fuzzy matches, so the worst case is a clear "not found" instead of a
		// silently wrong install.
		args = append(args, "--exact", req.Name)
	}

	fmt.Fprintf(os.Stderr, "pkgproxy: winget %s\n\n", strings.Join(args, " "))
	return runInherit(path, args)
}

// looksLikeWingetID reports whether the value has the Vendor.Product shape that
// winget package identifiers use. Names ("Firefox") do not contain a dot.
func looksLikeWingetID(s string) bool {
	return strings.Contains(s, ".") && !strings.ContainsAny(s, " \t")
}
