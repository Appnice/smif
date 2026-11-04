#!/usr/bin/env bash
# Renders docs/brand/icon.svg (python3 docs/brand/make.py) into internal/notify/smif.icns, the icon of the
# small Smif.app that shows smif's macOS notifications. macOS only: QuickLook, sips and iconutil come with it.
set -euo pipefail
cd "$(dirname "$0")"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
qlmanage -t -s 1024 -o "$tmp" icon.svg >/dev/null
set=$tmp/smif.iconset; mkdir "$set"
for s in 16 32 128 256 512; do
  sips -z $s $s "$tmp/icon.svg.png" --out "$set/icon_${s}x${s}.png" >/dev/null
  sips -z $((s * 2)) $((s * 2)) "$tmp/icon.svg.png" --out "$set/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$set" -o ../../internal/notify/smif.icns
echo "internal/notify/smif.icns"
