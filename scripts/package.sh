#!/bin/sh
# Make the first-download archives and latest.json from the signed programs in dist/.
#
# The Linux and Mac downloads are a .tar.gz (a browser drops the "can run" bit of a bare program). They hold only the
# program and are made the same every time: sorted, fixed date, root owner, no gzip name or date. So the same program
# always gives the same archive, byte for byte. The Windows download is the .exe itself.
# UNSIGNED=1 lets latest.json be made without signatures (trying a build locally only).
set -e
cd "$(dirname "$0")/.."
VERSION=$(cat dist/version.txt)
for p in linux-amd64 linux-arm64 macos; do
  t=$(mktemp -d)
  mkdir "$t/hots-dash-uploader"
  cp "dist/hots-dash-uploader-$p" "$t/hots-dash-uploader/hots-dash-uploader"
  chmod 755 "$t/hots-dash-uploader" "$t/hots-dash-uploader/hots-dash-uploader"
  tar --sort=name --mtime='2020-01-01 00:00:00 UTC' --owner=0 --group=0 --numeric-owner --format=ustar \
    -cf - -C "$t" hots-dash-uploader | gzip -9n > "dist/hots-dash-uploader-$p.tar.gz"
  rm -rf "$t"
done
flag=
[ "$UNSIGNED" = 1 ] && flag=-unsigned
go run ./tools/manifest -dir dist -version "$VERSION" -out dist/latest.json $flag
