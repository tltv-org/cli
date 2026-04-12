# tltv bridge

The bridge takes external streaming sources — TLTV channels, HLS streams, M3U
playlists, JSON channel lists, or local directories — and publishes them as
first-class TLTV channels with Ed25519 identities, signed metadata, and full
protocol endpoints. Each source channel gets a persistent keypair (stored in
`--keys-dir`, or a single key via `--key`) and is served over the standard TLTV
protocol at `/.well-known/tltv`, `/tltv/v1/channels/{id}/metadata.json`, etc.

## Usage

```bash
# Single HLS stream (--name required)
tltv bridge --stream http://example.com/live.m3u8 --name "My Channel"

# M3U playlist (multi-channel, names from playlist)
tltv bridge --stream http://provider.com/channels.m3u --guide http://provider.com/guide.xml

# JSON channel list
tltv bridge --stream ./channels.json

# Local directory of .m3u8 files
tltv bridge --stream /media/hls

# With TLS and hostname
tltv bridge --stream http://source.tv/live.m3u8 --tls --hostname mychannel.tv

# Rebroadcast a TLTV channel (affiliate rebroadcast)
tltv bridge --stream "tltv://TVAlice@alice.tv" -k my.key --name "My Channel"

# Private TLTV source (token embedded in URI)
tltv bridge --stream "tltv://TVAlice@alice.tv?token=secret" -k my.key --name "My Channel"

# Tunarr integration
tltv bridge --stream http://tunarr:8000/api/channels.m3u \
            --guide http://tunarr:8000/api/xmltv.xml --on-demand
```

## Source Formats

The `--stream` source format is auto-detected from content:

| Format | Detection | Notes |
|---|---|---|
| **TLTV URI** | Starts with `tltv://` | Single-channel. Resolves via protocol: fetches metadata, verifies signatures, extracts stream path. Token from URI passed through. Automatic `relay_from` attribution. |
| **M3U playlist** | Has `#EXTINF:` but NOT `#EXT-X-TARGETDURATION`, `#EXT-X-MEDIA-SEQUENCE`, or `#EXT-X-STREAM-INF` | Multi-channel. Parses `tvg-id`, `tvg-name`, `tvg-logo`, `group-title` attributes. |
| **JSON** | Starts with `[` or `{` | Array of channel objects or single object. Fields: `id`, `name`, `stream`, `description`, `tags`, `language`, `logo`, `access`, `token`, `on_demand`. |
| **Directory** | `os.Stat` succeeds and is a directory | Scans for `*.m3u8` files. Optional `{name}.json` sidecar per channel (see below). |
| **HLS stream** | Fallback (none of the above) | Single-stream mode. `--name` is required. |

### Directory Sidecar Files

In directory mode, each `.m3u8` file can have a matching `.json` sidecar:

```
/media/hls/
  sports.m3u8
  sports.json       # optional sidecar
  news.m3u8
```

Sidecar schema:

```json
{
  "name": "Sports Channel",
  "description": "24/7 sports coverage",
  "tags": ["sports", "live"],
  "language": "en",
  "logo": "https://example.com/logo.png",
  "access": "token",
  "token": "secret123",
  "on_demand": false,
  "guide": [
    {"start": "2026-04-08T12:00:00Z", "end": "2026-04-08T13:00:00Z", "title": "Game Day"}
  ]
}
```

## Guide Sources

The `--guide` flag accepts XMLTV (XML) or JSON, auto-detected by first character
(`<` = XMLTV, `[` or `{` = JSON). Can be a URL or local file path.

| Source | Description |
|---|---|
| **XMLTV** | Standard XMLTV format. Timestamps converted to ISO 8601 UTC. Matched to channels by `channel` attribute. |
| **JSON** | Array of `{"channel", "start", "end", "title", "description", "category"}` objects. |
| **Inline config** | Config file `"guide"` field with `{"entries": [...]}` — no external file needed. |
| **Sidecar** | Per-channel `guide` array in directory-mode `.json` sidecars. |
| **None** | Default: ephemeral midnight-to-midnight UTC entry, re-signed every poll cycle. |

## Private Channels

Channels with `access: "token"` are private:

- **Hidden** from `/.well-known/tltv` channel list and `/tltv/v1/peers` response.
- **All endpoints** (metadata, guide, stream) require the token as a query parameter
  (`?token=...`) or via `tltv://` URI embedding.
- **HLS manifests** are rewritten to inject the token into segment and sub-resource
  URLs, so players that follow manifest links stay authenticated.

Set via JSON source (`"access": "token", "token": "secret"`), directory sidecar,
or M3U attribute. The token is never included in signed metadata — only the
`access` field is published.

## Flags

| Flag | Env Var | Default | Description |
|---|---|---|---|
| `-s`, `--stream` | `STREAM` | | Channel source: TLTV URI, HLS URL, M3U, JSON, or directory |
| `-G`, `--guide` | `GUIDE` | | Guide source: XMLTV or JSON |
| `-n`, `--name` | `NAME` | | Channel name (single-stream mode only) |
| `--description` | `DESCRIPTION` | | Channel description (CLI default, source overrides) |
| `--tags` | `TAGS` | | Comma-separated tags, max 5 (CLI default, source overrides) |
| `--language` | `LANGUAGE` | | ISO 639-1 language code (CLI default, source overrides) |
| `--timezone` | `TIMEZONE` | | IANA timezone name for metadata |
| `--icon` | `ICON` | *(TLTV logo)* | Icon file (PNG, JPEG, SVG). Default: built-in TLTV logo. |
| `--on-demand` | `ON_DEMAND=1` | off | Mark all channels as on-demand |
| `--poll` | `POLL` | `60s` | Source re-poll interval |
| `-l`, `--listen` | `LISTEN` | `:8000` | Listen address (`:443` with `--tls`) |
| `-k`, `--key` | `KEY` | | Key file for single-channel mode (overrides `--keys-dir`) |
| `-K`, `--keys-dir` | `KEYS_DIR` | `/data/keys` | Key storage directory |
| `-H`, `--hostname` | `HOSTNAME` | | Public `host:port` for origins field. Omit to create a private origin that relays cannot discover. |
| `-P`, `--peers` | `PEERS` | | `tltv://` URIs to advertise in peer exchange |
| `-g`, `--gossip` | `GOSSIP=1` | off | Re-advertise gossip-discovered channels |
| `--proxy` | `PROXY` | | Proxy URL (`socks5://`, `http://`, `https://`) for upstream connections |
| `--config` | `CONFIG` | | Config file path (JSON) |
| `--dump-config` | | | Print resolved config as JSON and exit |
| `--cache` | `CACHE=1` | off | Enable in-memory response cache |
| `--cache-max-entries` | `CACHE_MAX_ENTRIES` | `100` | Max cached items |
| `--cache-stats` | `CACHE_STATS` | `0` | Log cache stats every N seconds |
| `--viewer` | `VIEWER=1` | off | Serve built-in web player at `/` |
| `--tls` | `TLS=1` | off | Enable TLS (Let's Encrypt if no cert/key) |
| `--tls-cert` | `TLS_CERT` | | TLS certificate file (PEM) |
| `--tls-key` | `TLS_KEY` | | TLS private key file (PEM) |
| `--tls-staging` | `TLS_STAGING=1` | off | Use Let's Encrypt staging |
| `--acme-email` | `ACME_EMAIL` | | Email for ACME account |
| `--log-level` | `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `error` |
| `--log-format` | `LOG_FORMAT` | `human` | Log format: `human`, `json` |
| `--log-file` | `LOG_FILE` | stderr | Log to file instead of stderr |

Flags override env vars. Env vars override config file values.

## Config File

Optional JSON config via `--config`. Field names use underscores (flag names use
dashes). Only non-default values needed. Hot-reloaded fields: `stream`, `name`,
`guide` (checked every ~60s on each poll cycle).

```json
{
  "stream": "http://provider.com/channels.m3u",
  "guide": "http://provider.com/guide.xml",
  "hostname": "bridge.example.com",
  "on_demand": true,
  "poll": "120s",
  "cache": true,
  "viewer": true,
  "tls": true,
  "log_level": "debug"
}
```

The `guide` field is polymorphic — it accepts a file path string, a URL string,
or an inline object with `{"entries": [...]}` for embedded guide data.

## Docker

```yaml
services:
  bridge:
    image: git.plutoniumtech.com/tltv/cli:latest
    command: ["bridge"]
    ports:
      - "8000:8000"
    environment:
      STREAM: "http://tunarr:8000/api/channels.m3u"
      GUIDE: "http://tunarr:8000/api/xmltv.xml"
      HOSTNAME: "bridge.example.com:8000"
      ON_DEMAND: "1"
      CACHE: "1"
    volumes:
      - bridge-keys:/data/keys
    restart: unless-stopped

volumes:
  bridge-keys:
```

## Notes

- **Set `HOSTNAME` explicitly in Docker.** Docker sets `HOSTNAME` to the container
  ID by default, which would be published in the origins field of signed metadata.
- **Private origins.** Omit `--hostname` to run a private origin. Without a
  hostname, signed metadata contains no `origins` field, so relays and peers
  cannot discover the bridge's address. Viewers must be given the address directly.
- **Mount `/data/keys`** to persist channel keypairs across container restarts.
  Without a volume, channels get new identities on every restart.
- **Keys are per-upstream-ID.** The bridge maps each source channel's ID to a
  persistent Ed25519 keypair stored as `{id}.key` in the keys directory. The
  TLTV channel ID is derived from the public key and never changes.
- **Manifest rewriting** is line-based. URI attributes in `EXT-X-MAP`, `EXT-X-KEY`,
  `EXT-X-MEDIA`, `EXT-X-I-FRAME-STREAM-INF`, and `EXT-X-SESSION-KEY` tags are
  rewritten to relative URLs. Bare URI lines are also rewritten.
- **Path traversal protection** rejects `..` in stream sub-paths. Local file
  serving uses symlink resolution and prefix checks as defense-in-depth.
- **Cache** deduplicates upstream HTTP requests via inline singleflight. Manifests
  get 1s TTL (spec §9.10), segments get 3600s (immutable). Local file streams
  bypass the cache. Metadata and guide are served from the registry (already
  in-memory), not cached.
- **Node info** lists bridged channels in the `channels` array (not `relaying`).
  Private channels are excluded from node info and peer exchange.
- **Config hot-reload** re-reads the config file each poll cycle. Only `stream`,
  `name`, and `guide` are reloadable — changes to `listen`, `keys-dir`,
  `hostname`, or TLS settings require a restart.
- **TLTV source resolution.** When `--stream` is a `tltv://` URI, the bridge
  resolves it through the protocol stack: fetches `/.well-known/tltv`, verifies
  signed metadata, and extracts the stream path. The resolved HLS URL feeds into
  the existing bridge stream machinery. Re-resolution every poll cycle tracks
  upstream stream path changes.
- **Automatic `relay_from`.** When sourcing from a TLTV channel, the bridge
  automatically sets `relay_from` on the default ephemeral guide entry to the
  source channel ID. Explicit guide data (from `--guide`, config, or sidecar)
  is never modified — only the auto-generated default guide gets attribution.
- **`--key` for single-channel mode.** Use `-k`/`--key` to pin the bridge's
  identity to an explicit key file. Invalid when the source resolves to more
  than one channel. `-K`/`--keys-dir` remains for multi-channel sources.
