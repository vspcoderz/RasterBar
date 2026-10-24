# PLAN — download verb + URL playback + media cache + update + settings

Status: **done.** `download` (URL + keyterm picker) downloads merged mp4s into
the cache and serves `play` of an already-downloaded ID from the file; `update`
self-swaps via GitHub releases; `settings` is the click-only store. Verified:
new unit tests green, real download of "Me at the zoo" into a sandbox cache,
"already cached" re-invocation, and a pty drive of `rasterbar <url>` that
presented live frames with "local" in the chrome. `update --check` reports the
honest 404 until the first release is tagged.

## Goal

1. `rasterbar download <keyterm | URL>` — download a track into a cache dir.
2. `rasterbar <URL>` / `rasterbar play <URL>` — play a URL directly instead of
   turning it into a YouTube search for the URL text.
3. Playback serves a remote track from cache when it is already downloaded.

## Approach

- **New file `src/download.go`**
  - `cacheDir()` — flag/env/default: `--cache-dir DIR`, `VSPZ_YT_CLI_CACHE_DIR`,
    default under the home dir. Two subdirs: `audio/` and `video/`.
  - `downloadTrack(t Track, cfg) error` — one `yt-dlp` call, routed through
    `ytdlpAuth.run` (cookies everywhere rule; a flagged machine needs cookies on
    the download path too or the bug only shows for flagged users).
  - Filename template `%(title)s [%(id)s].%(ext)s` — the ID in every name is what
    makes cache lookup a glob instead of an index.
  - `--download-archive downloaded.txt` + `--no-overwrites`: the archive is the
    "already have it" index keyed by video ID.
  - Format selection (AGENTS dead-end: no progressive `18` exists anymore, so
    every download is split streams):
    - audio: (dropped) no separate audio files — downloads carry the merged mp4
    video: `bestvideo[height<=1080]+bestaudio/best[height<=1080]/b`, mp4 out
  - Progress: print `downloading: <title>` before, result line after. stderr stays
    captured (raw-mode/renderer rule) and is shown on failure.
- **URL playback (`src/playlist.go` + `main.go`)**
  - `looksLikeURL` already exists. `expandFileArgs`' classification grows a third
    kind: a URL arg becomes a `Track{URL:…}`, mixed with file args in one queue.
    Search still happens only when no arg is a file or a URL
    (`TestSearchStillSearches` semantics).
  - Standalone URL helper: `trackForURL(url)` sets `Scraper:"url"`; the title is
    filled from the `-J` response already being fetched at playback (add `Title`
    to `ytInfo` — it is on the wire, we just never read it).
- **Cache hit (src/ascii.go `resolveMedia`)**
  - Before resolving a remote track, glob the cache for `[<ID>]*`. Found → probe
    the local file and play it — `Track.IsLocal()` machinery already handles
    everything downstream (no yt-dlp, no grants, seeking free).
  - Applies to search results and `download`-ed URLs alike, since ID is the key.
- **Verb wiring (`main.go`)**
  - `download` verb (+ `dl` alias): consumes args into a new `options` mode.
    Bare `rasterbar download` must not fall through to a search for the word
    "download" — needs the `neverBareVerbs` treatment (it takes a value: URL or
    keyterm).
  - Usage text updated.
- **Tests** (all offline, existing seams):
  - URL classification: URL → track, not search; mixed files+URLs; the
    "question-mark URL was a glob" family of regressions.
  - verb parsing: `download` consumes args, bare `download` errors nicely,
    `rasterbar play download.mp3` still a file.
  - `downloadTrack`'s yt-dlp arg list is pure code: assert format flags, merge
    format, archive path, and that cookies land in the list.
  - cache lookup: glob match by ID, miss → resolve as before.
  - End-to-end real download: needs network — manual verification step, same
    tier as the SIGSTOP/SIGINT cases.

## Files touched

- `src/download.go` (new), `src/download_test.go` (new)
- `src/main.go` — verb + intent dispatch + flag
- `src/playlist.go` — URL args as a third kind
- `src/ascii.go` — cache-hit lookup + `ytInfo.Title`
- `src/search.go` — untouched (search keeps working)
- `src/ytdlpauth.go` — untouched (used as-is)
- README/usage strings in `main.go`

## Verification

- `go vet ./... && go test ./... && go build -ldflags="-s -w" -o rasterbar ./src`
- pty drive: `tools/ptysync.py` — play a known URL twice; second run must show
  the cache hit (no yt-dlp network call: check with `--no-probe` timing or debug)
- Real download of one small video + one music track over network, manually.

## Open questions

None. Answers: picker (1a), flat `~/RasterStuff/` (2b), merged mp4 always (3).

## Scope additions (same build)

**`rasterbar update`** — self-update, stdlib only:
- GitHub REST `releases/latest` on `github.com/vspcoderz/rasterbar`, parse
  `tag_name`, pick the asset named for linux/$GOARCH; error lists what was found.
- Never overwrite blindly: download to `rasterbar.new-<pid>` in the binary's own
  *directory* (rename must be same-filesystem), verify `\x7fELF` magic, chmod
  0755, rename over `os.Executable()`. Running binary keeps executing the old
  inode, which is safe on Linux.
- Version compare strips a leading `v`; dev build (no stamp) warns and proceeds.
- `update --check` = compare only. `VSPZ_YT_CLI_UPDATE=auto` adds a background
  startup check (3s timeout, silent failure) that prints a notice line before
  playback starts — notice only, it never swaps the binary unasked.

**`rasterbar settings`** — click-only settings, zero third-party:
- `~/.config/rasterbar/config.json`, atomic write (temp + rename).
- Rows cycled with j/k + left/right or enter; no text inputs: values are cycles
  (viz names, palette names, quality/auto, music/video default, cookies-from
  browser or none, cache-dir presets including `~/RasterStuff`).
- Not a TTY: print current values.
- Precedence: explicit flag > settings > built-in default. Settings never
  override something the user typed.

## Files touched (final)

- `src/download.go` (new), `src/update.go` (new), `src/settings.go` (new)
- `src/main.go`, `src/playlist.go`, `src/ascii.go`, `src/ytdlpauth.go`, `src/tui.go`
- tests: `download_test.go`, `update_test.go`, `settings_test.go`, `args_test.go`
