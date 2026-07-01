#!/usr/bin/env bash
#
# Install kubeview from GitHub Releases.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/tpenzkofer/kubeview/main/install.sh | bash
#   ./install.sh v0.1.0            # a specific tag
#   KUBEVIEW_DEST=~/bin ./install.sh
#
set -euo pipefail

REPO="${KUBEVIEW_REPO:-tpenzkofer/kubeview}"
DEST="${KUBEVIEW_DEST:-/usr/local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac

tag="${1:-}"
if [ -z "$tag" ]; then
  tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | grep -m1 '"tag_name"' | cut -d'"' -f4)
fi
if [ -z "$tag" ]; then
  echo "error: could not determine the latest release tag for $REPO" >&2
  echo "pass one explicitly, e.g. install.sh v0.1.0" >&2
  exit 1
fi

asset="kubeview_${tag#v}_${os}_${arch}.tar.gz"
url="https://github.com/$REPO/releases/download/$tag/$asset"
echo "installing kubeview $tag ($os/$arch)"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsSL "$url" -o "$tmp/$asset"
tar -xzf "$tmp/$asset" -C "$tmp"

if install -m 0755 "$tmp/kubeview" "$DEST/kubeview" 2>/dev/null; then
  :
else
  echo "elevating with sudo to write $DEST"
  sudo install -m 0755 "$tmp/kubeview" "$DEST/kubeview"
fi

echo "installed: $("$DEST/kubeview" --version)"
