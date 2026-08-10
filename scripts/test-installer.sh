#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
INSTALLER=$ROOT/scripts/install.sh
GO=${GO:-go}
TMP=$(mktemp -d "${TMPDIR:-/tmp}/snapifact-installer-test.XXXXXX")
ORIGINAL_PATH=$PATH
SERVER_PID=
REAL_CURL=$(command -v curl)
fail() { printf 'installer test failed: %s\n' "$*" >&2; exit 1; }
cleanup() { [ -z "$SERVER_PID" ] || { kill "$SERVER_PID" 2>/dev/null || true; wait "$SERVER_PID" 2>/dev/null || true; }; rm -rf "$TMP"; }
trap cleanup EXIT HUP INT TERM
hash_file() { if command -v sha256sum >/dev/null 2>&1; then sha256sum -- "$1" | cut -d ' ' -f1; else shasum -a 256 -- "$1" | cut -d ' ' -f1; fi; }
assert_contains() { [[ "$1" == *"$2"* ]] || fail "missing $2 in: $1"; }
assert_mode_755() { local mode; mode=$(stat -c '%a' -- "$1" 2>/dev/null || stat -f '%Lp' "$1"); [[ "$mode" == 755 ]] || fail "mode $mode for $1"; }

mkdir -p "$TMP/fake-bin" "$TMP/fixture/v1.2.3" "$TMP/canonical/downloads/v1.2.3" "$TMP/downloads"
cat >"$TMP/fake-bin/uname" <<'EOF'
#!/bin/sh
case "$1" in -s) printf '%s\n' "${FAKE_UNAME_S:-Linux}" ;; -m) printf '%s\n' "${FAKE_UNAME_M:-x86_64}" ;; *) exit 1 ;; esac
EOF
chmod 755 "$TMP/fake-bin/uname"
cat >"$TMP/fake-bin/curl" <<EOF
#!/bin/sh
set -eu
url=
output=
previous=
after_separator=
for arg in "\$@"; do
  if [ "\$previous" = -o ]; then output=\$arg; previous=; continue; fi
  if [ "\$after_separator" = yes ]; then url=\$arg; continue; fi
  case "\$arg" in
    -o) previous=-o ;;
    --) after_separator=yes ;;
  esac
done
case "\$url" in
  https://snapifact.dev/downloads/*)
    printf '%s\n' "\${url#https://snapifact.dev}" >>"$TMP/requests"
    source="$TMP/canonical\${url#https://snapifact.dev}"
    [ -f "\$source" ] || exit 22
    cp "\$source" "\$output"
    ;;
  *) exec "$REAL_CURL" "\$@" ;;
esac
EOF
chmod 755 "$TMP/fake-bin/curl"

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
	os=${target%/*}; arch=${target#*/}; asset="snapifact_${os}_${arch}"
	CGO_ENABLED=0 GOFLAGS=-mod=readonly GOOS="$os" GOARCH="$arch" "$GO" build -trimpath -buildvcs=false -ldflags='-s -w -buildid= -X main.version=v1.2.3' -o "$TMP/fixture/v1.2.3/$asset" ./cmd/snapifact
done
"$ROOT/scripts/generate-installer.sh" "$TMP/fixture/v1.2.3/install.sh" v1.2.3
for asset in snapifact_linux_amd64 snapifact_linux_arm64 snapifact_darwin_amd64 snapifact_darwin_arm64 install.sh; do printf '%s  %s\n' "$(hash_file "$TMP/fixture/v1.2.3/$asset")" "$asset"; done >"$TMP/fixture/v1.2.3/SHA256SUMS"
cp -a "$TMP/fixture/v1.2.3/." "$TMP/canonical/downloads/v1.2.3/"

run_install() {
	local release_parent=$1 install_dir=$2
	HOME="$TMP/home" SNAPIFACT_VERSION=v1.2.3 SNAPIFACT_INSTALL_DIR="$install_dir" SNAPIFACT_DOWNLOAD_BASE="file://$release_parent" TMPDIR="$TMP/downloads" PATH="$TMP/fake-bin:$ORIGINAL_PATH" FAKE_UNAME_S="$FAKE_UNAME_S" FAKE_UNAME_M="$FAKE_UNAME_M" sh "$INSTALLER"
}
run_generated() {
	local install_dir=$1
	HOME="$TMP/home" SNAPIFACT_INSTALL_DIR="$install_dir" TMPDIR="$TMP/downloads" PATH="$TMP/fake-bin:$ORIGINAL_PATH" FAKE_UNAME_S="$FAKE_UNAME_S" FAKE_UNAME_M="$FAKE_UNAME_M" sh "$TMP/fixture/v1.2.3/install.sh"
}

FAKE_UNAME_S=Linux FAKE_UNAME_M=x86_64
generated_output=$(run_generated "$TMP/generated-install")
assert_contains "$generated_output" "$TMP/generated-install/snapifact"
[[ $(wc -l <"$TMP/requests") -eq 2 ]] || fail 'generated installer did not fetch exactly two canonical paths'
! grep -q '/latest/' "$TMP/requests" || fail 'generated installer used latest'

for target in Linux:x86_64:amd64 Linux:amd64:amd64 Linux:aarch64:arm64 Darwin:x86_64:amd64 Darwin:arm64:arm64; do
	IFS=: read -r FAKE_UNAME_S FAKE_UNAME_M expected_arch <<<"$target"
	dir="$TMP/installed-$FAKE_UNAME_S-$FAKE_UNAME_M"
	run_install "$TMP/fixture" "$dir" >/dev/null
	assert_mode_755 "$dir/snapifact"
	case "$FAKE_UNAME_S" in Linux) expected_os=linux ;; Darwin) expected_os=darwin ;; esac
	[[ "$(hash_file "$dir/snapifact")" == "$(hash_file "$TMP/fixture/v1.2.3/snapifact_${expected_os}_${expected_arch}")" ]] || fail 'wrong platform asset selected'
done

FAKE_UNAME_S=FreeBSD FAKE_UNAME_M=x86_64
if output=$(run_install "$TMP/fixture" "$TMP/unsupported" 2>&1); then fail 'unsupported OS was accepted'; else assert_contains "$output" 'unsupported operating system'; fi
FAKE_UNAME_S=Linux FAKE_UNAME_M=s390x
if output=$(run_install "$TMP/fixture" "$TMP/unsupported" 2>&1); then fail 'unsupported architecture was accepted'; else assert_contains "$output" 'unsupported architecture'; fi
FAKE_UNAME_S=Linux FAKE_UNAME_M=x86_64
if output=$(SNAPIFACT_VERSION=v1.2.3.4 SNAPIFACT_INSTALL_DIR="$TMP/invalid" PATH="$TMP/fake-bin:$ORIGINAL_PATH" sh "$TMP/fixture/v1.2.3/install.sh" 2>&1); then fail 'invalid version was accepted'; else assert_contains "$output" 'version must be an exact'; fi

bad="$TMP/bad-tampered/v1.2.3"; mkdir -p "$bad"; cp -a "$TMP/fixture/v1.2.3/." "$bad/"; printf tampered >>"$bad/snapifact_linux_amd64"
mkdir -p "$TMP/safe"; printf existing >"$TMP/safe/snapifact"; before=$(hash_file "$TMP/safe/snapifact")
if output=$(run_install "$TMP/bad-tampered" "$TMP/safe" 2>&1); then fail 'tampered asset was installed'; else assert_contains "$output" 'checksum mismatch'; fi
[[ "$before" == "$(hash_file "$TMP/safe/snapifact")" ]] || fail 'existing binary was replaced after verification failure'
! compgen -G "$TMP/downloads/snapifact-install.*" >/dev/null || fail 'private temporary files were left behind'

for command in --help -h version; do output=$(HOME="$TMP/home" "$TMP/generated-install/snapifact" "$command"); case "$command" in version) [[ "$output" == 'snapifact v1.2.3' ]] || fail "version output: $output" ;; *) assert_contains "$output" 'usage: snapifact' ;; esac; done
for command in diff compare text markdown mermaid html csv delete; do "$TMP/generated-install/snapifact" "$command" --help >/dev/null 2>&1 || fail "$command help failed"; done

cat >"$TMP/server.go" <<'EOF'
package main
import("encoding/json";"fmt";"net";"net/http";"os";"sync")
var mu sync.Mutex; var live=true
func main(){ l,_:=net.Listen("tcp","127.0.0.1:0"); os.WriteFile(os.Args[1],[]byte(fmt.Sprint(l.Addr().(*net.TCPAddr).Port)),0600); http.Serve(l,http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){ mu.Lock(); defer mu.Unlock(); switch { case r.Method=="POST"&&r.URL.Path=="/v1/snapshots": w.WriteHeader(201); json.NewEncoder(w).Encode(map[string]string{"id":"kpm2q6xxyegw5czekhga","url":"http://"+r.Host+"/v/kpm2q6xxyegw5czekhga","expires_at":"2026-08-13T00:00:00Z","delete_token":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}); live=true; case r.Method=="GET"&&r.URL.Path=="/v/kpm2q6xxyegw5czekhga"&&live: w.WriteHeader(200); case r.Method=="DELETE"&&r.URL.Path=="/v1/snapshots/kpm2q6xxyegw5czekhga"&&r.Header.Get("Authorization")=="Bearer AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA": live=false; w.WriteHeader(204); default: w.WriteHeader(404) }})) }
EOF
"$GO" run "$TMP/server.go" "$TMP/port" >/dev/null 2>&1 & SERVER_PID=$!
for _ in $(seq 1 50); do [ -s "$TMP/port" ] && break; sleep .1; done
[ -s "$TMP/port" ] || fail 'local HTTP handler did not start'
port=$(<"$TMP/port"); printf content >"$TMP/input.txt"; state="$TMP/state"
url=$(SNAPIFACT_SERVER="http://127.0.0.1:$port" SNAPIFACT_STATE_DIR="$state" "$TMP/generated-install/snapifact" text "$TMP/input.txt")
[[ "$url" == http://127.0.0.1:$port/v/kpm2q6xxyegw5czekhga ]] || fail "unexpected create URL: $url"
[[ "$(curl -s -o /dev/null -w '%{http_code}' "$url")" == 200 ]] || fail 'view did not return 200'
SNAPIFACT_SERVER="http://127.0.0.1:$port" SNAPIFACT_STATE_DIR="$state" "$TMP/generated-install/snapifact" delete "$url"
[[ "$(curl -s -o /dev/null -w '%{http_code}' "$url")" == 404 ]] || fail 'post-delete view did not return 404'
printf 'installer tests passed\n'
