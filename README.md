# TLTV CLI

Command-line tool for the [TLTV Federation Protocol](https://github.com/tltv-org/protocol). Generate channel identities, sign and verify documents, mine vanity IDs, probe nodes, crawl the gossip network, and inspect streams -- all from a single static binary with zero dependencies.

## Install

### From source

```bash
go install github.com/tltv-org/cli@latest
```

### Pre-built binaries

Download from the [releases page](https://github.com/tltv-org/cli/releases). Available for Linux and macOS (amd64 + arm64) and Windows (amd64).

### Build from source

```bash
git clone https://github.com/tltv-org/cli.git tltv-cli
cd tltv-cli
make build      # builds ./tltv
make install    # installs to $GOPATH/bin
make release    # cross-compiles all platforms to dist/
```

## Quick Start

```bash
# Generate a channel identity
tltv keygen

# Mine a vanity channel ID
tltv vanity -count 1 cool

# Inspect a channel ID
tltv inspect TVMkVHiXF9W1NgM9KLgs7tcBMvC1YtF4Daj4yfTrJercs3

# Sign a metadata document
tltv template metadata > meta.json
# (edit meta.json with your channel ID)
tltv sign -key <channel-id>.key -auto-seq < meta.json > signed.json

# Verify a signed document
tltv verify signed.json

# Resolve a tltv:// URI end-to-end
tltv resolve "tltv://TVabc...@example.com:443"

# Probe a node
tltv node example.com

# Fetch and verify channel metadata
tltv fetch TVabc...@example.com:443

# Check if a stream is live
tltv stream TVabc...@example.com:443

# Discover channels across the network
tltv crawl example.com
```

## Commands

### Identity & Keys

| Command | Description |
|---|---|
| `keygen` | Generate a new Ed25519 keypair and channel ID. Saves the 32-byte seed to `<channel-id>.key`. |
| `vanity <pattern>` | Mine channel IDs matching a pattern. Multi-threaded (uses all cores). Modes: `prefix` (default, after TV), `contains`, `suffix`. |
| `inspect <id>` | Decode a channel ID. Shows the public key, validates format, warns if it's the RFC 8032 test key. |

### Documents

| Command | Description |
|---|---|
| `sign -key <file>` | Sign a JSON document with an Ed25519 seed. Reads from stdin or `-in` file. Use `-auto-seq` to set `seq` and `updated` to now. |
| `verify [file]` | Verify a signed document's Ed25519 signature and protocol version. Reads from stdin or file. Auto-detects metadata vs. migration documents. Validates migration `to` field. |
| `template <type>` | Output a JSON template (`metadata`, `guide`, or `migration`) with current timestamps. |

### URIs

| Command | Description |
|---|---|
| `parse <uri>` | Parse a `tltv://` URI into its components: channel ID, peer hints, token. |
| `format <id>` | Build a `tltv://` URI. Accepts `--hint host:port` (repeatable) and `--token value`. Uses `@` syntax for the first hint. |

### Network

| Command | Description |
|---|---|
| `resolve <uri>` | Resolve a `tltv://` URI end-to-end: try hints, verify metadata, check stream. |
| `node <host>` | Fetch `/.well-known/tltv` from a node. Shows protocol version, channels, and relaying info. |
| `fetch <id@host>` | Fetch channel metadata and verify its signature. Handles migration documents. Exits non-zero on verification failure. |
| `guide <id@host>` | Fetch a channel guide and verify its signature. Displays entries in a table. Exits non-zero on verification failure. |
| `peers <host>` | Fetch the peer exchange list from a node. |
| `stream <id@host>` | Check HLS stream availability. Parses the manifest and reports segment count, target duration. |
| `crawl <host>` | BFS-crawl the gossip network starting from a host. Discovers channels across nodes via peer exchange. |

### Operations

| Command | Description |
|---|---|
| `migrate` | Create a signed key migration document. Requires `-from-key` (old seed) and `-to` (new channel ID). |
| `completion <shell>` | Generate shell completions for bash, zsh, or fish. |
| `version` | Show version, protocol version, Go version, and platform. |

## Global Flags

| Flag | Description |
|---|---|
| `--json` | Machine-readable JSON output on all commands. |
| `--no-color` | Disable colored terminal output. Also respects `NO_COLOR` env var. |
| `--insecure` | Skip TLS certificate verification (for development). |

## Vanity Mining

Channel IDs always start with `TV`. The remaining 44 characters are determined by the Ed25519 public key. The vanity miner brute-forces keypairs until it finds IDs matching your pattern.

```bash
# Find IDs starting with TVcoo... (prefix mode, default)
tltv vanity cool

# Find IDs containing "moon" anywhere
tltv vanity --mode contains moon

# Case-insensitive, stop after 5 matches
tltv vanity -i -count 5 News

# Use fewer threads
tltv vanity -threads 4 art
```

Due to the encoding math, only certain characters are achievable at position 2 (right after TV). Uppercase letters and digits work well for prefix mode. Use `--mode contains` if prefix matching doesn't find results.

Matched keys are saved to `<channel-id>.key` in the current directory.

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

Plus additional coverage: protocol version validation, migration identity binding, migration `to` field validation, future `updated`/`migrated` timestamp rejection. Run `make test` to verify (32 tests).

## Network Commands

Network commands default to HTTPS (port 443). For local development, `localhost` and `127.0.0.1` default to HTTP. Use `--insecure` to skip TLS verification for self-signed certificates.

The `<id@host>` format is used for commands that need both a channel ID and a host:

```bash
tltv fetch TVMkVH...@example.com           # HTTPS, port 443
tltv fetch TVMkVH...@example.com:8443      # HTTPS, custom port
tltv fetch TVMkVH...@localhost:8000        # HTTP (auto-detected)
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
signing.go          Canonical JSON (RFC 8785), Ed25519 sign/verify
uri.go              tltv:// URI parse and format
client.go           HTTP client for TLTV protocol endpoints
network.go          Network commands (node, fetch, guide, peers, stream, crawl)
vanity.go           Multi-threaded vanity channel ID miner
output.go           Terminal output formatting and colors
signal.go           OS signal handling
main_test.go        Tests against all protocol test vectors
Makefile            Build, test, install, cross-compile
```

Zero external dependencies. Everything uses the Go standard library (`crypto/ed25519`, `encoding/json`, `net/http`, `math/big`).

## License

MIT -- see [LICENSE](LICENSE).
