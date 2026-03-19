#!/bin/sh
set -e

# Install script for tltv-cli
# Usage: curl -sSL https://raw.githubusercontent.com/tltv-org/cli/main/install.sh | sh

REPO="tltv-org/cli"
BINARY="tltv"

main() {
  # Detect OS
  OS=$(uname -s | tr '[:upper:]' '[:lower:]')
  case "$OS" in
    linux)  ;;
    darwin) ;;
    freebsd) ;;
    mingw*|msys*|cygwin*)
      echo "Error: This script does not support Windows."
      echo "Download the .zip from https://github.com/${REPO}/releases/latest"
      exit 1
      ;;
    *) echo "Error: Unsupported OS: $OS"; exit 1 ;;
  esac

  # Detect architecture
  ARCH=$(uname -m)
  case "$ARCH" in
    x86_64|amd64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) echo "Error: Unsupported architecture: $ARCH"; exit 1 ;;
  esac

  # Get latest version tag from GitHub API
  VERSION=$(curl -sS "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed 's/.*: "//;s/".*//')
  if [ -z "$VERSION" ]; then
    echo "Error: Failed to determine latest version"
    exit 1
  fi

  INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
  ARCHIVE="tltv-cli_${VERSION}_${OS}-${ARCH}.tar.gz"
  URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"

  echo "Installing ${BINARY} ${VERSION} (${OS}/${ARCH})..."

  # Download and extract to temp dir
  tmpdir=$(mktemp -d)
  trap 'rm -rf "$tmpdir"' EXIT

  if ! curl -sfL "$URL" | tar xz -C "$tmpdir" 2>/dev/null; then
    echo "Error: Failed to download ${URL}"
    echo "Check https://github.com/${REPO}/releases for available builds."
    exit 1
  fi

  # Create install dir if needed
  mkdir -p "$INSTALL_DIR"

  # Install binary
  mv "$tmpdir/$BINARY" "$INSTALL_DIR/$BINARY"
  chmod +x "$INSTALL_DIR/$BINARY"

  echo "Installed ${BINARY} ${VERSION} to ${INSTALL_DIR}/${BINARY}"

  # Warn if not in PATH
  case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)
      echo ""
      echo "NOTE: ${INSTALL_DIR} is not in your PATH."
      echo "Add it to your shell profile:"
      echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
      ;;
  esac
}

main
