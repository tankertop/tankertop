#!/usr/bin/env bash
#
# Install tankertop from GitHub Releases.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/tankertop/tankertop/main/install.sh | bash
#   ./install.sh v0.1.0            # a specific tag
#   TANKERTOP_DEST=~/bin ./install.sh
#
set -euo pipefail

REPO="${TANKERTOP_REPO:-tankertop/tankertop}"
DEST="${TANKERTOP_DEST:-/usr/local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac

tag="${1:-}"
if [ -z "$tag" ]; then
  # `|| true`: without a release the API 404s, and under `set -e -o pipefail`
  # the failing pipeline would abort here instead of reaching the hint below.
  tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null \
    | grep -m1 '"tag_name"' | cut -d'"' -f4 || true)
fi
if [ -z "$tag" ]; then
  echo "error: $REPO has no published release to install." >&2
  echo "" >&2
  echo "  pass a tag explicitly:  install.sh v0.1.0" >&2
  echo "  or build from source:   go install github.com/$REPO@latest" >&2
  exit 1
fi

asset="tankertop_${tag#v}_${os}_${arch}.tar.gz"
url="https://github.com/$REPO/releases/download/$tag/$asset"
echo "installing tankertop $tag ($os/$arch)"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

if ! curl -fsSL "$url" -o "$tmp/$asset" 2>/dev/null; then
  echo "error: no asset $asset in release $tag of $REPO" >&2
  echo "see https://github.com/$REPO/releases for what is published" >&2
  exit 1
fi

# Verify the download against the release's published checksums.txt. This fails
# closed: a missing checksums file, a missing sha256 tool, or an unlisted asset
# is a hard error, so a tampered or truncated download can never install past a
# warning. Override only if you understand the risk: TANKERTOP_SKIP_CHECKSUM=1.
sums_url="https://github.com/$REPO/releases/download/$tag/checksums.txt"
if [ "${TANKERTOP_SKIP_CHECKSUM:-0}" = "1" ]; then
  echo "warning: TANKERTOP_SKIP_CHECKSUM=1 set — installing without verification" >&2
else
  if ! curl -fsSL "$sums_url" -o "$tmp/checksums.txt" 2>/dev/null; then
    echo "error: checksums.txt not found for $tag — refusing to install unverified" >&2
    echo "  (set TANKERTOP_SKIP_CHECKSUM=1 to override at your own risk)" >&2
    exit 1
  fi
  want=$(grep " ${asset}\$" "$tmp/checksums.txt" | awk '{print $1}')
  if [ -z "$want" ]; then
    echo "error: $asset is not listed in checksums.txt — refusing to install unverified" >&2
    exit 1
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    got=$(sha256sum "$tmp/$asset" | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    got=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')
  else
    echo "error: no sha256sum or shasum available to verify the download" >&2
    echo "  (set TANKERTOP_SKIP_CHECKSUM=1 to override at your own risk)" >&2
    exit 1
  fi
  if [ "$want" != "$got" ]; then
    echo "error: checksum mismatch for $asset" >&2
    echo "  expected $want" >&2
    echo "  got      $got" >&2
    exit 1
  fi
  echo "checksum verified"
fi

tar -xzf "$tmp/$asset" -C "$tmp"

if install -m 0755 "$tmp/tankertop" "$DEST/tankertop" 2>/dev/null; then
  :
else
  echo "elevating with sudo to write $DEST"
  sudo install -m 0755 "$tmp/tankertop" "$DEST/tankertop"
fi

echo "installed: $("$DEST/tankertop" --version)"
