# TLTV CLI

Command-line tool for the [TLTV Federation Protocol](https://github.com/tltv-org/protocol).
Single static binary, zero dependencies.

## Install

```bash
curl -sSL https://raw.githubusercontent.com/tltv-org/cli/main/install.sh | sh
```

Or with Docker:

```bash
docker run --rm -v tltv-keys:/data -p 8000:8000 tltv bridge \
  --stream http://provider.example.com/channels.m3u
```

Or from source (`go install github.com/tltv-org/cli@latest`). See [docs/install.md](docs/install.md) for all options.

## Quick Start

```bash
# Generate a channel identity
tltv keygen

# Mine a vanity channel ID
tltv vanity cool

# Sign a metadata document
tltv template metadata > meta.json
tltv sign -k <channel-id>.key --auto-seq < meta.json > signed.json

# Bridge external streams as TLTV channels
tltv bridge --stream http://provider.com/channels.m3u --guide http://provider.com/guide.xml

# Relay channels from another node
tltv relay --node origin.example.com:443

# Generate a test signal
tltv server test --name "My Channel" -k channel.key

# Watch a channel
tltv viewer demo.timelooptv.org
```

## Commands

### Identity & Keys

| Command | Description |
|---|---|
| `keygen` | Generate Ed25519 keypair and channel ID |
| `vanity <pattern>` | Mine channel IDs matching a pattern (multi-threaded) |
| `inspect <id>` | Decode and validate a channel ID |

### Documents

| Command | Description |
|---|---|
| `sign` | Sign a JSON document with Ed25519 (`-k`, `--auto-seq`) |
| `verify` | Verify a signed document's signature and protocol version |
| `template` | Output a JSON template (`metadata`, `guide`, `migration`) |

### URIs

| Command | Description |
|---|---|
| `parse <uri>` | Parse a `tltv://` URI into components |
| `format <id>` | Build a `tltv://` URI from channel ID and hints |

### Network

| Command | Description |
|---|---|
| `resolve <uri>` | End-to-end URI resolution with migration chain following |
| `node <host>` | Fetch node info from `/.well-known/tltv` |
| `fetch <target>` | Fetch and verify channel metadata |
| `guide <target>` | Fetch and verify channel guide (`--xmltv` for XML output) |
| `peers <host>` | Fetch peer exchange list |
| `stream <target>` | Check stream availability (`--url` for bare URL output) |
| `crawl <host>` | BFS-crawl the gossip network (`--depth` for max depth) |

### Daemons

| Command | Description |
|---|---|
| `server test` | SMPTE color bar test signal generator ([docs](docs/server.md)) |
| `bridge` | Origin server for external streams ([docs](docs/bridge.md)) |
| `relay` | Caching relay with signature verification ([docs](docs/relay.md)) |
| `receiver` | Headless HLS consumer (`--monitor`, `--record`, `--pipe`) |
| `viewer` | Local web viewer with HLS.js player |
| `loadtest` | Multi-receiver load simulator |

### Operations

| Command | Description |
|---|---|
| `migrate` | Create a signed key migration document |
| `update` | Self-update to latest GitHub release |
| `completion` | Shell completions (`--install` for auto-install) |
| `version` | Version, protocol version, platform info |

## Global Flags

| Flag | Description |
|---|---|
| `--json` / `-j` | Machine-readable JSON output |
| `--no-color` / `-C` | Disable colored output (also respects `NO_COLOR`) |
| `--insecure` / `-I` | HTTP transport, skip TLS verification |
| `--local` / `-L` | Allow local/private address hints in `resolve` and `crawl` |

Global flags work before or after the subcommand: `tltv --json fetch ...` and `tltv fetch --json ...` are equivalent.

## Documentation

- `tltv <command> --help` — flag reference for any command
- [docs/server.md](docs/server.md) — test signal generator
- [docs/bridge.md](docs/bridge.md) — bridge setup and operation
- [docs/relay.md](docs/relay.md) — relay deployment
- [docs/config.md](docs/config.md) — config files, hot-reload, `--dump-config`
- [docs/peer-exchange.md](docs/peer-exchange.md) — peer exchange and gossip
- [docs/tls.md](docs/tls.md) — TLS, ACME, Let's Encrypt
- [docs/install.md](docs/install.md) — installation methods

## Protocol Conformance

Validated against all 7 test vector suites from the [protocol specification](https://github.com/tltv-org/protocol) (C1–C7: identity encoding, signing, documents, URIs, guides, invalid inputs, migration). 350 tests. Run `make test` to verify.

## Links

- [timelooptv.org](https://timelooptv.org) — Project homepage
- [Spec](https://spec.timelooptv.org) — Protocol specification
- [Demo](https://demo.timelooptv.org) — Live demo
- [GitHub](https://github.com/tltv-org) — All repositories

## License

MIT — see [LICENSE](LICENSE).
