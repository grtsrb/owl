#!/usr/bin/env bash
# Optional dev GitLab (GitLab CE + its container registry) for testing how Owl scans in-house images that need a
# login (DESIGN.md "Add a login where the scan fails"). Not part of `make stack-up`; needs ~4 GB of Docker memory.
#
#   make stack-up          # first: creates the dev CA this GitLab's certificate is signed with
#   make gitlab-up         # GitLab + groups, deploy tokens, images and workloads in it-aws-test
#   make gitlab-down       # removes all of it again (the k3d nodes restart once to forget the registry)
#
# Two hostnames, like a real GitLab: gitlab.owl.test (web + the registry's token endpoint /jwt/auth) and
# registry.owl.test (the registry). Both exist only as Docker network aliases — on owl-net (Owl's Trivy) and on
# each k3d network (containerd pulls). The certificate is signed by the dev CA (dev/.registry/), which the Owl
# server and the k3d nodes already trust.
#
# What gets created (written to dev/.gitlab/tokens.env, 0600):
#   groups devops (subgroup devops/billing) and platform, each with a read_registry group deploy token;
#   images registry.owl.test/devops/billing/api:1.0, devops/web:2.3, platform/tools:0.4 (old public images, so
#   Trivy finds something); deployments in it-aws-test namespace corp-apps pulling them with pull secrets.
# Owl gets no login: add one from the failed scan (or Settings → Registries) with the tokens printed at the end.
set -euo pipefail
cd "$(dirname "$0")/.."
. dev/registry.sh

GL_NAME=owl-gitlab
GL_IMAGE=gitlab/gitlab-ce:19.5.9-ce.0
GL_WEB=gitlab.owl.test
GL_REG=registry.owl.test
GL_DIR=dev/.gitlab
GL_CLUSTER=it-aws-test
GL_NS=corp-apps
CRANE=gcr.io/go-containerregistry/crane:v0.22.1
CLUSTERS="it-hetzner it-aws-test it-aws-prod"

down() {
  kubectl --context "k3d-$GL_CLUSTER" delete namespace "$GL_NS" --ignore-not-found >/dev/null 2>&1 || true
  docker rm -f -v "$GL_NAME" >/dev/null 2>&1 || true
  docker volume rm owl-gitlab-config owl-gitlab-data owl-gitlab-logs >/dev/null 2>&1 || true
  rm -rf "$GL_DIR"
  for c in $CLUSTERS; do registry_cluster "$c"; done # drops the registry from the nodes' registries.yaml
  echo "dev GitLab removed"
}

up() {
  [[ -f $REG_DIR/ca.pem && -f $REG_DIR/ca-bundle.crt ]] || { echo "error: no dev CA yet — run make stack-up first" >&2; exit 1; }
  mkdir -p "$GL_DIR/ssl" "$GL_DIR/docker"
  chmod 700 "$GL_DIR"
  if [[ ! -f $GL_DIR/ssl/owl.crt ]]; then
    dev_cert "$GL_DIR/ssl/owl" "$GL_WEB" "DNS:$GL_WEB,DNS:$GL_REG"
  fi
  [[ -f $GL_DIR/root-password ]] || { openssl rand -base64 18 | tr -d '/+=' > "$GL_DIR/root-password"; chmod 600 "$GL_DIR/root-password"; }

  docker network create owl-net >/dev/null 2>&1 || true
  if [[ -z $(docker ps -q -f name="^$GL_NAME$") ]]; then
    docker rm -f "$GL_NAME" >/dev/null 2>&1 || true
    # Tuned for a small dev box (GitLab's "memory-constrained environment" settings).
    local cfg
    cfg=$(cat <<EOF
external_url 'https://$GL_WEB'
registry_external_url 'https://$GL_REG'
letsencrypt['enable'] = false
nginx['ssl_certificate'] = '/etc/gitlab/owl-ssl/owl.crt'
nginx['ssl_certificate_key'] = '/etc/gitlab/owl-ssl/owl.key'
registry_nginx['ssl_certificate'] = '/etc/gitlab/owl-ssl/owl.crt'
registry_nginx['ssl_certificate_key'] = '/etc/gitlab/owl-ssl/owl.key'
gitlab_rails['initial_root_password'] = File.read('/etc/gitlab/owl-ssl/root-password').strip
puma['worker_processes'] = 0
sidekiq['max_concurrency'] = 10
prometheus_monitoring['enable'] = false
gitlab_kas['enable'] = false
gitlab_rails['env'] = { 'MALLOC_CONF' => 'dirty_decay_ms:1000,muzzy_decay_ms:1000' }
gitaly['env'] = { 'MALLOC_CONF' => 'dirty_decay_ms:1000,muzzy_decay_ms:1000', 'GITALY_COMMAND_SPAWN_MAX_PARALLEL' => '2' }
EOF
)
    cp "$GL_DIR/root-password" "$GL_DIR/ssl/root-password"
    cp "$REG_DIR/ca.pem" "$GL_DIR/ssl/ca.pem"
    docker run -d --name "$GL_NAME" --hostname "$GL_WEB" --network owl-net \
      --network-alias "$GL_WEB" --network-alias "$GL_REG" --shm-size 256m \
      -v owl-gitlab-config:/etc/gitlab -v owl-gitlab-logs:/var/log/gitlab -v owl-gitlab-data:/var/opt/gitlab \
      -v "$PWD/$GL_DIR/ssl:/etc/gitlab/owl-ssl:ro" -e GITLAB_OMNIBUS_CONFIG="$cfg" "$GL_IMAGE" >/dev/null
  fi

  echo "waiting for GitLab (the first start takes 3–6 minutes)…"
  local ok=
  for _ in $(seq 1 180); do
    if docker exec "$GL_NAME" curl -sf --cacert /etc/gitlab/owl-ssl/ca.pem --resolve "$GL_WEB:443:127.0.0.1" "https://$GL_WEB/-/readiness" >/dev/null 2>&1; then ok=1; break; fi
    sleep 5
  done
  [[ -n $ok ]] || { echo "GitLab didn't come up: docker logs $GL_NAME" >&2; exit 1; }

  # A short-lived root token for seeding (replaced on every run; dev only).
  local pat
  pat=glpat-$(openssl rand -hex 10)
  docker exec -e PAT="$pat" "$GL_NAME" gitlab-rails runner '
    u = User.find_by_username("root")
    u.personal_access_tokens.where(name: "owl-dev-seed").each(&:revoke!)
    t = u.personal_access_tokens.create!(name: "owl-dev-seed", scopes: ["api", "read_registry", "write_registry"], expires_at: 2.days.from_now)
    t.set_token(ENV["PAT"]); t.save!' >/dev/null

  api() { # method path [json body] → response body
    docker exec "$GL_NAME" curl -sf --cacert /etc/gitlab/owl-ssl/ca.pem -X "$1" -H "PRIVATE-TOKEN: $pat" \
      -H 'Content-Type: application/json' ${3:+-d "$3"} "https://$GL_WEB/api/v4$2"
  }
  json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
  group() { # path [parent id] → id
    local full=${2:+$3/}$1 id
    id=$(api GET "/groups/$(printf %s "$full" | sed 's,/,%2F,g')" 2>/dev/null | json 'd["id"]' || true)
    [[ -n $id ]] || id=$(api POST /groups "{\"name\":\"$1\",\"path\":\"$1\",\"visibility\":\"private\"${2:+,\"parent_id\":$2}}" | json 'd["id"]')
    echo "$id"
  }
  project() { # name namespace-id namespace-path
    api GET "/projects/$(printf %s "$3/$1" | sed 's,/,%2F,g')" >/dev/null 2>&1 ||
      api POST /projects "{\"name\":\"$1\",\"path\":\"$1\",\"namespace_id\":$2,\"visibility\":\"private\"}" >/dev/null
  }
  local devops billing platform
  devops=$(group devops); billing=$(group billing "$devops" devops); platform=$(group platform)
  project api "$billing" devops/billing; project web "$devops" devops; project tools "$platform" platform

  # Group deploy tokens (read_registry). Their secrets are shown once, so they're recreated when the file is gone.
  if [[ ! -f $GL_DIR/tokens.env ]]; then
    local exp out
    exp=$(date -v+365d +%Y-%m-%d 2>/dev/null || date -d '+365 days' +%Y-%m-%d)
    : > "$GL_DIR/tokens.env"; chmod 600 "$GL_DIR/tokens.env"
    for g in devops:$devops platform:$platform; do
      for id in $(api GET "/groups/${g#*:}/deploy_tokens" | json '" ".join(str(t["id"]) for t in d if t["name"]=="owl-dev")'); do
        api DELETE "/groups/${g#*:}/deploy_tokens/$id" >/dev/null
      done
      out=$(api POST "/groups/${g#*:}/deploy_tokens" "{\"name\":\"owl-dev\",\"scopes\":[\"read_registry\"],\"expires_at\":\"$exp\"}")
      local key; key=$(printf %s "${g%%:*}" | tr a-z A-Z)
      printf '%s_USER=%s\n%s_TOKEN=%s\n' "$key" "$(printf %s "$out" | json 'd["username"]')" "$key" "$(printf %s "$out" | json 'd["token"]')" >> "$GL_DIR/tokens.env"
    done
    printf 'TOKEN_EXPIRES=%s\n' "$exp" >> "$GL_DIR/tokens.env"
  fi
  # shellcheck disable=SC1090
  . "$GL_DIR/tokens.env"

  # Images: old public images copied in with crane (no docker push: the host can't resolve the dev names).
  local auth
  auth=$(printf 'root:%s' "$pat" | base64 | tr -d '\n')
  printf '{"auths":{"%s":{"auth":"%s"}}}\n' "$GL_REG" "$auth" > "$GL_DIR/docker/config.json"
  chmod 600 "$GL_DIR/docker/config.json"
  for pair in alpine:3.18.0=devops/billing/api:1.0 alpine:3.19.0=devops/web:2.3 busybox:1.36.0=platform/tools:0.4; do
    docker run --rm --network owl-net -v "$PWD/$GL_DIR/docker:/cfg:ro" -v "$PWD/$REG_DIR/ca-bundle.crt:/ca.crt:ro" \
      -e DOCKER_CONFIG=/cfg -e SSL_CERT_FILE=/ca.crt "$CRANE" copy "${pair%%=*}" "$GL_REG/${pair#*=}" >>"$GL_DIR/crane.log" 2>&1 ||
      { echo "copying ${pair%%=*} failed: $GL_DIR/crane.log" >&2; exit 1; }
  done
  rm -f "$GL_DIR/docker/config.json"
  echo "images pushed: $GL_REG/devops/billing/api:1.0, devops/web:2.3, platform/tools:0.4"

  touch "$GL_DIR/up" # registry_cluster now also connects GitLab to the cluster and trusts its registry
  for c in $CLUSTERS; do registry_cluster "$c"; done

  local k="kubectl --context k3d-$GL_CLUSTER"
  $k create namespace "$GL_NS" --dry-run=client -o yaml | $k apply -f - >/dev/null
  $k -n "$GL_NS" create secret docker-registry gitlab-devops --docker-server="$GL_REG" \
    --docker-username="$DEVOPS_USER" --docker-password="$DEVOPS_TOKEN" --dry-run=client -o yaml | $k apply -f - >/dev/null
  $k -n "$GL_NS" create secret docker-registry gitlab-platform --docker-server="$GL_REG" \
    --docker-username="$PLATFORM_USER" --docker-password="$PLATFORM_TOKEN" --dry-run=client -o yaml | $k apply -f - >/dev/null
  for d in billing-api:devops/billing/api:1.0:gitlab-devops web:devops/web:2.3:gitlab-devops tools:platform/tools:0.4:gitlab-platform; do
    IFS=: read -r name path tag secret <<<"$d"
    cat <<EOF | $k apply -f - >/dev/null
apiVersion: apps/v1
kind: Deployment
metadata: {name: $name, namespace: $GL_NS}
spec:
  replicas: 1
  selector: {matchLabels: {app: $name}}
  template:
    metadata: {labels: {app: $name}}
    spec:
      imagePullSecrets: [{name: $secret}]
      containers:
      - name: $name
        image: $GL_REG/$path:$tag
        command: ["sleep", "infinity"]
        resources: {requests: {cpu: 5m, memory: 8Mi}, limits: {memory: 32Mi}}
EOF
  done
  $k -n "$GL_NS" rollout status deploy --timeout=120s >/dev/null && echo "workloads running in $GL_CLUSTER/$GL_NS"

  cat <<EOF

GitLab: https://$GL_WEB inside Docker only (root / $(cat "$GL_DIR/root-password"), in $GL_DIR/root-password).
Owl has no login for $GL_REG yet, so its scans fail with "auth failed". Logins to add (Username / Password):
  $GL_REG/devops    $DEVOPS_USER / $DEVOPS_TOKEN
  $GL_REG/platform  $PLATFORM_USER / $PLATFORM_TOKEN
(both expire $TOKEN_EXPIRES; also in $GL_DIR/tokens.env)
EOF
}

case ${1:-up} in
  up) up ;;
  down) down ;;
  *) echo "usage: $0 up|down" >&2; exit 2 ;;
esac
