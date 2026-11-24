#!/bin/sh
# Sign the programs in dist/ with the Ed25519 update key (UPLOADER_SIGNING_KEY: a file made by `go run ./tools/sign keygen`).
# The signature covers the version and the program's sha256 (updatesig package) and goes beside it as <program>.sig
# (base64). Run it after anything that changes the program's bytes (Windows code signing).
set -e
cd "$(dirname "$0")/.."
if [ -z "$UPLOADER_SIGNING_KEY" ] || [ ! -s "$UPLOADER_SIGNING_KEY" ]; then
  echo "UPLOADER_SIGNING_KEY (a key file) is needed" >&2
  exit 1
fi
VERSION=$(cat dist/version.txt)
for f in hots-dash-uploader.exe hots-dash-uploader-linux-amd64 hots-dash-uploader-linux-arm64 hots-dash-uploader-macos; do
  go run ./tools/sign sign -key "$UPLOADER_SIGNING_KEY" -version "$VERSION" -in "dist/$f" -out "dist/$f.sig"
done
