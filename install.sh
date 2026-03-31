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

  # Pick install directory: use INSTALL_DIR if set, otherwise find a
  # user-writable directory already in PATH. Fall back to ~/.local/bin.
  if [ -n "$INSTALL_DIR" ]; then
    : # user override, use as-is
  else
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
      if [ -n "$ZSH_VERSION" ] || [ "$(basename "$SHELL")" = "zsh" ]; then
        PROFILE="$HOME/.zshrc"
      elif [ -n "$BASH_VERSION" ] || [ "$(basename "$SHELL")" = "bash" ]; then
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
