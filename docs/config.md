# Config Files

All three daemons (`server test`, `bridge`, `relay`) support JSON config files
via `--config`. Config fields map 1:1 to CLI flags — underscores in config,
dashes in flags. Precedence: CLI flag > config file > env var > default.

## Usage

```bash
# Generate a config from current flags
tltv bridge --stream http://src.tv/live.m3u8 --name "My Channel" --cache --dump-config > bridge.json

# Run from config
tltv bridge --config bridge.json

# Override a config value with a flag
tltv bridge --config bridge.json --name "Override"
```

## Format

Standard JSON. Only include fields that differ from defaults. Unknown fields
are silently ignored (forward compatibility).

```json
{
  "stream": "http://provider.com/channels.m3u",
  "guide": "http://provider.com/guide.xml",
  "name": "My Channel",
  "listen": ":8000",
  "hostname": "origin.example.com:443",
  "cache": true,
  "viewer": true,
  "log_level": "info"
}
```

## Field Mapping

Config keys use underscores; flags use dashes. Examples:

| Config field | Flag | Type |
|---|---|---|
| `stream` | `--stream` | string |
| `key` / `keys_dir` | `--key` / `--keys-dir` | string |
| `name` | `--name` | string |
| `listen` | `--listen` | string |
| `hostname` | `--hostname` | string |
| `cache` | `--cache` | bool |
| `cache_max_entries` | `--cache-max-entries` | int |
| `cache_stats` | `--cache-stats` | int |
| `viewer` | `--viewer` | bool |
| `debug_viewer` | `--debug-viewer` | bool |
| `viewer_title` | `--viewer-title` | string |
| `viewer_footer` | `--no-viewer-footer` | bool (inverted: `false` = hidden) |
| `saved_channels` | `--saved-channels` | string (file path) |
| `tls` | `--tls` | bool |
| `tls_cert` | `--tls-cert` | string |
| `tls_key` | `--tls-key` | string |
| `description` | `--description` | string |
| `tags` | `--tags` | string (comma-separated) |
| `language` | `--language` | string |
| `timezone` | `--timezone` | string |
| `icon` | `--icon` | string |
| `channels` | `--channels` | int (server only) |
| `variants` | `--variants` | string (server only) |
| `proxy` | `--proxy` | string |
| `access` | `--access` | string |
| `token` | `--token` | string |
| `on_demand` | `--on-demand` | bool |
| `log_level` | `--log-level` | string |
| `log_format` | `--log-format` | string |
| `log_file` | `--log-file` | string |

Scalar fields (strings, numbers, booleans) are applied automatically via
`applyConfigToFlags`. Array fields (`channels`, `node`) and the polymorphic
`guide` field are handled per-daemon.

## Polymorphic Guide

The `guide` config field accepts three forms:

**Inline entries** — define a custom guide schedule directly in config:

```json
{
  "guide": {
    "entries": [
      {
        "start": "2026-04-08T08:00:00Z",
        "end": "2026-04-08T20:00:00Z",
        "title": "Day Block"
      },
      {
        "start": "2026-04-08T20:00:00Z",
        "end": "2026-04-09T08:00:00Z",
        "title": "Night Block"
      }
    ]
  }
}
```

**File reference** — path to an external guide file (XMLTV or JSON):

```json
{
  "guide": "guide.xml"
}
```

**Omitted or null** — uses the default ephemeral guide (midnight-to-midnight
UTC entry, auto-renewed).

## Hot-Reload

Config files are watched by mtime (one `os.Stat` per check, no inotify, no
platform-specific code). Check interval: 30 seconds. If the file disappears or
becomes unreadable, the current config is kept (fail-safe). Bad JSON logs an
error and keeps the current config running.

### Reloadable Fields

| Daemon | Reloadable | Requires restart |
|---|---|---|
| `server test` | `name`, `guide` | `key`, `listen`, `hostname`, `width`, `height`, `fps`, `qp` |
| `bridge` | `stream`, `name`, `guide` | `keys_dir`, `listen`, `hostname` |
| `relay` | `channels`, `node` | `listen`, `hostname` |

When the relay reloads `channels` or `node`, it re-discovers targets, adds new
channels, removes channels no longer in the config, and refreshes hints for
existing channel IDs — all without restart.

## --dump-config

Prints the current resolved config as JSON and exits. Only includes fields that
differ from compiled defaults. Useful for generating config files from flag
combinations:

```bash
tltv relay --node origin.example.com:443 --cache --hostname relay.tv:443 --dump-config
```

Output:

```json
{
  "node": ["origin.example.com:443"],
  "cache": true,
  "hostname": "relay.tv:443"
}
```

## Per-Daemon Examples

**Server:**

```json
{
  "key": "channel.key",
  "name": "TLTV Test",
  "width": 1920,
  "height": 1080,
  "cache": true,
  "viewer": true,
  "guide": {
    "entries": [
      {"start": "2026-01-01T00:00:00Z", "end": "2026-12-31T23:59:59Z", "title": "Test Signal"}
    ]
  }
}
```

**Bridge:**

```json
{
  "stream": "http://provider.com/channels.m3u",
  "guide": "http://provider.com/guide.xml",
  "hostname": "bridge.example.com:443",
  "cache": true
}
```

**Relay:**

```json
{
  "channels": ["tltv://TVabc123@origin.example.com:443"],
  "node": ["origin.example.com:443", "backup.example.com:443"],
  "hostname": "relay.example.com:443",
  "cache": true
}
```

The relay accepts both `"node"` (matches the flag name) and `"nodes"` (legacy).
