#!/usr/bin/env bash
# Builds Fabrica.app, with the fabrica CLI inside it, into dist/.
#
#   macos/build.sh                 # or: make app
#   VERSION=v0.1.0 macos/build.sh  # stamp a release version
#
# Both the app and the CLI are universal (Apple silicon and Intel). The app
# is signed ad hoc, which is enough to run it on the Mac that built it. Set
# CODESIGN_IDENTITY to a Developer ID ("Developer ID Application: …") to
# sign it for other Macs, with the hardened runtime that notarization needs.
# When it first opens, the app links ~/.local/bin/fabrica to the CLI inside
# it, so installing the app installs the CLI.

set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
out="${OUT:-$root/dist}"
app="$out/Fabrica.app"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

version="${VERSION:-}"
ldflags="-s -w"
if [ -n "$version" ]; then
  ldflags="$ldflags -X github.com/staltz/fabrica/internal/cli.version=$version"
fi
# The app's version: the release, or the latest tag for builds in between.
app_version="${version:-$(git -C "$root" describe --tags --abbrev=0 2>/dev/null || echo v0.0.0)}"
app_version="${app_version#v}"

echo "Building the fabrica CLI"
for arch in arm64 amd64; do
  (cd "$root" && CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "$ldflags" \
    -o "$tmp/fabrica-$arch" ./cmd/fabrica)
done
lipo -create -output "$tmp/fabrica" "$tmp/fabrica-arm64" "$tmp/fabrica-amd64"

echo "Building Fabrica.app"
swift_flags=(-c release --package-path "$root/macos" --arch arm64 --arch x86_64)
swift build "${swift_flags[@]}"
bin="$(swift build "${swift_flags[@]}" --show-bin-path)/Fabrica"

echo "Drawing the icon"
swift "$root/macos/scripts/make-icon.swift" "$tmp/AppIcon.iconset" >/dev/null
iconutil -c icns -o "$tmp/AppIcon.icns" "$tmp/AppIcon.iconset"

rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Helpers" "$app/Contents/Resources"
cp "$bin" "$app/Contents/MacOS/Fabrica"
cp "$tmp/fabrica" "$app/Contents/Helpers/fabrica"
cp "$tmp/AppIcon.icns" "$app/Contents/Resources/AppIcon.icns"
sed "s/__VERSION__/$app_version/g" "$root/macos/Resources/Info.plist" > "$app/Contents/Info.plist"

identity="${CODESIGN_IDENTITY:--}"
sign_flags=(--force --sign "$identity")
if [ "$identity" != "-" ]; then
  sign_flags+=(--options runtime --timestamp)
fi
codesign "${sign_flags[@]}" "$app/Contents/Helpers/fabrica"
codesign "${sign_flags[@]}" "$app"

echo "Built $app ($app_version, with $("$app/Contents/Helpers/fabrica" version))"
