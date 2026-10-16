#!/bin/bash
# Demo data for the README screenshots, run ON the box as root:
#   sudo bash demo-data.sh [owner]
# Creates /DATA/Demo/Photos (24 generated images) and /DATA/Demo/Documents
# (12 text files), and empties /DATA/Demo-Backups so that the demo jobs make
# a first run again. Touches nothing else.
set -euo pipefail
OWNER="${1:-root}"
rm -rf /DATA/Demo-Backups
mkdir -p /DATA/Demo/Photos /DATA/Demo/Documents /DATA/Demo-Backups
python3 - <<'PY'
import random, struct, zlib
random.seed(7)
def png(path, w, h):
    raw = b''.join(b'\x00' + bytes(random.randrange(256) for _ in range(w * 3)) for _ in range(h))
    def chunk(t, d):
        return struct.pack('>I', len(d)) + t + d + struct.pack('>I', zlib.crc32(t + d) & 0xffffffff)
    with open(path, 'wb') as f:
        f.write(b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', w, h, 8, 2, 0, 0, 0))
                + chunk(b'IDAT', zlib.compress(raw, 1)) + chunk(b'IEND', b''))
places = ['Lake', 'Mountains', 'Garden', 'Beach', 'City', 'Forest']
for i in range(24):
    png(f'/DATA/Demo/Photos/{places[i % 6]}-{2024 + i // 12}-{i % 12 + 1:02d}.png', 240, 180)
topics = ['Invoice', 'Contract', 'Notes', 'Recipe', 'Manual', 'Letter']
words = ['backup', 'restore', 'folder', 'disk', 'share', 'photo', 'plan', 'weekly', 'daily']
for i in range(12):
    with open(f'/DATA/Demo/Documents/{topics[i % 6]} {i // 6 + 1}.txt', 'w') as f:
        f.write(f'{topics[i % 6]} {i // 6 + 1}\n' + ' '.join(random.choice(words) for _ in range(9000)) + '\n')
PY
chown -R "$OWNER" /DATA/Demo /DATA/Demo-Backups
echo "Photos: $(ls /DATA/Demo/Photos | wc -l) files, Documents: $(ls /DATA/Demo/Documents | wc -l) files, /DATA/Demo-Backups empty"
