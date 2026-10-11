//go:build windows

package main

import (
	"strings"
	"testing"
	"unicode/utf16"
)

// Real output captured from `scoop bucket list` on Windows, BOM and CRLF
// included, so the parser is tested against the actual shape rather than a
// cleaned-up idealisation.
const realBucketList = "\ufeff" +
	"Name         Source                                              Updated                Manifests\r\n" +
	"----         ------                                              -------                ---------\r\n" +
	"main         https://github.com/ScoopInstaller/Main              10/11/2026 10:33:07 PM      1678\r\n" +
	"extras       https://github.com/ScoopInstaller/Extras            10/11/2026 10:49:22 PM      2420\r\n" +
	"nirsoft      https://github.com/ScoopInstaller/Nirsoft.git       10/5/2026 5:14:49 PM         291\r\n" +
	"nerd-fonts   https://github.com/matthewjberger/scoop-nerd-fonts  9/26/2026 10:59:28 PM        367\r\n" +
	"jastmaskerrr https://github.com/Jastmaskerrr/Rojem-Scoop         10/11/2026 8:56:29 PM        124\r\n" +
	"\r\n"

func TestParseBucketList(t *testing.T) {
	got := parseBucketList(realBucketList)

	want := []bucketEntry{
		{Name: "main", Source: "https://github.com/ScoopInstaller/Main"},
		{Name: "extras", Source: "https://github.com/ScoopInstaller/Extras"},
		{Name: "nirsoft", Source: "https://github.com/ScoopInstaller/Nirsoft.git"},
		{Name: "nerd-fonts", Source: "https://github.com/matthewjberger/scoop-nerd-fonts"},
		{Name: "jastmaskerrr", Source: "https://github.com/Jastmaskerrr/Rojem-Scoop"},
	}

	if len(got) != len(want) {
		t.Fatalf("got %d buckets, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("bucket[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseBucketListHandlesNoHeader(t *testing.T) {
	got := parseBucketList("main  https://github.com/ScoopInstaller/Main\n")
	if len(got) != 1 || got[0].Name != "main" || got[0].Source != "https://github.com/ScoopInstaller/Main" {
		t.Fatalf("unexpected parse: %+v", got)
	}
}

func TestCanonicalRepo(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://github.com/hoilc/scoop-lemon", "github.com/hoilc/scoop-lemon"},
		{"https://github.com/ScoopInstaller/Main", "github.com/scoopinstaller/main"},
		{"https://github.com/ScoopInstaller/Nirsoft.git", "github.com/scoopinstaller/nirsoft"},
		{"https://github.com/ScoopInstaller/Nirsoft/", "github.com/scoopinstaller/nirsoft"},
		{"http://github.com/foo/bar", "github.com/foo/bar"},
		{"github.com/foo/bar", "github.com/foo/bar"},
		{"git@github.com:foo/bar.git", "github.com/foo/bar"},
		{"HTTPS://GitHub.com/Foo/Bar", "github.com/foo/bar"},
		{"", ""},
	}

	for _, tt := range tests {
		if got := canonicalRepo(tt.in); got != tt.want {
			t.Errorf("canonicalRepo(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// The .git suffix in the real bucket list must not defeat matching.
func TestCanonicalRepoMatchesRealBucketEntries(t *testing.T) {
	if canonicalRepo("https://github.com/ScoopInstaller/Nirsoft.git") != canonicalRepo("https://github.com/ScoopInstaller/Nirsoft") {
		t.Error("Nirsoft.git and Nirsoft should canonicalise to the same value")
	}
}

func TestDefaultBucketName(t *testing.T) {
	tests := []struct{ in, want string }{
		// The spec's example: the GitHub username, not the repository name.
		{"https://github.com/foo/bar", "foo"},
		{"https://github.com/hoilc/scoop-lemon", "hoilc"},
		{"https://github.com/Jastmaskerrr/Rojem-Scoop", "Jastmaskerrr"},
		{"github.com/foo/bar", "foo"},
		{"git@github.com:foo/bar.git", "foo"},
		{"https://gitlab.com/team/repo", "team"},
		// Only one path segment: that segment is the best available guess.
		{"https://example.com/repo", "repo"},
		// No path at all: fall back to the first host label.
		{"https://example.com", "example"},
	}

	for _, tt := range tests {
		if got := defaultBucketName(tt.in); got != tt.want {
			t.Errorf("defaultBucketName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSanitizeBucketName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"foo", "foo"},
		{"  foo  ", "foo"},
		{"scoop lemon", "scoop-lemon"},
		{"-leading", "leading"},
		{".dotted.", "dotted"},
		{"a/b\\c", "a-b-c"},
		{"", ""},
	}

	for _, tt := range tests {
		if got := sanitizeBucketName(tt.in); got != tt.want {
			t.Errorf("sanitizeBucketName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLooksLikeRepoURL(t *testing.T) {
	for _, s := range []string{
		"https://github.com/foo/bar",
		"github.com/foo/bar",
		"git@github.com:foo/bar.git",
	} {
		if !looksLikeRepoURL(s) {
			t.Errorf("looksLikeRepoURL(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"main", "extras", "nerd-fonts"} {
		if looksLikeRepoURL(s) {
			t.Errorf("looksLikeRepoURL(%q) = true, want false", s)
		}
	}
}

func TestLooksLikeWingetID(t *testing.T) {
	for _, s := range []string{"Git.Git", "Microsoft.VCRedist.2015+.x64", "7zip.7zip"} {
		if !looksLikeWingetID(s) {
			t.Errorf("looksLikeWingetID(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"Firefox", "Google Chrome", "7-Zip"} {
		if looksLikeWingetID(s) {
			t.Errorf("looksLikeWingetID(%q) = true, want false", s)
		}
	}
}

func TestHandlerCommandQuotes(t *testing.T) {
	got := handlerCommand(`C:\Program Files\pkgproxy\pkgproxy.exe`)
	want := `"C:\Program Files\pkgproxy\pkgproxy.exe" "%1"`
	if got != want {
		t.Errorf("handlerCommand() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// output decoding
// ---------------------------------------------------------------------------

// utf16LE renders s the way a PowerShell host writes it to a redirected stdout:
// UTF-16LE with a BOM. Both pwsh 7 and Windows PowerShell 5.1 do this, so it is
// the shape `scoop bucket list` actually arrives in.
func utf16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, 2+2*len(u))
	b = append(b, 0xFF, 0xFE)
	for _, r := range u {
		b = append(b, byte(r), byte(r>>8))
	}
	return b
}

func TestDecodeConsoleOutput(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{"utf16le with BOM", utf16LE("main\thttps://github.com/ScoopInstaller/Main\r\n"), "main\thttps://github.com/ScoopInstaller/Main\r\n"},
		{"utf16be with BOM", []byte{0xFE, 0xFF, 0x00, 'm', 0x00, 'a'}, "ma"},
		{"utf8 with BOM", []byte{0xEF, 0xBB, 0xBF, 'm', 'a', 'i', 'n'}, "main"},
		{"plain utf8", []byte("main https://github.com/foo/bar"), "main https://github.com/foo/bar"},
		{"utf16le without BOM", []byte{'m', 0, 'a', 0, 'i', 0, 'n', 0}, "main"},
		{"empty", nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decodeConsoleOutput(tt.in); got != tt.want {
				t.Errorf("decodeConsoleOutput(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsBlank(t *testing.T) {
	// What an empty bucket list looks like: a BOM and a blank line.
	if !isBlank("\ufeff\r\n") {
		t.Error(`isBlank(BOM + blank line) = false, want true`)
	}
	if !isBlank("") {
		t.Error(`isBlank("") = false, want true`)
	}
	// A header on its own is not blank: it parsed to nothing, which is a bug
	// worth reporting rather than treating as an empty list.
	if isBlank("Name         Source   Updated   Manifests\r\n----  ------  -------  ---------\r\n") {
		t.Error("isBlank(header only) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// capture invocation
// ---------------------------------------------------------------------------

// Regression: scoop's shims/scoop.ps1 forwards to the real script and ends in
// `exit $LASTEXITCODE`. Run with -File, it loses everything it forwards, so
// `scoop bucket list` came back as a bare BOM and pkgproxy decided no bucket
// tracked the URL. -Command keeps the objects on the success stream.
func TestPsCaptureArgsDoesNotUseFile(t *testing.T) {
	got := psCaptureArgs(`C:\Users\Shaan\AppData\Local\Scoop\shims\scoop.ps1`, []string{"bucket", "list"})

	want := []string{
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command",
		`& 'C:\Users\Shaan\AppData\Local\Scoop\shims\scoop.ps1' 'bucket' 'list' | Out-String -Width 400`,
	}
	if len(got) != len(want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for _, a := range got {
		if a == "-File" {
			t.Error("-File must not be used to capture scoop output")
		}
	}
	// Out-String -Width is load bearing: Format-Table truncates a long Source
	// URL to fit the host, which would break URL matching silently.
	if !strings.Contains(got[len(got)-1], "Out-String -Width") {
		t.Error("capture command must pin the render width")
	}
}

func TestPsQuote(t *testing.T) {
	tests := []struct{ in, want string }{
		{`C:\scoop\shims\scoop.ps1`, `'C:\scoop\shims\scoop.ps1'`},
		{`C:\it's\scoop.ps1`, `'C:\it''s\scoop.ps1'`},
		{"$env:SCOOP", "'$env:SCOOP'"}, // single quotes: no expansion
	}
	for _, tt := range tests {
		if got := psQuote(tt.in); got != tt.want {
			t.Errorf("psQuote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// the reported bug, end to end through the parser
// ---------------------------------------------------------------------------

// Regression for the reported failure: `scoop bucket list` showed
//
//	shanto   https://github.com/shanto/Scoop
//
// and scoop://install/ldns?bucket=https://github.com/shanto/Scoop still reported
// "no local bucket tracks https://github.com/shanto/Scoop". Two defects stacked:
// the capture returned nothing, and what did come back was UTF-16LE, which the
// line parser cannot read.
func TestBucketListFromPowerShellIsReadable(t *testing.T) {
	const table = "\ufeff" +
		"Name         Source                                              Updated                Manifests\r\n" +
		"----         ------                                              -------                ---------\r\n" +
		"shanto       https://github.com/shanto/Scoop                     10/12/2026 1:22:19 AM         12\r\n" +
		"verymucho    https://github.com/verymucho/knick-knacks           10/12/2026 12:10:53 AM        67\r\n"

	entries := parseBucketList(decodeConsoleOutput(utf16LE(table)))
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}
	if entries[0].Name != "shanto" || entries[0].Source != "https://github.com/shanto/Scoop" {
		t.Fatalf("shanto entry = %+v", entries[0])
	}

	// The whole point: the URL from the link resolves to that bucket.
	link := "https://github.com/shanto/Scoop"
	if canonicalRepo(entries[0].Source) != canonicalRepo(link) {
		t.Errorf("bucket source %q does not match link %q", entries[0].Source, link)
	}
}

// scoop's own duplicate check compares repository URIs with PowerShell -eq,
// which ignores case, so github.com/shanto/Scoop and github.com/shanto/scoop
// are one and the same bucket. A link may carry either spelling.
func TestCanonicalRepoIgnoresCase(t *testing.T) {
	same := [][2]string{
		{"https://github.com/shanto/Scoop", "https://github.com/shanto/scoop"},
		{"https://github.com/shanto/Scoop", "https://github.com/SHANTO/SCOOP"},
		{"https://github.com/ScoopInstaller/Main", "https://github.com/scoopinstaller/main"},
		{"https://github.com/Jastmaskerrr/Rojem-Scoop", "https://github.com/jastmaskerrr/rojem-scoop"},
		{"https://github.com/matthewjberger/scoop-nerd-fonts", "https://github.com/MatthewJberger/Scoop-Nerd-Fonts"},
	}
	for _, p := range same {
		if canonicalRepo(p[0]) != canonicalRepo(p[1]) {
			t.Errorf("%q and %q should canonicalise to the same value (%q vs %q)",
				p[0], p[1], canonicalRepo(p[0]), canonicalRepo(p[1]))
		}
	}
}
