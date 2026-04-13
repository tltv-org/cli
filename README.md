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

# Rebroadcast another TLTV channel under your own identity
tltv bridge --stream "tltv://TVAlice@alice.tv" -k my.key --name "My Channel"

# Relay channels from another node
tltv relay --node origin.example.com:443

# Mirror an origin (same key, auto-failover)
tltv mirror --source "tltv://TVabc@primary.tv" --key channel.key --hostname mirror.tv --buffer 2h

# Generate a test signal
tltv server test --name "My Channel" -k channel.key

# Watch a channel
tltv viewer demo.timelooptv.org
```

## Commands

### Network

| Command | Description |
|---|---|
| `info <target>` | Show all info about a target (`--watch` for auto-refresh) |
| `channel <target>` | Fetch and verify channel metadata |
| `stream <target>` | Check stream status and manifest info (`--url` for bare URL) |
| `guide <target>` | Fetch and verify channel guide (`--xmltv` for XML output) |
| `node <host>` | Query node identity from `/.well-known/tltv` |
| `peers <host>` | List peers from a node |

### Discovery

| Command | Description |
|---|---|
| `resolve <uri>` | End-to-end URI resolution with migration chain following |
| `crawl <host>` | BFS-crawl the gossip network (`--depth` for max depth) |

### Servers

| Command | Description |
|---|---|
| `server test` | Test signal generator (`--channels N`, `--variants 1080p,720p`) ([docs](docs/server.md)) |
| `bridge` | Bridge origin server ([docs](docs/bridge.md)) |
| `relay` | Caching relay with signature verification ([docs](docs/relay.md)) |
| `mirror` | Mirror origin — same-key replication with auto-promotion ([docs](docs/mirror.md)) |
| `router` | SNI routing reverse proxy with built-in ACME TLS ([docs](docs/router.md)) |

> **Private origins:** Run a server or bridge without `--hostname` to create a
> private origin. Without a hostname, signed metadata contains no `origins` field,
> so relays cannot discover the server's address.
>
> **Private embedded viewer:** On a private `server test` origin with `--viewer`,
> open `/?token=...`. The embedded viewer routes are token-gated and `/api/info`
> does not echo the secret back to the browser.

### Clients

| Command | Description |
|---|---|
| `viewer <target>` | Web viewer with HLS.js player (`--viewer` production, `--debug-viewer` diagnostic) |
| `receiver <target>` | Headless HLS consumer (`--monitor`, `--record`, `--pipe`, `--quality`) |

### Identity

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
| `migrate` | Create a signed key migration document |

### URIs

| Command | Description |
|---|---|
| `parse <uri>` | Parse a `tltv://` URI into components |
| `format <id>` | Build a `tltv://` URI from channel ID and hints |

### Tools

| Command | Description |
|---|---|
| `loadtest <target>` | Multi-receiver load simulator |
| `version` | Version, protocol version, platform info |
| `update` | Self-update to latest GitHub release |
| `completion` | Shell completions (`--install` for auto-install) |

## Global Flags

| Flag | Description |
|---|---|
| `--json` / `-j` | Machine-readable JSON output |
| `--no-color` / `-C` | Disable colored output (also respects `NO_COLOR`) |
| `--insecure` / `-I` | HTTP transport, skip TLS verification |
| `--local` / `-L` | Allow local/private address hints in `resolve` and `crawl` |

Global flags work before or after the subcommand: `tltv --json channel ...` and `tltv channel --json ...` are equivalent.

## Documentation

- `tltv <command> --help` — flag reference for any command
- [docs/server.md](docs/server.md) — test signal generator
- [docs/bridge.md](docs/bridge.md) — bridge setup and operation
- [docs/relay.md](docs/relay.md) — relay deployment
- [docs/mirror.md](docs/mirror.md) — mirror origin (same-key replication)
- [docs/router.md](docs/router.md) — SNI routing reverse proxy
- [docs/viewer.md](docs/viewer.md) — web viewer and portal
- [docs/config.md](docs/config.md) — config files, hot-reload, `--dump-config`
- [docs/peer-exchange.md](docs/peer-exchange.md) — peer exchange and gossip
- [docs/tls.md](docs/tls.md) — TLS, ACME, Let's Encrypt
- [docs/install.md](docs/install.md) — installation methods

## Protocol Conformance

Validated against all 7 test vector suites from the [protocol specification](https://github.com/tltv-org/protocol) (C1–C7: identity encoding, signing, documents, URIs, guides, invalid inputs, migration). 661 tests. Run `make test` to verify.

## Links

- [timelooptv.org](https://timelooptv.org) — Project homepage
- [Spec](https://spec.timelooptv.org) — Protocol specification
- [Demo](https://demo.timelooptv.org) — Live demo
- [GitHub](https://github.com/tltv-org) — All repositories

## License

MIT — see [LICENSE](LICENSE).
