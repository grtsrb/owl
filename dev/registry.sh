#!/usr/bin/env bash
# Dev-only image registry (owl-registry:5000) so the dev clusters *pull* the agent image instead of having it
# imported: kubelet then reports a registry digest and Owl scans its own agent image like any in-house image.
#
# HTTPS with a throwaway dev CA (dev/.registry/, gitignored) — Owl's Trivy and k3s only pull over TLS:
#   - k3d nodes get /etc/rancher/k3s/registries.yaml + the CA (a node restarts once when that changes);
#   - the dev owl-server gets the system CA bundle plus the dev CA (dev/.registry/ca-bundle.crt);
#   - the host pushes to 127.0.0.1:5050 (Docker allows localhost registries without trusting the CA).
# Sourced by dev/stack-up.sh: registry_up before the server starts, registry_cluster per cluster.
REG_DIR=dev/.registry
REG_NAME=owl-registry
REG_HOST=$REG_NAME:5000
REG_IMAGE=registry@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373 # registry:2

dev_cert() { # $1 = output path without extension (.crt/.key), $2 = CN, $3 = subjectAltName list — signed by the dev CA
  openssl req -newkey rsa:2048 -nodes -subj "/CN=$2" -keyout "$1.key" -out "$1.csr" 2>/dev/null
  printf 'subjectAltName=%s\nextendedKeyUsage=serverAuth\n' "$3" > "$1.ext"
  openssl x509 -req -in "$1.csr" -CA "$REG_DIR/ca.pem" -CAkey "$REG_DIR/ca.key" -CAcreateserial \
    -days 825 -extfile "$1.ext" -out "$1.crt" 2>/dev/null
  rm -f "$1.csr" "$1.ext"
  chmod 600 "$1.key"
}

registry_up() { # $1 = server image (its CA bundle is extended), $2 = agent image (pushed)
  mkdir -p "$REG_DIR"
  if [[ ! -f $REG_DIR/ca.pem ]]; then
    openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -subj "/CN=Owl dev registry CA" \
      -keyout "$REG_DIR/ca.key" -out "$REG_DIR/ca.pem" 2>/dev/null
    dev_cert "$REG_DIR/registry" "$REG_NAME" "DNS:$REG_NAME,DNS:localhost,IP:127.0.0.1"
    mv "$REG_DIR/registry.crt" "$REG_DIR/registry.pem"
  fi
  if [[ -z $(docker ps -q -f name="^$REG_NAME$") ]]; then
    docker rm -f "$REG_NAME" >/dev/null 2>&1 || true
    docker run -d --name "$REG_NAME" --restart unless-stopped -p 127.0.0.1:5050:5000 -v owl-registry-data:/var/lib/registry \
      -v "$PWD/$REG_DIR:/certs:ro" -e REGISTRY_HTTP_TLS_CERTIFICATE=/certs/registry.pem \
      -e REGISTRY_HTTP_TLS_KEY=/certs/registry.key "$REG_IMAGE" >/dev/null
  fi
  # The server image's CA bundle + the dev CA, mounted over the image's bundle (Go and Trivy read it).
  local c
  c=$(docker create "$1")
  rm -f "$REG_DIR/ca-bundle.crt"
  docker cp "$c:/etc/ssl/certs/ca-certificates.crt" "$REG_DIR/ca-bundle.crt" >/dev/null
  docker rm "$c" >/dev/null
  chmod 644 "$REG_DIR/ca-bundle.crt" # docker cp keeps the image's read-only mode
  cat "$REG_DIR/ca.pem" >> "$REG_DIR/ca-bundle.crt"

  until curl -sfk https://127.0.0.1:5050/v2/ >/dev/null; do sleep 1; done
  docker tag "$2" "127.0.0.1:5050/owl-agent:dev"
  docker push -q "127.0.0.1:5050/owl-agent:dev" >/dev/null
  echo "registry: https://127.0.0.1:5050 (in the clusters: $REG_HOST) — owl-agent:dev pushed"
}

registry_cluster() { # $1 = cluster name: the node trusts the registry (restarts once if that changed)
  local node=k3d-$1-server-0 want
  docker network connect "k3d-$1" "$REG_NAME" >/dev/null 2>&1 || true
  want=$(printf 'mirrors:\n  "%s":\n    endpoint: ["https://%s"]\nconfigs:\n  "%s":\n    tls:\n      ca_file: /etc/rancher/k3s/owl-dev-ca.pem\n' \
    "$REG_HOST" "$REG_HOST" "$REG_HOST")
  # The optional dev GitLab (dev/gitlab.sh): its registry, signed by the same CA. No Docker DNS alias on the
  # cluster network — the Owl server joins it too and must not resolve GitLab's names there (with dev/ziti.sh
  # it may only reach them through Ziti); the node gets /etc/hosts entries instead (gitlab_hosts, below).
  if [[ -f dev/.gitlab/up ]]; then
    docker network disconnect "k3d-$1" owl-gitlab >/dev/null 2>&1 || true
    docker network connect "k3d-$1" owl-gitlab >/dev/null 2>&1 || true
    want+=$(printf '\n  "registry.owl.test":\n    tls:\n      ca_file: /etc/rancher/k3s/owl-dev-ca.pem\n')
  else
    docker network disconnect "k3d-$1" owl-gitlab >/dev/null 2>&1 || true
  fi
  if [[ $(docker exec "$node" cat /etc/rancher/k3s/registries.yaml 2>/dev/null) != "$want" ]] ||
     [[ $(docker exec "$node" cat /etc/rancher/k3s/owl-dev-ca.pem 2>/dev/null) != "$(cat "$REG_DIR/ca.pem")" ]]; then
    docker cp "$REG_DIR/ca.pem" "$node:/etc/rancher/k3s/owl-dev-ca.pem"
    printf '%s\n' "$want" | docker exec -i "$node" sh -c 'cat > /etc/rancher/k3s/registries.yaml'
    docker restart "$node" >/dev/null
    until kubectl --context "k3d-$1" get --raw /readyz >/dev/null 2>&1; do sleep 2; done
    echo "cluster $1: registry config updated ($REG_HOST$([[ -f dev/.gitlab/up ]] && echo ", registry.owl.test")) — node restarted"
  fi
  gitlab_hosts "$1"
}

gitlab_hosts() { # $1 = cluster: the node resolves the dev GitLab's names to its address on the cluster network
  local node=k3d-$1-server-0 ip=""
  [[ -f dev/.gitlab/up ]] && ip=$(docker inspect -f "{{(index .NetworkSettings.Networks \"k3d-$1\").IPAddress}}" owl-gitlab 2>/dev/null || true)
  docker exec -e IP="$ip" "$node" sh -c 'grep -v "owl\.test" /etc/hosts > /tmp/hosts.owl; [ -z "$IP" ] || echo "$IP gitlab.owl.test registry.owl.test" >> /tmp/hosts.owl; cat /tmp/hosts.owl > /etc/hosts'
}
