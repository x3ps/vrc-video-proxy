# vrc-video-proxy

A small Go HTTP server that sits between VRChat (or any AVPro/Unity-based player)
and the internet. A yt-dlp-replacement *wrapper* asks this server for a video; the
server extracts stream info with yt-dlp, returns a playable URL (or yt-dlp-like
JSON for Resonite), caches the video
on disk, and serves the cached file back over HTTP with full `Range` support.

The same binary can also run as a Steam launch-options wrapper that starts the
server and then launches the game.

## Architecture

```
VRChat ──> yt-dlp wrapper ──> GET /api/getvideo?url=...   (this server)
                                   │
                  cache hit ───────┤────────── cache miss
                                   │                │
        url -> /video/<id>.mp4     │   yt-dlp extract + start background download job
        (finished, Range-capable)  │   url -> /live/<id>.mp4  (streamed while downloading)
                                   ▼                ▼
                          AVPro/Unity plays the returned url
```

- **Cache id** is a SHA-256 of the (lightly normalized) source URL, hex-encoded —
  stable and filename-safe (`<id>.mp4`).
- **Response format.** VRChat invokes yt-dlp expecting a single plain-text URL on
  stdout, so for `source=vrchat` (the default) the server returns just the resolved
  `url` as `text/plain`. Resonite invokes yt-dlp with `-J` and parses the full
  document, so `source=resonite` gets the yt-dlp-like JSON instead.
- **Cache hit:** the server points `url` at `http://<host>/video/<id>.mp4`, served
  from disk with HEAD/Range/206 support.
- **Cache miss:** the server runs yt-dlp to get metadata and a single *progressive*
  stream URL, starts a background download job (deduplicated per cache id), and
  returns `url = http://<host>/live/<id>.mp4`. `/live` streams the bytes to the
  player *as they download* and tees them to disk. The request never blocks on the
  full download. When the download finishes, the temp file is atomically renamed
  into the cache, so subsequent requests are cache hits.
- **Sparse cache / live seeking.** When the upstream supports HTTP `Range` and
  reports a total size, `/live/<id>.mp4` runs in **sparse mode**: a single
  fixed-size file is filled out of order. A background filler downloads gaps from
  the lowest offset, and a player **seek ahead** of the filler triggers an
  on-demand range fetch for that gap, so `/live` answers real `Range`/`206`
  requests *during* download. Concurrent upstream fetches are capped globally
  (default 3), and identical in-flight gaps for the same video are de-duplicated.
  When the upstream lacks `Range` or a known size, `/live` falls back to
  sequential streaming (`Accept-Ranges: none`).
- **Progressive vs. HLS/DASH.** A single progressive file is downloaded and cached
  as-is (no ffmpeg needed). HLS/DASH sources are routed by kind: VOD is remuxed by
  ffmpeg into one cached MP4, live is proxied in real time (see *Stream vs. video*
  under [Endpoints](#endpoints)). `ffmpeg` is required only for the HLS/DASH paths.

## Endpoints

| Method      | Path                  | Description |
|-------------|-----------------------|-------------|
| GET         | `/health`             | Liveness check, returns `ok`. |
| GET         | `/api/getvideo`       | Query: `url` (required, absolute http/https), `avpro` (bool), `source` (default `vrchat`). Returns a plain-text URL for `vrchat`, or yt-dlp-like JSON for `resonite`. |
| GET, HEAD   | `/video/<id>.mp4`     | Serves a **finished** cached file via `http.ServeContent`: HEAD, `Range`, `206 Partial Content`, `Content-Length`, `Content-Range`, `Accept-Ranges: bytes`, `Content-Type: video/mp4`. |
| GET, HEAD   | `/live/<id>.mp4`      | Streams a download-in-progress. In **sparse mode** it honors `Range`/`206`/`Content-Range` and back-fills on seek; in the **sequential fallback** it streams from offset 0 with `Accept-Ranges: none`. If the download has already finished, it transparently serves the cached file with full Range support. |
| GET, HEAD   | `/hls/manifest`       | Query: `t` (a signed token carrying an upstream manifest URL + headers). Fetches a **live** HLS or DASH manifest, rewrites/﻿converts it to HLS pointing back at this server, and returns it (`application/vnd.apple.mpegurl`, `Cache-Control: no-store`). |
| GET, HEAD   | `/hls/segment`        | Query: `t` (signed token). Proxies a live HLS/DASH segment, init section, or key, served from a short-lived in-memory cache with concurrent-fetch de-duplication. |

`<id>` must be a 64-character lowercase hex string; anything else returns 404, so
path traversal is impossible. The server only fetches upstream URLs that it
extracted (progressive) or that it signed itself (manifest/segment tokens), and
every upstream fetch is SSRF-guarded — it is not an open redirect or open proxy.

### Stream vs. video: how HLS/DASH is handled

The kind of source decides the serving path:

- **VOD (a finite video)** — HLS with `#EXT-X-ENDLIST`, DASH `MPD@type="static"`, or
  yt-dlp `is_live=false` — is **remuxed by ffmpeg into a single MP4** on disk (codec
  copy, or re-encode to H.264/AAC when the source codecs are not MP4-compatible),
  then served exactly like a progressive file via `/live` → `/video` with full
  Range/seek and instant replays from cache.
- **Live (an unbounded stream)** — Twitch-style live HLS/DASH — cannot be cached, so
  it is **proxied in real time**: `/hls/manifest` rewrites the playlist (or converts
  a DASH MPD to HLS) and `/hls/segment` proxies the segments with a short in-memory
  TTL cache. AVPro plays the HLS directly.

### Realtime playback limitations

- `/live/<id>.mp4` supports seeking only when the upstream is Range-capable with a
  known size (sparse mode). Otherwise it falls back to **sequential** streaming
  with `Accept-Ranges: none`, and seeking before the file is cached will not work.
  Once cached, `/video/<id>.mp4` always supports full `Range` seeking.
- Sparse mode opens extra upstream connections for seek-driven back-fill (capped
  globally). Some hosts (e.g. googlevideo) may throttle or `403` parallel
  connections; this is a known caveat.
- In-progress (sparse) downloads are **not resumed across restarts** — `tmp/` is
  cleared on startup; only finished `<id>.mp4` files persist.
- Progressive sources are cached as a single file; HLS/DASH VOD is remuxed into one
  MP4 (see above) and then cached the same way.
- **Live DASH** support covers `SegmentTemplate` + `SegmentTimeline` manifests
  (the common live shape); other MPD shapes return `501`. Live segments are not
  cached across restarts (the in-memory segment cache is process-local).
- **ffmpeg is required** for HLS/DASH remux and transcoding. It remains optional if
  you only use progressive sources; a warning is logged if it is missing.

## Planned features

- [x] HLS/DASH live streams, including Twitch-style live URLs.
- [x] ffmpeg-based remuxing/transcoding pipeline.
- [ ] Separate audio/video stream support (muxing two distinct tracks; today
  remux/transcode operates on a single combined input).
- [ ] Configurable logging modes: `debug`, `info`, `warn`, and `error`.

## Build

```sh
go build -o vrc-video-proxy-server ./server
go build -o yt-dlp ./wrapper
```

On Windows, build the wrapper as `yt-dlp.exe` before placing it where VRChat
expects `yt-dlp.exe`.

## Configuration

Priority: command-line flags > environment variables > defaults.

| Env var                   | Flag               | Default                              | Meaning |
|---------------------------|--------------------|--------------------------------------|---------|
| `VRCVP_LISTEN`            | `--listen`         | `127.0.0.1:8080`                     | HTTP listen address (loopback only by default). |
| `VRCVP_SHUTDOWN_TIMEOUT`  | `--shutdown-timeout`| `5s`                                | Graceful shutdown timeout. |
| `VRCVP_CACHE_DIR`         | `--cache-dir`      | OS user cache dir `/vrc-video-proxy` | Directory for cached videos. |
| `VRCVP_CACHE_MAX_SIZE`    | `--cache-max-size` | `10GB`                               | Cache budget (`10GB`, `500MB`, `1.5G`, or raw bytes). LRU eviction by mtime. |
| `VRCVP_YTDLP_PATH`        | `--ytdlp-path`     | `yt-dlp`                             | Path to the yt-dlp executable (required). |
| `VRCVP_FFMPEG_PATH`       | `--ffmpeg-path`    | `ffmpeg`                             | Path to ffmpeg (required for HLS/DASH remux and transcoding). |
| `VRCVP_COOKIES_FILE`      | `--cookies-file`   | (none)                               | Optional yt-dlp cookies file. |
| `VRCVP_SECRET`            | `--secret`         | (random per process)                 | Secret for signing manifest/segment URLs. Set a fixed value if exposing the proxy publicly so tokens survive restarts. |
| `VRCVP_SEGMENT_CACHE_TTL` | `--segment-cache-ttl` | `5m`                              | In-memory live HLS/DASH segment cache TTL. |
| `VRCVP_SEGMENT_CACHE_SIZE`| `--segment-cache-size`| `512`                             | In-memory live HLS/DASH segment cache entry count. |

The wrapper reads `VRCVP_SERVER_URL` and defaults to `http://127.0.0.1:8080`.

```sh
VRCVP_CACHE_DIR=/var/cache/vrcvp \
VRCVP_CACHE_MAX_SIZE=20GB \
./vrc-video-proxy-server --listen 127.0.0.1:9090
```

The server is required to find `yt-dlp` at startup. `ffmpeg` is optional but
needed for HLS/DASH: a warning is logged if it is missing and the server still
runs, but HLS/DASH remux and transcoding will be unavailable. When `ffmpeg` is
present the preferred H.264 encoder (hardware if available, else `libx264`) is
probed once at startup.

## Steam Launch Options

Use the same server binary as a Steam launch-options wrapper. Keep `%command%`
after `--`:

```sh
/path/to/vrc-video-proxy-server -- %command%
```

With GameMode:

```sh
/path/to/vrc-video-proxy-server -- gamemoderun %command%
```

The server starts, waits until it is reachable, launches the game, and shuts down
gracefully when the game exits.

## Trying it out

```sh
# Cache miss -> returns a /live URL, starts a background download
curl 'http://127.0.0.1:8080/api/getvideo?url=https%3A%2F%2Fexample.com%2Fvideo.mp4&avpro=true&source=vrchat'

# After it has cached, getvideo returns a /video URL; confirm Range support:
curl -I  'http://127.0.0.1:8080/video/<id>.mp4'
curl -r 0-1023 'http://127.0.0.1:8080/video/<id>.mp4' -o /dev/null -D -
```

## Security / operational notes

- Listens on `127.0.0.1` by default.
- HTTP server timeouts are set (`ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`,
  `IdleTimeout`); the write deadline is cleared per-request on the streaming
  endpoints so large videos are not cut off.
- yt-dlp is invoked via `exec.CommandContext` with an argument slice (no shell) and
  a timeout. Downloads have their own timeout.
- The background downloader has a basic SSRF guard that rejects upstream hosts
  resolving to loopback/private/link-local addresses.
- All logging goes through `log/slog`. A failed download is logged and cleaned up;
  it never crashes the server.

## yt-dlp Wrapper

The `./wrapper` command is a yt-dlp-style stub that VRChat invokes instead of
`yt-dlp`. It finds the first `http(s)` URL in the arguments, calls this server's
`/api/getvideo`, and writes the JSON response to stdout. The response `url` field
points back at this server, e.g.:

```json
{
  "url": "http://127.0.0.1:8080/video/<id>.mp4",
  "original_url": "https://example.com/video.mp4"
}
```

By convention with VRChat's yt-dlp calls, the wrapper sends `avpro=false` when
the argument list contains `[protocol^=http]`; otherwise it sends `avpro=true`.
If the arguments contain `--flat-playlist`, it sends `source=resonite`, matching
the behavior used by VRCVideoCacher's stub.

### Preventing VRChat from overwriting the wrapper

After replacing VRChat's `yt-dlp.exe` with this wrapper, make the file read-only
so VRChat's own yt-dlp auto-update cannot silently overwrite it.

CMD:

```bat
attrib +R path\to\yt-dlp.exe
```

PowerShell:

```powershell
Set-ItemProperty -Path "path\to\yt-dlp.exe" -Name IsReadOnly -Value $true
```

POSIX shell:

```sh
chmod a-w /path/to/yt-dlp
```

Unlock it before reinstalling or replacing the wrapper.

CMD:

```bat
attrib -R path\to\yt-dlp.exe
```

PowerShell:

```powershell
Set-ItemProperty -Path "path\to\yt-dlp.exe" -Name IsReadOnly -Value $false
```

POSIX shell:

```sh
chmod u+w /path/to/yt-dlp
```

## Inspiration

This project was inspired by
[VRCVideoCacher](https://github.com/EllyVR/VRCVideoCacher).

## License

MIT.
