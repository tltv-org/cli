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
| `bridge` | Start a bridge origin server. Takes external streaming sources (HLS URLs, M3U playlists, JSON channel lists, directories of .m3u8 files) and publishes them as TLTV channels with Ed25519 identities and signed metadata. Supports private channels with token authentication, XMLTV guide output, and automatic re-polling. All flags also work as environment variables for Docker. |
| `relay` | Start a relay node. Re-serves existing TLTV channels from upstream nodes with full signature verification. Serves upstream-signed documents verbatim (preserves unknown fields). Refuses private, on-demand, and retired channels per spec. Participates in peer exchange with validated gossip. Supports `--channels` (specific URIs), `--node` (relay all from a node), and `--config` (JSON config file). |

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

Plus additional coverage: protocol version validation, migration identity binding, migration `to` field validation, future `updated`/`migrated` timestamp rejection, document size limits, timestamp format validation, local address detection, IPv6 hint parsing, XMLTV time conversion, JCS canonical JSON edge cases, SSRF hint validation, strict document field validation, trailing JSON rejection, `tltv://` URI target parsing, hex seed file round-trip with binary backward compatibility, stream URL construction. Optional tests against the public demo node (`demo.timelooptv.org`) exercise the full network stack end-to-end and skip gracefully when the demo is unreachable. Run `make test` to verify (75 tests).

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

All flags also work as environment variables for Docker: `STREAM`, `GUIDE`, `NAME`, `ON_DEMAND=1`, `POLL`, `LISTEN`, `KEYS_DIR`, `HOSTNAME`, `PEERS`.

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

Migration chains are followed automatically (up to 5 hops). Peer exchange participates in gossip with validated entries, 7-day staleness cutoff, and 100-entry limit.

Config file format:
```json
{
  "channels": ["tltv://TVabc...@origin.example.com:443"],
  "nodes": ["origin.example.com:443"]
}
```

Environment variables: `CHANNELS`, `NODE`, `CONFIG`, `LISTEN`, `HOSTNAME`, `PEERS`, `META_POLL`, `GUIDE_POLL`, `PEER_POLL`.

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
bridge*.go          Bridge origin server (source parsing, identity, HLS rewriting, HTTP)
relay*.go           Relay node (upstream fetch+verify, caching, gossip, HTTP)
main_test.go        82 tests against all protocol test vectors + edge cases
bridge_test.go      73 bridge tests (source parsing, manifest rewriting, endpoints)
relay_test.go       37 relay tests (fetch+verify, access checks, migration, endpoints)
Makefile            Build, test, install, cross-compile (CGO_ENABLED=0)
Dockerfile          Multi-stage: golang:1.22-alpine -> scratch (~10 MB)
```

Zero external dependencies. Everything uses the Go standard library (`crypto/ed25519`, `encoding/json`, `net/http`, `math/big`). 192 tests.

## License

MIT -- see [LICENSE](LICENSE).
