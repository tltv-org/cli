# TLS

All three daemons (`server test`, `bridge`, `relay`) support built-in TLS via
automatic Let's Encrypt certificates (ACME HTTP-01) or manual certificate files.
Zero external dependencies — the ACME client uses only Go stdlib crypto.

## Automatic Certificates (ACME)

```bash
# Let's Encrypt auto-cert (requires port 80 and 443)
tltv bridge --stream http://src.tv/live.m3u8 --tls --hostname mychannel.tv

# With email for expiry notifications
tltv relay --node origin:443 --tls --hostname relay.tv --acme-email admin@relay.tv

# Staging (testing without rate limits)
tltv server test --name "Test" --tls --tls-staging --hostname test.example.com
```

When `--tls` is enabled without `--tls-cert`/`--tls-key`, the daemon
automatically issues a certificate from Let's Encrypt:

1. Starts an HTTP server on `:80` for ACME HTTP-01 challenges and
   HTTP→HTTPS redirects.
2. Issues a certificate for the `--hostname` domain (blocking on first start).
3. Stores the cert in `./certs/` (override with `TLS_DIR` env var).
4. Renews automatically in the background (checks hourly, renews when <30 days
   remain).
5. Hot-swaps the certificate via `GetCertificate` callback — no restart needed.

**Requirements:** Port 80 must be reachable for ACME challenges. `--hostname`
must be a publicly resolvable domain pointing to this server.

## Manual Certificates

```bash
# PEM certificate and key files
tltv bridge --stream src.m3u8 --tls-cert cert.pem --tls-key key.pem

# Self-signed for local testing
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
  -keyout key.pem -out cert.pem -days 365 -nodes -subj '/CN=localhost'
tltv server test --name "Test" --tls-cert cert.pem --tls-key key.pem
```

Both `--tls-cert` and `--tls-key` must be provided together.

## Port Behavior

When TLS is enabled (any mode), the default listen port changes from `:8000` to
`:443`. An explicitly set `--listen` value is preserved.

| TLS mode | Default port |
|---|---|
| Disabled (default) | `:8000` |
| `--tls` (ACME) | `:443` |
| `--tls-cert` + `--tls-key` | `:443` |

## Certificate Storage

ACME certificates and account keys are stored in a local directory:

```
certs/
  account-key.pem    ECDSA P-256 account private key
  account.json       ACME account URL
  hostname.pem       Certificate chain (PEM)
  hostname-key.pem   Certificate private key (PEM)
```

Default directory: `./certs/`. Override with `TLS_DIR` env var.

## Flags

| Flag | Env | Default | Description |
|---|---|---|---|
| `--tls` | `TLS=1` | off | Enable TLS (ACME if no cert/key provided) |
| `--tls-cert` | `TLS_CERT` | — | PEM certificate file |
| `--tls-key` | `TLS_KEY` | — | PEM private key file |
| `--tls-staging` | `TLS_STAGING=1` | off | Use Let's Encrypt staging directory |
| `--acme-email` | `ACME_EMAIL` | — | Email for ACME account (optional) |
| — | `TLS_DIR` | `./certs/` | Certificate storage directory |

These flags are shared across all three daemons.

## Docker

```yaml
services:
  bridge:
    image: tltv
    command: bridge
    ports:
      - "80:80"    # ACME challenges
      - "443:443"  # HTTPS
    volumes:
      - certs:/data/certs
      - keys:/data/keys
    environment:
      STREAM: http://source:8000/live.m3u8
      TLS: "1"
      HOSTNAME: mychannel.tv
      ACME_EMAIL: admin@mychannel.tv
volumes:
  certs:
  keys:
```

Mount the certs volume to persist certificates across container restarts.
Without persistence, a new certificate is issued on every container start
(Let's Encrypt has rate limits).

## Notes

**ACME signing.** JWS/ES256 with ECDSA P-256 (not ASN.1 DER — raw r‖s
64-byte format). JWK thumbprint per RFC 7638. P-256 coordinates zero-padded
to 32 bytes per RFC 7518 §6.2.1.

**Nonce retry.** The ACME client automatically retries on `badNonce` errors.

**Staging.** Use `--tls-staging` for testing. Staging certificates are not
trusted by browsers but avoid Let's Encrypt's rate limits (5 certs per domain
per week on production).

**`--insecure` and TLS.** The `--insecure` flag affects outbound connections
(relay→origin), not the daemon's own listener. A relay can serve HTTPS to
viewers while connecting to an upstream origin over plain HTTP.
