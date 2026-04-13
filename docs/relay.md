# tltv relay

The relay re-serves existing TLTV channels from upstream origins, acting as a
caching proxy with full cryptographic verification. Every document (metadata,
guide, stream segments) is fetched from the origin, verified against the
channel's public key, and served verbatim to downstream viewers. The relay
never re-signs or re-serializes documents — it stores and serves the exact
upstream bytes, preserving unknown fields and avoiding signature invalidation.

## Usage

```bash
# Relay specific channels by URI
tltv relay --channels "tltv://TVabc123@origin.example.com:443"

# Relay all public channels from a node
tltv relay --node origin.example.com:443

# Both together — explicit channels plus discovery
tltv relay --channels "tltv://TVabc@a.example.com" --node b.example.com:443

# With caching, TLS, and a viewer
tltv relay --node origin:443 --cache --tls --hostname relay.example.com --viewer
```

## Channel Selection

Channels to relay are specified via `--channels`, `--node`, or both.

**`--channels`** takes comma-separated `tltv://` URIs or `id@host:port` pairs.
Each channel is fetched individually from the hint in the URI. Use this when
you want to relay specific channels from known origins.

**`--node`** takes comma-separated `host:port` values. The relay fetches
`/.well-known/tltv` from each node, discovers all listed channels, and relays
every public channel it finds. Channels are re-discovered on each metadata poll
cycle, so new channels added to the origin appear automatically.

When both are provided, the relay serves the union. Duplicate channels (same
channel ID from both sources) are deduplicated.

## Verification

The relay verifies every document it fetches:

- **Metadata**: Ed25519 signature checked against the channel's public key
  (derived from the channel ID). Protocol version, timestamps, and identity
  binding are all validated.
- **Guide**: Same signature verification. Timestamp format and ranges checked.
- **Migration chains**: If a channel has migrated, the relay follows the chain
  automatically — up to 5 hops with loop detection. Each hop is independently
  verified. Broken chains (fetch failure, bad signature, missing `to`, loops,
  exceeded hops) cause the channel to be dropped.

**Raw byte serving.** Verified documents are stored as exact upstream bytes and
served verbatim. The relay does no JSON parsing, re-serialization, or field
modification on responses. This preserves any unknown fields the origin includes
(a spec requirement) and guarantees downstream clients see byte-identical
signatures.

## Access Enforcement

Per spec section 10.2, the relay refuses to serve channels with restricted
access. On every metadata poll, the relay checks:

| Condition | Action |
|---|---|
| `access = "token"` | Refuse — private channels require origin-issued tokens |
| `on_demand = true` | Refuse — on-demand channels are origin-only |
| `status = "retired"` | Refuse — retired channels should not be propagated |

If a previously-public channel transitions to any of these states, the relay
stops serving it **immediately** on the next metadata poll cycle. No manual
intervention required.

## HLS Cache

Enable with `--cache` (or `CACHE=1`). The in-memory cache deduplicates
upstream fetches and reduces origin load.

**Singleflight deduplication.** When N viewers request the same segment
simultaneously, the cache collapses them into a single upstream fetch. The
first request triggers the fetch; all others block and receive the same result.
This is the primary scaling benefit — 500 concurrent viewers generate 1
upstream request per segment, not 500.

**Protocol-compliant TTLs.** The cache ignores upstream HTTP cache headers and
applies TTLs from the spec:

- **Manifests** (`.m3u8`) and **protocol documents** (`.json`, `.xml`): **1 second** (spec §9.10)
- **Segments** (`.ts`, `.mp4`, `.m4s`): **3600 seconds** (immutable by sequence number)

**`Cache-Status` header.** Every cached response includes a `Cache-Status`
header per RFC 9211, reporting `HIT` or `MISS`. Useful for monitoring cache
effectiveness.

**Eviction.** LRU eviction at `--cache-max-entries` (default 100). Maximum
item size: 50 MB. Only HTTP 200 responses are cached.

## Flags

| Flag | Env Var | Default | Description |
|---|---|---|---|
| `--channels` | `CHANNELS` | | `tltv://` URIs or `id@host`, comma-separated |
| `--node` | `NODE` | | Relay all public channels from node(s), comma-separated |
| `--listen`, `-l` | `LISTEN` | `:8000` | Listen address (`:443` when TLS enabled) |
| `--hostname`, `-H` | `HOSTNAME` | | Public `host:port` for peer exchange |
| `--meta-poll` | `META_POLL` | `60s` | Metadata poll interval |
| `--guide-poll` | `GUIDE_POLL` | `15m` | Guide poll interval |
| `--peer-poll` | `PEER_POLL` | `30m` | Peer exchange poll interval |
| `--max-peers` | `MAX_PEERS` | `100` | Maximum peers tracked in exchange |
| `--stale-days` | `STALE_DAYS` | `7` | Drop peers not seen in N days |
| `--cache` | `CACHE=1` | off | Enable in-memory HLS cache |
| `--cache-max-entries` | `CACHE_MAX_ENTRIES` | `100` | Max cached items before LRU eviction |
| `--cache-stats` | `CACHE_STATS` | | Log cache stats at this interval (e.g. `30s`) |
| `--viewer` | `VIEWER=1` | off | Embed production web viewer on `GET /` |
| `--debug-viewer` | `DEBUG_VIEWER=1` | off | Embed debug viewer on `GET /` (mutually exclusive with `--viewer`) |
| `--viewer-title` | `VIEWER_TITLE` | | Nav bar label text |
| `--no-viewer-footer` | `VIEWER_FOOTER=0` | on | Hide footer links |
| `--peers`, `-P` | `PEERS` | | `tltv://` URIs to advertise in peer exchange |
| `--gossip`, `-g` | `GOSSIP=1` | off | Include validated gossip peers in responses |
| `--proxy` | `PROXY` | | Proxy URL (`socks5://`, `http://`, `https://`) for upstream connections |
| `--tls` | `TLS=1` | off | Enable automatic Let's Encrypt TLS |
| `--tls-cert` | `TLS_CERT` | | Path to manual TLS certificate |
| `--tls-key` | `TLS_KEY` | | Path to manual TLS private key |
| `--tls-staging` | `TLS_STAGING=1` | off | Use Let's Encrypt staging directory |
| `--acme-email` | `ACME_EMAIL` | | Email for ACME account registration |
| `--log-level` | `LOG_LEVEL` | `info` | `debug`, `info`, or `error` |
| `--log-format` | `LOG_FORMAT` | `human` | `human` or `json` |
| `--log-file` | `LOG_FILE` | stderr | Path to log output file |
| `--config` | `CONFIG` | | Path to config file (JSON) |
| `--dump-config` | | off | Print resolved config as JSON and exit |

Global flags `--insecure`/`-I` and `--local`/`-L` also apply (HTTP transport
and local address allowance).

## Config File

The `--config` flag accepts a JSON file. Fields map 1:1 to CLI flags
(underscores in config, dashes in flags). CLI flags override config values.

```json
{
  "channels": "tltv://TVabc@origin.example.com:443",
  "node": "origin2.example.com:443",
  "hostname": "relay.example.com",
  "listen": ":443",
  "meta_poll": "60s",
  "guide_poll": "15m",
  "peer_poll": "30m",
  "max_peers": 100,
  "stale_days": 7,
  "cache": true,
  "cache_max_entries": 200,
  "tls": true,
  "acme_email": "ops@example.com",
  "gossip": true,
  "log_level": "info",
  "log_format": "json",
  "log_file": "/var/log/tltv-relay.log"
}
```

**Hot-reload.** The config file is checked every 30 seconds. Reloadable fields:
`channels`, `node` (the relay re-discovers targets, adds new channels, removes
old ones dynamically, and refreshes hints for existing channel IDs). Non-
reloadable fields (listen, hostname, TLS settings) require a restart.

## Docker

```bash
docker run -d --name tltv-relay \
  -e NODE=origin.example.com:443 \
  -e HOSTNAME=relay.example.com \
  -e CACHE=1 \
  -e GOSSIP=1 \
  -e LOG_FORMAT=json \
  -p 8000:8000 \
  -v relay-data:/data \
  tltv relay
```

Set `HOSTNAME` explicitly — Docker defaults it to the container ID, which
would be advertised as the relay's public hostname in peer exchange. Add
`-e TLS=1 -e ACME_EMAIL=... -p 443:443 -p 80:80` for automatic TLS.

## Notes

- **Node info.** Relayed channels appear in the `relaying` array (not
  `channels`) in the relay's `/.well-known/tltv` response.
- **Gossip validation.** Per spec section 11.5, before adding a
  gossip-discovered peer the relay: (1) fetches `/.well-known/tltv` from the
  peer's hint, (2) verifies the channel is listed, (3) fetches and verifies
  the channel's signed metadata. Only then is the peer added to the exchange.
  Stale entries (older than `--stale-days`) are pruned automatically.
- **`--insecure` for Docker internals.** Use `tltv --insecure relay` when the
  relay connects to an origin over plain HTTP inside a Docker network (e.g.
  `--node origin:8000`). This switches the default transport to HTTP and the
  default port to 80.
- **Gossip gating.** The `--gossip` flag only controls whether gossip-discovered
  peers appear in the `/tltv/v1/peers` response. The relay always polls peers
  internally for fallback upstream discovery regardless of this flag.
- **No direct stream paths.** All stream access goes through protocol paths
  (`/tltv/v1/channels/{id}/...`). There are no shortcut URLs.
