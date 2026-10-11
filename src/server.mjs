#!/usr/bin/env node
/**
 * pkgproxy — personal proxy for scoop + winget package metadata.
 *
 * One search endpoint in front of two upstreams:
 *   scoop  -> ScoopSearch azure index (primary, needs SCOOP_AZURE_KEY)
 *             | jsdelivr official-bucket listing (keyless fallback)
 *   winget -> winstall.app public JSON API
 *
 * Every result carries a stable `uri`:
 *   scoop://install/<app>?bucket=<short-name-or-repo-url>
 *   winget://install/<percent-encoded-package-id>
 *
 * Zero dependencies. Node 22+ (uses global fetch and process.loadEnvFile).
 *
 *   node server.mjs
 *   PORT=9000 node server.mjs
 *   SCOOP_SOURCE=jsdelivr node server.mjs
 */

import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

/**
 * Optional .env next to this file. Real environment variables always win, so a
 * shell or compose value overrides the file — which is how the container can be
 * given a key without the file ever entering the image.
 */
try { process.loadEnvFile(path.join(__dirname, '.env')); } catch { /* no .env */ }

const PORT = Number(process.env.PORT || 8787);
const HOST = process.env.HOST || '127.0.0.1';
/**
 * Where the jsDelivr fallback index and the github repo map live.
 * Defaults to ./.cache next to the source. Override with CACHE_DIR when the
 * source tree is bind-mounted read-write and you would rather not write into it.
 */
const CACHE_DIR = process.env.CACHE_DIR
  ? path.resolve(process.env.CACHE_DIR)
  : path.join(__dirname, '.cache');

const WINSTALL_BASE = 'https://api.winstall.app';
const WINSTALL_PAGE_BASE = 'https://winstall.app';
/**
 * Icon fallback only. winstall ships an `icon` filename for ~57% of packages;
 * for the rest this is a plain image URL needing no search and no key, and it
 * answers 404 for an id it does not know, which the UI turns into initials.
 * Drop this constant (and the fallback in iconForWinget) for a single upstream.
 */
const WINGETLY_ICON_BASE = 'https://wingetly.io';
/** winget-pkgs manifest folders, addressed by path + version — no API involved. */
const WINGETPKGS_MANIFEST_BASE = 'https://github.com/microsoft/winget-pkgs/tree/master/manifests';
const JSDELIVR_API = 'https://data.jsdelivr.com/v1/packages/gh';

const UA = 'pkgproxy/1.0 (personal local metadata proxy)';
const DEFAULT_LIMIT = 25;
const MAX_LIMIT = 50;

/**
 * Icon strategy for scoop packages.
 *   auto   (default) — full chain: site favicon -> repo website field -> owner avatar
 *   ddg              — legacy: plain DuckDuckGo favicon for the homepage domain
 *   avatar           — always use the GitHub owner avatar for github.com homepages
 */
const SCOOP_ICON_MODE = (process.env.SCOOP_ICONS || 'auto').toLowerCase();

// ---------------------------------------------------------------------------
// github repo website cache
//
// ~40% of scoop manifests point `homepage` at github.com/<owner>/<repo>, which makes
// every one of them render the same generic octocat. Every repo object carries a
// `homepage` field for the project's own site — but it is frequently EMPTY, so this
// is a best-effort improvement, not a guarantee.
//
// Lookups are cached to disk forever (with a TTL), fetched lazily only for repos that
// actually appear in results, and rate-limit aware. Set GITHUB_TOKEN for 5000 req/h
// instead of 60 — that is the difference between weeks and hours for a full warm-up.
// ---------------------------------------------------------------------------

const GH_CACHE_PATH = path.join(CACHE_DIR, 'github-repos.json');
const GITHUB_TOKEN = (process.env.GITHUB_TOKEN || '').trim();
const GH_TTL_MS = Number(process.env.GITHUB_TTL_DAYS || 30) * 86400_000;
const GH_PER_SEARCH = Number(process.env.GITHUB_LOOKUPS_PER_SEARCH || 25);
const GH_DEADLINE_MS = Number(process.env.GITHUB_DEADLINE_MS || 1200);
const GH_CONCURRENCY = Number(process.env.GITHUB_CONCURRENCY || 8);

// ---------------------------------------------------------------------------
// scoop sources
// ---------------------------------------------------------------------------

/**
 * Official buckets, mapped repo URL -> the bucket name scoop uses locally.
 * Anything not in this map is a community bucket and gets referred to by URL.
 */
const OFFICIAL_BUCKETS = new Map([
  ['https://github.com/ScoopInstaller/Main', 'main'],
  ['https://github.com/ScoopInstaller/Extras', 'extras'],
  ['https://github.com/ScoopInstaller/Versions', 'versions'],
  ['https://github.com/ScoopInstaller/Nirsoft', 'nirsoft'],
  ['https://github.com/ScoopInstaller/Sysinternals', 'sysinternals'],
  ['https://github.com/ScoopInstaller/PHP', 'php'],
  ['https://github.com/matthewjberger/scoop-nerd-fonts', 'nerd-fonts'],
  ['https://github.com/ScoopInstaller/Nonportable', 'nonportable'],
  ['https://github.com/ScoopInstaller/Java', 'java'],
  ['https://github.com/ScoopInstaller/Games', 'games'],
]);

/** Which scoop source to prefer. azure | jsdelivr */
const SCOOP_SOURCE = (process.env.SCOOP_SOURCE || 'azure').toLowerCase();

const SCOOP_AZURE_URL =
  'https://scoopsearch.search.windows.net/indexes/apps/docs/search?api-version=2020-06-30';
const SCOOP_AZURE_KEY = (process.env.SCOOP_AZURE_KEY || '').trim();

/**
 * Docs pulled per azure query, before dedup and slicing. The index is full-text,
 * so it hands back loose matches ("zstd" for "7zip-zs") and the same package
 * from several buckets; we over-fetch to survive both.
 */
const AZURE_FETCH_MIN = 50;
const AZURE_FETCH_MAX = 200;

/**
 * Azure's `searchMode` for the query string. Defaults to `all`, which is what
 * scoop.sh itself sends — every term must match.
 *
 * This is not cosmetic. Azure's own default is `any`, and the analyzer splits a
 * hyphenated name into terms, so "ssh-agent-bridge" under `any` becomes
 * ssh OR agent OR bridge: 3584 docs, ranked by relevance, and a low-star exact
 * name never reaches the top 200. Under `all` it is ssh AND agent AND bridge:
 * 2 docs, exact name first. Same reason "visual studio code" drops from 10832
 * loose matches to 196 real ones. Single-term queries are unaffected — one term
 * is the same set either way.
 */
const AZURE_SEARCH_MODE = (process.env.SCOOP_SEARCH_MODE || 'all').toLowerCase();

/** Official buckets, by bare name, for the jsDelivr fallback index. */
const SCOOP_BUCKETS_OFFICIAL = [
  'Main', 'Extras', 'Versions', 'NerdFonts',
  'Java', 'PHP', 'Nonportable', 'Games',
];

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

async function jfetch(url, opts = {}) {
  const res = await fetch(url, {
    ...opts,
    signal: AbortSignal.timeout(opts.timeoutMs || 25_000),
    headers: { 'User-Agent': UA, ...(opts.headers || {}) },
  });
  const text = await res.text();
  let json = null;
  try { json = JSON.parse(text); } catch { /* non-JSON body */ }
  return { ok: res.ok, status: res.status, json, text };
}

function clampLimit(v) {
  const n = Number.parseInt(v, 10);
  if (!Number.isFinite(n) || n <= 0) return DEFAULT_LIMIT;
  return Math.min(n, MAX_LIMIT);
}

/**
 * Build the canonical install URI. Single source of truth for the scheme contract.
 *
 * scoop  : scoop://install/<app>?bucket=<bucket-name>   for official buckets
 *          scoop://install/<app>?bucket=<repo-url>      for community buckets
 * winget : winget://install/<PackageId>                 (percent-encoded)
 */
function scoopUri(app, bucketValue) {
  return `scoop://install/${encodeURIComponent(app)}?bucket=${encodeBucketValue(bucketValue)}`;
}
function wingetUri(id) {
  return `winget://install/${encodeURIComponent(id)}`;
}

/**
 * Encode a bucket value for the query string, but leave `:` and `/` intact so a
 * repo URL stays readable and hand-parseable:
 *   ?bucket=https://github.com/owner/repo
 */
function encodeBucketValue(v) {
  return String(v).replace(/[%&=#?+\s]/g, (c) =>
    '%' + c.charCodeAt(0).toString(16).toUpperCase().padStart(2, '0'));
}

/** Last path segment of a repo URL, minus any .git suffix. */
function repoShortName(url) {
  try {
    const seg = new URL(url).pathname.split('/').filter(Boolean);
    return (seg[seg.length - 1] || '').replace(/\.git$/, '');
  } catch {
    return '';
  }
}

/** Bare hostname from a homepage URL, for the favicon proxy. Strips www. */
function domainOf(url) {
  if (!url || typeof url !== 'string') return '';
  try {
    return new URL(url).hostname.replace(/^www\./, '');
  } catch {
    return '';
  }
}

/** scoop manifests declare license either as a string or as { identifier, url }. */
function licenseOf(v) {
  if (!v) return '';
  if (typeof v === 'string') return v;
  if (typeof v === 'object') return v.identifier || '';
  return '';
}

/** Upstream marks some versions with surrounding asterisks. Strip them. */
function cleanVersion(v) {
  if (v == null) return '';
  return String(v).replace(/^\*+|\*+$/g, '').trim();
}

/**
 * Percent-encode one URL path segment, but keep `+` literal.
 *
 * winget package ids spell it that way (`Notepad++.Notepad++`) and so does the
 * winget-pkgs tree, so `%2B` would address a path that does not exist. Both the
 * winstall route and the GitHub tree accept the literal form.
 */
function pathSeg(s) {
  return encodeURIComponent(String(s)).replace(/%2B/g, '+');
}

/** Extract "owner/repo" from a github.com URL, or null if it is not a repo URL. */
function githubRepoOf(url) {
  if (!url || typeof url !== 'string') return null;
  try {
    const u = new URL(url);
    if (u.hostname !== 'github.com' && u.hostname !== 'www.github.com') return null;
    const seg = u.pathname.split('/').filter(Boolean);
    if (seg.length < 2) return null;
    const [owner, repo] = seg;
    const reserved = new Set([
      'orgs', 'users', 'sponsors', 'topics', 'collections', 'marketplace', 'settings',
      'features', 'about', 'apps', 'pricing', 'enterprise', 'site', 'readme', 'search',
    ]);
    if (reserved.has(owner.toLowerCase())) return null;
    if (/\.(png|jpe?g|svg|gif|md|txt|zip|exe|msi)$/i.test(repo)) return null;
    return `${owner}/${repo.replace(/\.git$/, '')}`;
  } catch {
    return null;
  }
}

function githubOwnerOf(url) {
  const r = githubRepoOf(url);
  if (!r) return null;
  return r.split('/')[0];
}

/** Lexicographic compare of two rank arrays. Lower is better. */
function compareRank(a, b) {
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return a[i] < b[i] ? -1 : 1;
  }
  return 0;
}

/** Run an async fn over items with bounded concurrency. */
async function mapLimit(items, limit, fn) {
  const out = new Array(items.length);
  let cursor = 0;
  const workers = Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (cursor < items.length) {
      const i = cursor++;
      try { out[i] = await fn(items[i], i); } catch { out[i] = undefined; }
    }
  });
  await Promise.all(workers);
  return out;
}

// ---------------------------------------------------------------------------
// github repo website cache — storage, fetching, icon resolution
// ---------------------------------------------------------------------------

let ghCache = null;            // Map "owner/repo" -> { homepage, checkedAt }
let ghDirty = false;
let ghSaveTimer = null;

const ghState = {
  cached: 0,
  lookups: 0,
  errors: 0,
  rateLimit: { remaining: null, limit: null, reset: null },
  lastError: null,
  token: Boolean(GITHUB_TOKEN),
};

function loadGhCache() {
  if (ghCache) return ghCache;
  try {
    ghCache = new Map(Object.entries(JSON.parse(fs.readFileSync(GH_CACHE_PATH, 'utf8'))));
  } catch {
    ghCache = new Map();
  }
  ghState.cached = ghCache.size;
  return ghCache;
}

/** Debounced atomic write, so a burst of lookups produces one save. */
function scheduleGhSave() {
  ghDirty = true;
  if (ghSaveTimer) return;
  ghSaveTimer = setTimeout(() => {
    ghSaveTimer = null;
    if (!ghDirty || !ghCache) return;
    ghDirty = false;
    try {
      fs.mkdirSync(CACHE_DIR, { recursive: true });
      const tmp = `${GH_CACHE_PATH}.tmp`;
      fs.writeFileSync(tmp, JSON.stringify(Object.fromEntries(ghCache)));
      fs.renameSync(tmp, GH_CACHE_PATH);
      ghState.cached = ghCache.size;
    } catch { /* best effort */ }
  }, 1500);
  if (ghSaveTimer.unref) ghSaveTimer.unref();
}

/** Flush pending cache writes immediately (used on shutdown). */
function flushGhCache() {
  if (!ghDirty || !ghCache) return;
  ghDirty = false;
  try {
    fs.mkdirSync(CACHE_DIR, { recursive: true });
    const tmp = `${GH_CACHE_PATH}.tmp`;
    fs.writeFileSync(tmp, JSON.stringify(Object.fromEntries(ghCache)));
    fs.renameSync(tmp, GH_CACHE_PATH);
    ghState.cached = ghCache.size;
  } catch { /* best effort */ }
}

function ghRateLimited() {
  const r = ghState.rateLimit.remaining;
  return r != null && r <= 1;
}

async function fetchRepoWebsite(fullName) {
  const headers = { Accept: 'application/vnd.github+json', 'User-Agent': UA };
  if (GITHUB_TOKEN) headers.Authorization = `Bearer ${GITHUB_TOKEN}`;

  const res = await fetch(`https://api.github.com/repos/${fullName}`, {
    headers,
    signal: AbortSignal.timeout(8000),
  });

  const remaining = res.headers.get('x-ratelimit-remaining');
  if (remaining != null) ghState.rateLimit.remaining = Number(remaining);
  const limit = res.headers.get('x-ratelimit-limit');
  if (limit != null) ghState.rateLimit.limit = Number(limit);
  const reset = res.headers.get('x-ratelimit-reset');
  if (reset != null) ghState.rateLimit.reset = Number(reset);

  if (res.status === 403 || res.status === 429) {
    ghState.lastError = `github rate limited (${res.status})`;
    throw new Error('rate limited');
  }
  if (res.status === 404) {
    // deleted / renamed / private — remember that so we do not retry every search
    return { homepage: '', checkedAt: Date.now(), missing: true };
  }
  if (!res.ok) throw new Error(`github HTTP ${res.status}`);

  const j = await res.json();
  return { homepage: (j.homepage || '').trim(), checkedAt: Date.now() };
}

/**
 * Resolve website fields for a set of "owner/repo" keys.
 * Returns a Map of key -> website (possibly ''). Cached hits are instant; misses are
 * fetched with bounded concurrency under a wall-clock deadline so a search never hangs.
 */
async function resolveRepoWebsites(keys, { deadlineMs = GH_DEADLINE_MS, max = GH_PER_SEARCH } = {}) {
  const cache = loadGhCache();
  const out = new Map();
  const now = Date.now();
  const todo = [];

  for (const k of keys) {
    const hit = cache.get(k);
    if (hit && now - hit.checkedAt < GH_TTL_MS) out.set(k, hit.homepage);
    else todo.push(k);
  }
  if (!todo.length || ghRateLimited()) return out;

  const deadline = Date.now() + deadlineMs;
  const batch = todo.slice(0, max);

  await mapLimit(batch, GH_CONCURRENCY, async (k) => {
    if (Date.now() > deadline || ghRateLimited()) return;
    try {
      const r = await fetchRepoWebsite(k);
      cache.set(k, r);
      scheduleGhSave();
      ghState.lookups++;
      out.set(k, r.homepage);
    } catch (e) {
      ghState.errors++;
      ghState.lastError = String(e?.message || e);
    }
  });

  return out;
}

/**
 * Icon for a scoop row.
 *
 * Chain:
 *   1. homepage is a normal site       -> DDG favicon for that domain
 *   2. homepage is github.com/<o>/<r>  -> repo's own website field, DDG favicon for it
 *   3. repo has no website field set   -> owner avatar (distinct per org, beats the octocat)
 *   4. nothing known                   -> '' (UI renders initials)
 *
 * SCOOP_ICONS=ddg pins legacy behaviour (plain favicon, octocat for all github repos).
 */
function iconForRow(row, repoWebsite) {
  const gh = githubRepoOf(row.homepage);

  if (gh) {
    if (SCOOP_ICON_MODE === 'ddg') {
      return 'https://icons.duckduckgo.com/ip3/github.com.ico';
    }
    if (repoWebsite) {
      const d = domainOf(repoWebsite);
      if (d) return `https://icons.duckduckgo.com/ip3/${d}.ico`;
    }
    const owner = gh.split('/')[0];
    return `https://github.com/${encodeURIComponent(owner)}.png?size=64`;
  }

  const d = domainOf(row.homepage);
  return d ? `https://icons.duckduckgo.com/ip3/${d}.ico` : '';
}

/**
 * Fill in iconUrl for scoop rows, resolving github repo websites as needed.
 * Rows are returned with a usable icon either way — unresolved repos get the owner
 * avatar now and the better favicon on a later search once the cache is warm.
 */
async function decorateScoopIcons(rows) {
  if (!rows.length) return rows;

  const keys = new Set();
  for (const r of rows) {
    const gh = githubRepoOf(r.homepage);
    if (gh) keys.add(gh);
  }

  let websites = new Map();
  if (keys.size) {
    try {
      websites = await resolveRepoWebsites([...keys]);
    } catch { /* fall through with whatever we have */ }
  }

  for (const r of rows) {
    const gh = githubRepoOf(r.homepage);
    if (gh) {
      const site = websites.has(gh)
        ? websites.get(gh)
        : (loadGhCache().get(gh)?.homepage ?? '');
      r.repoWebsite = site || '';
      r.iconUrl = iconForRow(r, site);
    } else {
      r.iconUrl = iconForRow(r, '');
    }
  }
  return rows;
}

// ---------------------------------------------------------------------------
// unified scoop row builder
// ---------------------------------------------------------------------------

function buildScoopRow({ name, version, description, license, homepage, bucketUrl, stars, manifestUrl }) {
  const officialName = OFFICIAL_BUCKETS.get(bucketUrl);
  const short = officialName || repoShortName(bucketUrl) || 'unknown';
  const owner = githubOwnerOf(bucketUrl);
  const qualified = officialName === 'main' ? name : `${short}/${name}`;

  const installCommand = officialName
    ? `scoop install ${qualified}`
    : `scoop bucket add ${owner} ${bucketUrl} && scoop install ${owner}/${name}`;

  const domain = domainOf(homepage);

  return {
    source: 'scoop',
    id: name,
    name,
    version: cleanVersion(version),
    description: description || '',
    publisher: '',
    license: licenseOf(license),
    bucket: officialName || githubRepoOf(bucketUrl),
    bucketUrl,
    official: Boolean(officialName),
    stars: stars || 0,
    homepage: homepage || '',
    domain,
    repoWebsite: '',
    iconUrl: '',                    // filled by decorateScoopIcons()
    installCommand,
    uri: scoopUri(name, officialName || bucketUrl),
    detailUrl: manifestUrl || (bucketUrl ? `${bucketUrl}#readme` : ''),
  };
}

// ---------------------------------------------------------------------------
// scoop source A — ScoopSearch azure index (primary)
// ---------------------------------------------------------------------------

/**
 * Query the ScoopSearch index (scoopinstaller.github.io) — 180k manifests across
 * every bucket it discovers, official and community alike.
 *
 * It is a full-text index, so a single term still answers loosely — "7zip"
 * also matches "7zip-zstd" and "7zip@21.03" — and it holds every bucket's copy
 * of a package, so one name can arrive nine times. Two passes fix that:
 *
 *   1. docs carrying `Metadata.DuplicateOf` are mirrors of a package that
 *      already lives in another bucket, and are dropped outright;
 *   2. what is left is deduped by name, preferring
 *      exact > prefix > official bucket > more stars > shorter name.
 *
 * Azure's own relevance order decides which candidates we ever see, so we
 * over-fetch a few times the requested limit before slicing.
 */
async function searchScoopAzure(q, limit) {
  const top = Math.min(AZURE_FETCH_MAX, Math.max(AZURE_FETCH_MIN, limit * 4));
  const body = {
    search: q,
    searchMode: AZURE_SEARCH_MODE,
    count: true,
    top,
    select: 'Name,Version,Description,License,Homepage,Metadata',
  };
  const r = await jfetch(SCOOP_AZURE_URL, {
    method: 'POST',
    headers: { 'api-key': SCOOP_AZURE_KEY, 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!r.ok) {
    const detail = r.json?.error?.message || r.text.slice(0, 160) || `HTTP ${r.status}`;
    throw new Error(`Azure Search ${r.status}: ${detail}`);
  }

  const docs = (r.json?.value || []).filter((d) => !d.Metadata?.DuplicateOf);
  const ql = q.toLowerCase();
  const best = new Map();

  for (const d of docs) {
    const name = d.Name || '';
    const nl = name.toLowerCase();
    const rank = [
      nl === ql ? 0 : 1,
      nl.startsWith(ql) ? 0 : 1,
      OFFICIAL_BUCKETS.has(d.Metadata?.Repository) ? 0 : 1,
      -(d.Metadata?.RepositoryStars || 0),
      name.length,
    ];
    const cur = best.get(nl);
    if (!cur || compareRank(rank, cur.rank) < 0) best.set(nl, { d, rank });
  }

  const picked = [...best.values()].sort((a, b) => compareRank(a.rank, b.rank));

  return {
    total: best.size,
    rows: picked.slice(0, limit).map(({ d }) => buildScoopRow({
      name: d.Name,
      version: d.Version,
      description: d.Description,
      license: d.License,
      homepage: d.Homepage,
      bucketUrl: d.Metadata?.Repository || '',
      manifestUrl: manifestUrlOf(d.Metadata),
      stars: d.Metadata?.RepositoryStars || 0,
    })),
  };
}

/**
 * Manifest link from an azure doc. Pinned to the commit SHA the index recorded,
 * since a branch name drifts away from the indexed revision. Falls back to the
 * recorded branch, then to `master`, and gives up entirely with no repository —
 * a half-built URL is worse than none, because the row falls back to the bucket
 * readme instead.
 */
function manifestUrlOf(m) {
  if (!m?.Repository) return '';
  if (!m.FilePath) return m.Repository;
  return `${m.Repository}/blob/${m.Sha || m.BranchName || 'master'}/${m.FilePath}`;
}

// ---------------------------------------------------------------------------
// scoop source B — jsDelivr official-bucket listing (keyless fallback)
// ---------------------------------------------------------------------------

let localIndex = null;
let localIndexBuilding = null;
const indexCachePath = () => path.join(CACHE_DIR, 'scoop-local-index.json');

/** How long a cached fallback index is trusted before it is rebuilt. */
const INDEX_TTL_MS = Number(process.env.SCOOP_INDEX_TTL_HOURS || 24) * 3600_000;

/** The index file as it sits on disk, TTL ignored. null when unreadable. */
function readIndexFile() {
  try { return JSON.parse(fs.readFileSync(indexCachePath(), 'utf8')); }
  catch { return null; }
}

/**
 * Cached index when it is still fresh, else null. Freshness comes from the
 * file's mtime rather than a field inside it, so the on-disk format stays a
 * plain array. Refresh is automatic — there is no endpoint to poke.
 */
function loadCachedIndex() {
  if (localIndex) return localIndex;
  try {
    if (Date.now() - fs.statSync(indexCachePath()).mtimeMs > INDEX_TTL_MS) return null;
  } catch {
    return null;
  }
  localIndex = readIndexFile();
  return localIndex;
}

async function buildLocalIndex() {
  if (localIndexBuilding) return localIndexBuilding;
  localIndexBuilding = (async () => {
    try {
      const entries = [];
      const results = await Promise.allSettled(
        SCOOP_BUCKETS_OFFICIAL.map(async (bucket) => {
          const r = await jfetch(`${JSDELIVR_API}/ScoopInstaller/${bucket}@master?structure=flat`);
          if (!r.ok || !Array.isArray(r.json?.files)) return [];
          const out = [];
          for (const f of r.json.files) {
            const m = /^\/bucket\/(.+)\.json$/.exec(f.name);
            if (m) out.push({ name: m[1], bucket });
          }
          return out;
        }),
      );
      for (const r of results) if (r.status === 'fulfilled') entries.push(...r.value);

      // A jsDelivr outage that empties every bucket must not be written over a
      // good index — that would turn a transient failure into a permanent one.
      if (!entries.length) return null;

      entries.sort((a, b) => a.name.localeCompare(b.name));
      try {
        fs.mkdirSync(CACHE_DIR, { recursive: true });
        const tmp = `${indexCachePath()}.tmp`;
        fs.writeFileSync(tmp, JSON.stringify(entries));
        fs.renameSync(tmp, indexCachePath());
      } catch { /* best effort */ }
      localIndex = entries;
      return entries;
    } finally {
      localIndexBuilding = null;
    }
  })();
  return localIndexBuilding;
}

async function searchScoopLocal(q, limit) {
  let idx = loadCachedIndex();
  if (!idx?.length) {
    // keep the stale copy around: a failed rebuild should degrade to old data,
    // not to zero results
    const stale = readIndexFile();
    idx = (await buildLocalIndex()) || stale || [];
  }

  const ql = q.toLowerCase();
  const scored = [];
  for (const e of idx) {
    const i = e.name.toLowerCase().indexOf(ql);
    if (i === -1) continue;
    scored.push({ e, rank: i === 0 ? 0 : 1, len: e.name.length });
  }
  scored.sort((a, b) => a.rank - b.rank || a.len - b.len || a.e.name.localeCompare(b.e.name));

  const rows = scored.slice(0, limit).map(({ e }) => buildScoopRow({
    name: e.name,
    version: '',
    description: '',
    license: '',
    homepage: '',
    bucketUrl: `https://github.com/ScoopInstaller/${e.bucket}`,
    stars: 0,
  }));
  return { total: scored.length, rows };
}

// ---------------------------------------------------------------------------
// scoop manifest enrichment — only needed for the jsdelivr fallback
// ---------------------------------------------------------------------------

const manifestCache = new Map();
const MANIFEST_CONCURRENCY = 12;

async function fetchManifest(bucket, app) {
  const key = `${bucket}/${app}`;
  if (manifestCache.has(key)) return manifestCache.get(key);
  const url = `https://cdn.jsdelivr.net/gh/ScoopInstaller/${bucket}@master/bucket/${app}.json`;
  let manifest = null;
  try {
    const r = await jfetch(url);
    if (r.ok && r.json && typeof r.json === 'object') manifest = r.json;
  } catch { /* stay null */ }
  manifestCache.set(key, manifest);
  return manifest;
}

async function enrichScoopRows(rows) {
  await mapLimit(rows, MANIFEST_CONCURRENCY, async (row) => {
    if (!row.bucketUrl?.includes('ScoopInstaller/')) return;
    const bucket = repoShortName(row.bucketUrl);
    const m = await fetchManifest(bucket, row.id);
    if (!m) return;
    if (!row.version && m.version != null) row.version = cleanVersion(m.version);
    if (!row.description && m.description) row.description = String(m.description);
    if (!row.license) row.license = licenseOf(m.license);
    const home = typeof m.homepage === 'string' ? m.homepage : '';
    if (home && !row.homepage) {
      row.homepage = home;
      row.domain = domainOf(home);
    }
  });
  return rows;
}

// ---------------------------------------------------------------------------
// winget source — winstall.app
//
// One request per search and nothing else. winstall returns the full package
// object inline — homepage, licence, tags, categories, publisher, the manifest
// path, and every version with its installer type and installer URLs — so there
// is no per-app follow-up to make and nothing to cache. (The previous sources
// needed a second request per app just to learn the homepage.)
//
// The one thing it does not carry is a link to the manifest itself, and that
// needs no API: winget-pkgs lays manifests out as
// manifests/<first letter>/<Publisher>/<Name>/<Version>, derivable from the
// `path` field.
// ---------------------------------------------------------------------------

/** The GitHub link to a version's manifest folder. Pure string work, no fetch. */
function wingetManifestUrl(appPath, version) {
  if (!appPath || !version) return '';
  const segs = String(appPath).split('/').filter(Boolean);
  if (segs.length < 2) return '';
  const first = segs[0][0].toLowerCase();
  return `${WINGETPKGS_MANIFEST_BASE}/${first}/${segs.map(pathSeg).join('/')}/${pathSeg(version)}`;
}

/**
 * Icon for a winget row. winstall's own icon when it has one, otherwise
 * wingetly's endpoint — see WINGETLY_ICON_BASE for why the fallback exists.
 */
function iconForWinget(app) {
  if (app.icon) return `${WINSTALL_BASE}/icons/${pathSeg(app.icon)}`;
  return app._id ? `${WINGETLY_ICON_BASE}/api/icon/${pathSeg(app._id)}` : '';
}

/** Build a row from a winstall package object. */
function buildWingetRow(app) {
  const id = app._id;
  const version = cleanVersion(app.latestVersion || '');
  const homepage = typeof app.homepage === 'string' ? app.homepage.trim() : '';
  const manifestUrl = wingetManifestUrl(app.path, version);

  // the version entry the row advertises, for installer type + URLs
  const versions = Array.isArray(app.versions) ? app.versions : [];
  const latest = versions.find((v) => v?.version === app.latestVersion) || versions[0] || {};
  const installers = Array.isArray(latest.installers) ? latest.installers.filter(Boolean) : [];

  return {
    source: 'winget',
    id,
    name: app.name || id,
    version,
    description: app.desc || '',
    publisher: app.publisher || '',
    license: app.license || '',
    licenseUrl: app.licenseUrl || '',
    tags: Array.isArray(app.tags) ? app.tags : [],
    categories: Array.isArray(app.categories) ? app.categories : [],
    bucket: '',
    official: true,
    stars: 0,
    homepage,
    domain: domainOf(homepage),
    repoWebsite: '',
    iconUrl: iconForWinget(app),
    installCommand: `winget install --id ${id} -e`,
    uri: wingetUri(id),
    packageUrl: app.path ? `${WINSTALL_PAGE_BASE}/apps/${app.path}` : '',
    manifestUrl,
    detailUrl: manifestUrl,
    installerType: latest.installerType || '',
    installers: installers.slice(0, 8),
    versionCount: versions.length,
  };
}

/**
 * Search winstall's package index.
 *
 * `q` is matched against name, publisher, tags and description; results come
 * back paginated as { total, offset, limit, data[] }, with a server-side cap of
 * 200 per request — well above MAX_LIMIT, so one request always fills a page.
 */
async function searchWinget(q, limit) {
  const want = Math.min(limit, MAX_LIMIT);
  const url = `${WINSTALL_BASE}/apps/search?q=${encodeURIComponent(q)}&offset=0&limit=${want}`;
  const r = await jfetch(url);
  if (!r.ok) throw new Error(`winstall ${r.status}`);

  const rows = (r.json?.data || []).map(buildWingetRow).filter((row) => row.id);
  return { total: Number(r.json?.total) || rows.length, rows: rows.slice(0, want) };
}

/**
 * Exact single-package lookup for /api/resolve.
 *
 * GET /apps/<id> returns the one package an id names, so a resolve never has to
 * trust a relevance ranking. Falls back to search + exact-match only if that
 * 404s, so a slightly-off id still resolves to something rather than null.
 */
async function resolveWinget(id) {
  const r = await jfetch(`${WINSTALL_BASE}/apps/${pathSeg(id)}`);
  if (r.ok && r.json?._id) return buildWingetRow(r.json);

  const s = await searchWinget(id, 25);
  const exact = s.rows.find((x) => x.id.toLowerCase() === id.toLowerCase());
  return exact || s.rows[0] || null;
}

// ---------------------------------------------------------------------------
// aggregate search
// ---------------------------------------------------------------------------

/**
 * Pick the scoop source.
 *
 * azure is the default and needs a key. Without one there is nothing to query,
 * so it degrades to the keyless jsDelivr index rather than failing every search
 * — that index only covers official buckets and carries no descriptions, which
 * is why the rows are enriched from their manifests.
 *
 * A *failing* azure request is not degraded silently: the error propagates to
 * the caller and shows up in the response's `errors.scoop`, because quietly
 * answering with a lesser source is how you end up debugging the wrong thing.
 */
async function searchScoop(q, limit) {
  let result;

  if (SCOOP_SOURCE !== 'jsdelivr' && SCOOP_AZURE_KEY) {
    result = await searchScoopAzure(q, limit);
  } else {
    result = await searchScoopLocal(q, limit);
    await enrichScoopRows(result.rows);
  }

  // single place where icons get resolved for every scoop path
  await decorateScoopIcons(result.rows);
  return result;
}

async function search(q, source, limit) {
  const wantScoop = source === 'all' || source === 'scoop';
  const wantWinget = source === 'all' || source === 'winget';

  const tasks = [];
  const keys = [];
  if (wantScoop) { keys.push('scoop'); tasks.push(searchScoop(q, limit)); }
  if (wantWinget) { keys.push('winget'); tasks.push(searchWinget(q, limit)); }

  const settled = await Promise.allSettled(tasks);
  const rows = [];
  const errors = {};

  settled.forEach((r, i) => {
    if (r.status === 'fulfilled') rows.push(...r.value.rows);
    else errors[keys[i]] = String(r.reason?.message || r.reason);
  });

  return {
    query: q,
    source,
    scoopMode: activeScoopSource(),
    rows,
    errors: Object.keys(errors).length ? errors : undefined,
  };
}

/** The scoop source a search would actually use right now. */
function activeScoopSource() {
  return SCOOP_SOURCE !== 'jsdelivr' && SCOOP_AZURE_KEY ? 'azure' : 'jsdelivr';
}

// ---------------------------------------------------------------------------
// http server
// ---------------------------------------------------------------------------

function sendJson(res, code, obj) {
  res.writeHead(code, {
    'Content-Type': 'application/json; charset=utf-8',
    'Cache-Control': 'no-store',
  });
  res.end(JSON.stringify(obj, null, 2));
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host || HOST}`);
  const { pathname, searchParams } = url;

  try {
    if (pathname === '/' || pathname === '/index.html') {
      let html, tags;
      try { html = fs.readFileSync(path.join(__dirname, 'ui.html'), 'utf8'); }
      catch { res.writeHead(500, { 'Content-Type': 'text/plain' }); return res.end('ui.html not found'); }
      res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' });
      try { tags = fs.readFileSync(path.join(__dirname, 'tags.html'), 'utf8'); html = html.replace('</head>', `${tags}\n</head>`) }
      catch { }
      return res.end(html);
    }

    if (pathname === '/api/health') {
      return sendJson(res, 200, {
        ok: true,
        port: PORT,
        scoop: {
          configuredSource: SCOOP_SOURCE,
          activeSource: activeScoopSource(),
          azure: {
            endpoint: SCOOP_AZURE_URL,
            index: 'apps',
            keyPresent: Boolean(SCOOP_AZURE_KEY),
            fetchWindow: [AZURE_FETCH_MIN, AZURE_FETCH_MAX],
          },
        },
        winget: {
          search: `${WINSTALL_BASE}/apps/search`,
          detail: `${WINSTALL_BASE}/apps/<id>`,
          page: `${WINSTALL_PAGE_BASE}/apps/<path>`,
          iconFallback: WINGETLY_ICON_BASE,
          manifestUrl: 'derived from `path` + version — no API call',
          strategy: 'one request per search; the package object carries homepage, licence, tags, manifest path and installers',
        },
        iconMode: SCOOP_ICON_MODE,
        githubRepoCache: {
          cached: ghState.cached,
          lookups: ghState.lookups,
          errors: ghState.errors,
          tokenPresent: ghState.token,
          rateLimit: ghState.rateLimit,
          lastError: ghState.lastError,
          ttlDays: GH_TTL_MS / 86400_000,
          perSearchCap: GH_PER_SEARCH,
          deadlineMs: GH_DEADLINE_MS,
          strategy: 'lazy — only repos appearing in search results are looked up',
        },
        uriContract: {
          scoop: 'scoop://install/<app>?bucket=<name-or-repo-url>',
          winget: 'winget://install/<percent-encoded-id>',
        },
      });
    }

    if (pathname === '/api/search') {
      const q = (searchParams.get('q') || '').trim();
      const source = (searchParams.get('source') || 'all').toLowerCase();
      const limit = clampLimit(searchParams.get('limit'));
      if (!q) return sendJson(res, 200, { query: '', rows: [] });
      if (!['all', 'scoop', 'winget'].includes(source)) {
        return sendJson(res, 400, { error: 'source must be all|scoop|winget' });
      }
      return sendJson(res, 200, await search(q, source, limit));
    }

    if (pathname === '/api/resolve') {
      const source = (searchParams.get('source') || '').toLowerCase();
      const id = (searchParams.get('id') || '').trim();
      if (!id) return sendJson(res, 400, { error: 'id required' });
      if (source === 'winget') {
        return sendJson(res, 200, { row: await resolveWinget(id) });
      }
      if (source === 'scoop') {
        const bare = id.split('/').pop();
        const r = await searchScoop(bare, 25);
        const exact = r.rows.find((x) => x.name.toLowerCase() === bare.toLowerCase());
        return sendJson(res, 200, { row: exact || r.rows[0] || null });
      }
      return sendJson(res, 400, { error: 'source must be scoop|winget' });
    }

    res.writeHead(404, { 'Content-Type': 'text/plain' });
    return res.end('not found');
  } catch (err) {
    return sendJson(res, 500, { error: String(err?.message || err) });
  }
});

// warm the repo cache so the first search does not pay for the file read
loadGhCache();

server.listen(PORT, HOST, () => {
  const src = activeScoopSource();
  const known = ['azure', 'jsdelivr'].includes(SCOOP_SOURCE);
  process.stdout.write(
    `\npkgproxy listening on http://${HOST}:${PORT}\n` +
    `  node   : ${process.version}\n` +
    `  scoop  : ${src}` +
      `${src === 'jsdelivr'
          ? ` (jsDelivr official buckets — SCOOP_AZURE_KEY ${SCOOP_AZURE_KEY ? 'ignored' : 'not set'})`
          : known
            ? ' (ScoopSearch azure index)'
            : ` (ScoopSearch azure index — SCOOP_SOURCE="${SCOOP_SOURCE}" is not azure|jsdelivr)`}\n` +
    `  winget : ${WINSTALL_BASE}/apps/search  (homepage + licence + installers inline)\n` +
    `  icons  : ${SCOOP_ICON_MODE} (github repo website cache, lazy` +
      `${GITHUB_TOKEN ? ', token set' : ', no token'}, ${ghState.cached} repos known)\n` +
    `  uris   : scoop://install/<app>?bucket=<name-or-url>  |  winget://install/<id>\n\n`,
  );
});

// flush the repo cache cleanly — containers send SIGTERM
function shutdown() {
  flushGhCache();
  server.close(() => process.exit(0));
  setTimeout(() => process.exit(0), 2000).unref();
}
process.on('SIGINT', shutdown);
process.on('SIGTERM', shutdown);
