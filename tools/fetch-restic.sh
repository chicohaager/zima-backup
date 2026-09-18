#!/bin/bash
# Download a restic release binary for linux/<arch>, verify it against the
# release's SHA256SUMS and place it at <dest>.
#   tools/fetch-restic.sh <amd64|arm64> <dest> [version]
set -euo pipefail
ARCH="${1:?arch}"; DEST="${2:?dest}"; VERSION="${3:-0.19.1}"
BASE="https://github.com/restic/restic/releases/download/v${VERSION}"
FILE="restic_${VERSION}_linux_${ARCH}.bz2"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
curl -fsSL -o "$TMP/$FILE" "$BASE/$FILE"
curl -fsSL -o "$TMP/SHA256SUMS" "$BASE/SHA256SUMS"
( cd "$TMP" && grep " $FILE\$" SHA256SUMS | sha256sum -c --quiet )
bunzip2 -c "$TMP/$FILE" > "$TMP/restic"
chmod 755 "$TMP/restic"
mkdir -p "$(dirname "$DEST")"
mv "$TMP/restic" "$DEST"
echo "restic $VERSION linux/$ARCH -> $DEST"
