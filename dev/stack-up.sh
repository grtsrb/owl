#!/usr/bin/env bash
# Runs the server in Docker and connects an agent in each local k3d cluster to it.
#
# Networking: the server container joins each cluster's Docker network and agents use its IP there.
# It is published on 127.0.0.1:8080 only, so the (not yet authenticated) API isn't exposed to the LAN.
# Re-running rotates the agent tokens; the database volume (inventory-data) is kept.
set -euo pipefail
cd "$(dirname "$0")/.."

IMAGE=${IMAGE:-service-inventory:dev}
clusters="it-hetzner:staging it-aws-test:prelive it-aws-prod:live"

# GITHUB_TOKEN (no scopes) from .env, passed by name so it never appears in `ps`/shell history.
if [[ -f .env ]]; then set -a; . ./.env; set +a; fi
[[ -n ${GITHUB_TOKEN:-} ]] || echo "warning: GITHUB_TOKEN not set (.env); upstream data will be rate limited" >&2

docker rm -f inventory-server >/dev/null 2>&1 || true
docker run -d --name inventory-server -p 127.0.0.1:8080:8080 -v inventory-data:/data \
  -e INV_LISTEN=0.0.0.0:8080 -e INV_INTERNAL_REGISTRIES=registry.gitlab.example -e GITHUB_TOKEN \
  "$IMAGE" server >/dev/null
until curl -sf 127.0.0.1:8080/healthz >/dev/null; do sleep 1; done
echo "server: http://127.0.0.1:8080"

for entry in $clusters; do
  name=${entry%%:*} stage=${entry#*:} ctx=k3d-${entry%%:*}
  docker network connect "k3d-$name" inventory-server
  ip=$(docker inspect -f "{{(index .NetworkSettings.Networks \"k3d-$name\").IPAddress}}" inventory-server)

  if docker exec inventory-server /inv cluster list | awk 'NR>1{print $1}' | grep -qx "$name"; then
    token=$(docker exec inventory-server /inv cluster rotate-token --name "$name" 2>/dev/null)
  else
    token=$(docker exec inventory-server /inv cluster add --name "$name" --stage "$stage" --tenant IT 2>/dev/null)
  fi

  k3d image import "$IMAGE" -c "$name" >/dev/null 2>&1
  kubectl --context "$ctx" apply -f deploy/agent/agent.yaml >/dev/null
  kubectl --context "$ctx" -n service-inventory create secret generic inventory-agent \
    --from-literal=INV_SERVER_URL="http://$ip:8080" --from-literal=INV_TOKEN="$token" \
    --from-literal=INV_ALLOW_INSECURE_HTTP=true --dry-run=client -o yaml | kubectl --context "$ctx" apply -f - >/dev/null
  # Local image: never pull from a registry.
  kubectl --context "$ctx" -n service-inventory patch deploy inventory-agent --type=json -p \
    "[{\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/image\",\"value\":\"$IMAGE\"},
      {\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/imagePullPolicy\",\"value\":\"Never\"}]" >/dev/null
  kubectl --context "$ctx" -n service-inventory rollout restart deploy/inventory-agent >/dev/null
  echo "agent: $name ($stage) -> http://$ip:8080"
done
echo "first snapshots arrive within ~30s:  docker exec inventory-server /inv cluster list"
