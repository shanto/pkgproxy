//go:build windows

// Command pkgproxy is a 2-in-1 URI handler for the scoop:// and winget:// URL
// schemes.
//
// It is deliberately thin: the only external dependency is golang.org/x/sys, so
// the binary builds with CGO_ENABLED=0 and needs no C toolchain.
//
// Scope note: scoop is driven through its PowerShell script rather than the
// .cmd shim, because Go builds command lines with CRT-style quoting while
// cmd.exe re-parses them with its own (incompatible) rules. Anything containing
// & | ^ % or < > survives a `powershell.exe -File` invocation and does not
// survive a .cmd one.
package main

import (
	"fmt"
	"os"
	"strings"
)

// version is overridable at build time:
//
//	go build -ldflags "-X main.version=1.2.3"
var version = "0.1.0"

const usageText = `pkgproxy - one URI handler for scoop:// and winget://

USAGE
  pkgproxy <uri>          install from a scoop:// or winget:// link
  pkgproxy register       register both URL schemes for the current user
  pkgproxy unregister     remove the URL scheme registrations
  pkgproxy help           show this help
  pkgproxy version        show the version

URI FORMS
  scoop://install/<pkg>?repo=<bucket-name-or-repo-url>
  winget://install/<PackageId>

  "repo" is also accepted as "bucket", because the pkgproxy search proxy emits
  ?bucket=. Percent-encoding is decoded, so winget ids containing "+" arrive
  intact (Microsoft.VCRedist.2015%2B.x64).

OPTIONS
  -y, --yes       assume "yes" when a scoop bucket has to be added
      --no-pause  do not wait for a keypress after the install finishes
      --force     with unregister: remove the keys even if they are not ours
  -h, --help      show this help
      --version   show the version

EXAMPLES
  pkgproxy "scoop://install/ripgrep?repo=main"
  pkgproxy "scoop://install/7zip-zs?repo=https://github.com/hoilc/scoop-lemon"
  pkgproxy "winget://install/Git.Git"
  pkgproxy register
`

type options struct {
	assumeYes bool
	noPause   bool
	force     bool
	help      bool
	showVer   bool
	args      []string
}

func parseArgs(argv []string) (options, error) {
	var o options
	for _, a := range argv {
		switch {
		case a == "-h" || a == "--help" || a == "help":
			o.help = true
		case a == "--version" || a == "version":
			o.showVer = true
		case a == "-y" || a == "--yes":
			o.assumeYes = true
		case a == "--no-pause":
			o.noPause = true
		case a == "--force":
			o.force = true
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("unknown option %q", a)
		default:
			o.args = append(o.args, a)
		}
	}
	return o, nil
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	opt, err := parseArgs(argv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pkgproxy: %v\n\n%s", err, usageText)
		return 2
	}
	if opt.help {
		fmt.Print(usageText)
		return 0
	}
	if opt.showVer {
		fmt.Println("pkgproxy " + version)
		return 0
	}
	if len(opt.args) == 0 {
		fmt.Fprint(os.Stderr, usageText)
		return 2
	}

	switch cmd := opt.args[0]; {
	case cmd == "register":
		return cmdRegister(opt)
	case cmd == "unregister":
		return cmdUnregister(opt)
	case isSchemeURI(cmd):
		return cmdHandleURI(cmd, opt)
	default:
		fmt.Fprintf(os.Stderr, "pkgproxy: unknown command or URI %q\n\n%s", cmd, usageText)
		return 2
	}
}

func isSchemeURI(s string) bool {
	l := strings.ToLower(s)
	return strings.HasPrefix(l, SchemeScoop+":") || strings.HasPrefix(l, SchemeWinget+":")
}

// cmdHandleURI is the path Windows takes when a scoop:// or winget:// link is
// clicked. It always holds the console open afterwards (unless --no-pause), so
// the package manager's output stays readable.
func cmdHandleURI(raw string, opt options) int {
	req, err := ParseRequest(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pkgproxy: %v\n", err)
		holdTerminal(opt, "Task failed.")
		return 2
	}

	var code int
	switch req.Scheme {
	case SchemeScoop:
		code = installScoop(req, opt)
	case SchemeWinget:
		code = installWinget(req, opt)
	}

	if code == 0 {
		holdTerminal(opt, "Task completed.")
	} else {
		holdTerminal(opt, fmt.Sprintf("Task failed (exit code %d).", code))
	}
	return code
}
