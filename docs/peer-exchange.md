# Peer Exchange and Gossip

All three daemons (`server test`, `bridge`, `relay`) participate in the TLTV
peer exchange protocol (spec section 11). Peer exchange lets nodes discover
channels from each other, forming a decentralized network without a central
registry.

## --peers (Manual Curation)

Pass `tltv://` URIs of channels you vouch for. The daemon verifies their
metadata and advertises them in its `/tltv/v1/peers` response.

```bash
# Server promotes a friend's channel
tltv server test --name "My Channel" --peers "tltv://TVfriend@friend.tv:443"

# Bridge promotes multiple channels alongside its own
tltv bridge --stream source.m3u --peers "tltv://TVa@node1.tv,tltv://TVb@node2.tv"

# Relay with curated peers
tltv relay --channels "tltv://TV...@origin.tv" --peers "tltv://TVfriend@friend.tv"
```

Each daemon periodically fetches and verifies metadata for `--peers` targets
(every 5 minutes). Channels that migrate, go private, or become retired are
automatically dropped from the peers response.

Only `tltv://` URIs are accepted — bare `host:port` values are rejected. Each
URI must include at least one hint (`@host:port`).

## --gossip (One-Hop Discovery)

When `--gossip` is enabled, the daemon fetches `/tltv/v1/peers` from the nodes
behind its `--peers` targets, validates any discovered channels, and includes
them in its own peers response.

```bash
# Relay opts in to re-advertise gossip-discovered channels
tltv relay --channels "tltv://TV...@origin.tv" --gossip

# Server with gossip
tltv server test --name "Test" --peers "tltv://TV...@node.tv" --gossip
```

**Default off.** The operator must opt-in to vouch for channels they didn't
explicitly choose. Without `--gossip`, only `--peers` entries and the daemon's
own channels appear in the peers response.

**One-hop only.** Gossip discovery goes one level deep — the daemon fetches
peers from its direct peers but does not recursively follow discovered nodes.

## Validation Flow

Every gossip-discovered channel is validated before being advertised:

1. **Node info** — Fetch `/.well-known/tltv` from the channel's hint. Verify
   the channel is listed in `channels` or `relaying`.
2. **Metadata** — Fetch and verify the channel's signed metadata document
   (Ed25519 signature check).
3. **Access check** — Reject private (`access=token`), on-demand
   (`on_demand=true`), and retired (`status=retired`) channels.

Channels that fail any step are silently skipped.

## Peers Response

The `/tltv/v1/peers` endpoint returns a JSON document with a `peers` array.
Each entry includes:

```json
{
  "peers": [
    {
      "id": "TVabc123...",
      "name": "Channel Name",
      "hints": ["host.example.com:443"],
      "last_seen": "2026-04-08T12:00:00Z"
    }
  ]
}
```

What each daemon includes in its peers response:

| Source | Server | Bridge | Relay |
|---|---|---|---|
| Own channels | — | Public originated channels (with hostname as hint) | Relayed channels (with hostname as hint) |
| `--peers` entries | Verified external peers | Verified external peers | Verified external peers |
| Gossip-discovered | Only if `--gossip` | Only if `--gossip` | Only if `--gossip` |

Entries are deduplicated by channel ID. Gossip entries older than `--stale-days`
(default 7) are pruned.

## Crawl

The `tltv crawl` command discovers channels across the network by BFS-crawling
peer exchange endpoints:

```bash
# Discover channels starting from a node
tltv crawl example.com

# Deeper crawl
tltv crawl --depth 3 example.com

# JSON output
tltv --json crawl example.com
```

The crawler uses an SSRF-safe HTTP client — local/private address hints are
skipped unless `--local` is set.

## Flags

| Flag | Env | Default | Description |
|---|---|---|---|
| `-P`, `--peers` | `PEERS` | — | Comma-separated `tltv://` URIs to advertise |
| `-g`, `--gossip` | `GOSSIP=1` | off | Include validated gossip-discovered channels |

These flags are available on all three daemons. The relay has additional
peer-related flags:

| Flag | Env | Default | Description |
|---|---|---|---|
| `--max-peers` | `MAX_PEERS` | `100` | Max peer entries in response |
| `--stale-days` | `STALE_DAYS` | `7` | Prune gossip entries older than N days |

## Notes

**Network bootstrapping.** A new node joins the network by setting `--peers` to
one or more known channels. Enabling `--gossip` lets it gradually discover more
of the network.

**Relay gossip gating.** The relay always polls peers internally (for fallback
upstream discovery) regardless of `--gossip`. The flag only controls whether
discovered channels appear in the outward-facing peers response.

**Bridge/server gossip nodes.** These daemons derive gossip poll targets from
the hints in `--peers` URIs via `gossipNodesFromPeers`.
