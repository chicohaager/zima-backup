#!/bin/bash
# Download an rclone release binary for linux/<arch>, verify it against the
# release's SHA256SUMS and place it at <dest>. Used by the tests only —
# ZimaOS ships rclone in its base image (1.7.1: v1.74.3-adrive.4), the
# module does not bundle it.
#   tools/fetch-rclone.sh <amd64|arm64> <dest> [version]
set -euo pipefail
ARCH="${1:?arch}"; DEST="${2:?dest}"; VERSION="${3:-1.74.3}"
BASE="https://downloads.rclone.org/v${VERSION}"
NAME="rclone-v${VERSION}-linux-${ARCH}"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
curl -fsSL -o "$TMP/$NAME.zip" "$BASE/$NAME.zip"
curl -fsSL -o "$TMP/SHA256SUMS" "$BASE/SHA256SUMS"
( cd "$TMP" && grep " $NAME.zip\$" SHA256SUMS | sha256sum -c --quiet )
unzip -q -o "$TMP/$NAME.zip" -d "$TMP"
mkdir -p "$(dirname "$DEST")"
mv "$TMP/$NAME/rclone" "$DEST"
chmod 755 "$DEST"
echo "rclone $VERSION linux/$ARCH -> $DEST"
