# vrc-video-proxy

A small Go HTTP server that sits between VRChat (or any AVPro/Unity-based player)
and the internet. A yt-dlp-replacement *wrapper* asks this server for a video; the
server extracts stream info with yt-dlp, returns yt-dlp-like JSON, caches the video
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
- **Cache hit:** the server returns yt-dlp-like JSON whose `url` points at
  `http://<host>/video/<id>.mp4`, served from disk with HEAD/Range/206 support.
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
- **Transcoding is not performed yet.** The raw progressive stream is cached as-is.
  yt-dlp is asked for a single file that already contains both audio and video, so
  no muxing is required. `ffmpeg` is therefore *not required* to run the server
  (the `VRCVP_FFMPEG_PATH` setting is reserved for a future transcode step).

## Endpoints

| Method      | Path                  | Description |
|-------------|-----------------------|-------------|
| GET         | `/health`             | Liveness check, returns `ok`. |
| GET         | `/api/getvideo`       | Query: `url` (required, absolute http/https), `avpro` (bool), `source` (default `vrchat`). Returns yt-dlp-like JSON. |
| GET, HEAD   | `/video/<id>.mp4`     | Serves a **finished** cached file via `http.ServeContent`: HEAD, `Range`, `206 Partial Content`, `Content-Length`, `Content-Range`, `Accept-Ranges: bytes`, `Content-Type: video/mp4`. |
| GET, HEAD   | `/live/<id>.mp4`      | Streams a download-in-progress. In **sparse mode** it honors `Range`/`206`/`Content-Range` and back-fills on seek; in the **sequential fallback** it streams from offset 0 with `Accept-Ranges: none`. If the download has already finished, it transparently serves the cached file with full Range support. |

`<id>` must be a 64-character lowercase hex string; anything else returns 404, so
path traversal is impossible. The server only fetches the upstream URL that yt-dlp
extracted for the user's requested `url` — it is not an open redirect or open proxy.

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
- Only single-file progressive streams are cached.
- **Transcoding is not performed yet:** the raw progressive stream is cached as-is,
  so `faststart`/`moov`-at-front depends on the upstream file (`VRCVP_FFMPEG_PATH`
  is reserved for a future transcode step; `ffmpeg` is not required to run).

## Planned features

- [ ] HLS/DASH live streams, including Twitch-style live URLs.
- [ ] ffmpeg-based remuxing/transcoding pipeline.
- [ ] Separate audio/video stream support.
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
| `VRCVP_FFMPEG_PATH`       | `--ffmpeg-path`    | `ffmpeg`                             | Path to ffmpeg (reserved; not required). |
| `VRCVP_COOKIES_FILE`      | `--cookies-file`   | (none)                               | Optional yt-dlp cookies file. |

The wrapper reads `VRCVP_SERVER_URL` and defaults to `http://127.0.0.1:8080`.

```sh
VRCVP_CACHE_DIR=/var/cache/vrcvp \
VRCVP_CACHE_MAX_SIZE=20GB \
./vrc-video-proxy-server --listen 127.0.0.1:9090
```

The server is required to find `yt-dlp` at startup. `ffmpeg` is optional: a warning
is logged if it is missing, but the server still runs.

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
