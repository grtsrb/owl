#!/usr/bin/env bash
# Runs the server in Docker and connects an agent in each local k3d cluster to it.
#
# Networking: the server container joins each cluster's Docker network and agents use its IP there.
# It is published on 127.0.0.1:8080 only (plain HTTP is fine on localhost, nowhere else). Mailpit
# catches the emails Owl sends (http://127.0.0.1:8025). The first run creates an admin invite and
# prints its link. Re-running rotates the agent tokens; the database volume (owl-data) is kept.
set -euo pipefail
cd "$(dirname "$0")/.."

IMAGE=${IMAGE:-owl:dev}
AGENT_IMAGE=${AGENT_IMAGE:-owl-agent:dev}  # the slim agent image (make image builds both)
# Check both images before touching anything (a failed import later would leave a rotated token behind).
for img in "$IMAGE" "$AGENT_IMAGE"; do
  docker image inspect "$img" >/dev/null 2>&1 || { echo "error: image $img not found — run make image" >&2; exit 1; }
done
clusters="it-hetzner:staging it-aws-test:prelive it-aws-prod:live"

# Secrets come from .env (gitignored), passed by name so they never appear in `ps`/shell history.
if [[ -f .env ]]; then set -a; . ./.env; set +a; fi
# Webhook of the dev Mattermost (make mattermost-up), if it's running.
if [[ -f dev/.mattermost.env ]]; then set -a; . dev/.mattermost.env; set +a; fi
[[ -z ${OWL_ADMIN_TOKEN:-} ]] || echo "note: OWL_ADMIN_TOKEN in .env is no longer used (sign-in replaced it); you can delete it" >&2
ADMIN_EMAIL=${OWL_DEV_ADMIN:-admin@owl.local}
[[ -n ${GITHUB_TOKEN:-} ]] || echo "warning: GITHUB_TOKEN not set (.env); upstream data will be rate limited" >&2

# Local S3 for backups (S3Mock: S3-compatible test server; MinIO no longer publishes images). It
# accepts any credentials — permissions (no delete for the server) can only be tested on real S3.
docker network create owl-net >/dev/null 2>&1 || true
docker rm -f owl-s3 >/dev/null 2>&1 || true
docker run -d --name owl-s3 --network owl-net -e initialBuckets=owl-backups adobe/s3mock:3.12.0 >/dev/null
# Mailpit: a fake mail server with a web inbox. Nothing leaves this machine.
docker rm -f owl-mailpit >/dev/null 2>&1 || true
docker run -d --name owl-mailpit --network owl-net -p 127.0.0.1:8025:8025 axllent/mailpit:v1.27.0 >/dev/null

docker rm -f owl-server >/dev/null 2>&1 || true
# Docker's own copy of the logs is capped (the server also writes rotated files to /data/logs).
docker run -d --name owl-server --network owl-net -p 127.0.0.1:8080:8080 -v owl-data:/data \
  --log-opt max-size=10m --log-opt max-file=3 \
  -e OWL_LISTEN=0.0.0.0:8080 -e OWL_INTERNAL_REGISTRIES=registry.gitlab.example -e GITHUB_TOKEN \
  -e OWL_PUBLIC_URL=http://127.0.0.1:8080 \
  -e OWL_SMTP_HOST=owl-mailpit -e OWL_SMTP_PORT=1025 -e OWL_SMTP_TLS=none -e "OWL_SMTP_FROM=Owl <owl@owl.local>" \
  -e OWL_GOOGLE_CLIENT_ID -e OWL_GOOGLE_CLIENT_SECRET -e OWL_GOOGLE_DOMAIN -e OWL_MATTERMOST_WEBHOOK_URL \
  -e OWL_BACKUP_S3_ENDPOINT=owl-s3:9090 -e OWL_BACKUP_S3_BUCKET=owl-backups -e OWL_BACKUP_S3_PREFIX=dev \
  -e OWL_BACKUP_S3_ACCESS_KEY=dev -e OWL_BACKUP_S3_SECRET_KEY=dev-only-not-a-secret -e OWL_BACKUP_S3_INSECURE=true \
  "$IMAGE" server >/dev/null
until curl -sf 127.0.0.1:8080/healthz >/dev/null; do sleep 1; done
echo "server: http://127.0.0.1:8080   mail: http://127.0.0.1:8025"
[[ -z ${OWL_MATTERMOST_WEBHOOK_URL:-} ]] || echo "notifications: Mattermost at http://127.0.0.1:8065 (Settings → Notifications to switch them on)"
if [[ -z $(docker exec owl-server /owl db user list | awk 'NR>1') ]]; then
  echo "first admin ($ADMIN_EMAIL) — open this link to set a password:"
  docker exec -e OWL_PUBLIC_URL=http://127.0.0.1:8080 owl-server /owl db user invite --admin "$ADMIN_EMAIL" 2>/dev/null
fi

for entry in $clusters; do
  name=${entry%%:*} stage=${entry#*:} ctx=k3d-${entry%%:*}
  docker network connect "k3d-$name" owl-server
  ip=$(docker inspect -f "{{(index .NetworkSettings.Networks \"k3d-$name\").IPAddress}}" owl-server)

  if docker exec owl-server /owl db cluster list | awk 'NR>1{print $1}' | grep -qx "$name"; then
    token=$(docker exec owl-server /owl db cluster rotate-token --name "$name" 2>/dev/null)
  else
    token=$(docker exec owl-server /owl db cluster add --name "$name" --stage "$stage" --tenant IT 2>/dev/null)
  fi

  k3d image import "$AGENT_IMAGE" -c "$name" >/dev/null 2>&1
  kubectl --context "$ctx" apply -f deploy/agent/agent.yaml >/dev/null
  kubectl --context "$ctx" -n owl create secret generic owl-agent \
    --from-literal=OWL_SERVER_URL="http://$ip:8080" --from-literal=OWL_TOKEN="$token" \
    --from-literal=OWL_ALLOW_INSECURE_HTTP=true --dry-run=client -o yaml | kubectl --context "$ctx" apply -f - >/dev/null
  # Local image: never pull from a registry.
  kubectl --context "$ctx" -n owl patch deploy owl-agent --type=json -p \
    "[{\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/image\",\"value\":\"$AGENT_IMAGE\"},
      {\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/imagePullPolicy\",\"value\":\"Never\"}]" >/dev/null
  kubectl --context "$ctx" -n owl rollout restart deploy/owl-agent >/dev/null
  echo "agent: $name ($stage) -> http://$ip:8080"
done
echo "first snapshots arrive within ~30s:  docker exec owl-server /owl db cluster list"
