#!/bin/sh
# Build the uploader into dist/: Windows, Linux (x86-64 and ARM) and one Mac program for Apple silicon and Intel.
#
#   VERSION=2026.11.25.1430 UPLOADER_SIGNING_KEY=<key file> scripts/build.sh
#
# VERSION defaults to the current UTC date and time (YYYY.MM.DD.HHMM: a bigger one is newer, which is how installed
# copies decide to update). With UPLOADER_SIGNING_KEY the matching public key is built into the program (it refuses
# updates that aren't signed with the key) and the programs are signed (scripts/sign.sh), unless SKIP_SIGN=1:
# the release workflow builds with the public keys only and signs in a later job, after the (planned) Windows code signing.
#
# UPLOADER_PUBKEY (hex) gives that public key without the private one (with both, they have to match).
# UPLOADER_BACKUP_PUBKEY (hex) is a second key built in beside it: updates signed with either are accepted.
set -e
cd "$(dirname "$0")/.."
mkdir -p dist
VERSION=${VERSION:-$(date -u +%Y.%m.%d.%H%M)}
case "$VERSION" in
  [0-9][0-9][0-9][0-9].[0-9][0-9].[0-9][0-9].[0-9][0-9][0-9][0-9]) ;;
  *) echo "VERSION must look like 2026.11.01.2016, not $VERSION" >&2; exit 1 ;;
esac
# pubkey NAME VALUE: VALUE as 64 lower-case hex digits, or stop. A mistyped key would make a program that refuses
# every update.
pubkey() {
  k=$(printf '%s' "$2" | tr -d '[:space:]' | tr 'A-F' 'a-f')
  case "$k" in
    *[!0-9a-f]*) k= ;;
  esac
  if [ "${#k}" != 64 ]; then
    echo "$1 must be a public key: 64 hex digits (go run ./tools/sign pub)" >&2
    exit 1
  fi
  printf '%s' "$k"
}
PUB=
if [ -n "$UPLOADER_PUBKEY" ]; then PUB=$(pubkey UPLOADER_PUBKEY "$UPLOADER_PUBKEY"); fi
if [ -n "$UPLOADER_SIGNING_KEY" ]; then
  derived=$(CGO_ENABLED=0 go run ./tools/sign pub -key "$UPLOADER_SIGNING_KEY")
  if [ -n "$PUB" ] && [ "$PUB" != "$derived" ]; then
    echo "UPLOADER_PUBKEY isn't the public key of UPLOADER_SIGNING_KEY" >&2
    exit 1
  fi
  PUB=$derived
fi
if [ -n "$UPLOADER_BACKUP_PUBKEY" ]; then
  backup=$(pubkey UPLOADER_BACKUP_PUBKEY "$UPLOADER_BACKUP_PUBKEY")
  if [ -z "$PUB" ]; then
    echo "UPLOADER_BACKUP_PUBKEY needs UPLOADER_PUBKEY or UPLOADER_SIGNING_KEY beside it" >&2
    exit 1
  fi
  if [ "$backup" = "$PUB" ]; then
    echo "UPLOADER_BACKUP_PUBKEY must be a different key from the release key" >&2
    exit 1
  fi
  PUB=$PUB,$backup
fi
export CGO_ENABLED=0
FLAGS="-s -w -X main.version=$VERSION -X main.updateKey=$PUB"
# The Windows program's icon and file details (winres/winres.json, with VERSION as its version). go build links any
# .syso it finds here, so it is made just for this one build and removed again, also when the build fails.
SYSO=rsrc_windows_amd64.syso
trap 'rm -f "$SYSO"' EXIT
trap 'exit 1' HUP INT TERM
GOOS= GOARCH= go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json --arch amd64 --out rsrc \
  --file-version "$VERSION" --product-version "$VERSION"
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-H windowsgui $FLAGS" -o dist/hots-dash-uploader.exe .
rm "$SYSO"
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$FLAGS" -o dist/hots-dash-uploader-linux-amd64 .
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$FLAGS" -o dist/hots-dash-uploader-linux-arm64 .
GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$FLAGS" -o dist/darwin-arm64 .
GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "$FLAGS" -o dist/darwin-amd64 .
go run ./tools/lipo -out dist/hots-dash-uploader-macos dist/darwin-arm64 dist/darwin-amd64
rm dist/darwin-arm64 dist/darwin-amd64
printf '%s' "$VERSION" > dist/version.txt
if [ -n "$UPLOADER_SIGNING_KEY" ] && [ "$SKIP_SIGN" != 1 ]; then
  scripts/sign.sh
  echo "built and signed dist/ ($VERSION)"
else
  echo "built dist/ ($VERSION, not signed)"
fi
