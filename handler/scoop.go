//go:build windows

package main

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// scoopTool is a located scoop entry point plus the way it must be invoked.
type scoopTool struct {
	path string
	kind exeKind
}

func (t scoopTool) argv(args []string) (string, []string) {
	if t.kind == kindPS1 {
		return powershellPath(), psFileArgs(t.path, args)
	}
	return t.path, args
}

// run drives scoop with the console attached, so the user sees progress and can
// answer any prompt.
func (t scoopTool) run(args []string) int {
	name, argv := t.argv(args)
	return runInherit(name, argv)
}

// capture drives scoop with its output collected, for calls we need to parse.
//
// The bytes are decoded before they are returned: a PowerShell child writes
// UTF-16LE to a pipe, so without decoding the parser would see NUL-interleaved
// text and find no rows.
func (t scoopTool) capture(args []string) (string, error) {
	var (
		out []byte
		err error
	)
	if t.kind == kindPS1 {
		out, err = runCapture(powershellPath(), psCaptureArgs(t.path, args))
	} else {
		out, err = runCapture(t.path, args)
	}
	return decodeConsoleOutput(out), err
}

// ---------------------------------------------------------------------------
// install
// ---------------------------------------------------------------------------

func installScoop(req *Request, opt options) int {
	tool, err := findScoop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pkgproxy: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "pkgproxy: using %s\n", tool.path)

	target := req.Name
	if req.Repo != "" {
		bucket := req.Repo
		if looksLikeRepoURL(req.Repo) {
			bucket, err = tool.resolveBucket(req.Repo, opt)
			if err != nil {
				fmt.Fprintf(os.Stderr, "pkgproxy: %v\n", err)
				return 1
			}
		}
		// `main` is scoop's default bucket, so it is addressed unqualified.
		if !strings.EqualFold(bucket, "main") {
			target = bucket + "/" + req.Name
		}
	}

	fmt.Fprintf(os.Stderr, "pkgproxy: scoop install %s\n\n", target)
	return tool.run([]string{"install", target})
}

// looksLikeRepoURL distinguishes a bucket name from a repository reference.
// Bucket names cannot contain a slash, so a slash -- or a scheme, or the scp
// form -- settles it.
func looksLikeRepoURL(s string) bool {
	return strings.Contains(s, "://") || strings.Contains(s, "/") || strings.HasPrefix(s, "git@")
}

// resolveBucket turns a repository URL into a locally usable bucket name.
//
// If a bucket already tracks that URL its existing name is reused, which is the
// point: community bucket names are a local alias, and re-adding the same URL
// under a second name would fail or duplicate it. Otherwise the user is asked
// to confirm a name, and the bucket is added on confirmation.
func (t scoopTool) resolveBucket(repoURL string, opt options) (string, error) {
	entries, err := t.buckets()
	if err != nil {
		return "", err
	}

	want := canonicalRepo(repoURL)
	for _, e := range entries {
		if canonicalRepo(e.Source) == want {
			fmt.Fprintf(os.Stderr, "pkgproxy: %s is already tracked as bucket %q\n", repoURL, e.Name)
			return e.Name, nil
		}
	}

	proposed := defaultBucketName(repoURL)
	name := proposed
	if !opt.assumeYes {
		var ok bool
		if name, ok = promptBucketName(repoURL, proposed, entries); !ok {
			return "", fmt.Errorf("no bucket added for %s; nothing was installed", repoURL)
		}
	}

	if err := t.addBucket(name, repoURL); err != nil {
		return "", err
	}
	return name, nil
}

func (t scoopTool) buckets() ([]bucketEntry, error) {
	out, err := t.capture([]string{"bucket", "list"})
	if err != nil {
		return nil, fmt.Errorf("scoop bucket list failed: %w", err)
	}
	entries := parseBucketList(out)
	if len(entries) == 0 && !isBlank(out) {
		// Output we cannot read is worse than no output at all: it looks
		// exactly like "nothing tracks this URL", which sends the user down
		// the add-bucket path for a bucket that is already there. Say so
		// instead of guessing.
		return nil, errors.New("could not read `scoop bucket list` output; refusing to guess whether the bucket is already present")
	}
	return entries, nil
}

// isBlank reports whether captured output carries nothing at all -- not even a
// header. A BOM on its own is what an empty bucket list looks like.
func isBlank(s string) bool {
	return strings.TrimSpace(strings.TrimPrefix(stripANSI(s), "\ufeff")) == ""
}

func (t scoopTool) addBucket(name, repoURL string) error {
	fmt.Fprintf(os.Stderr, "pkgproxy: scoop bucket add %s %s\n", name, repoURL)
	if code := t.run([]string{"bucket", "add", name, repoURL}); code != 0 {
		return fmt.Errorf("scoop bucket add %s failed (exit code %d)", name, code)
	}
	return nil
}

// ---------------------------------------------------------------------------
// `scoop bucket list` parsing
// ---------------------------------------------------------------------------

type bucketEntry struct {
	Name   string
	Source string
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// parseBucketList reads the table printed by `scoop bucket list`:
//
//	Name         Source                                              Updated                Manifests
//	----         ------                                              -------                ---------
//	main         https://github.com/ScoopInstaller/Main              10/11/2026 10:33:07 PM      1678
//	nirsoft      https://github.com/ScoopInstaller/Nirsoft.git       10/5/2026 5:14:49 PM         291
//
// The Updated column itself contains spaces, and its width is locale dependent
// (24-hour locales drop the AM/PM token), so the source is taken as everything
// between the name and the last four tokens. For a URL that is exact, and it
// still holds for a local path that contains spaces.
func parseBucketList(raw string) []bucketEntry {
	raw = strings.TrimPrefix(stripANSI(raw), "\ufeff")

	var out []bucketEntry
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "Name") || strings.HasPrefix(line, "----") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		source := f[1]
		if len(f) >= 6 {
			source = strings.Join(f[1:len(f)-4], " ")
		}
		out = append(out, bucketEntry{Name: f[0], Source: source})
	}
	return out
}

// ---------------------------------------------------------------------------
// repository reference handling
// ---------------------------------------------------------------------------

var scpLikeRe = regexp.MustCompile(`^git@([^:]+):(.+)$`)

// canonicalRepo reduces a repository reference to host/path, lowercased, with
// any .git suffix and trailing slashes removed. The scheme is ignored on
// purpose, so the https, http and ssh spellings of one bucket compare equal --
// real bucket lists contain entries like .../Nirsoft.git alongside .../Main.
func canonicalRepo(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if m := scpLikeRe.FindStringSubmatch(s); m != nil {
		return strings.ToLower(m[1]) + "/" + strings.ToLower(trimRepoPath(m[2]))
	}
	if !strings.Contains(s, "://") {
		if strings.Contains(s, `\`) {
			return strings.ToLower(s) // local path; will simply never match
		}
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return strings.ToLower(raw)
	}
	p := trimRepoPath(u.Path)
	if p == "" {
		return strings.ToLower(u.Host)
	}
	return strings.ToLower(u.Host) + "/" + strings.ToLower(p)
}

func trimRepoPath(p string) string {
	p = strings.Trim(p, "/")
	p = strings.TrimSuffix(p, ".git")
	return strings.Trim(p, "/")
}

// defaultBucketName proposes a name for a repository URL.
//
// The GitHub username (owner) is used, not the repository name, because that is
// the convention scoop users already follow for community buckets:
//
//	https://github.com/hoilc/scoop-lemon  ->  hoilc   (not "scoop-lemon")
//	https://github.com/foo/bar            ->  foo    (not "bar")
func defaultBucketName(raw string) string {
	host, segs := repoParts(raw)
	if len(segs) > 0 {
		return sanitizeBucketName(segs[0])
	}
	if i := strings.Index(host, "."); i > 0 {
		return sanitizeBucketName(host[:i])
	}
	return sanitizeBucketName(host)
}

func repoParts(raw string) (host string, segs []string) {
	s := strings.TrimSpace(raw)
	if m := scpLikeRe.FindStringSubmatch(s); m != nil {
		return strings.ToLower(m[1]), splitRepoPath(m[2])
	}
	if !strings.Contains(s, "://") && !strings.Contains(s, `\`) {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", nil
	}
	return strings.ToLower(u.Host), splitRepoPath(u.Path)
}

func splitRepoPath(p string) []string {
	p = trimRepoPath(p)
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

var bucketNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitizeBucketName(s string) string {
	s = strings.TrimSpace(s)
	s = bucketNameRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-._")
}

// ---------------------------------------------------------------------------
// prompting
// ---------------------------------------------------------------------------

func promptBucketName(repoURL, proposed string, entries []bucketEntry) (string, bool) {
	fmt.Fprintf(os.Stderr, "\npkgproxy: no local bucket tracks %s\n", repoURL)
	if len(entries) > 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name)
		}
		fmt.Fprintf(os.Stderr, "          you already have: %s\n", strings.Join(names, ", "))
	}
	fmt.Fprintf(os.Stderr, "          proposed bucket name: %s\n", proposed)
	fmt.Fprintf(os.Stderr, "add it? [Y]es / [n]o / or type another name: ")

	line, ok := readLine()
	if !ok {
		fmt.Fprintln(os.Stderr, "\npkgproxy: no input available, aborting")
		return "", false
	}

	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return proposed, true
	case "n", "no":
		return "", false
	}

	name := sanitizeBucketName(line)
	if name == "" {
		fmt.Fprintf(os.Stderr, "pkgproxy: %q is not a usable bucket name\n", line)
		return "", false
	}
	return name, true
}

func readLine() (string, bool) {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	return line, true
}
