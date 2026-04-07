# TLTV CLI

Command-line tool for the [TLTV Federation Protocol](https://github.com/tltv-org/protocol). Generate channel identities, sign and verify documents, mine vanity IDs, probe nodes, crawl the gossip network, and inspect streams -- all from a single static binary with zero dependencies.

## Install

Pre-built static binaries are available for all platforms. No dependencies required.

### Linux / macOS

```bash
curl -sSL https://raw.githubusercontent.com/tltv-org/cli/main/install.sh | sh
```

### Windows

Download the latest `.zip` from the [releases page](https://github.com/tltv-org/cli/releases/latest) and add `tltv.exe` to your PATH.

### Docker

```bash
docker run --rm -v tltv-keys:/data -p 8000:8000 tltv bridge \
  --stream http://provider.example.com/channels.m3u

docker run --rm -v tltv-data:/data -p 8000:8000 tltv relay \
  --node origin.example.com:443
```

Or build locally: `docker build -t tltv .`

### From source

Requires [Go](https://go.dev/dl/) 1.22+:

```bash
go install github.com/tltv-org/cli@latest
```

Or clone and build:

```bash
git clone https://github.com/tltv-org/cli.git tltv-cli
cd tltv-cli
make build      # builds ./tltv
make install    # installs to $GOPATH/bin
```

## Quick Start

```bash
# Generate a channel identity
tltv keygen

# Mine a vanity channel ID
tltv vanity cool

# Inspect a channel ID
tltv inspect TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3

# Sign a metadata document
tltv template metadata > meta.json
# (edit meta.json with your channel ID)
tltv sign --key <channel-id>.key --auto-seq < meta.json > signed.json

# Verify a signed document
tltv verify signed.json

# Resolve a tltv:// URI end-to-end
tltv resolve "tltv://TVabc...@example.com:443"

# Probe a node
tltv node example.com

# Fetch and verify channel metadata (both formats work)
tltv fetch "tltv://TVabc...@example.com:443"
tltv fetch TVabc...@example.com:443

# Check if a stream is live
tltv stream "tltv://TVabc...@example.com:443"

# Discover channels across the network
tltv crawl example.com

# Bridge external streams as TLTV channels
tltv bridge --stream http://provider.com/channels.m3u --guide http://provider.com/guide.xml

# Relay channels from another TLTV node
tltv relay --node origin.example.com:443

# Install shell completions
tltv completion --install zsh
```

## Commands

### Identity & Keys

| Command | Description |
|---|---|
| `keygen` | Generate a new Ed25519 keypair and channel ID. Saves the seed as hex to `<channel-id>.key`. Use `--output -` (`-o`) to write seed to stdout. |
| `vanity <pattern>` | Mine channel IDs matching a pattern. Multi-threaded (uses all cores). Modes: `--mode prefix` (default, after TV), `contains`, `suffix`. Defaults to 1 match; use `--count` (`-n`) for more. Use `--output` (`-o`) to set output directory. |
| `inspect <id>` | Decode a channel ID. Shows the public key, validates format, warns if it's the RFC 8032 test key. |

### Documents

| Command | Description |
|---|---|
| `sign --key <file>` | Sign a JSON document with an Ed25519 seed. Reads from stdin or `--input` file. Use `--auto-seq` to set `seq` and `updated` to now. Short flags: `-k`, `-i`. |
| `verify [file]` | Verify a signed document's Ed25519 signature and protocol version. Reads from stdin or file. Auto-detects metadata vs. migration documents. Validates migration `to` field. Enforces 64 KB document size limit. |
| `template <type>` | Output a JSON template (`metadata`, `guide`, or `migration`) with current timestamps. |

### URIs

| Command | Description |
|---|---|
| `parse <uri>` | Parse a `tltv://` URI into its components: channel ID, peer hints, token. |
| `format <id>` | Build a `tltv://` URI. Accepts `--hint host:port` (repeatable) and `--token value`. Uses `@` syntax for the first hint. |

### Network

| Command | Description |
|---|---|
| `resolve <uri>` | Resolve a `tltv://` URI end-to-end: try hints, verify metadata, follow migration chains (max 5 hops), check stream. Filters local/private hints unless `--local`. |
| `node <host>` | Fetch `/.well-known/tltv` from a node. Shows protocol version, channels, and relaying info. |
| `fetch <uri\|id@host>` | Fetch channel metadata and verify its signature. Accepts `tltv://` URIs or `id@host`. Shows full stream/guide URLs. Exits non-zero on verification failure. |
| `guide <uri\|id@host>` | Fetch a channel guide and verify its signature. Marks the currently-airing entry. Use `--xmltv` for XMLTV XML output. Exits non-zero on verification failure. |
| `peers <host>` | Fetch the peer exchange list from a node. |
| `stream <uri\|id@host>` | Check HLS stream availability. Shows stream URL, segment count, target duration. Use `--url` to print only the stream URL for piping. |
| `crawl <host>` | BFS-crawl the gossip network starting from a host. Discovers channels across nodes via peer exchange. Use `--depth` (`-d`) to set max depth. |

### Server

| Command | Description |
|---|---|
| `server test` | Start a TLTV test signal generator. Generates a full SMPTE EG 1-1990 color bar pattern (3-row with PLUGE) with channel name, wall clock, and 1 kHz audio tone, entirely in pure Go -- no ffmpeg or external tools. Full TLTV protocol endpoints with signed metadata and guide. Configurable resolution, frame rate, QP, and HLS settings. Safe to run indefinitely -- PTS wraps correctly after 80+ hours. |
| `bridge` | Start a bridge origin server. Takes external streaming sources (HLS URLs, M3U playlists, JSON channel lists, directories of .m3u8 files) and publishes them as TLTV channels with Ed25519 identities and signed metadata. Supports private channels with token authentication, XMLTV guide output, and automatic re-polling. All flags also work as environment variables for Docker. |
| `relay` | Start a relay node. Re-serves existing TLTV channels from upstream nodes with full signature verification. Serves upstream-signed documents verbatim (preserves unknown fields). Built-in HLS cache with singleflight deduplication (`--cache`). Refuses private, on-demand, and retired channels per spec. Participates in peer exchange with validated gossip. Supports `--channels` (specific URIs), `--node` (relay all from a node), and `--config` (JSON config file). |
| `receiver <target>` | Headless HLS stream consumer. Connects to a TLTV channel, fetches segments, verifies metadata, and reports statistics. Modes: `--monitor` (health check, exit 0/1), `--record` (save to file), `--pipe` (raw TS to stdout), `--duration` (timed run). Tracks latency percentiles, cache hit rates, and bandwidth. |
| `loadtest <target>` | Multi-receiver load simulator. Spawns N concurrent receivers (`--receivers`/`-n`) with optional ramp-up (`--ramp`). Reports aggregate stats: segment/manifest latency percentiles, cache hit rates, bandwidth, error rates. |

### Operations

| Command | Description |
|---|---|
| `migrate` | Create a signed key migration document. Requires `-from-key` (old seed) and `-to` (new channel ID). |
| `update` | Update to the latest release from GitHub. Replaces the current binary in-place. |
| `completion <shell>` | Generate shell completions for bash, zsh, or fish. Use `--install` to write directly to the standard location. |
| `version` | Show version, protocol version, Go version, and platform. |

## Global Flags

| Flag | Description |
|---|---|
| `--json` | Machine-readable JSON output on all commands. |
| `--no-color` | Disable colored terminal output. Also respects `NO_COLOR` env var. |
| `--insecure` | Skip TLS certificate verification (for development). |
| `--local` | Allow local/private address hints in `resolve` and `crawl`. Without this flag, loopback, RFC 1918, link-local, and CGN addresses are skipped (per spec section 3.1). |

## Vanity Mining

Channel IDs always start with `TV`. Do not include the `TV` prefix in your pattern -- it is implied. The vanity miner brute-forces keypairs until it finds IDs matching your pattern.

```bash
# Find an ID starting with TVcoo... (prefix mode, default)
tltv vanity cool

# Find 5 matches
tltv vanity --count 5 cool

# Find IDs containing "moon" anywhere
tltv vanity --mode contains moon

# Case-insensitive
tltv vanity -i News

# Run indefinitely (0 = unlimited)
tltv vanity --count 0 art

# Save keys to a specific directory
tltv vanity --output ~/keys cool
```

Due to the encoding math, only certain characters are achievable at position 2 (right after TV). Uppercase letters and digits work well for prefix mode. Use `--mode contains` if prefix matching doesn't find results.

Each match saves a hex-encoded `.key` file (the channel's private seed). Use `--output` to choose the output directory.

### Difficulty estimates (prefix mode, single character after TV)

| Pattern length | ~Keys to check | Time (16 cores) |
|---|---|---|
| 1 char | ~30 | instant |
| 2 chars | ~1,700 | instant |
| 3 chars | ~100,000 | seconds |
| 4 chars | ~6,000,000 | minutes |
| 5 chars | ~350,000,000 | hours |

## Protocol Conformance

The implementation is validated against all 7 test vector suites from the [protocol specification](https://github.com/tltv-org/protocol):

- **C1** -- Channel ID encoding (RFC 8032 test keypair)
- **C2** -- Canonical JSON serialization and Ed25519 signatures
- **C3** -- Complete signed metadata document round-trip
- **C4** -- URI parsing (8 valid + 3 invalid cases)
- **C5** -- Signed guide document with entries
- **C6** -- Invalid input rejection (malformed IDs, tampered docs, truncated sigs)
- **C7** -- Key migration document signing and verification

Plus additional coverage: protocol version validation, migration identity binding, migration `to` field validation, future `updated`/`migrated` timestamp rejection, document size limits, timestamp format validation, local address detection, IPv6 hint parsing, XMLTV time conversion, JCS canonical JSON edge cases, SSRF hint validation, strict document field validation, trailing JSON rejection, `tltv://` URI target parsing, hex seed file round-trip with binary backward compatibility, stream URL construction. Optional tests against the public demo node (`demo.timelooptv.org`) exercise the full network stack end-to-end and skip gracefully when the demo is unreachable. Run `make test` to verify.

## Network Commands

Network commands default to HTTPS (port 443). For local development, `localhost` and `127.0.0.1` default to HTTP. Use `--insecure` to skip TLS verification for self-signed certificates.

The `resolve` and `crawl` commands use an SSRF-safe HTTP client that validates hints (rejecting URLs, userinfo, paths) and checks resolved DNS addresses against private/loopback/link-local ranges at connection time. Use `--local` to allow local addresses for development.

Commands that need both a channel ID and a host accept either a `tltv://` URI or the compact `id@host` format:

```bash
tltv fetch "tltv://TVMkVH...@example.com:443"   # tltv:// URI
tltv fetch TVMkVH...@example.com                 # compact format, HTTPS port 443
tltv fetch TVMkVH...@example.com:8443            # custom port
tltv fetch TVMkVH...@localhost:8000              # HTTP (auto-detected)
```

The `stream` command's `--url` flag outputs just the bare stream URL, making it composable with other tools:

```bash
# Extract a single frame from a live stream (requires ffmpeg)
ffmpeg -i "$(tltv stream --url <target>)" -vframes 1 frame.png

# Play a stream directly (requires mpv, vlc, or ffplay)
mpv "$(tltv stream --url <target>)"

# Record 30 seconds of a stream to a file
ffmpeg -i "$(tltv stream --url <target>)" -t 30 -c copy clip.ts
```

All network commands support `--json` for scripting:

```bash
tltv --json peers example.com | jq '.peers[].id'
tltv --json crawl example.com | jq '.channels | length'
```

## Server

`tltv server` is the content origination subsystem. Subcommands generate or serve media as TLTV channels with full protocol endpoints.

### Test Signal Generator

A self-contained test signal generator that produces live HLS video and audio entirely in pure Go -- no ffmpeg, no C libraries, no external dependencies. One command gives you a full TLTV channel with live video, a 1 kHz diagnostic audio tone, and signed protocol endpoints.

```bash
# Start with defaults (640x360 @ 30fps)
tltv server test --name "My Channel" -k channel.key

# HD resolution
tltv server test --name "TLTV Test" --width 1920 --height 1080 --fps 30

# Custom listen address and HLS settings
tltv server test --name "Test" --listen :9000 --hostname test.example.com:443

# Show local time instead of UTC
tltv server test --name "Test" --timezone America/New_York

# Adjust HLS segment timing
tltv server test --name "Test" --segment-duration 4 --segment-count 3

# Docker with environment variables
docker run -e NAME=TEST -e WIDTH=1280 -e HEIGHT=720 tltv server test
```

Generates a full SMPTE EG 1-1990 color bar test pattern (3-row: 75% bars, reverse castellations, PLUGE) with "TLTV" branding, channel name, and wall clock overlay. Text size auto-scales with resolution (overridable via `--font-scale`). Any resolution is accepted -- non-16-aligned dimensions are rounded up internally with SPS frame cropping.

The H.264 encoder uses adaptive I_16x16/I_4x4 prediction with CAVLC entropy coding -- Baseline profile, all-IDR frames. Frame caching re-encodes only when the clock tick changes (once per second), reducing CPU usage by 30× at 30fps. Level is auto-selected from resolution and frame rate.

The MPEG-TS muxer wraps encoded frames into 188-byte transport stream packets with PAT/PMT tables, PES headers, and PCR timestamps. The HLS segmenter maintains a configurable sliding-window playlist (default: 2-second segments, 5-segment window).

Full TLTV protocol endpoints are served: `/.well-known/tltv`, signed metadata, signed guide, HLS stream, and peers. Documents are re-signed every 5 minutes. If no `--key` is provided, an ephemeral key is generated. Use `--timezone` with an IANA timezone name (e.g. `America/New_York`) to display local time on the clock overlay. All flags also accept environment variables for Docker deployment.

All three long-running commands (server, bridge, relay) support structured logging: `--log-level` (debug/info/error), `--log-format` (human/json), `--log-file` (path). Environment variables: `LOG_LEVEL`, `LOG_FORMAT`, `LOG_FILE`.

## Bridge

The bridge is a long-running origin server that takes external streaming sources and publishes them as first-class TLTV channels with Ed25519 identities, signed metadata, and the full protocol HTTP API.

```bash
# Bridge a single HLS stream
tltv bridge --stream http://example.com/live.m3u8 --name "My Channel"

# Bridge an M3U playlist with XMLTV guide
tltv bridge --stream http://provider.com/channels.m3u --guide http://provider.com/guide.xml

# Bridge a directory of .m3u8 files (with optional sidecar .json metadata)
tltv bridge --stream /media/hls

# With on-demand channels and custom settings
tltv bridge --stream http://mediaserver:8000/api/channels.m3u \
  --guide http://mediaserver:8000/api/xmltv.xml --on-demand \
  --listen :8000 --hostname origin.example.com:443
```

Source formats are auto-detected: M3U playlists (with tvg-id/tvg-name attributes), JSON channel arrays, local directories with sidecar `.json` files, or single HLS streams. Guide data can be XMLTV or JSON.

All flags also work as environment variables for Docker: `STREAM`, `GUIDE`, `NAME`, `ON_DEMAND=1`, `POLL`, `LISTEN`, `KEYS_DIR`, `HOSTNAME`, `PEERS`, `LOG_LEVEL`, `LOG_FORMAT`, `LOG_FILE`.

Docker Compose example:
```yaml
services:
  bridge:
    image: tltv
    command: bridge
    ports: ["8000:8000"]
    volumes: [bridge-keys:/data]
    environment:
      STREAM: http://mediaserver:8000/api/channels.m3u
      GUIDE: http://mediaserver:8000/api/xmltv.xml
      HOSTNAME: bridge.example.com:443
volumes:
  bridge-keys:
```

Mount `/data` to persist channel keys across restarts. Set `HOSTNAME` explicitly -- Docker's default `HOSTNAME` is the container ID.

Private channels (`access: "token"`) are supported: hidden from node info and peers, token required on all endpoints, token injected into every URI in the HLS playlist graph.

## Relay

The relay is a long-running node that re-serves existing TLTV channels from upstream nodes with full signature verification. It does not have channel private keys and cannot modify signed documents.

```bash
# Relay specific channels
tltv relay --channels "tltv://TVabc...@origin.example.com:443"

# Relay all public channels from a node
tltv relay --node origin.example.com:443

# Relay with a config file
tltv relay --config relay.json --hostname relay.example.com:443
```

The relay verifies every metadata and guide document against the channel's Ed25519 public key before caching. Documents are served verbatim (raw bytes preserved, unknown fields intact). Private, on-demand, and retired channels are refused per spec. If a channel transitions to any of these states, the relay stops immediately.

### HLS Cache

Enable `--cache` for in-memory caching with singleflight deduplication. When 500 viewers request the same segment simultaneously, the relay makes one upstream fetch -- the other 499 get the cached result. This is the core scaling mechanism.

```bash
# Caching relay with stats logging
tltv relay --node origin.example.com:443 --cache --cache-stats 30

# Docker
docker run -e NODE=origin.example.com:443 -e CACHE=1 tltv relay
```

TTLs follow the protocol spec (§9.10): 1 second for manifests, 3600 seconds for segments (immutable once created). The relay ignores upstream HTTP cache headers and applies protocol-recommended TTLs. Responses include a `Cache-Status` header (RFC 9211) reporting `HIT` or `MISS`.

Cache flags: `--cache` (`CACHE=1`), `--cache-max-entries` (`CACHE_MAX_ENTRIES`, default 100), `--cache-stats N` (`CACHE_STATS`, log stats every N seconds).

Migration chains are followed automatically (up to 5 hops). Peer exchange participates in gossip with validated entries, 7-day staleness cutoff, and 100-entry limit.

Config file format:
```json
{
  "channels": ["tltv://TVabc...@origin.example.com:443"],
  "nodes": ["origin.example.com:443"]
}
```

Environment variables: `CHANNELS`, `NODE`, `CONFIG`, `LISTEN`, `HOSTNAME`, `PEERS`, `CACHE=1`, `CACHE_MAX_ENTRIES`, `CACHE_STATS`, `META_POLL`, `GUIDE_POLL`, `PEER_POLL`, `MAX_PEERS`, `STALE_DAYS`, `LOG_LEVEL`, `LOG_FORMAT`, `LOG_FILE`.

Docker Compose example:
```yaml
services:
  relay:
    image: tltv
    command: relay
    ports: ["8000:8000"]
    volumes: [relay-data:/data]
    environment:
      NODE: origin.example.com:443
      HOSTNAME: relay.example.com:443
volumes:
  relay-data:
```

## Receiver

The receiver is a headless HLS stream consumer. It connects to a TLTV channel, fetches segments, verifies protocol compliance, and reports statistics. Used directly as a monitoring/recording tool, and internally by `loadtest`.

```bash
# Watch a channel -- prints live stats, Ctrl-C to stop
tltv receiver tltv://TVabc...@demo.timelooptv.org:443

# Health check -- exit 0 if stream is live, 1 if not
tltv receiver --monitor --timeout 10s tltv://TVabc...@demo.timelooptv.org:443

# Record to file
tltv receiver --record out.ts tltv://TVabc...@demo.timelooptv.org:443

# Pipe to a player
tltv receiver --pipe tltv://TVabc...@demo.timelooptv.org:443 | mpv -

# Timed run with JSON stats
tltv --json receiver --duration 5m tltv://TVabc...@demo.timelooptv.org:443
```

Tracks segment latency (p50/p95/p99), cache hit rates (`Cache-Status` header from relays), bandwidth, and error rates. Retries failed fetches with exponential backoff. Periodically re-verifies metadata signatures. `--pipe` and `--record` are mutually exclusive. `--pipe` and `--json` are mutually exclusive.

Environment variables: `MONITOR=1`, `TIMEOUT`, `DURATION`, `RECORD`, `PIPE=1`, `URL`, `LOG_LEVEL`, `LOG_FORMAT`, `LOG_FILE`.

## Load Testing

Spawns N concurrent receivers against a target to simulate viewer load.

```bash
# 200 viewers for 5 minutes
tltv loadtest -n 200 -d 5m tltv://TVabc...@demo.timelooptv.org:443

# Ramp up gradually
tltv loadtest -n 500 --ramp 60s -d 10m TVabc...@localhost:8000

# Target a direct HLS URL
tltv loadtest -n 100 -d 5m --url https://demo.timelooptv.org/tltv/v1/channels/TVabc.../stream.m3u8

# JSON output for scripting
tltv --json loadtest -n 50 -d 2m TVabc...@localhost:8000
```

Reports aggregate statistics: manifest/segment latency percentiles, cache hit rates, total bandwidth, and error rates. Validates the target before spawning receivers. Live progress output every 5 seconds.

Environment variables: `RECEIVERS`, `DURATION`, `RAMP`, `URL`, `CONNECT_TIMEOUT`, `LOG_LEVEL`, `LOG_FORMAT`, `LOG_FILE`.

## Project Structure

```
main.go             Entry point, command dispatch, simple commands
base58.go           Base58 encode/decode (Bitcoin alphabet)
identity.go         Channel ID: make, parse, validate
signing.go          JCS canonical JSON (RFC 8785), Ed25519 sign/verify
uri.go              tltv:// URI parse and format
client.go           HTTP client, SSRF-safe client, hint validation
network.go          Network commands (node, fetch, guide, peers, stream, crawl)
vanity.go           Multi-threaded vanity channel ID miner
output.go           Terminal output formatting and colors
signal.go           OS signal handling
logging.go          Structured logging: levels, human + JSON format, file output
server.go           Server entry point, flags, key management, frame loop, shutdown
server_h264.go      H.264 Baseline encoder (I_16x16 + I_4x4, ~2400 lines, pure Go)
server_audio.go     1 kHz AAC-LC tone generator (pre-encoded ADTS frame, 48kHz mono)
server_mpegts.go    MPEG-TS muxer (188-byte packets, PAT/PMT, PES, PCR, video + audio)
server_hls.go       HLS segmenter (ring buffer, sliding window m3u8)
server_pattern.go   SMPTE EG 1-1990 bars (3-row + PLUGE), 8x8 bitmap font, frame rendering
server_serve.go     HTTP handlers (TLTV protocol endpoints)
bridge*.go          Bridge origin server (source parsing, identity, HLS rewriting, HTTP)
relay*.go           Relay node (upstream fetch+verify, HLS cache, singleflight, gossip, HTTP)
receiver.go         HLS receiver (manifest parser, segment tracking, stats, retry)
loadtest.go         Multi-receiver load simulator (ramp-up, aggregate stats, percentiles)
main_test.go        82 tests against all protocol test vectors + edge cases
bridge_test.go      73 bridge tests (source parsing, manifest rewriting, endpoints)
relay_test.go       37 relay tests (fetch+verify, access checks, migration, endpoints)
relay_cache_test.go 17 cache tests (hit/miss, TTL, singleflight, eviction, error non-caching)
receiver_test.go    21 receiver tests (HLS parser, segment resolution, stats, live stream)
server_gen_test.go  15 tests (raw H.264, solid gray, multi-resolution, text overlay, I_4x4, font specimen, audio tone, PMT, audio muxing)
Makefile            Build, test, install, cross-compile (CGO_ENABLED=0)
Dockerfile          Multi-stage: golang:1.22-alpine -> scratch (~10 MB)
```

Zero external dependencies. Everything uses the Go standard library (`crypto/ed25519`, `encoding/json`, `net/http`, `math/big`). 234 tests.

## Links

- [timelooptv.org](https://timelooptv.org) — Project homepage
- [Spec](https://spec.timelooptv.org) — Protocol specification
- [Demo](https://demo.timelooptv.org) — Live demo
- [GitHub](https://github.com/tltv-org) — All repositories

## License

MIT -- see [LICENSE](LICENSE).
