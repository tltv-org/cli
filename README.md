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
main_test.go        75 tests against all protocol test vectors + edge cases
Makefile            Build, test, install, cross-compile (CGO_ENABLED=0)
```

Zero external dependencies. Everything uses the Go standard library (`crypto/ed25519`, `encoding/json`, `net/http`, `math/big`).

## License

MIT -- see [LICENSE](LICENSE).
