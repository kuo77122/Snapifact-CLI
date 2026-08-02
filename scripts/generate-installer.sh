#!/usr/bin/env bash
set -euo pipefail

usage() { printf 'usage: %s OUTPUT vMAJOR.MINOR.PATCH\n' "$0" >&2; exit 2; }
[[ $# -eq 2 ]] || usage
output=$1
version=$2
[[ "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || usage
mkdir -p "$(dirname -- "$output")"

cat >"$output" <<'INSTALLER'
#!/bin/sh
set -eu

default_download_base='https://snapifact.dev/downloads'
default_version='@VERSION@'
version=${SNAPIFACT_VERSION:-$default_version}
install_dir=${SNAPIFACT_INSTALL_DIR:-}

fail() {
	printf 'snapifact installer: %s\n' "$*" >&2
	exit 1
}

awk -v v="$version" 'BEGIN { exit !(v ~ /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/) }' || fail "version must be an exact vMAJOR.MINOR.PATCH: $version"
case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) fail "unsupported operating system: $(uname -s)" ;;
esac
case "$(uname -m)" in
	x86_64|amd64) arch=amd64 ;;
	aarch64|arm64) arch=arm64 ;;
	*) fail "unsupported architecture: $(uname -m)" ;;
esac

command -v curl >/dev/null 2>&1 || fail 'curl is required'
if command -v sha256sum >/dev/null 2>&1; then checksum_tool=sha256sum
elif command -v shasum >/dev/null 2>&1; then checksum_tool=shasum
else fail 'sha256sum or shasum is required'; fi

asset="snapifact_${os}_${arch}"
if [ -n "${SNAPIFACT_DOWNLOAD_BASE:-}" ]; then
	release_dir="${SNAPIFACT_DOWNLOAD_BASE%/}/$version"
else
	release_dir="$default_download_base/$version"
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/snapifact-install.XXXXXX") || fail 'cannot create private temporary directory'
chmod 700 "$tmp"
tmp_install=
cleanup() { rm -rf "$tmp"; [ -z "${tmp_install:-}" ] || rm -f "$tmp_install"; }
trap cleanup EXIT HUP INT TERM
binary="$tmp/$asset"
checksums="$tmp/SHA256SUMS"
curl -fsSL -o "$binary" -- "$release_dir/$asset" || fail "download failed for $asset"
curl -fsSL -o "$checksums" -- "$release_dir/SHA256SUMS" || fail 'download failed for SHA256SUMS'

expected=$(awk -v asset="$asset" '
BEGIN { malformed = 0; found = 0 }
{
 if (length($0) < 67 || substr($0,65,2) != "  " || length(substr($0,1,64)) != 64 || substr($0,1,64) !~ /^[[:xdigit:]]+$/ || substr($0,67) !~ /^[^[:space:]]+$/) { malformed=1; next }
 name=substr($0,67)
 if (name != "snapifact_linux_amd64" && name != "snapifact_linux_arm64" && name != "snapifact_darwin_amd64" && name != "snapifact_darwin_arm64" && name != "install.sh") { malformed=1; next }
 if (seen[name]++) malformed=1
 if (name == asset) { found++; expected=substr($0,1,64) }
}
END { if (malformed || found != 1 || length(seen) != 5) exit 1; print expected }
' "$checksums") || fail 'invalid checksum manifest'
case "$checksum_tool" in
	sha256sum) actual=$(sha256sum -- "$binary" | awk '{print $1}') ;;
	shasum) actual=$(shasum -a 256 -- "$binary" | awk '{print $1}') ;;
esac
[ "$(printf '%s' "$actual" | tr '[:upper:]' '[:lower:]')" = "$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')" ] || fail "checksum mismatch for $asset"

if [ -z "$install_dir" ]; then [ -n "${HOME:-}" ] || fail 'HOME is not set'; install_dir=$HOME/.local/bin; fi
mkdir -p "$install_dir" || fail "cannot create install directory: $install_dir"
tmp_install=$(mktemp "$install_dir/.snapifact.XXXXXX") || fail "cannot create install temporary file in $install_dir"
cp "$binary" "$tmp_install" || fail "cannot prepare installed binary in $install_dir"
chmod 755 "$tmp_install" || fail "cannot prepare installed binary in $install_dir"
mv -f "$tmp_install" "$install_dir/snapifact" || fail "cannot replace $install_dir/snapifact"
tmp_install=
printf 'installed %s\n' "$install_dir/snapifact"
INSTALLER

sed "s/@VERSION@/$version/" "$output" >"$output.tmp"
mv "$output.tmp" "$output"
chmod 755 "$output"
