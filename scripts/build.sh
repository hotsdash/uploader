#!/bin/sh
# Build the uploader into dist/: Windows, Linux (x86-64 and ARM) and one Mac program for Apple silicon and Intel.
#
#   VERSION=2026.11.25.1430 UPLOADER_SIGNING_KEY=<key file> scripts/build.sh
#
# VERSION defaults to the current UTC date and time (YYYY.MM.DD.HHMM: a bigger one is newer, which is how installed
# copies decide to update). With UPLOADER_SIGNING_KEY the matching public key is built into the program (it refuses
# updates that aren't signed with the key) and the programs are signed (scripts/sign.sh), unless SKIP_SIGN=1:
# the release workflow signs in a later step, after the (planned) Windows code signing.
set -e
cd "$(dirname "$0")/.."
mkdir -p dist
VERSION=${VERSION:-$(date -u +%Y.%m.%d.%H%M)}
case "$VERSION" in
  [0-9][0-9][0-9][0-9].[0-9][0-9].[0-9][0-9].[0-9][0-9][0-9][0-9]) ;;
  *) echo "VERSION must look like 2026.11.01.2016, not $VERSION" >&2; exit 1 ;;
esac
PUB=
if [ -n "$UPLOADER_SIGNING_KEY" ]; then PUB=$(CGO_ENABLED=0 go run ./tools/sign pub -key "$UPLOADER_SIGNING_KEY"); fi
export CGO_ENABLED=0
FLAGS="-s -w -X main.version=$VERSION -X main.updateKey=$PUB"
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-H windowsgui $FLAGS" -o dist/hots-dash-uploader.exe .
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
