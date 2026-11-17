#!/usr/bin/env bash
# Runs a throwaway Mattermost (preview image: server + database in one container) for testing Owl's
# notifications, creates a team, a channel and an incoming webhook, and writes the webhook URL to
# dev/.mattermost.env (gitignored), which `make stack-up` passes to the Owl server.
#
#   make mattermost-up     # then: make stack-up
#   open http://127.0.0.1:8065 and sign in as owladmin / the password printed below
#   make mattermost-down   # removes the container, its data and dev/.mattermost.env
#
# The image is amd64-only (runs under Rosetta/QEMU on Apple silicon) and about 1 GB.
set -euo pipefail
cd "$(dirname "$0")/.."

IMAGE=mattermost/mattermost-preview:12.0.2
NAME=owl-mattermost
PASS=${OWL_DEV_MM_PASSWORD:-Owl-dev-only-1234}

docker network create owl-net >/dev/null 2>&1 || true
if ! docker ps --format '{{.Names}}' | grep -qx "$NAME"; then
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  docker run -d --name "$NAME" --network owl-net --platform linux/amd64 -p 127.0.0.1:8065:8065 \
    -e MM_SERVICESETTINGS_SITEURL=http://127.0.0.1:8065 \
    -e MM_SERVICESETTINGS_ENABLELOCALMODE=true \
    -e MM_SERVICESETTINGS_ENABLEINCOMINGWEBHOOKS=true \
    -e MM_SERVICESETTINGS_ENABLEPOSTUSERNAMEOVERRIDE=true \
    -e MM_TEAMSETTINGS_ENABLEOPENSERVER=false \
    "$IMAGE" >/dev/null
fi

echo "waiting for Mattermost (the first start takes a minute or two)…"
for _ in $(seq 1 120); do
  curl -sf 127.0.0.1:8065/api/v4/system/ping >/dev/null && break
  sleep 3
done
curl -sf 127.0.0.1:8065/api/v4/system/ping >/dev/null || { echo "Mattermost didn't come up: docker logs $NAME" >&2; exit 1; }

mm() { docker exec "$NAME" mmctl --local "$@"; }
for _ in $(seq 1 30); do mm system version >/dev/null 2>&1 && break; sleep 2; done

mm user create --email owladmin@owl.local --username owladmin --password "$PASS" --system-admin >/dev/null 2>&1 || true
mm team create --name owl --display-name Owl >/dev/null 2>&1 || true
mm team users add owl owladmin >/dev/null 2>&1 || true
mm channel create --team owl --name owl-alerts --display-name "Owl alerts" >/dev/null 2>&1 || true
mm channel users add owl:owl-alerts owladmin >/dev/null 2>&1 || true

id=$(mm webhook list-incoming --json 2>/dev/null | python3 -c 'import sys,json
try: hooks=json.load(sys.stdin)
except Exception: hooks=[]
print(next((h["id"] for h in hooks or [] if h.get("display_name")=="Owl"), ""))')
if [[ -z $id ]]; then
  id=$(mm webhook create-incoming --channel owl:owl-alerts --user owladmin --display-name Owl --json |
    python3 -c 'import sys,json; print(json.load(sys.stdin)["id"])')
fi

# The Owl server reaches Mattermost over the owl-net Docker network by container name.
printf 'OWL_MATTERMOST_WEBHOOK_URL=http://%s:8065/hooks/%s\n' "$NAME" "$id" > dev/.mattermost.env
chmod 600 dev/.mattermost.env
echo "Mattermost: http://127.0.0.1:8065  (owladmin / $PASS, team Owl, channel \"Owl alerts\")"
echo "webhook URL written to dev/.mattermost.env — run 'make stack-up' to pass it to the Owl server"
