#!/usr/bin/env bash
# claude-cockpit installer — downloads a prebuilt, dependency-free binary and
# self-registers it into Claude Code. No Go, no jq, no runtime required.
#
#   curl -fsSL https://raw.githubusercontent.com/Agent-Hellboy/claude-cockpit/main/install.sh | bash
#
# Env overrides: COCKPIT_VERSION (e.g. v0.1.0), CLAUDE_CONFIG_DIR.
set -euo pipefail

REPO="Agent-Hellboy/claude-cockpit"
CLAUDE_DIR="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
BIN_DIR="$CLAUDE_DIR/bin"

die() { printf '\033[31mx\033[0m %s\n' "$1" >&2; exit 1; }
say() { printf '\033[36m==>\033[0m %s\n' "$1"; }

command -v curl >/dev/null 2>&1 || die "curl is required."
command -v tar  >/dev/null 2>&1 || die "tar is required."

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64)  arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) die "unsupported arch: $arch" ;;
esac
case "$os" in
  darwin|linux) ;;
  *) die "unsupported OS: $os (binaries are built for darwin/linux)" ;;
esac

ver="${COCKPIT_VERSION:-latest}"
asset="claude-cockpit_${os}_${arch}.tar.gz"
if [ "$ver" = "latest" ]; then
  url="https://github.com/$REPO/releases/latest/download/$asset"
else
  url="https://github.com/$REPO/releases/download/$ver/$asset"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
say "Downloading $asset ($ver)"
curl -fsSL "$url" -o "$tmp/c.tar.gz" || die "download failed: $url"
tar -xzf "$tmp/c.tar.gz" -C "$tmp" || die "extract failed"
[ -f "$tmp/cockpit" ] || die "archive did not contain the cockpit binary"

mkdir -p "$BIN_DIR"
install -m 0755 "$tmp/cockpit" "$BIN_DIR/cockpit"
# Clear macOS Gatekeeper quarantine on the downloaded binary.
[ "$os" = "darwin" ] && xattr -d com.apple.quarantine "$BIN_DIR/cockpit" 2>/dev/null || true

say "Installed binary -> $BIN_DIR/cockpit"
"$BIN_DIR/cockpit" install
