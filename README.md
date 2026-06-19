# vrc-video-proxy

A small Go HTTP server that sits between VRChat (or any AVPro/Unity-based player)
and the internet. A yt-dlp-replacement *wrapper* asks this server for a video; the
server resolves the stream with yt-dlp, returns a playable URL (or yt-dlp-like JSON
for Resonite) pointing back at itself, and then **streams the upstream bytes
through to the player** with full `Range` support. There is no caching and no
transcoding — the server is a thin, codec-agnostic passthrough proxy.

## Architecture

```
VRChat ──> yt-dlp wrapper ──> GET /api/getvideo?url=...   (this server)
                                   │
                                   │  yt-dlp resolves the stream URL + replay headers,
                                   │  stashed under a short-lived handle id
                                   ▼
                       url -> /stream/<id>.mp4
                                   │
                  AVPro/Unity plays /stream/<id>.mp4
                                   │
                                   ▼  the server proxies the upstream bytes,
                                      forwarding Range and mirroring 200/206
```

- **Two-step flow.** `/api/getvideo` resolves the source URL once with yt-dlp and
  returns a `/stream/<id>.mp4` URL. The player then fetches the bytes from
  `/stream/<id>.mp4`, which the server relays from the resolved upstream.
- **Handle store.** The resolved upstream URL and the request headers to replay
  (User-Agent, Referer, Cookie) are kept in an in-memory store keyed by an
  unguessable random id. The id's TTL **slides on every access**, so an
  actively-playing or seeking client keeps its handle alive; abandoned handles
  expire (default 30m) and are reaped. State is process-local and does not survive
  a restart.
- **Response format.** VRChat invokes yt-dlp expecting a single plain-text URL on
  stdout, so for `source=vrchat` (the default) the server returns just the
  `/stream` `url` as `text/plain`. Resonite invokes yt-dlp with `-J` and parses the
  full document, so `source=resonite` gets the yt-dlp-like JSON instead, with the
  playback `url` rewritten to `/stream/<id>.mp4`.
- **Passthrough serving.** `/stream/<id>.mp4` forwards the client's `Range` (and
  conditional headers) to the upstream and mirrors the upstream status and headers
  (`Content-Type`, `Content-Length`, `Content-Range`, `Accept-Ranges`, …). Seeking
  works whenever the upstream itself is Range-capable. Nothing is written to disk
  and nothing is re-encoded, so the format selected by yt-dlp is what the player
  receives.

## Endpoints

| Method      | Path                  | Description |
|-------------|-----------------------|-------------|
| GET         | `/health`             | Liveness check, returns `ok`. |
| GET         | `/api/getvideo`       | Query: `url` (required, absolute http/https), `avpro` (bool), `source` (default `vrchat`). Resolves the stream and returns a plain-text `/stream/<id>.mp4` URL for `vrchat`, or yt-dlp-like JSON for `resonite`. |
| GET, HEAD   | `/stream/<id>.mp4`    | Proxies the resolved upstream stream byte-for-byte, forwarding `Range` and mirroring the upstream status (`200`/`206`/`416`) and headers. `<id>` is the handle minted by `/api/getvideo`. |

`<id>` must be a 32-character lowercase hex string; anything else returns 404. The
server only fetches upstream URLs it resolved itself (the id is an unguessable
capability), and every upstream fetch is SSRF-guarded against hosts resolving to
loopback/private/link-local addresses — it is not an open proxy.

## Build

```sh
go build -o vrc-video-proxy-server ./cmd/server
go build -o yt-dlp ./cmd/wrapper
```

On Windows, build the wrapper as `yt-dlp.exe` before placing it where VRChat
expects `yt-dlp.exe`.

## Configuration

Priority: command-line flags > environment variables > defaults.

| Env var                   | Flag               | Default          | Meaning |
|---------------------------|--------------------|------------------|---------|
| `VRCVP_LISTEN`            | `--listen`         | `127.0.0.1:8080` | HTTP listen address (loopback only by default). |
| `VRCVP_SHUTDOWN_TIMEOUT`  | `--shutdown-timeout`| `5s`            | Graceful shutdown timeout. |
| `VRCVP_YTDLP_PATH`        | `--ytdlp-path`     | `yt-dlp`         | Path to the yt-dlp executable (required at startup). |
| `VRCVP_COOKIES_FILE`      | `--cookies-file`   | (none)           | Optional yt-dlp cookies file. |
| `VRCVP_STREAM_TTL`        | `--stream-ttl`     | `30m`            | How long a resolved stream handle stays valid; the TTL slides on every access. |
| `VRCVP_LOG_LEVEL`         | `--log-level`      | `info`           | Log level: `debug`, `info`, `warn`, or `error`. |
| `VRCVP_PROXY`             | `--proxy`          | (none)           | Proxy for upstream traffic and yt-dlp, as `protocol://host:port` (`http`, `https`, `socks5`, `socks5h`; userinfo is sent as proxy auth). Routes the Go HTTP client and `yt-dlp` (via `--proxy`). When unset, an ambient `HTTP_PROXY`/`HTTPS_PROXY` is still honored. |
| `VRCVP_YTDLP_FORMAT`      | `--ytdlp-format`   | combined progressive MP4 selector | yt-dlp `-f` selector. The default prefers a single HTTP MP4 with audio+video, falls back to any combined stream, then to yt-dlp's `best`. |
| `VRCVP_YTDLP_EXTRA_ARGS_JSON` | `--ytdlp-extra-args-json` | (none) | Extra yt-dlp arguments as a JSON string array. JSON preserves spaces in values, e.g. `["--add-headers","User-Agent: Mozilla/5.0"]`. |

The server requires `yt-dlp` on `PATH` (or at `VRCVP_YTDLP_PATH`) at startup.

```sh
VRCVP_LOG_LEVEL=debug ./vrc-video-proxy-server --listen 127.0.0.1:9090
```

The wrapper reads `VRCVP_SERVER_URL` (default `http://127.0.0.1:8080`) and
`VRCVP_LOG_LEVEL` (default `info`). The wrapper logs to its own stderr; when the
level is `debug` it also appends to a `wrapper.log` file next to the wrapper
executable, so a debugging user gets a persistent record even though VRChat gives
the wrapper no console.

Wrapper-only playback options can be set per process with `VRCVP_OPTION_*`
environment variables. The wrapper forwards them to `/api/getvideo` as `vrcvp_*`
query parameters, e.g. `VRCVP_OPTION_TRANSCODE=true` becomes
`vrcvp_transcode=true`. If the source video URL already contains a `vrcvp_*`
parameter, the wrapper removes it from the source URL and forwards it separately;
per-URL values override `VRCVP_OPTION_*` defaults.

```sh
VRCVP_OPTION_TRANSCODE=true ./yt-dlp 'https://example.com/watch?v=1'
```

## Trying it out

```sh
# Resolve a source URL -> returns a /stream URL
curl 'http://127.0.0.1:8080/api/getvideo?url=https%3A%2F%2Fexample.com%2Fvideo.mp4&avpro=true&source=vrchat'

# Play it back through the proxy; confirm Range support (expects 206 + Content-Range):
curl -I  'http://127.0.0.1:8080/stream/<id>.mp4'
curl -r 0-1023 'http://127.0.0.1:8080/stream/<id>.mp4' -o /dev/null -D -
```

## Security / operational notes

- Listens on `127.0.0.1` by default.
- HTTP server timeouts are set (`ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`,
  `IdleTimeout`); the write deadline is cleared per-request on `/stream` so large
  videos are not cut off mid-transfer.
- yt-dlp is invoked via `exec.CommandContext` with an argument slice (no shell) and
  a timeout.
- Every upstream fetch has a basic SSRF guard that rejects hosts resolving to
  loopback/private/link-local addresses, and only handle ids minted by the server
  resolve to an upstream URL.
- All logging goes through `log/slog` at a configurable level (`VRCVP_LOG_LEVEL` /
  `--log-level`).

## yt-dlp Wrapper

The `./cmd/wrapper` command is a yt-dlp-style stub that VRChat invokes instead of
`yt-dlp`. It finds the first `http(s)` URL in the arguments, calls this server's
`/api/getvideo`, and writes the server response to stdout. For normal VRChat
calls, that response is a single plain-text playback URL pointing back at this
server, e.g.:

```text
http://127.0.0.1:8080/stream/<id>.mp4
```

For Resonite-style calls (`--flat-playlist`, sent as `source=resonite`), the
server returns yt-dlp-like JSON instead:

```json
{
  "url": "http://127.0.0.1:8080/stream/<id>.mp4",
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
