# tltv router

Lightweight L7 reverse proxy with built-in multi-domain ACME TLS. Routes by
`Host` header after TLS termination. Eliminates the need for traefik, nginx, or
caddy in TLTV deployments.

## Usage

```bash
# Single route
tltv router --route demo.tv=localhost:8000 --tls

# Multiple routes
tltv router \
  --route demo.tv=localhost:8000 \
  --route relay.tv=localhost:8001 \
  --tls --acme-email ops@example.com

# Path-prefix routing
tltv router \
  --route "demo.tv/tltv=relay:8000" \
  --route "demo.tv=web:3000" \
  --tls

# Multi-backend round-robin
tltv router --route "demo.tv=relay1:8000,relay2:8000" --tls

# Combined
tltv router \
  --route "demo.tv/tltv=relay1:8000,relay2:8000" \
  --route "demo.tv=web:3000" \
  --health-check /health \
  --tls
```

## Route Syntax

```
--route HOST[/PREFIX]=BACKEND[,BACKEND...]
```

| Component | Description |
|---|---|
| `HOST` | Hostname to match (case-insensitive, auto-lowercased) |
| `/PREFIX` | Optional path prefix (longest-prefix match within a host) |
| `BACKEND` | `host:port` of the upstream server |
| `,BACKEND` | Additional backends for round-robin load balancing |

**Examples:**

| Route | Matches | Backend |
|---|---|---|
| `demo.tv=relay:8000` | All requests to `demo.tv` | `relay:8000` |
| `demo.tv/tltv=relay:8000` | Paths starting with `/tltv` on `demo.tv` | `relay:8000` |
| `demo.tv=a:8000,b:8000` | All requests to `demo.tv` | Round-robin between `a` and `b` |

Path-prefix routes take priority over catch-all routes for the same hostname
(longest-prefix-first matching).

## Route Sources

Routes merge from three sources in priority order:

1. **`--route` flags** — repeatable, highest priority
2. **`ROUTES` env var** — comma or semicolon separated
3. **Config file** — `routes` object in JSON config

```bash
# Env var
ROUTES="demo.tv=relay:8000,relay2.tv=relay2:8000" tltv router --tls

# Config file
tltv router --config router.json --tls
```

### Config File Format

```json
{
  "routes": {
    "demo.tv": "relay:8000",
    "multi.tv": ["relay1:8000", "relay2:8000"],
    "prefix.tv/tltv": "relay:8000",
    "prefix.tv/tltv/prefix": ["relay1:8000", "relay2:8000"]
  },
  "listen": ":443",
  "tls": true,
  "acme_email": "ops@example.com",
  "health_check": "/health",
  "health_interval": "30s"
}
```

Route values can be strings (single backend) or arrays (round-robin). Path
prefixes are specified in the key.

## Health Checks

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--health-check` | `HEALTH_CHECK` | *(off)* | HTTP path to poll on each backend |
| `--health-interval` | `HEALTH_INTERVAL` | `30s` | Poll interval |

When enabled, the router polls each backend at the given path. Unhealthy backends
(non-2xx or connection refused) are skipped during round-robin selection. State
transitions (up→down, down→up) are logged. If all backends for a route are
unhealthy, requests receive a 503 `backend_unavailable` JSON error.

Health state is tracked per-backend with `atomic.Bool` for lock-free reads.

## Config Hot-Reload

The config file is watched by mtime (30-second check interval). When routes
change:

- New hostnames are added and ACME certificates provisioned automatically
- Removed hostnames stop routing (certs are kept for renewal)
- Backend changes take effect immediately via atomic pointer swap

The route table uses `atomic.Pointer` for lock-free hot-reload — no request
blocking during config changes.

## TLS

The router uses `certStore` for multi-domain ACME certificates. Each hostname
in the route table gets its own Let's Encrypt certificate, automatically issued
and renewed.

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `--tls` | `TLS=1` | off | Enable automatic Let's Encrypt TLS |
| `--tls-cert` | `TLS_CERT` | | Manual cert (shared across all hostnames) |
| `--tls-key` | `TLS_KEY` | | Manual key |
| `--tls-staging` | `TLS_STAGING=1` | off | Use Let's Encrypt staging |
| `--acme-email` | `ACME_EMAIL` | | Email for ACME account |

Port 80 handler serves ACME challenges for all managed hostnames and redirects
HTTP to HTTPS.

## Flags

| Flag | Short | Env | Default | Description |
|------|-------|-----|---------|-------------|
| `--route` | `-r` | `ROUTES` | | Route rule (repeatable) |
| `--listen` | `-l` | `LISTEN` | `:443` | Listen address |
| `--health-check` | | `HEALTH_CHECK` | | Health check path |
| `--health-interval` | | `HEALTH_INTERVAL` | `30s` | Health check interval |
| `--config` | | `CONFIG` | | Config file path (JSON) |
| `--dump-config` | | | | Print resolved config and exit |
| `--log-level` | | `LOG_LEVEL` | `info` | Log level |
| `--log-format` | | `LOG_FORMAT` | `human` | Log format |
| `--log-file` | | `LOG_FILE` | stderr | Log file |

Plus all TLS flags listed above.

## Reverse Proxy Behavior

- `X-Forwarded-For` set to the client IP
- `X-Forwarded-Proto` set to `https` (TLS) or `http`
- `Host` header preserved from the original request
- Request port stripped from `Host` header before route matching
- JSON error responses: `no_route` (502), `backend_unavailable` (503),
  `backend_error` (502)

## Docker

```bash
docker run -d --name router \
  -e ROUTES="demo.tv=relay:8000" \
  -e TLS=1 \
  -e ACME_EMAIL=ops@example.com \
  -p 443:443 -p 80:80 \
  -v certs:/data/certs \
  tltv router
```

## --dump-config

```bash
tltv router --route demo.tv=relay:8000 --route multi.tv=a:8000,b:8000 \
  --health-check /health --dump-config
```

Output:

```json
{
  "routes": {
    "demo.tv": "relay:8000",
    "multi.tv": ["a:8000", "b:8000"]
  },
  "health_check": "/health"
}
```

Single-backend routes are serialized as strings; multi-backend routes as arrays.
Only non-default values are included.
