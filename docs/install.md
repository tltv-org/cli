# Installation

TLTV CLI is a single static binary with zero dependencies. Pre-built binaries
are available for Linux, macOS, Windows, and FreeBSD on both amd64 and arm64.

## Quick Install (Linux / macOS)

```bash
curl -sSL https://raw.githubusercontent.com/tltv-org/cli/main/install.sh | sh
```

Downloads the latest release binary for your OS and architecture, installs to
`/usr/local/bin/tltv` (or `./tltv` if no write permission).

## Docker

```bash
# Bridge
docker run --rm -v tltv-keys:/data -p 8000:8000 tltv bridge \
  --stream http://provider.example.com/channels.m3u

# Relay
docker run --rm -v tltv-data:/data -p 8000:8000 tltv relay \
  --node origin.example.com:443

# Build locally
docker build -t tltv .
```

The Docker image is multi-stage (`golang:1.22-alpine` → `scratch`, ~10 MB).
`WORKDIR /data` — mount a volume here to persist channel keys. CA certificates
are included for outbound HTTPS.

**HOSTNAME:** Docker sets `HOSTNAME` to the container ID by default. Always set
it explicitly (`-e HOSTNAME=public.example.com`) when running bridge or relay.

## From Source

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

All builds use `CGO_ENABLED=0` for fully static binaries.

## Windows

Download the latest `.zip` from the
[releases page](https://github.com/tltv-org/cli/releases/latest) and add
`tltv.exe` to your PATH.

## Shell Completions

```bash
# Install directly to the standard location
tltv completion --install zsh
tltv completion --install bash
tltv completion --install fish

# Or print to stdout for manual placement
tltv completion zsh >> ~/.zshrc
```

## Self-Update

```bash
tltv update
```

Downloads the latest release from GitHub and replaces the current binary
in-place. Detects OS and architecture automatically.

## Platform Matrix

| OS | Arch | Format | Notes |
|---|---|---|---|
| Linux | amd64 | `.tar.gz` | |
| Linux | arm64 | `.tar.gz` | |
| macOS | amd64 | `.tar.gz` | Intel Macs |
| macOS | arm64 | `.tar.gz` | Apple Silicon |
| Windows | amd64 | `.zip` | |
| FreeBSD | amd64 | `.tar.gz` | |
| Docker | multi | OCI image | `scratch` base, ~10 MB |

## Verifying the Binary

```bash
tltv version
```

Shows version, protocol version, Go version, and platform. Use `--json` for
machine-readable output.
