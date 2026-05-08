#!/bin/sh
set -e

# Install script for tltv-cli
# Usage: curl -fsSL timelooptv.org/install | sh
#        curl -fsSL timelooptv.org/install | sh -s -- --version v1.17.0

REPO="tltv-org/cli"
BINARY="tltv"
REQUESTED_VERSION=""

# Parse arguments
while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help)
      echo "Usage: curl -fsSL timelooptv.org/install | sh"
      echo "       curl -fsSL timelooptv.org/install | sh -s -- --version v1.17.0"
      echo ""
      echo "Options:"
      echo "  -v, --version <version>  Install a specific version (e.g., v1.17.0)"
      echo "  -h, --help               Show this help"
      exit 0
      ;;
    -v|--version)
      if [ $# -ge 2 ] && [ -n "$2" ]; then
        REQUESTED_VERSION="$2"
        shift 2
      else
        echo "Error: --version requires a version argument"
        exit 1
      fi
      ;;
    *)
      echo "Warning: Unknown option '$1'" >&2
      shift
      ;;
  esac
done

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

  # Rosetta 2 detection: prefer native arm64 binary on Apple Silicon
  if [ "$OS" = "darwin" ] && [ "$ARCH" = "amd64" ]; then
    ROSETTA=$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)
    if [ "$ROSETTA" = "1" ]; then
      ARCH="arm64"
    fi
  fi

  # Determine version to install
  if [ -n "$REQUESTED_VERSION" ]; then
    # Ensure version starts with 'v'
    case "$REQUESTED_VERSION" in
      v*) VERSION="$REQUESTED_VERSION" ;;
      *)  VERSION="v${REQUESTED_VERSION}" ;;
    esac
    # Verify the release exists
    HTTP_STATUS=$(curl -sI -o /dev/null -w "%{http_code}" "https://github.com/${REPO}/releases/tag/${VERSION}")
    if [ "$HTTP_STATUS" = "404" ]; then
      echo "Error: Release ${VERSION} not found"
      echo "Available releases: https://github.com/${REPO}/releases"
      exit 1
    fi
  else
    VERSION=$(curl -sS "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed 's/.*: "//;s/".*//')
    if [ -z "$VERSION" ]; then
      echo "Error: Failed to determine latest version"
      exit 1
    fi
  fi

  # Check if already installed
  UPGRADING=false
  if command -v "$BINARY" >/dev/null 2>&1; then
    EXISTING_PATH=$(command -v "$BINARY")
    EXISTING_DIR=$(dirname "$EXISTING_PATH")
    INSTALLED_VERSION=$("$BINARY" version 2>/dev/null | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1 || echo "")
    if [ "$INSTALLED_VERSION" = "$VERSION" ]; then
      echo "tltv ${VERSION} already installed at ${EXISTING_PATH}"
      exit 0
    elif [ -n "$INSTALLED_VERSION" ]; then
      UPGRADING=true
      # Upgrade in place if we can write to the existing location
      if [ -w "$EXISTING_DIR" ]; then
        INSTALL_DIR="$EXISTING_DIR"
      fi
    fi
  fi

  # Pick install directory (if not already set by upgrade detection or env)
  if [ -z "${INSTALL_DIR:-}" ]; then
    INSTALL_DIR=""
    for dir in "$HOME/.local/bin" "$HOME/bin" /usr/local/bin; do
      case ":$PATH:" in
        *":$dir:"*)
          if [ -w "$dir" ] || [ ! -e "$dir" -a -w "$(dirname "$dir")" ]; then
            INSTALL_DIR="$dir"
            break
          fi
          ;;
      esac
    done
    INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
  fi

  ARCHIVE="tltv-cli_${VERSION}_${OS}-${ARCH}.tar.gz"
  URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"

  if [ "$UPGRADING" = true ]; then
    echo "Upgrading tltv from ${INSTALLED_VERSION} to ${VERSION} (${OS}/${ARCH})..."
  else
    echo "Installing tltv ${VERSION} (${OS}/${ARCH})..."
  fi

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

  echo "Installed tltv ${VERSION} to ${INSTALL_DIR}/${BINARY}"

  # Skip PATH setup on upgrade — it's already in PATH
  if [ "$UPGRADING" = true ]; then
    return
  fi

  # Add to PATH if needed
  case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)
      # Use $HOME-relative path so the line is portable
      case "$INSTALL_DIR" in
        "$HOME"/*) PATH_EXPR="\$HOME${INSTALL_DIR#$HOME}" ;;
        *)         PATH_EXPR="$INSTALL_DIR" ;;
      esac
      LINE="export PATH=${PATH_EXPR}:\$PATH"

      # Detect shell profile
      PROFILE=""
      if [ -n "${ZSH_VERSION:-}" ] || [ "$(basename "${SHELL:-}")" = "zsh" ]; then
        PROFILE="$HOME/.zshrc"
      elif [ -n "${BASH_VERSION:-}" ] || [ "$(basename "${SHELL:-}")" = "bash" ]; then
        PROFILE="$HOME/.bashrc"
      elif [ -f "$HOME/.profile" ]; then
        PROFILE="$HOME/.profile"
      fi

      if [ -n "$PROFILE" ]; then
        # Don't add if already present
        if ! grep -qF "$INSTALL_DIR" "$PROFILE" 2>/dev/null; then
          echo "" >> "$PROFILE"
          echo "# tltv" >> "$PROFILE"
          echo "$LINE" >> "$PROFILE"
          echo "Added ${INSTALL_DIR} to PATH in ${PROFILE}"
          echo "Restart your shell or run: source ${PROFILE}"
        fi
      else
        echo ""
        echo "NOTE: Could not detect shell profile."
        echo "Add this to your shell config:"
        echo "  $LINE"
      fi
      ;;
  esac
}

main
