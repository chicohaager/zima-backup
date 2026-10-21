#!/bin/bash
# Build the Sync & Backup sysext for ZimaOS.
#   ./build.sh [amd64|arm64]      (default: amd64)
# Produces zbackup-<arch>.raw next to this script and prints its sha256.
# The sysext image must be installed as zbackup.raw (the name has to match
# extension-release.zbackup); the arch suffix is for the release listing.
set -euo pipefail
cd "$(dirname "$0")"

ARCH="${1:-amd64}"
case "$ARCH" in
  amd64|arm64) ;;
  *) echo "unsupported arch: $ARCH (amd64|arm64)" >&2; exit 2 ;;
esac

# Packaging is lintux-modkit's sysext/build-sysext.sh (extension-release, -all-root, reproducible
# timestamps, unit lint). CI checks lintux-modkit out and sets LINTUX_MODKIT.
MODKIT="${LINTUX_MODKIT:-../lintux-modkit}"
[ -f "$MODKIT/sysext/build-sysext.sh" ] || { echo "lintux-modkit not found at $MODKIT (set LINTUX_MODKIT)" >&2; exit 1; }
VERSION=$(sed -n 's/^\s*version\s*=\s*"\(.*\)".*/\1/p' cmd/zbackupd/main.go)
[ -n "$VERSION" ] || { echo "version not found" >&2; exit 1; }
OUT="zbackup-${ARCH}.raw"

echo "zbackup v${VERSION} linux/${ARCH}"
tools/fetch-restic.sh "$ARCH" raw/usr/libexec/zbackup/restic
python3 tools/check-i18n.py
# cache-buster: the shell caches module assets aggressively, the version pins them
sed -i -E "s/(styles\.css|app\.js|i18n\.js|wordlist\.js)\?v=[0-9A-Za-z.-]+/\1?v=${VERSION}/g" raw/usr/share/casaos/www/modules/zbackup/index.html
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -ldflags="-s -w" -o raw/usr/bin/zbackupd ./cmd/zbackupd/
# Writes extension-release (ARCHITECTURE, SYSEXT_VERSION_ID) into a copy; raw/ stays as it is.
sh "$MODKIT/sysext/build-sysext.sh" zbackup raw "$ARCH" . "$VERSION"
# An older build-sysext.sh ignores the version argument: refuse an image without it.
RELEASE=$(unsquashfs -cat "$OUT" usr/lib/extension-release.d/extension-release.zbackup)
grep -qx "SYSEXT_VERSION_ID=$VERSION" <<<"$RELEASE" \
  || { echo "$OUT has no SYSEXT_VERSION_ID=$VERSION - lintux-modkit too old?" >&2; exit 1; }
cat "$OUT.sha256"
