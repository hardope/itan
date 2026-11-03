#!/bin/bash
# Builds Itan.app, with ffmpeg inside, and a disk image to install it from.
#   packaging/macos/build.sh <version> <arm64|amd64> [out-dir]
# Writes <out-dir>/Itan-<version>-macos-<arch>.dmg (out-dir: dist).
#
# The app is signed ad hoc, not with an Apple Developer ID, so macOS asks
# before opening it the first time (see the README).
set -euo pipefail
version=$1
arch=$2
out=${3:-dist}
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
minos=13.0 # what Go itself supports
case "$arch" in
amd64) clang_arch=x86_64 ;;
arm64) clang_arch=arm64 ;;
*) echo "unknown architecture $arch" >&2; exit 2 ;;
esac

# shellcheck source=../ffmpeg.env
. "$root/packaging/ffmpeg.env"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

echo "--- ffmpeg $FFMPEG_VERSION for $arch"
ARCH=$(echo "$arch" | tr '[:lower:]' '[:upper:]')
build_var=FFMPEG_MACOS_${ARCH}_BUILD
cache=${FFMPEG_CACHE:-$root/dist/ffmpeg-cache}/macos-$arch-${!build_var}
mkdir -p "$cache"
for tool in ffmpeg ffprobe; do
	if [ ! -x "$cache/$tool" ]; then
		sum_var=FFMPEG_MACOS_${ARCH}_$(echo "$tool" | tr '[:lower:]' '[:upper:]')_SHA256
		curl -fsSL -o "$work/$tool.zip" "$FFMPEG_MACOS_URL/$arch/${!build_var}/$tool.zip"
		echo "${!sum_var}  $work/$tool.zip" | shasum -a 256 -c -
		unzip -oq "$work/$tool.zip" -d "$cache"
	fi
done

echo "--- Itan.app $version for $arch"
app="$work/dmg/Itan.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
(cd "$root" && CGO_ENABLED=1 GOOS=darwin GOARCH="$arch" CC="clang -arch $clang_arch" \
	CGO_CFLAGS="-O2 -mmacosx-version-min=$minos" CGO_LDFLAGS="-mmacosx-version-min=$minos" \
	go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$app/Contents/MacOS/Itan" ./cmd/itan-desktop)
cp "$cache/ffmpeg" "$cache/ffprobe" "$app/Contents/MacOS/"
cp "$here/Itan.icns" "$app/Contents/Resources/"
# CFBundleShortVersionString is numbers only: 0.2.0-rc.1 becomes 0.2.0.
sed -e "s/@VERSION@/${version%%-*}/g" -e "s/@MINOS@/$minos/g" "$here/Info.plist" >"$app/Contents/Info.plist"
plutil -lint "$app/Contents/Info.plist" >/dev/null
res="$app/Contents/Resources"
cp "$root/LICENSE" "$res/LICENSE.txt"
cp "$root/packaging/ffmpeg-NOTICE.txt" "$root/packaging/GPL-3.0.txt" "$res/"
(cd "$root" && go run ./packaging/notices ./cmd/itan-desktop) >"$res/THIRD-PARTY.txt"

# Ad hoc signature: Apple Silicon only runs signed code. ffmpeg and ffprobe
# keep their own signatures.
codesign --force --sign - "$app"
codesign --verify --verbose=1 "$app"

echo "--- disk image"
ln -s /Applications "$work/dmg/Applications"
mkdir -p "$out"
dmg="$out/Itan-$version-macos-$arch.dmg"
hdiutil create -quiet -volname "Itan" -srcfolder "$work/dmg" -ov -format UDZO "$dmg"
echo "wrote $dmg"
