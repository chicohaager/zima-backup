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
  amd64) SYSEXT_ARCH=x86-64 ;;
  arm64) SYSEXT_ARCH=arm64 ;;
  *) echo "unsupported arch: $ARCH (amd64|arm64)" >&2; exit 2 ;;
esac
VERSION=$(sed -n 's/^\s*version\s*=\s*"\(.*\)".*/\1/p' cmd/zbackupd/main.go)
[ -n "$VERSION" ] || { echo "version not found" >&2; exit 1; }
OUT="zbackup-${ARCH}.raw"

echo "zbackup v${VERSION} linux/${ARCH}"
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -ldflags="-s -w" -o raw/usr/bin/zbackupd ./cmd/zbackupd/
printf 'ID=_any\nARCHITECTURE=%s\n' "$SYSEXT_ARCH" > raw/usr/lib/extension-release.d/extension-release.zbackup
mksquashfs raw/ "$OUT" -noappend -comp gzip -quiet
sha256sum "$OUT" | tee "$OUT.sha256"
