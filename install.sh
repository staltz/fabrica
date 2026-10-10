#!/usr/bin/env bash
# Installs the fabrica binary.
#
#   curl -fsSL https://raw.githubusercontent.com/staltz/fabrica/main/install.sh | bash
#
# Settings (all optional):
#   FABRICA_VERSION      a release tag like v0.1.0 (default: the latest release)
#   FABRICA_INSTALL_DIR  where to put the binary (default: ~/.local/bin)
#   FABRICA_DOWNLOAD_URL where the release files are (default: GitHub
#                        releases), for mirrors
#
# It downloads the static binary for your OS and CPU from the GitHub release,
# checks it against the release's checksums, and puts it in place. Nothing
# else on your system is touched.

set -euo pipefail

repo="staltz/fabrica"
version="${FABRICA_VERSION:-latest}"
install_dir="${FABRICA_INSTALL_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "$*"; }
fail() { printf 'fabrica install: %s\n' "$*" >&2; exit 1; }

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "unsupported OS $(uname -s); build from source with: go install github.com/$repo/cmd/fabrica@latest" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "unsupported CPU $(uname -m); build from source with: go install github.com/$repo/cmd/fabrica@latest" ;;
esac

if [ -n "${FABRICA_DOWNLOAD_URL:-}" ]; then
  base="${FABRICA_DOWNLOAD_URL%/}"
elif [ "$version" = "latest" ]; then
  base="https://github.com/$repo/releases/latest/download"
else
  base="https://github.com/$repo/releases/download/$version"
fi
asset="fabrica-$os-$arch"

command -v curl >/dev/null || fail "curl is needed to download fabrica"
command -v git >/dev/null || say "Note: fabrica needs git to work, and git isn't on your PATH yet."

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

say "Downloading $asset ($version)..."
curl -fsSL "$base/$asset" -o "$tmp/$asset" || fail "couldn't download $base/$asset"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || fail "couldn't download the checksums"

expected="$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/checksums.txt")"
[ -n "$expected" ] || fail "the release has no checksum for $asset"
if command -v sha256sum >/dev/null; then
  actual="$(sha256sum "$tmp/$asset" | awk '{ print $1 }')"
else
  actual="$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')"
fi
[ "$expected" = "$actual" ] || fail "checksum mismatch for $asset (expected $expected, got $actual)"

mkdir -p "$install_dir"
chmod +x "$tmp/$asset"
mv "$tmp/$asset" "$install_dir/fabrica"
say "Installed $("$install_dir/fabrica" version) to $install_dir/fabrica"
say "To update it later, run: fabrica self-update"

case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) say "Add $install_dir to your PATH to run it as 'fabrica', for example:"
     say "  echo 'export PATH=\"$install_dir:\$PATH\"' >> ~/.bashrc" ;;
esac
