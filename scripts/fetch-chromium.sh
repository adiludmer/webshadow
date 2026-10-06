#!/bin/sh
# Downloads the pinned Chromium snapshot for GOOS/GOARCH (default: the host)
# into internal/chromium/bundle/, verifying it against chromium.lock, so a
# build with -tags chromium_bundle can embed it.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
lock="$root/internal/chromium/chromium.lock"
out="$root/internal/chromium/bundle"
goos=${GOOS:-$(go env GOOS)}
goarch=${GOARCH:-$(go env GOARCH)}

line=$(grep -v '^#' "$lock" | awk -v o="$goos" -v a="$goarch" '$1 == o && $2 == a')
if [ -z "$line" ]; then
	echo "fetch-chromium: no pinned Chromium for $goos/$goarch" >&2
	exit 1
fi
set -- $line

# sha256 FILE prints the file's SHA-256 (sha256sum on Linux, shasum on macOS).
sha256() {
	if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1
}
platform=$3 revision=$4 zip=$5 sum=$6

mkdir -p "$out"
if [ -f "$out/REVISION" ] && [ "$(cat "$out/REVISION")" = "$platform/$revision" ] &&
	[ -f "$out/chromium.zip" ] && [ "$(sha256 "$out/chromium.zip")" = "$sum" ]; then
	echo "fetch-chromium: $platform/$revision already present"
	exit 0
fi
url="https://storage.googleapis.com/chromium-browser-snapshots/$platform/$revision/$zip"
echo "fetch-chromium: $url"
curl -fSL --retry 3 -o "$out/chromium.zip.tmp" "$url"
if [ "$(sha256 "$out/chromium.zip.tmp")" != "$sum" ]; then
	echo "fetch-chromium: checksum mismatch for $url" >&2
	rm -f "$out/chromium.zip.tmp"
	exit 1
fi
mv "$out/chromium.zip.tmp" "$out/chromium.zip"
echo "$platform/$revision" > "$out/REVISION"
