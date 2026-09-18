#!/bin/bash
# End-to-end checks against a running Sync & Backup instance behind the
# ZimaOS gateway.
#
#   ZIMA_USER=admin ZIMA_PASS=… ./test_deployment.sh http://<zimaos-host> [target-dir]
#
# target-dir is a folder on the box under /DATA, /media or /mnt where the
# script may create and remove its own subfolder (default /DATA/zbackup-e2e).
# Point it at a USB disk to exercise that disk. Credentials come from the
# environment only (never from argv, which is visible in `ps`). Every check
# prints PASS/FAIL with the measured value; the script exits non-zero if any
# check failed. Test jobs are removed at the end, including on abort; the
# files the jobs wrote under target-dir stay for inspection.
set -uo pipefail

BASE="${1:?usage: ZIMA_USER=… ZIMA_PASS=… $0 http://<host> [target-dir]}"
BASE="${BASE%/}"
TARGET="${2:-/DATA/zbackup-e2e}"
: "${ZIMA_USER:?set ZIMA_USER}"
: "${ZIMA_PASS:?set ZIMA_PASS}"
API="$BASE/v2/zbackup/api"

pass=0; fail=0; created=()
ok()   { pass=$((pass+1)); printf '  PASS  %s\n' "$1"; }
bad()  { fail=$((fail+1)); printf '  FAIL  %s\n' "$1"; }
check() { # check <label> <expected> <actual>
  if [ "$2" = "$3" ]; then ok "$1 = $3"; else bad "$1: expected '$2', got '$3'"; fi
}
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1]))" "$1" 2>/dev/null; }
post() { curl -s -X POST -H "$AUTH" -H 'Content-Type: application/json' ${2:+-d "$2"} "$API/$1"; }
wait_idle() { # wait_idle <id> [seconds]
  local i; for ((i = 0; i < ${2:-120}; i++)); do
    sleep 1; [ "$(curl -s -H "$AUTH" "$API/jobs/$1" | json "d['running']")" = "False" ] && return 0
  done; return 1
}
last() { curl -s -H "$AUTH" "$API/jobs/$1" | json "d['last_result']['$2']"; }

cleanup() {
  for id in "${created[@]:-}"; do
    [ -n "$id" ] && curl -s -o /dev/null -X DELETE -H "$AUTH" "$API/jobs/$id"
  done
}
trap cleanup EXIT

echo "== login =="
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ZIMA_USER\",\"password\":\"$ZIMA_PASS\"}" "$BASE/v1/users/login" \
  | json "d['data']['token']['access_token']")
[ -n "$TOKEN" ] || { echo "login failed"; exit 2; }
AUTH="Authorization: Bearer $TOKEN"
ok "session token obtained"

echo "== auth =="
check "GET /api/jobs without token"   401 "$(curl -s -o /dev/null -w '%{http_code}' "$API/jobs")"
check "GET /api/health without token" 200 "$(curl -s -o /dev/null -w '%{http_code}' "$API/health")"
check "GET /api/jobs with token"      200 "$(curl -s -o /dev/null -w '%{http_code}' -H "$AUTH" "$API/jobs")"
check "POST with foreign Origin"      403 "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "$AUTH" -H 'Origin: http://evil.example' -H 'Content-Type: application/json' -d '{}' "$API/jobs")"
check "UI served"                     200 "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/modules/zbackup/")"
echo "  running version: $(curl -s "$API/health" | json "d['version']")"

echo "== validation =="
check "sync without folders"   sources_required "$(post jobs '{"name":"x","kind":"sync","sources":[],"target":{"type":"local","path":"/DATA/x"},"schedule":{"type":"manual"}}' | json "d['code']")"
check "backup without passphrase" passphrase_required "$(post jobs '{"name":"x","kind":"backup","sources":["/DATA"],"target":{"type":"local","path":"/media/x"},"schedule":{"type":"manual"}}' | json "d['code']")"
check "target inside source"   target_nested "$(post jobs '{"name":"x","kind":"sync","sources":["/DATA"],"target":{"type":"local","path":"/DATA/x"},"schedule":{"type":"manual"}}' | json "d['code']")"
check "target outside roots"   target_path_invalid "$(post jobs '{"name":"x","kind":"sync","sources":["/DATA/a"],"target":{"type":"local","path":"/tmp/x"},"schedule":{"type":"manual"}}' | json "d['code']")"

# The source of both jobs is the module's own data folder: it exists on
# every installation and holds a few small files. keys/ (the module's ssh
# key pair) is excluded — a test must not copy a private key onto a disk
# that may not keep permissions.
SRC=/DATA/AppData/zbackup
EXCL='"excludes":["keys"],'

echo "== backup to $TARGET/repo =="
BODY=$(post jobs "{\"name\":\"e2e backup\",\"kind\":\"backup\",\"sources\":[\"$SRC\"],$EXCL\"target\":{\"type\":\"local\",\"path\":\"$TARGET/repo\"},\"schedule\":{\"type\":\"manual\"},\"retention\":{\"keep_last\":2},\"passphrase\":\"e2e-passphrase\",\"enabled\":true}")
BK=$(echo "$BODY" | json "d['id']"); created+=("$BK")
[ -n "$BK" ] || { bad "create backup job: $BODY"; exit 1; }
ok "backup job created ($BK)"
post "jobs/$BK/run" > /dev/null; wait_idle "$BK" || bad "backup run did not finish"
check "backup result"          completed "$(last "$BK" code)"
SNAP=$(curl -s -H "$AUTH" "$API/jobs/$BK/snapshots" | json "d[0]['id']")
if [ -n "$SNAP" ]; then ok "snapshot listed (${SNAP:0:8})"; else bad "no snapshot listed"; fi
NAMES=$(curl -s -H "$AUTH" "$API/jobs/$BK/snapshots/$SNAP/ls?path=$SRC" | json "','.join(sorted(n['name'] for n in d))")
case "$NAMES" in *keys*) bad "snapshot listing contains the excluded keys/ ($NAMES)";; *jobs.json*) ok "snapshot listing contains jobs.json, not keys ($NAMES)";; *) bad "snapshot listing: $NAMES";; esac
post "jobs/$BK/restore" "{\"snapshot\":\"$SNAP\",\"paths\":[\"$SRC/jobs.json\"],\"target\":\"$TARGET/restored\"}" > /dev/null
wait_idle "$BK" || bad "restore did not finish"
check "restore result"         restored  "$(last "$BK" code)"
post "jobs/$BK/check" > /dev/null; wait_idle "$BK" || bad "check did not finish"
check "repository check"       check_ok  "$(last "$BK" code)"
post "jobs/$BK/run" > /dev/null; wait_idle "$BK" || bad "second backup did not finish"
check "second backup"          completed "$(last "$BK" code)"
check "history entries"        4 "$(curl -s -H "$AUTH" "$API/jobs/$BK/logs" | json "len(d)")"

# The restored folder is a static source: the daemon keeps writing its own
# data folder, so a preview right after a sync of it would see changes.
RSRC="$TARGET/restored$SRC"
echo "== sync $RSRC to $TARGET/mirror =="
BODY=$(post jobs "{\"name\":\"e2e sync\",\"kind\":\"sync\",\"sources\":[\"$RSRC\"],\"target\":{\"type\":\"local\",\"path\":\"$TARGET/mirror\"},\"schedule\":{\"type\":\"manual\"},\"delete_extraneous\":true,\"enabled\":true}")
SYNC=$(echo "$BODY" | json "d['id']"); created+=("$SYNC")
[ -n "$SYNC" ] || { bad "create sync job: $BODY"; exit 1; }
ok "sync job created ($SYNC)"
check "secret fields absent in job view" 0 "$(echo "$BODY" | grep -c '"passphrase"\|"secret"')"
PREVIEW=$(post "jobs/$SYNC/preview")
COPY=$(echo "$PREVIEW" | json "d['files_copy']")
if [ "${COPY:-0}" -gt 0 ]; then ok "preview before first run: $COPY files to copy"; else bad "preview before first run: $PREVIEW"; fi
post "jobs/$SYNC/run" > /dev/null; wait_idle "$SYNC" || bad "sync run did not finish"
check "sync result"            completed "$(last "$SYNC" code)"
check "sync files copied"      "$COPY"   "$(last "$SYNC" files)"
check "preview after run: copy" 0 "$(post "jobs/$SYNC/preview" | json "d['files_copy']")"
post "jobs/$SYNC/run" > /dev/null; wait_idle "$SYNC" || bad "second sync run did not finish"
check "second run (unchanged, no attr errors)" completed "$(last "$SYNC" code)"

echo "== wrong passphrase is named =="
BODY=$(post jobs "{\"name\":\"e2e wrong pw\",\"kind\":\"backup\",\"sources\":[\"$SRC\"],$EXCL\"target\":{\"type\":\"local\",\"path\":\"$TARGET/repo\"},\"schedule\":{\"type\":\"manual\"},\"passphrase\":\"not-the-passphrase\",\"enabled\":true}")
WP=$(echo "$BODY" | json "d['id']"); created+=("$WP")
post "jobs/$WP/run" > /dev/null; wait_idle "$WP" || bad "wrong-passphrase run did not finish"
check "wrong passphrase"       passphrase_wrong "$(last "$WP" code)"

echo "== unmounted disk is refused =="
BODY=$(post jobs '{"name":"e2e missing disk","kind":"sync","sources":["/DATA/AppData/zbackup"],"target":{"type":"local","path":"/media/zbackup-e2e-no-such-disk/mirror"},"schedule":{"type":"manual"},"enabled":true}')
MD=$(echo "$BODY" | json "d['id']"); created+=("$MD")
post "jobs/$MD/run" > /dev/null; wait_idle "$MD" || bad "missing-disk run did not finish"
check "missing disk"           target_unavailable "$(last "$MD" code)"

echo
echo "passed $pass, failed $fail — files left under $TARGET for inspection"
[ "$fail" -eq 0 ]
