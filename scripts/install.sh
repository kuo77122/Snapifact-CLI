#!/bin/sh
set -eu

default_download_base='https://snapifact.dev/downloads'
version=${SNAPIFACT_VERSION:-}
install_dir=${SNAPIFACT_INSTALL_DIR:-}
fail() { printf 'snapifact installer: %s\n' "$*" >&2; exit 1; }
[ -n "$version" ] || fail 'SNAPIFACT_VERSION is required for the source installer'
awk -v v="$version" 'BEGIN { exit !(v ~ /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/) }' || fail "version must be an exact vMAJOR.MINOR.PATCH: $version"
case "$(uname -s)" in Linux) os=linux ;; Darwin) os=darwin ;; *) fail "unsupported operating system: $(uname -s)" ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) fail "unsupported architecture: $(uname -m)" ;; esac
command -v curl >/dev/null 2>&1 || fail 'curl is required'
if [ -n "${SNAPIFACT_DOWNLOAD_BASE:-}" ]; then release_dir="${SNAPIFACT_DOWNLOAD_BASE%/}/$version"; else release_dir="$default_download_base/$version"; fi
tmp=$(mktemp -d "${TMPDIR:-/tmp}/snapifact-install.XXXXXX") || fail 'cannot create private temporary directory'
chmod 700 "$tmp"; trap 'rm -rf "$tmp"' EXIT HUP INT TERM
asset="snapifact_${os}_${arch}"
curl -fsSL -o "$tmp/$asset" -- "$release_dir/$asset" || fail "download failed for $asset"
curl -fsSL -o "$tmp/SHA256SUMS" -- "$release_dir/SHA256SUMS" || fail 'download failed for SHA256SUMS'
expected=$(awk -v asset="$asset" '{ if (length($0) < 67 || substr($0,65,2) != "  " || length(substr($0,1,64)) != 64 || substr($0,1,64) !~ /^[[:xdigit:]]+$/ || substr($0,67) !~ /^[^[:space:]]+$/ || seen[substr($0,67)]++) bad=1; if (substr($0,67) == asset) { found++; expected=substr($0,1,64) } } END { if (bad || found != 1) exit 1; print expected }' "$tmp/SHA256SUMS") || fail 'invalid checksum manifest'
if command -v sha256sum >/dev/null 2>&1; then actual=$(sha256sum -- "$tmp/$asset" | awk '{print $1}'); else actual=$(shasum -a 256 -- "$tmp/$asset" | awk '{print $1}'); fi
[ "$actual" = "$expected" ] || fail "checksum mismatch for $asset"
[ -n "$install_dir" ] || install_dir=$HOME/.local/bin
mkdir -p "$install_dir"; tmp_install=$(mktemp "$install_dir/.snapifact.XXXXXX"); trap 'rm -rf "$tmp"; rm -f "$tmp_install"' EXIT HUP INT TERM
cp "$tmp/$asset" "$tmp_install"; chmod 755 "$tmp_install"; mv -f "$tmp_install" "$install_dir/snapifact"; tmp_install=
printf 'installed %s\n' "$install_dir/snapifact"
