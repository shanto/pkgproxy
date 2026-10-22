# pkgproxy

A local proxy that puts **one search endpoint** in front of two package
managers, and returns every result as a **`scoop://` or `winget://` hyperlink**.

- **scoop** → the [ScoopSearch](https://scoopinstaller.github.io/) azure index — the official
  Scoop search engine. **180,218 manifests** across official *and* community buckets,
  full-text, with descriptions, licences and per-repo stars. Needs `SCOOP_AZURE_KEY`.
  Without a key it falls back to the keyless jsDelivr listing of the eight official buckets.
- **winget** → the [`winstall.app`](https://api.winstall.app) API. One request per search, and
  the package object carries everything inline — **homepage, licence, tags, categories, the
  manifest path and every installer URL** — so there is no per-app follow-up and nothing to
  cache. No key.

Zero dependencies. Node 22+ (uses global `fetch` and `process.loadEnvFile`).


https://github.com/user-attachments/assets/c51099cf-0f17-40a9-93b7-478fc490f68a


## Run

```bash
cd src
node server.mjs              # http://127.0.0.1:8787
PORT=9000 node server.mjs    # custom port
```

Configuration lives in `src/.env`, which the server loads at startup:

```
SCOOP_AZURE_KEY=<your azure search query key>
SCOOP_SOURCE=azure
```

Real environment variables always win over the file, so `SCOOP_SOURCE=jsdelivr node server.mjs`
overrides it. With no key the proxy answers from the jsDelivr fallback instead of failing
every search — and the health line in the UI goes red to say so.

Open <http://127.0.0.1:8787>. Type a name, get results from both camps **side by side** in a
2-column grid that collapses to 1 column under 960px.

Searches are deep-linkable:

```
http://127.0.0.1:8787/?q=firefox
http://127.0.0.1:8787/?q=git&source=scoop
```

---

## The URI contract

This is the part you wire up. Every result row carries a `uri` field:

| Case | URI form | Example |
|---|---|---|
| scoop, official bucket | `scoop://install/<app>?bucket=<name>` | `scoop://install/ripgrep?bucket=main` |
| scoop, community bucket | `scoop://install/<app>?bucket=<repo-url>` | `scoop://install/7zip-zs?bucket=https://github.com/hoilc/scoop-lemon` |
| winget | `winget://install/<PackageId>` | `winget://install/Git.Git` |

Rules:

- **The action segment is always `install`** — treat it as a verb, not part of the name.
- **`bucket` carries the official name, or the repo URL for community buckets.** This is
  deliberate: community bucket names are a *local alias* you chose at `scoop bucket add`
  time, so guessing one would produce URIs that silently fail. The URL is unambiguous and
  lets your handler add the bucket if it isn't present yet.
- **`:` and `/` are left unencoded** in the bucket value so repo URLs stay readable and
  hand-parseable. Only `% & = # ? +` and whitespace are percent-encoded.
- **winget IDs are percent-encoded** — they can contain `+`:
  `Microsoft.VCRedist.2015+.x64` → `winget://install/Microsoft.VCRedist.2015%2B.x64`.
  Decode the path before use.
- **Neither scheme exists by default.** You are registering them yourself — winget-cli and
  scoop ship no URI handler.

### Matching install commands

```
scoop://install/ripgrep?bucket=main
  -> scoop install ripgrep

scoop://install/7zip-beta?bucket=versions
  -> scoop install versions/7zip-beta

scoop://install/7zip-zs?bucket=https://github.com/hoilc/scoop-lemon
  -> scoop bucket add hoilc https://github.com/hoilc/scoop-lemon && scoop install hoilc/7zip-zs

winget://install/Git.Git
  -> winget install --id Git.Git -e
```

`-e` (`--exact`) is deliberate: without it winget does fuzzy matching and can install the
wrong package. `server.mjs` always emits it.

**Handler caveat for community buckets:** if the bucket is already added under a different
local alias, `scoop bucket add` will fail or create a duplicate. The robust handler pattern
is to check `scoop bucket list` for the URL first and reuse the existing name if present.

---

## The URI handler (`handler/`, Go)

`server.mjs` *emits* `scoop://` and `winget://` links. Something has to *handle* them, and
neither winget-cli nor scoop ships a URI handler. This is that something: one small Windows
binary that routes both schemes to the right package manager.

Go, with `CGO_ENABLED=0` and a single dependency (`golang.org/x/sys`, for the registry and
console APIs), so it builds to a self-contained exe with no C toolchain involved.

```bash
cd handler
go build -o pkgproxy.exe .                 # add -ldflags "-s -w" to strip
go test ./...                              # 18 tests
```

### Usage

```
pkgproxy "scoop://install/ripgrep?repo=main"
pkgproxy "scoop://install/7zip-zs?repo=https://github.com/hoilc/scoop-lemon"
pkgproxy "winget://install/Git.Git"
pkgproxy register                          # claim both schemes for the current user
pkgproxy unregister                        # give them back
```

### Routing

| URI | Command run |
|---|---|
| `scoop://install/<pkg>?repo=main` | `scoop install <pkg>` |
| `scoop://install/<pkg>?repo=<bucket>` | `scoop install <bucket>/<pkg>` |
| `scoop://install/<pkg>?repo=<repo-url>` | resolves the bucket, then as above |
| `scoop://install/<pkg>` | `scoop install <pkg>` |
| `winget://install/<Vendor.Product>` | `winget install --id <Vendor.Product> -e` |
| `winget://install/<name>` | `winget install --exact <name>` |

`main` is addressed unqualified, matching the `installCommand` column `server.mjs` emits.

**`repo` and `bucket` are both accepted.** `server.mjs` emits `?bucket=`; `repo` is the
documented spelling. Whichever is present wins, `repo` first. Values are percent-decoded, so
`winget://install/Microsoft.VCRedist.2015%2B.x64` arrives as `Microsoft.VCRedist.2015+.x64`.

**winget is never matched fuzzily.** A dotted value is treated as a package id and gets
`--id … -e`; anything else gets `--exact`. Both refuse substring matches, so a bad link fails
loudly instead of silently installing the wrong package.

### Executable discovery

`scoop` and `winget` are looked up on `PATH` first, then at the documented fallbacks:
`%LOCALAPPDATA%\Microsoft\WindowsApps\winget.exe`, and
`%SCOOP%\apps\scoop\current\bin\scoop.ps1` (`%SCOOP%` falling back to `%USERPROFILE%\scoop`).
The resolved path is printed before every install, which matters because a shadowing build —
UniGetUI bundles its own winget — will win if it comes first on `PATH`.

The PowerShell host is picked the other way round from scoop's own `.cmd` shim: **`powershell.exe`
first, `pwsh` only as a fallback.** scoop is a tool you installed and chose; a link handler has to
work on a stock machine, where `pwsh` is not present. Neither host changes what is parsed —
both render host output as UTF-16LE and both take the same `-File` and `-Command` forms.

### Scoop repository resolution

When `repo` is a repository URL rather than a bucket name, `scoop bucket list` is consulted
first. If a bucket already tracks that URL, its **existing local name is reused** — the point
being that community bucket names are a local alias, so re-adding the same URL under a second
name would fail or duplicate it. URLs are compared canonically (scheme, **case**, trailing `/`
and a `.git` suffix are all ignored), so `…/Nirsoft.git` in the bucket list still matches
`…/Nirsoft` in a link, and `…/shanto/Scoop` matches `…/shanto/scoop`. scoop's own duplicate
check is case-insensitive for the same reason.

If nothing tracks the URL, you are asked to confirm a name and the bucket is added on
confirmation. **The proposed default is the repository owner, not the repository name** —
`github.com/hoilc/scoop-lemon` proposes `hoilc`. Type another name to override, `n` to abort,
or pass `-y` to accept the default without being asked.

#### Reading `scoop bucket list` (two traps)

That call is the one place the handler reads scoop's output instead of just running it, and it
is driven through PowerShell differently from an install: **`-Command`, not `-File`.**

```
powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass `
     -Command "& 'C:\…\Scoop\shims\scoop.ps1' 'bucket' 'list' | Out-String -Width 400"
```

- **`-File` silently returns nothing here.** scoop's `shims/scoop.ps1` is a two-line forwarder
  that ends in `exit $LASTEXITCODE`; run with `-File`, the objects it forwards never reach the
  redirected stdout. Measured against the real shim: `-File` yields **6 bytes** (a BOM and a
  blank line), `-Command` yields the whole table. The failure mode was nasty — an empty list is
  indistinguishable from "no bucket tracks this URL", so a link for an already-added bucket sent
  you down the add-bucket path instead. Unreadable output is now reported as an error rather
  than treated as an empty list. `-Command` is only used for calls whose arguments are ours, so
  it costs none of the quoting safety `-File` buys on the install path.
- **The output is UTF-16LE.** A PowerShell host writes UTF-16LE with a BOM whenever its stdout
  is a pipe — pwsh 7 and Windows PowerShell 5.1 alike, whatever the locale. Fed straight to the
  line parser it produced no rows at all, so it is decoded first (UTF-8 and BOM-less UTF-16LE
  are handled too).
- `Out-String -Width 400` pins the render width. `Format-Table` sizes columns to the host, so at
  the default width a long `Source` URL is truncated with an ellipsis — which would silently
  defeat URL matching rather than fail loudly.

### Registration

`register` writes `HKCU\Software\Classes\<scheme>` for both schemes, resolving its own path
at runtime so the entry always points at wherever the binary actually lives:

```
HKCU\Software\Classes\scoop
  (Default)                          = "URL:scoop Protocol"
  URL Protocol                       = ""            <- presence is what makes it a URI scheme
  shell\open\command
    (Default)                        = "C:\path\to\pkgproxy.exe" "%1"
```

Per-user (`HKCU`) needs no elevation and matches how scoop itself installs. `unregister`
deletes the trees depth first, and **refuses to delete a handler it did not install** unless
`--force` is given.

### Output handling

A handler launched from a link gets its own console window, which would vanish the moment the
process exits. After every install — success or failure — the terminal is held open with
`Task completed. Press any key to terminate.`, reading a single keypress (no Enter needed).
It is skipped automatically when stdin is not a console, and can be disabled with `--no-pause`.

### Why PowerShell and not `cmd`

Scoop is driven through `scoop.ps1` with `powershell.exe -File`, never through the `.cmd` shim. Go
builds command lines with CRT-style quoting; `cmd.exe` then re-parses them with its own,
incompatible rules, and `&`, `|`, `%`, `^` and `<` do not survive the round trip. `-File`
passes everything after the script path positionally, so PowerShell never re-interprets it.
This was verified directly: `a&b`, `c|d`, `i%j`, `g^h` and `Microsoft.VCRedist.2015+.x64` all
arrive byte-for-byte via `-File`, while the `.cmd` shim mangles `a&b` into a broken command.

### Verification status

**Verified** (Go 1.26.5, scoop 0.6.0, winget present):

- 18 unit tests pass, covering URI parsing, percent-decoding, `scoop bucket list` parsing
  against real captured output, PowerShell output decoding (UTF-16LE, both BOM orders, UTF-8,
  BOM-less UTF-16LE), the capture argv, URL canonicalisation and owner-name derivation.
- End-to-end against stub `scoop.ps1` / native `winget.exe` stand-ins: routing for all six
  rows above, reuse of an already-tracked bucket, the add-bucket prompt (accept default,
  decline, custom name, `-y`), and child exit-code propagation.
- Bucket resolution against the **real** scoop and the **real** shim, on both hosts: a link
  carrying `…/shanto/Scoop` for a bucket added as `…/shanto/Scoop` resolves to it instead of
  prompting, and so do the upper-cased URL, the `.git`-suffixed URL and a URL tracked under a
  different alias. An untracked URL still reaches the prompt, now listing the buckets that do
  exist. Run under `pwsh` and under `powershell.exe` (5.1) — identical results.
- `register` / `unregister` round trip against the real registry, plus a live
  `Start-Process "scoop://…"` confirming Windows resolves the scheme and launches the binary.
  The `--force` guard was exercised against a deliberately foreign handler.

**Scope:** registration is per-user (`HKCU`) only; machine-wide `HKLM` is not implemented, by
design.

---

## Result detail

**Versions and descriptions** come straight from the azure index for scoop, and from
`winstall.app` for winget. Each scoop document carries `Name, Version, Description, License,
Homepage` plus a `Metadata` object holding the repository URL, its star count, the commit
SHA and the manifest path — so a scoop search is **one HTTP request**, with no per-app
fetching.

The azure index is **full-text and cross-bucket**, and that is the one thing to know about it:
it answers loosely (query `7zip-zs`, also get `zstd` and `7zip@21.03+zs`) and it holds one
document per bucket that carries a package. Both are handled in `searchScoopAzure`, see
[Dedup](#dedup).

### Row links

Each row links out the way `scoop.sh` does, but **no extra fetching is involved** — every
target already rides along in the payload, so this is a presentation change only:

| Row element | Links to | Payload field | Coverage |
|---|---|---|---|
| version | the manifest file | `detailUrl` (built from `Metadata.Sha` + `FilePath`) | scoop rows with a manifest path |
| bucket name | the bucket repository | `bucketUrl` | scoop rows |
| project domain | the project's own site | `repoWebsite`, else `homepage` | most scoop rows |

`scoop.sh` builds the manifest link as `<repo>/blob/master/<filePath>` with a hardcoded
`master`. GitHub resolves `master` as a legacy alias even on repos whose default branch is
`main`, so that form works — but the index records the **commit SHA** each document was
indexed at, and a SHA cannot drift away from the revision you are actually looking at. So the
link is pinned to the SHA, falling back to the recorded branch and then to `master` when a
document carries no SHA. A document with no repository at all gets no manifest link, and the
row falls back to the bucket readme.

Winget rows get the same treatment with what they have: the version links to the package's
**manifest folder in `microsoft/winget-pkgs`**, and the publisher, project domain and licence
come from the search payload. There is no bucket, so that link is omitted.

### Winget: one request, and where the manifest link comes from

`GET /apps/search?q=&offset=&limit=` returns `{ total, offset, limit, data[] }`, and each entry
in `data` is the **whole package object** — not a search-result stub. That is the whole reason
this source replaced the previous one: a search result already contains `homepage`, `license`,
`licenseUrl`, `tags`, `categories`, `publisher`, `author`, `minOS`, `path`, `latestVersion`,
`versions[]` (each with `installerType` and `installers[]`), `releaseNotes` and counts. Nothing
needs a second request, so there is no cache to keep warm.

The one thing it does not carry is a link to the manifest itself — and that needs no API.
winget-pkgs lays manifests out as
`manifests/<first letter of publisher>/<Publisher>/<Name>/<Version>`, which is exactly the
`path` field plus the version:

```
path "7zip/7zip"                  + 26.04           -> manifests/7/7zip/7zip/26.04
path "BurntSushi/ripgrep/MSVC"    + 15.2.0          -> manifests/b/BurntSushi/ripgrep/MSVC/15.2.0
path "Notepad++/Notepad++"        + 8.9.8.1         -> manifests/n/Notepad++/Notepad++/8.9.8.1
```

`path` is not always two segments — `BurntSushi.ripgrep.MSVC` yields three — so it is joined
verbatim rather than assumed.

Two things worth knowing:

- **`limit` caps at 200 server-side**, well above `MAX_LIMIT` (50), so one request always fills
  a page. `offset` is a real row offset.
- **Every row costs ~45 KB of upstream payload.** The API has no field selection and always
  ships the full object, so `limit=50` pulls ~2.3 MB and takes 1.3–4.5 s; `limit=10` is ~1 MB
  and ~1 s. The proxy trims that to ~1.7 KB per row before answering, but the download is
  unavoidable. If a search feels slow, lower `limit` — that is the only lever.

### Icons

**The common case** is a `homepage` pointing at a normal project site — those get a
DuckDuckGo favicon for that domain, and always have.

The problem is the manifests that point `homepage` at `github.com/<owner>/<repo>`, which is a
large minority of the index. Taken naively they all render the same generic octocat. So the
icon chain is:

| # | Condition | Icon |
|---|---|---|
| 1 | `homepage` is a normal site | DDG favicon for that domain |
| 2 | `homepage` is a github repo, and the repo has a website set | DDG favicon for that site |
| 3 | `homepage` is a github repo, no website set | owner avatar (`github.com/<owner>.png`) |
| 4 | nothing known | initials, rendered client-side |

winget icons come from winstall's own `/icons/<file>` endpoint when the package carries an
`icon` field — about **57%** of packages do. For the remaining 43%, `iconForWinget` falls back
to wingetly's `/api/icon/<id>`, which is a plain image URL needing no search and no key and
fills roughly two thirds of that gap. **That fallback is the only wingetly dependency left**;
delete the constant and the fallback branch to go single-upstream, at the cost of those icons.

**The GitHub repo website cache.** Every repo object carries a `homepage` field for the
project's own site, so step 2 recovers a real favicon where one exists:

```
github.com/mcmilk/7-Zip-zstd  ->  mcmilk.de/projects/7-Zip-zstd/  ->  icons.duckduckgo.com/ip3/mcmilk.de.ico
```

But be aware of the hit rate: **the field is frequently empty.** Of five repos sampled,
ripgrep, fd, bat and dust all return `""` — only 7-Zip-zstd had one. Step 3 exists because
of that; those packages get a distinctive per-organisation avatar rather than the octocat.

Lookups are **lazy** — only repos that actually appear in a set of search results are ever
fetched. There is no bulk warm-up, so the cache grows in proportion to what you actually
search for. Results are cached to `$CACHE_DIR/github-repos.json` with a 30-day TTL, written
atomically via a debounced background flush.

The index spans thousands of distinct GitHub repos, so a full sweep would take days
unauthenticated (60 req/h) versus hours with `GITHUB_TOKEN` (5,000 req/h). The lazy strategy
is what makes the unauthenticated default workable.

```bash
GITHUB_TOKEN=ghp_... node server.mjs     # run from src/; 5000 req/h instead of 60
```

The proxy tracks `x-ratelimit-remaining` from response headers and stops issuing lookups
when the budget runs out — it never blocks a search waiting on GitHub. Unresolved repos get
the owner avatar now and the better favicon on a later search once the cache is warm.

> DuckDuckGo answers **404 with a placeholder image body** for unknown domains. Browsers
> render that placeholder rather than firing `onerror`, so you get a generic grey glyph; the
> initials fallback only triggers on a hard load failure.

### Dedup

The azure index holds **one document per bucket that carries a package** — `ripgrep` is
indexed nine times, and 26 of the first 100 documents for `git` are flagged as mirrors of a
package living in another bucket. Two passes in `searchScoopAzure` clean that up:

1. documents carrying `Metadata.DuplicateOf` are dropped outright — they are byte-level
   mirrors of a package that already has a home bucket;
2. the survivors are deduped by name, preferring
   exact match → prefix match → official bucket → higher stars → shorter name.

Because azure's own relevance order decides which candidates ever arrive, the proxy
over-fetches **4× the requested limit, clamped to 50–200 documents**, before deduping and
slicing to `limit`.

---

## HTTP API

### `GET /api/search`

| Param | Default | Notes |
|---|---|---|
| `q` | — | required; empty returns no rows |
| `source` | `all` | `all` \| `scoop` \| `winget` |
| `limit` | `25` | clamped to 50 |

```bash
curl "http://127.0.0.1:8787/api/search?q=7zip&source=scoop&limit=5"
```

```json
{
  "query": "7zip-zs",
  "source": "scoop",
  "scoopMode": "azure",
  "rows": [
    {
      "source": "scoop",
      "id": "7zip-zs",
      "name": "7zip-zs",
      "version": "26.02-v1.5.7-R2",
      "description": "7-Zip with support for Brotli, Fast-LZMA2, Lizard, LZ4, LZ5 and Zstandard.",
      "publisher": "",
      "license": "LGPL-2.0-only",
      "bucket": "hoilc/scoop-lemon",
      "bucketUrl": "https://github.com/hoilc/scoop-lemon",
      "official": false,
      "stars": 281,
      "homepage": "https://github.com/mcmilk/7-Zip-zstd",
      "domain": "github.com",
      "repoWebsite": "https://mcmilk.de/projects/7-Zip-zstd/",
      "iconUrl": "https://icons.duckduckgo.com/ip3/mcmilk.de.ico",
      "installCommand": "scoop bucket add hoilc https://github.com/hoilc/scoop-lemon && scoop install hoilc/7zip-zs",
      "uri": "scoop://install/7zip-zs?bucket=https://github.com/hoilc/scoop-lemon",
      "detailUrl": "https://github.com/hoilc/scoop-lemon/blob/e9ceca08ffdfbcf373dc3b550a10540930b031e4/bucket/7zip-zs.json"
    }
  ]
}
```

`scoopMode` reports the source a search actually used (`azure` or `jsdelivr`), which is not
always the configured one — see [Refresh behaviour](#refresh-behaviour).

A winget row carries the fields winstall supplies:

```json
{
  "source": "winget",
  "id": "7zip.7zip",
  "name": "7-Zip",
  "version": "26.04",
  "description": "A file archiver with a high compression ratio.",
  "publisher": "Igor Pavlov",
  "license": "LGPL-2.1",
  "licenseUrl": "https://7-zip.org/license.txt",
  "tags": ["7zfiles", "archive", "archiver", "compress", "compression", "unzip", "zip"],
  "categories": ["utilities"],
  "homepage": "https://7-zip.org/download.html",
  "domain": "7-zip.org",
  "iconUrl": "https://api.winstall.app/icons/7zip.7zip.png",
  "installCommand": "winget install --id 7zip.7zip -e",
  "uri": "winget://install/7zip.7zip",
  "packageUrl": "https://winstall.app/apps/7zip/7zip",
  "manifestUrl": "https://github.com/microsoft/winget-pkgs/tree/master/manifests/7/7zip/7zip/26.04",
  "detailUrl": "https://github.com/microsoft/winget-pkgs/tree/master/manifests/7/7zip/7zip/26.04",
  "installerType": "",
  "installers": ["https://www.7-zip.org/a/7z2604.exe", "https://www.7-zip.org/a/7z2604-x64.exe"],
  "versionCount": 1
}
```

`installers` is capped at 8 entries; `versionCount` is how many versions winstall knows.

Partial failures are non-fatal — if one upstream is down, the other still returns rows and
the failure appears in `errors`:

```json
{ "rows": [...], "errors": { "winget": "winstall 503" } }
```

### `GET /api/resolve?source=&id=`

Single-package lookup. Returns `{ "row": {...} }` with the best match, or `null`.

For winget this goes through `GET /apps/<id>` — the exact route — so a resolve never has to
trust a relevance ranking. Only if that 404s does it fall back to a search plus exact-match.

### `GET /api/health`

Reports the configured and the active scoop source, whether the azure key is present, the
github repo cache counters, and the URI contract.

```json
{
  "scoop": {
    "configuredSource": "azure",
    "activeSource": "azure",
    "azure": {
      "endpoint": "https://scoopsearch.search.windows.net/indexes/apps/docs/search?api-version=2020-06-30",
      "index": "apps",
      "keyPresent": true,
      "fetchWindow": [50, 200]
    }
  }
}
```

**There are no mutating endpoints.** `/api/scoop-db/sync` and `/api/scoop-index/rebuild` are
both gone — nothing needs a nudge from outside. Azure needs no local refresh at all, and the
jsDelivr fallback index rebuilds itself on a TTL.

---

## Refresh behaviour

**azure needs no local cache.** Every scoop search is one request to the index, so there is
nothing to download, poll or expire — no ETag dance, no 50 MB file on disk.

The jsDelivr fallback index is the only cached thing on the scoop side. It is written to
`$CACHE_DIR/scoop-local-index.json` and rebuilt automatically once the file is older than
`SCOOP_INDEX_TTL_HOURS` (default 24). Freshness is read from the file's **mtime**, so the
on-disk format stays a plain array.

Two failure modes are handled explicitly, because the naive version turns a blip into a
permanent outage:

- a rebuild that comes back **empty** (jsDelivr down for every bucket) is discarded rather
  than written, so a transient failure cannot overwrite a good index;
- a search that cannot rebuild falls back to the **stale copy** on disk, so you get old data
  instead of zero results.

The github repo website cache is written to `$CACHE_DIR/github-repos.json` with a 30-day TTL,
debounced and atomically renamed.

---

## Environment variables

| Var | Default | Effect |
|---|---|---|
| `PORT` | `8787` | listen port |
| `HOST` | `127.0.0.1` | bind address (**set `0.0.0.0` in a container**) |
| `CACHE_DIR` | `./.cache` | where the fallback index and repo map are written |
| `SCOOP_SOURCE` | `azure` | `azure` \| `jsdelivr` |
| `SCOOP_AZURE_KEY` | — | azure search query key — **required** for the azure source |
| `SCOOP_INDEX_TTL_HOURS` | `24` | how long the jsDelivr fallback index is trusted |
| `SCOOP_ICONS` | `auto` | `auto` = full chain · `ddg` = plain favicon only · `avatar` = always owner avatar |
| `GITHUB_TOKEN` | — | 5,000 req/h instead of 60 for repo website lookups |
| `GITHUB_TTL_DAYS` | `30` | how long a cached repo website is trusted |
| `GITHUB_LOOKUPS_PER_SEARCH` | `25` | max uncached repos resolved per search |
| `GITHUB_DEADLINE_MS` | `1200` | wall-clock budget for those lookups |
| `GITHUB_CONCURRENCY` | `8` | parallel GitHub requests |

---

## Container

There is **no Dockerfile.** The app is zero-dependency plain Node with no build step, so the
stock `node:24-alpine` image is the whole story. `compose.yml` runs it and bind-mounts
`./src` to `/app`, so editing `src/server.mjs` or `src/ui.html` takes effect on the next
`docker compose up -d` — no rebuild.

```bash
docker compose up -d
docker compose logs -f
```

```yaml
services:
  app:
    image: node:24-alpine
    working_dir: /app
    command: node server.mjs
    ports: ["8787:8787"]
    environment:
      HOST: 0.0.0.0
      SCOOP_SOURCE: azure
      CACHE_DIR: /cache
    volumes:
      - ./src:/app
      - pkgproxy-cache:/cache
```

Notes:

- **`HOST=0.0.0.0` is required.** The app defaults to `127.0.0.1`, which is unreachable from
  outside the container. The compose file sets it.
- **The key rides in through the bind mount.** `./src` is mounted at `/app`, so the app reads
  `/app/.env` — the same file you use locally. Nothing secret goes into `compose.yml` or the
  image. An `environment:` entry still wins over the file if you would rather pass it that way.
- **The cache is deliberately *not* under `/app`.** With `./src` bind-mounted there, the
  default `./.cache` would write generated files into your source tree. `CACHE_DIR=/cache`
  plus a named volume keeps `./src` clean.
- `init: true` gives the container a real PID 1, so `SIGTERM` from `docker compose down`
  reaches the app's shutdown handler, which flushes the repo cache.
- The `node` image runs as `root` by default. That is fine here since the only writable path
  is the cache volume. To harden, uncomment `user: node` — but the named volume must then be
  writable by uid 1000, so create it first with the right ownership.
- You can mount the source read-only (`./src:/app:ro`) since the app no longer writes into
  `/app`. Host-side edits still work.
- Set `GITHUB_TOKEN` in the environment block for 5,000 req/h instead of 60.

### Verification status

Node 22.22.2 runs the proxy against the live azure index. Verified end to end:

- `SCOOP_AZURE_KEY` is picked up from `src/.env`, and a real environment variable overrides
  the file — checked directly, which is what makes the compose override work.
- Health reports `activeSource: azure`; search returns SHA-pinned manifest links, per-repo
  stars and resolved icons; dedup collapses the nine `ripgrep` documents to one.
- Both removed endpoints now 404.
- With no key the server reports `jsdelivr` and serves the fallback: a stale index
  (file mtime > TTL) rebuilt itself on the first search, and rows were enriched from their
  manifests with version, licence and homepage.

The winget side was verified against the live `winstall.app` API:

- `GET /apps/search` returns homepage, licence, licence URL and tags on **every** row —
  50/50 across sampled queries, against 0% homepage from the source it replaced.
- Versions are current: `7zip.7zip` → 26.04, `Microsoft.PowerToys` → 0.101.2362.0,
  `Notepad++.Notepad++` → 8.9.8.1, `Microsoft.VCRedist.2015+.x64` → 14.51.36247.0.
- `/api/resolve?source=winget` returns the exact package for all four, including the two with
  `+` in the id — `+` is left literal in the path, and the API accepts it that way.
- Derived manifest URLs resolve 200 on GitHub, including the three-segment
  `manifests/b/BurntSushi/ripgrep/MSVC/15.2.0`.
- `limit` 5 / 25 / 50 → exactly that many unique rows every time; the server caps at 200, so a
  single request always fills a page.
- Payload and latency measured directly against the API: ~45 KB per row, `limit=50` ≈ 2.3 MB
  and 1.3–4.5 s, `limit=10` ≈ 1 MB and ~1 s. Documented above as the one real cost of this
  source.
- Icons: winstall's own for ~57% of packages, wingetly fallback for most of the rest —
  34/35 images loaded in the rendered page.
- UI screenshotted via CDP: 35 rows (25 scoop + 10 winget), mode line
  `scoop: azure · winget: winstall`.

The app has no build step and no native dependencies, so there is nothing that can be missing
from a stock `node:24-alpine` image.

---

## Files

| File | Purpose |
|---|---|
| `compose.yml` | the whole deployment — stock `node:24-alpine`, `./src` bind-mounted to `/app` |
| `src/server.mjs` | the proxy — HTTP server, all upstream adapters, URI builder |
| `src/ui.html` | search UI, served at `/` |
| `src/.env` | `SCOOP_AZURE_KEY` and friends; loaded at startup, gitignored |
| `handler/*.go` | the Go URI handler — routes `scoop://` and `winget://`, `register`/`unregister` |
| `handler/pkgproxy.exe` | built handler binary (gitignored) |
| `.cache/scoop-local-index.json` | generated; jsDelivr fallback index |
| `.cache/github-repos.json` | generated; repo → website map |

Generated files land in `CACHE_DIR`, which defaults to `.cache` next to `server.mjs` and is
set to `/cache` (a named volume) in the compose file.

`ui.html` renders each result with a live `<a href="scoop://install/...">` link plus a
copy-command button as a fallback, so the UI is usable before you've wired anything. The
version, bucket name and project domain are additionally links out to the manifest, the
bucket repo and the project site — see [Row links](#row-links).
