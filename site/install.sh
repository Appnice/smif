#!/bin/sh
# Installs the latest smif release into ~/.local/bin, or $SMIF_INSTALL_DIR.
# Source: https://github.com/smifdev/smif/blob/main/site/install.sh
set -eu

fail() {
  echo "smif: $1" >&2
  exit 1
}

main() {
  case $(uname -s) in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) fail "no binary for $(uname -s): https://github.com/smifdev/smif#install" ;;
  esac
  case $(uname -m) in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) fail "no binary for $(uname -m): https://github.com/smifdev/smif#install" ;;
  esac
  command -v curl >/dev/null || fail "curl is needed"
  if command -v sha256sum >/dev/null; then
    sum="sha256sum"
  elif command -v shasum >/dev/null; then
    sum="shasum -a 256"
  else
    fail "sha256sum or shasum is needed"
  fi

  name="smif_${os}_${arch}.tar.gz"
  url="https://github.com/smifdev/smif/releases/latest/download"
  dir="${SMIF_INSTALL_DIR:-$HOME/.local/bin}"
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT

  echo "Downloading $name"
  curl -fsSL -o "$tmp/$name" "$url/$name"
  curl -fsSL -o "$tmp/checksums.txt" "$url/checksums.txt"
  grep " $name\$" "$tmp/checksums.txt" >"$tmp/expected" || fail "$name is not in checksums.txt"
  (cd "$tmp" && $sum -c expected >/dev/null) || fail "checksum mismatch for $name"
  tar xzf "$tmp/$name" -C "$tmp" smif

  mkdir -p "$dir"
  cp "$tmp/smif" "$dir/.smif.new"
  chmod 755 "$dir/.smif.new"
  mv "$dir/.smif.new" "$dir/smif"
  echo "Installed $("$dir/smif" --version) in $dir"

  case ":$PATH:" in
    *":$dir:"*) echo "Run: smif" ;;
    *) echo "$dir is not on your PATH. Add it to your shell profile, or run: $dir/smif" ;;
  esac
}

main
