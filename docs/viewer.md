# Viewer

The TLTV viewer is an HLS.js-based web player with two modes and a standalone
portal. It can run as a standalone command (`tltv viewer`) or be embedded in any
daemon via `--viewer` or `--debug-viewer`.

## Modes

### Production Viewer (`--viewer`)

Phosphor-matching dark theme with clean playback UI:

- HLS.js video player with Safari native fallback
- SVG icon controls: volume slider, fullscreen, picture-in-picture
- EPG guide grid with per-channel program blocks (4px/min scale, 30-minute slots)
- Channel selector dropdown for multi-channel daemons
- Audio and subtitle track selectors
- Relay badge from signed origins (green = origin, dim = relay, yellow = spoofed)
- `tltv://` URI display with click-to-copy
- Footer with TLTV mark and links
- Stall detection with auto-recovery
- Audio-only overlay (music note SVG + label)
- Mobile responsive at 640px

### Debug Viewer (`--debug-viewer`)

Diagnostic viewer with five live sections:

- **Channel** — curated metadata fields plus remaining keys
- **Stream** — live HLS stats (segments, bitrate, buffer, resolution), per-variant
  breakdown (resolution, bandwidth, codecs, active label), audio/subtitle tracks
- **Guide** — entries with now-playing marker, `updated` and `seq` fields
- **Node** — origin verification from signed metadata, channel list
- **Peers** — peer exchange entries

### Portal Mode (Standalone)

`tltv viewer` without a target starts a portal with a freeform tune box:

```bash
# Portal mode — opens tune box in browser
tltv viewer

# Direct tune to a host
tltv viewer demo.timelooptv.org

# Direct tune to a channel URI
tltv viewer "tltv://TVabc@origin.tv"
```

The portal resolves channels server-side via `/api/resolve`:

- Fetches `/.well-known/tltv`, verifies metadata, resolves stream
- Hostname tune discovers ALL channels from the host
- Icons fetched as base64 data-URIs at resolve time (no CORS issues)
- Guide data fetched per-discovered-channel

**Note:** The standalone portal is explicitly local/single-user. The process
holds a single global upstream — one client tuning changes playback for all
connected browsers. Do not expose on a public network for multi-user access.

## Embedding in Daemons

All four daemons (server, bridge, relay, mirror) support viewer embedding:

```bash
# Production viewer on a server
tltv server test -k channel.key --viewer --name "My Channel"

# Debug viewer on a relay
tltv relay --node origin:443 --debug-viewer

# Production viewer on a bridge with display config
tltv bridge --stream source.m3u --viewer --viewer-title "My Network"
```

The flags are mutually exclusive — setting both `--viewer` and `--debug-viewer`
is a startup error.

## Display Config

| Flag | Short | Env | Default | Description |
|------|-------|-----|---------|-------------|
| `--viewer-title` | `-e` | `VIEWER_TITLE` | *(empty)* | Nav bar label text |
| `--no-viewer-footer` | `-Z` | `VIEWER_FOOTER=0` | on | Hide footer links |

Values are JSON-encoded into the viewer config to prevent injection.

## Saved Channels

The portal persists tuned channels in the browser's `localStorage` as
`tltv_saved_channels`. Each successful tune adds the channel to the guide grid.

Optional server-side file persistence:

| Flag | Short | Env | Description |
|------|-------|-----|-------------|
| `--saved-channels` | `-E` | `SAVED_CHANNELS` | JSON file for channel list persistence |

When set, the portal server reads the file on startup, serves it via
`GET /api/saved-channels`, accepts `POST /api/saved-channels`, and writes back.
Channels are deduplicated by ID.

**Security:** Raw access tokens are never persisted in saved channel data.
Channel removal via the `x` button auto-tunes the next channel.

## Private Viewer Auth

On a private daemon (`--access token`), the embedded viewer root (`/`) and
`/api/info` are gated by the same `?token=...` query parameter used by protocol
endpoints. Open `/?token=secret` to access the viewer.

The viewer JS uses a shared `withToken()` helper to propagate the query token to
`/api/info` and stream requests. `/api/info` never echoes the raw token back to
the browser — the browser keeps the token in its own query string.

## SSRF Protection

When the standalone viewer listens on a non-loopback address, `/api/resolve` uses
the SSRF-safe client to prevent probing internal networks. The `--local`/`-L`
global flag overrides this block for development.

## Flags

| Flag | Short | Env | Default | Description |
|------|-------|-----|---------|-------------|
| `--viewer` | `-V` | `VIEWER=1` | off | Enable production viewer |
| `--debug-viewer` | | `DEBUG_VIEWER=1` | off | Enable debug viewer |
| `--viewer-title` | `-e` | `VIEWER_TITLE` | *(empty)* | Nav bar label |
| `--no-viewer-footer` | `-Z` | `VIEWER_FOOTER=0` | on | Hide footer |
| `--saved-channels` | `-E` | `SAVED_CHANNELS` | | File persistence for portal |

## Notes

- **Channel selector.** When `/api/info` returns multiple channels, the viewer
  shows a dropdown. Channel switch reloads via `?channel=id` query parameter.
- **Guide grid.** Each channel row shows its own program blocks from per-channel
  guide data. Icons appear next to channel names in guide labels.
- **Click-to-copy.** The `tltv://` URI uses `navigator.clipboard` with
  `document.execCommand('copy')` fallback for non-HTTPS contexts.
- **Timer management.** Portal mode uses `clearTimers()`/`startTimers()` to
  prevent accumulation of pollers on repeated tunes.
- **Token leak prevention.** Standalone mode never exposes upstream URLs (which
  may contain tokens) in browser-facing JSON responses.
