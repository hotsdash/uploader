#!/bin/sh
# Sign the programs in dist/ with the Ed25519 update key (UPLOADER_SIGNING_KEY: a file made by `go run ./tools/sign keygen`).
# Two signatures go beside each program (base64, updatesig package): <program>.sig covers the version and the program's
# sha256 (v1, which installed copies check), <program>.sig2 the platform as well (v2). Run it after anything that changes the program's bytes (Windows code signing).
set -e
cd "$(dirname "$0")/.."
if [ -z "$UPLOADER_SIGNING_KEY" ] || [ ! -s "$UPLOADER_SIGNING_KEY" ]; then
  echo "UPLOADER_SIGNING_KEY (a key file) is needed" >&2
  exit 1
fi
VERSION=$(cat dist/version.txt)
# platform:program, the platform as latest.json names it
for p in windows:hots-dash-uploader.exe linux-amd64:hots-dash-uploader-linux-amd64 \
  linux-arm64:hots-dash-uploader-linux-arm64 macos:hots-dash-uploader-macos; do
  f=${p#*:}
  go run ./tools/sign sign -key "$UPLOADER_SIGNING_KEY" -version "$VERSION" -platform "${p%%:*}" \
    -in "dist/$f" -out "dist/$f.sig"
done
