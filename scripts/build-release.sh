#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GO=${GO:-go}
export GOCACHE=${GOCACHE:-$ROOT/.gocache}

binaries=(snapifact_linux_amd64 snapifact_linux_arm64 snapifact_darwin_amd64 snapifact_darwin_arm64)
manifest_assets=("${binaries[@]}" install.sh)
release_assets=("${manifest_assets[@]}" SHA256SUMS)

fail() { printf 'release build: %s\n' "$*" >&2; exit 1; }
hash_file() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum -- "$1" | cut -d ' ' -f1
	else shasum -a 256 -- "$1" | cut -d ' ' -f1
	fi
}
is_manifest_asset() {
	case "$1" in snapifact_linux_amd64|snapifact_linux_arm64|snapifact_darwin_amd64|snapifact_darwin_arm64|install.sh) return 0 ;; *) return 1 ;; esac
}
is_release_asset() { is_manifest_asset "$1" || [[ "$1" == SHA256SUMS ]]; }

validate_version() {
	[[ "$1" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]
}

write_manifest() {
	local directory=$1 asset
	: >"$directory/SHA256SUMS"
	for asset in "${manifest_assets[@]}"; do printf '%s  %s\n' "$(hash_file "$directory/$asset")" "$asset" >>"$directory/SHA256SUMS"; done
}

validate_release_dir() {
	local directory=$1 manifest line hash name actual seen=''
	[[ -d "$directory" ]] || fail "release directory is missing: $directory"
	shopt -s nullglob dotglob
	local paths=("$directory"/*)
	shopt -u dotglob nullglob
	local path
	for path in "${paths[@]}"; do
		[[ -f "$path" && ! -L "$path" ]] || fail "release asset is not a regular file: ${path##*/}"
		is_release_asset "${path##*/}" || fail "unexpected release asset: ${path##*/}"
	done
	[[ ${#paths[@]} -eq 6 ]] || fail 'release must contain exactly six assets'
	manifest=$directory/SHA256SUMS
	[[ -f "$manifest" ]] || fail 'missing checksum manifest'
	while IFS= read -r line || [[ -n "$line" ]]; do
		[[ "$line" =~ ^([[:xdigit:]]{64})[[:space:]][[:space:]]([^[:space:]]+)$ ]] || fail 'malformed checksum entry'
		hash=${BASH_REMATCH[1]}; name=${BASH_REMATCH[2]}
		is_manifest_asset "$name" || fail "unexpected checksum asset: $name"
		case " $seen " in *" $name "*) fail "duplicate checksum entry: $name" ;; esac
		seen="$seen $name"
		actual=$(hash_file "$directory/$name")
		[[ "${actual,,}" == "${hash,,}" ]] || fail "checksum mismatch: $name"
	done <"$manifest"
	[[ $(wc -l <"$manifest") -eq 5 ]] || fail 'checksum manifest must contain exactly five entries'
	for name in "${manifest_assets[@]}"; do case " $seen " in *" $name "*) ;; *) fail "missing checksum entry: $name" ;; esac; done
}

build_assets() {
	local output=$1 build_version=$2 target os arch asset
	rm -rf "$output"; mkdir -p "$output"
	local ldflags="-s -w -buildid= -X main.version=$build_version"
	for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
		os=${target%/*}; arch=${target#*/}; asset="snapifact_${os}_${arch}"
		CGO_ENABLED=0 GOFLAGS=-mod=readonly GOOS="$os" GOARCH="$arch" "$GO" build -trimpath -buildvcs=false -ldflags "$ldflags" -o "$output/$asset" ./cmd/snapifact
	done
	"$ROOT/scripts/generate-installer.sh" "$output/install.sh" "$build_version"
	write_manifest "$output"
	validate_release_dir "$output"
}

assert_same_assets() {
	local first=$1 second=$2 asset
	for asset in "${release_assets[@]}"; do cmp -s "$first/$asset" "$second/$asset" || fail "non-reproducible asset: $asset"; done
}

test_release() {
	local tmp asset
	tmp=$(mktemp -d "${TMPDIR:-/tmp}/snapifact-release-test.XXXXXX")
	trap 'rm -rf "$tmp"' RETURN
	build_assets "$tmp/first" v0.0.0
	build_assets "$tmp/second" v0.0.0
	assert_same_assets "$tmp/first" "$tmp/second"
	cp -a "$tmp/first" "$tmp/tampered"
	printf tampered >>"$tmp/tampered/snapifact_linux_amd64"
	if (validate_release_dir "$tmp/tampered"); then fail 'tampered bytes were accepted'; fi
	for asset in v01.2.3 v1.02.3 v1.2.03 nope; do if validate_version "$asset" 2>/dev/null; then fail "malformed version accepted: $asset"; fi; done
	printf 'release build test passed\n'
}

usage() { printf 'usage: %s test|validate RELEASE_DIR|vMAJOR.MINOR.PATCH\n' "$0" >&2; exit 2; }
[[ $# -ge 1 ]] || usage
case "$1" in
	test) [[ $# -eq 1 ]] || usage; test_release ;;
	validate) [[ $# -eq 2 ]] || usage; validate_release_dir "$2" ;;
	v*)
		[[ $# -eq 1 ]] || usage
		version=$1; validate_version "$version" || fail "version must be an exact vMAJOR.MINOR.PATCH: $version"
		[[ "${GITHUB_REF:-}" == "refs/tags/$version" ]] || fail 'GITHUB_REF is not the canonical version tag'
		[[ "${GITHUB_REF_NAME:-}" == "$version" ]] || fail 'GITHUB_REF_NAME is not the canonical version'
		head=$(git -C "$ROOT" rev-parse HEAD)
		[[ "${GITHUB_SHA:-}" == "$head" ]] || fail 'GITHUB_SHA does not match local HEAD'
		tag_commit=$(git -C "$ROOT" rev-parse --verify "refs/tags/$version^{commit}" 2>/dev/null) || fail "version tag is not present: $version"
		[[ "$tag_commit" == "$head" ]] || fail 'peeled tag commit does not match local HEAD'
		mkdir -p "$ROOT/dist"; build_assets "$ROOT/dist" "$version"; printf 'release assets written to %s/dist\n' "$ROOT" ;;
	*) usage ;;
esac
