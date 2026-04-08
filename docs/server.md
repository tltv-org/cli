# tltv server

Content origination subsystem. Generates live HLS video with signed TLTV
protocol endpoints — a fully self-contained origin server in a single binary.
Currently the only subcommand is `test`; future subcommands (`image`, `loop`)
will add other source types.

## tltv server test

Generates a live SMPTE EG 1-1990 color bar test pattern with TLTV branding,
channel name, wall clock (or uptime counter), and a continuous 1 kHz audio tone.
Pure Go H.264 Baseline encoder (I_16x16 + I_4x4 prediction, CAVLC), AAC-LC
audio (48 kHz mono), MPEG-TS muxer, and HLS segmenter — no ffmpeg required.

### Usage

```bash
# Minimal — ephemeral key, 640x360, port 8000
tltv server test

# Named channel with persistent key
tltv server test -k channel.key --name "TLTV Test"

# 1080p at 30fps with custom QP
tltv server test -k channel.key --name "My Channel" --width 1920 --height 1080 --qp 22

# With TLS (auto Let's Encrypt), viewer, and cache
tltv server test -k channel.key --name "Test" --hostname test.example.com \
  --tls --viewer --cache

# Docker
docker run -v keys:/data -p 8000:8000 \
  -e KEY=channel.key -e NAME="Docker Test" -e HOSTNAME=test.example.com \
  tltv server test
```

### Flags

#### Identity

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `-k`, `--key` | `KEY` | *(ephemeral)* | Ed25519 seed file (hex). Auto-generated if omitted. |

#### Display

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `-n`, `--name` | `NAME` | `TLTV` | Channel name shown on test screen |
| `--uptime` | `UPTIME=1` | off | Show elapsed time instead of wall clock |
| `--timezone` | `TIMEZONE` | UTC | IANA timezone for clock (e.g. `America/New_York`) |
| `--font-scale` | `FONT_SCALE` | 0 (auto) | Text scale factor. Auto: `height/120` for SD, `height/135` for HD+ |

#### Encoder

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--width` | `WIDTH` | 640 | Video width (16–7680). Non-16-aligned values rounded up internally. |
| `--height` | `HEIGHT` | 360 | Video height (16–4320). Non-16-aligned values rounded up internally. |
| `--fps` | `FPS` | 30 | Frames per second |
| `--qp` | `QP` | 26 | Quantization parameter (0–51). Lower = better quality, more bits. |

#### HLS

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--segment-duration` | `SEGMENT_DURATION` | 2 | Segment duration in seconds |
| `--segment-count` | `SEGMENT_COUNT` | 5 | Sliding window size (number of segments in playlist) |

#### Network

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `-l`, `--listen` | `LISTEN` | `:8000` | Listen address. Changes to `:443` when `--tls` is enabled. |
| `-H`, `--hostname` | `HOSTNAME` | *(auto)* | Public `host:port` for the `origins` field in metadata |

#### TLS

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--tls` | `TLS=1` | off | Enable TLS. Uses ACME (Let's Encrypt) if no cert/key provided. |
| `--tls-cert` | `TLS_CERT` | — | PEM certificate file (manual TLS) |
| `--tls-key` | `TLS_KEY` | — | PEM private key file (manual TLS) |
| `--tls-staging` | `TLS_STAGING=1` | off | Use Let's Encrypt staging directory |
| `--acme-email` | `ACME_EMAIL` | — | Email for ACME account (optional) |

ACME certs are stored in `./certs/` (override with `TLS_DIR`). Renewal runs
hourly in the background; certs renew when <30 days remain.

#### Cache

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--cache` | `CACHE=1` | off | Enable in-memory response cache with singleflight dedup |
| `--cache-max-entries` | `CACHE_MAX_ENTRIES` | 100 | Max cached items (LRU eviction) |
| `--cache-stats` | `CACHE_STATS` | 0 | Log cache stats every N seconds (0 = off) |

#### Peers

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `-P`, `--peers` | `PEERS` | — | Comma-separated `tltv://` URIs to advertise in peer exchange |
| `-g`, `--gossip` | `GOSSIP=1` | off | Include validated gossip-discovered channels in peers response |

#### Viewer

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--viewer` | `VIEWER=1` | off | Serve built-in HLS.js web player at `/` |

#### Logging

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--log-level` | `LOG_LEVEL` | `info` | `debug`, `info`, or `error` |
| `--log-format` | `LOG_FORMAT` | `human` | `human` (timestamped) or `json` (one object per line) |
| `--log-file` | `LOG_FILE` | stderr | Log to file instead of stderr |

#### Config

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--config` | `CONFIG` | — | JSON config file (fields map 1:1 to flags, underscores) |
| `--dump-config` | — | — | Print resolved config as JSON and exit |

### Config File

Config fields map 1:1 to CLI flags (underscores instead of dashes). CLI flags
override config values; config overrides env vars.

```json
{
  "key": "channel.key",
  "name": "My Test Channel",
  "width": 1920,
  "height": 1080,
  "fps": 30,
  "qp": 22,
  "listen": ":8000",
  "hostname": "test.example.com:443",
  "segment_duration": 2,
  "segment_count": 5,
  "tls": true,
  "viewer": true,
  "cache": true,
  "log_level": "info",
  "guide": {
    "entries": [
      {
        "title": "Test Signal",
        "start": "2026-01-01T00:00:00Z",
        "end": "2026-12-31T23:59:59Z"
      }
    ]
  }
}
```

The `guide` field accepts three forms:
- **Inline entries:** `{"entries": [...]}` — define a custom guide schedule
- **File reference:** `"guide.json"` — path to a guide JSON file
- **Omitted/null:** uses the default ephemeral guide (midnight-to-midnight UTC, auto-renewed)

### Notes

**Frame caching.** The display changes once per second (clock tick). At 30 fps
the encoder caches the NAL data and only re-renders when the time string
changes — a 30x reduction in encoding work.

**Config hot-reload.** The server watches the config file by mtime (no inotify,
30-second check interval). `name` and `guide` are reloadable without restart;
encoder settings (`width`, `height`, `fps`, `qp`), `key`, and `listen` require
a restart.

**Ephemeral guide.** When no guide is provided, the server generates a
midnight-to-midnight UTC guide entry automatically. It re-signs every 5 minutes
so the guide rolls over at midnight.

**Protocol endpoints.** The server exposes the full TLTV v1 protocol surface:
`/.well-known/tltv`, metadata, guide, stream (HLS), and peers. Documents are
re-signed every 5 minutes.

**No shortcut paths.** The server does NOT serve `/stream.m3u8` or `/seg0.ts`
convenience paths. All stream access goes through protocol paths
(`/tltv/v1/channels/{id}/stream.m3u8`).

**Docker timezone.** The binary embeds `time/tzdata` so `--timezone` works in
`scratch` containers without filesystem timezone data.

**HOSTNAME in Docker.** Docker sets `HOSTNAME` to the container ID by default.
Always set it explicitly (`-e HOSTNAME=public.example.com`) or the metadata
`origins` field will contain the container ID.

**Resolution rounding.** Non-16-aligned dimensions are rounded up internally
(H.264 macroblock alignment). The SPS includes frame cropping to report the
requested resolution to decoders.

**Audio.** 1 kHz AAC-LC tone (48 kHz, mono). Pre-encoded 48-frame ADTS loop
(~1 second, 8.5 KB) cycled for each segment. Audio PTS is continuous across
segment boundaries — no gaps.
