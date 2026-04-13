# tltv mirror

The mirror is a same-key origin replica (spec section 10.8). Unlike a relay, a mirror
holds the channel's private key and is listed in the signed `origins` field. From
the protocol's perspective, a mirror IS an origin — the distinction is operational,
not protocol-level.

Use a mirror when you need:

- **Failover** — if the primary origin goes down, the mirror promotes to active
  signer automatically
- **Geographic redundancy** — multiple origins serving the same channel from
  different locations
- **Zero-downtime maintenance** — take the primary offline for upgrades while
  the mirror keeps serving

## Usage

```bash
# Basic mirror (passive replication)
tltv mirror --source "tltv://TVabc@primary.tv" \
    --key channel.key --hostname mirror.tv

# Mirror with buffer for resilience
tltv mirror --source "tltv://TVabc@primary.tv" \
    --key channel.key --hostname mirror.tv \
    --buffer 2h --tls --cache

# Mirror of a private channel
tltv mirror --source "tltv://TVabc@primary.tv?token=secret" \
    --key channel.key --hostname mirror.tv \
    --token secret --buffer 2h
```

## How It Works

### Startup Validation

The mirror performs strict validation at startup:

1. **Key match** — the key file must derive the same channel ID as the source URI.
   If they don't match: `error: key does not match source channel ID`.

2. **Origins check** — the mirror's `--hostname` must appear in the primary's signed
   `origins` array. The channel operator must explicitly list the mirror before it
   can start. If not listed: `error: hostname not listed in source origins`.

3. **Private channel** — if the upstream channel has `access: "token"`, the mirror
   requires `--token` to validate incoming viewer requests. If missing:
   `error: upstream is a private channel — --token is required`.

### Passive Mode (Default)

In passive mode, the mirror acts like a verified pass-through:

- **Metadata**: re-served verbatim from the primary (exact bytes, no re-serialization)
- **Guide**: re-served verbatim (preserves upstream `relay_from` entries)
- **Icon**: fetched from upstream and cached, served at the protocol icon path
- **Stream**: proxied from upstream (buffer → cache → direct proxy)
- **Well-known**: channel listed in `channels[]` (not `relaying[]`) because the
  mirror is an origin
- **Private channels**: excluded from `/.well-known/tltv`, token required on all
  endpoints

The mirror polls upstream metadata and guide at configurable intervals (`--meta-poll`,
`--guide-poll`) to stay current.

### Promotion

After `--promote-after` consecutive metadata poll failures (default: 3), the mirror
promotes to active signer:

1. Computes `seq = max(seq_floor, now) + 1` — guaranteed greater than any upstream
   seq ever seen
2. Signs new metadata preserving all upstream fields (name, description, tags,
   language, timezone, icon, origins)
3. Re-signs the guide with the same entries
4. Persists the new seq floor to the state file

Once promoted, the mirror re-signs metadata and guide periodically (same cadence as
the poll loop). If a buffer is configured, viewers continue streaming seamlessly from
buffered segments.

**Demotion is manual** — restart the mirror to return to passive mode. Automatic
demotion risks split-brain scenarios on partition heal.

### State Persistence

The state file (`--state-file`, default `mirror-state.json`) tracks:

| Field | Purpose |
|---|---|
| `seq_floor` | Highest metadata seq seen or signed — must never decrease |
| `last_media_seq` | Last HLS media sequence number |
| `promoted` | Whether the mirror is in active signer mode |
| `last_verified` | Timestamp of last successful upstream poll |

The state file is fail-safe:
- **Missing file** → fresh start (first run)
- **Corrupt file** → fresh start with warning
- **Channel ID mismatch** → fresh start (different channel key)

The `seq_floor` is the critical invariant. Even after restart, the mirror will
never sign metadata with a seq lower than the floor. This prevents clients from
rejecting the mirror's documents due to seq regression.

## Flags

### Required

| Flag | Short | Env | Description |
|------|-------|-----|-------------|
| `--source` | `-s` | `SOURCE` | `tltv://` URI of the primary origin |
| `--key` | `-k` | `KEY` | Channel key file (same key as primary) |
| `--hostname` | `-H` | `HOSTNAME` | Public hostname (must be in primary's origins) |

### Server

| Flag | Short | Env | Description |
|------|-------|-----|-------------|
| `--listen` | `-l` | `LISTEN` | Listen address (default `:8000`, `:443` with `--tls`) |
| `--token` | `-t` | `TOKEN` | Access token for private channels |

### Buffer

| Flag | Short | Env | Description |
|------|-------|-----|-------------|
| `--buffer` | `-b` | `BUFFER` | Proactive buffer duration (e.g. `2h`) |
| `--buffer-max-memory` | `-B` | `BUFFER_MAX_MEMORY` | Max total buffer memory (default `1g`) |

### Failover

| Flag | | Env | Description |
|------|--|-----|-------------|
| `--promote-after` | | `PROMOTE_AFTER` | Consecutive failures before promotion (default `3`) |
| `--state-file` | | `STATE_FILE` | State file path (default `mirror-state.json`) |
| `--fallback-stream` | | `FALLBACK_STREAM` | Local HLS source after buffer drains |
| `--fallback-guide` | | `FALLBACK_GUIDE` | Guide file for fallback content |
| `--icon` | | `ICON` | Local icon override for promoted mode |

### Infrastructure

All shared daemon flags are supported: `--tls`, `--cache`, `--viewer`,
`--debug-viewer`, `--viewer-title`, `--no-viewer-footer`, `--peers`, `--gossip`,
`--proxy`, `--config`, `--dump-config`, and log flags. See
[docs/tls.md](tls.md), [docs/viewer.md](viewer.md), [docs/config.md](config.md),
[docs/peer-exchange.md](peer-exchange.md).

## Mirror vs. Relay vs. Bridge

| | Relay | Bridge | Mirror |
|---|---|---|---|
| Has private key | No | Yes (new) | Yes (same) |
| Listed in `origins` | No | Yes (own) | Yes (shared) |
| Can sign metadata | No | Yes (own) | Yes (same channel) |
| Channel ID | N/A | New ID | Same ID as primary |
| Stream content | Proxied | Re-originated | Replicated |
| In well-known | `relaying[]` | `channels[]` | `channels[]` |

**Relay** — re-serves another channel's signed documents. Different identity.
Viewers know they're on a relay. Use for CDN-like distribution.

**Bridge** — creates a new channel sourced from external content (or another TLTV
channel). New key, new identity. Use for rebroadcast under your own brand.

**Mirror** — replicates an origin with the same key. Viewers can't distinguish the
mirror from the primary. Use for redundancy and failover.

## Examples

### Failover pair

```bash
# Primary
tltv bridge --stream /content/ --key channel.key \
    --hostname primary.tv --tls --cache

# Mirror (same key, listed in primary's origins)
tltv mirror --source "tltv://TVabc@primary.tv" \
    --key channel.key --hostname mirror.tv \
    --buffer 2h --tls --cache
```

If `primary.tv` goes down, the mirror keeps serving from its 2-hour buffer, then
promotes to active signer. Viewers on `mirror.tv` see no interruption. Viewers
on `primary.tv` fail over via the `origins` array in signed metadata.

### Private channel mirror

```bash
tltv mirror --source "tltv://TVabc@primary.tv?token=secret" \
    --key channel.key --hostname mirror.tv \
    --token secret --buffer 2h --tls
```

The token in the URI authenticates upstream fetches. The `--token` flag enforces
authentication on the mirror's own endpoints.

### Docker deployment

```bash
docker run -d --name mirror \
    -v /data/keys:/data \
    -e SOURCE="tltv://TVabc@primary.tv" \
    -e KEY=/data/channel.key \
    -e HOSTNAME=mirror.tv \
    -e BUFFER=2h \
    -e TLS=1 \
    -e CACHE=1 \
    -p 443:443 -p 80:80 \
    tltv mirror
```
