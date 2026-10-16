#!/usr/bin/env python3
"""Demo jobs for the README screenshots, through the module's API.

  ZB_URL=http://<box> ZB_USER=<user> ZB_PASSFILE=<file with the password> \\
    python3 demo-jobs.py setup     # delete the demo jobs, create them again, run each once
  ... python3 demo-jobs.py rerun   # run the backup job again (for the log screenshot)
  ... python3 demo-jobs.py card    # add a failing scheduled job for the dashboard card
  ... python3 demo-jobs.py nocard  # delete it again

Run demo-data.sh on the box first: it empties /DATA/Demo-Backups, so every
job makes a first run. Only jobs named here are deleted or created.
"""
import json
import os
import secrets
import sys
import time
import urllib.error
import urllib.request

URL = os.environ["ZB_URL"].rstrip("/")
API = URL + "/v2/zbackup/api"
WORDS = ["amber", "river", "stone", "cedar", "north", "lamp", "quiet", "orbit", "maple", "harbor"]

DEMO = [
    {"name": "Documents", "kind": "sync", "sources": ["/DATA/Demo/Documents"],
     "target": {"type": "local", "path": "/DATA/Demo-Backups/Sync/Documents"},
     "schedule": {"type": "cron", "cron_expr": "0 3 * * 0"}, "enabled": True},
    {"name": "Photos, Documents", "kind": "backup", "sources": ["/DATA/Demo/Photos", "/DATA/Demo/Documents"],
     "target": {"type": "local", "path": "/DATA/Demo-Backups/Photos+Documents"},
     "schedule": {"type": "cron", "cron_expr": "0 3 * * *"},
     "retention": {"keep_daily": 7, "keep_weekly": 4, "keep_monthly": 6}, "enabled": True},
    {"name": "This box", "kind": "system", "target": {"type": "local", "path": "/DATA/Demo-Backups/System"},
     "schedule": {"type": "manual"}, "retention": {"keep_last": 3}, "enabled": True},
]
CARD = {"name": "Photos → USB disk", "kind": "sync", "sources": ["/DATA/Demo/Photos"],
        "target": {"type": "local", "path": "/media/usb-backup/Photos"},
        "schedule": {"type": "cron", "cron_expr": "* * * * *"}, "enabled": True}


def req(path, data=None, tok=None, method=None):
    r = urllib.request.Request(path, data=json.dumps(data).encode() if data is not None else None, method=method,
                               headers={"Content-Type": "application/json", **({"Authorization": "Bearer " + tok} if tok else {})})
    try:
        body = urllib.request.urlopen(r, timeout=120).read()
        return json.loads(body) if body else {}
    except urllib.error.HTTPError as e:
        sys.exit(f"{method or 'GET'} {path}: HTTP {e.code} {e.read()[:200].decode(errors='replace')}")


def login():
    with open(os.environ["ZB_PASSFILE"]) as f:
        pw = f.read().strip()
    return req(URL + "/v1/users/login", {"username": os.environ["ZB_USER"], "password": pw})["data"]["token"]["access_token"]


def delete(tok, names):
    for j in req(API + "/jobs", tok=tok):
        if j["name"] in names:
            req(f"{API}/jobs/{j['id']}", tok=tok, method="DELETE")
            print("deleted", j["name"])


def run(tok, job):
    req(f"{API}/jobs/{job['id']}/run", {}, tok)
    time.sleep(2)
    while req(f"{API}/jobs/{job['id']}", tok=tok).get("running"):
        time.sleep(2)
    lr = req(f"{API}/jobs/{job['id']}", tok=tok).get("last_result") or {}
    print(job["name"], "→", lr.get("code"), lr.get("message"))


def main():
    cmd = sys.argv[1] if len(sys.argv) > 1 else ""
    tok = login()
    if cmd == "setup":
        delete(tok, {d["name"] for d in DEMO})
        for d in DEMO:
            body = dict(d)
            if d["kind"] != "sync":
                body["passphrase"] = "-".join(secrets.choice(WORDS) for _ in range(7))  # demo only, not kept
            run(tok, req(API + "/jobs", body, tok))
    elif cmd == "rerun":
        run(tok, next(j for j in req(API + "/jobs", tok=tok) if j["name"] == "Photos, Documents"))
    elif cmd == "card":
        print("created", req(API + "/jobs", CARD, tok).get("name"), "— the card appears after its first run (a minute)")
    elif cmd == "nocard":
        delete(tok, {CARD["name"]})
    else:
        sys.exit(__doc__)


if __name__ == "__main__":
    main()
