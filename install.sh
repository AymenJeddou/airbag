#!/bin/sh
# Installs the latest airbag release: curl -fsSL https://raw.githubusercontent.com/AymenJeddou/airbag/main/install.sh | sh
set -eu

repo=AymenJeddou/airbag
dir=${AIRBAG_INSTALL_DIR:-$HOME/.local/bin}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $os in
linux | darwin) ;;
*) echo "airbag: no prebuilt binary for $os, use: go install github.com/$repo@latest" >&2 && exit 1 ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) echo "airbag: unsupported CPU $(uname -m)" >&2 && exit 1 ;;
esac

asset="airbag_${os}_${arch}.tar.gz"
url="https://github.com/$repo/releases/latest/download"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsSL "$url/$asset" -o "$tmp/$asset"
curl -fsSL "$url/checksums.txt" -o "$tmp/checksums.txt"
want=$(grep " $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1)
got=$( (sha256sum "$tmp/$asset" 2>/dev/null || shasum -a 256 "$tmp/$asset") | cut -d' ' -f1)
if [ -z "$want" ] || [ "$want" != "$got" ]; then
	echo "airbag: checksum mismatch for $asset, not installing" >&2
	exit 1
fi

tar -xzf "$tmp/$asset" -C "$tmp" airbag
mkdir -p "$dir"
mv "$tmp/airbag" "$dir/airbag"
chmod +x "$dir/airbag"
echo "installed $dir/airbag"
case ":$PATH:" in
*":$dir:"*) ;;
*) echo "add $dir to your PATH" ;;
esac
echo "next: airbag install"
