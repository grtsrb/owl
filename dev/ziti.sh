#!/usr/bin/env bash
# Optional dev OpenZiti (DESIGN.md "Add a login where the scan fails"): the dev GitLab (make gitlab-up) becomes
# reachable for the Owl server ONLY through Ziti, like registries behind a company's zero-trust overlay. Owl has
# no Ziti code — this proves its networking assumptions and error messages, in both ways teams connect:
#
#   make ziti-up                  # mode sidecar (default): owl-server shares a ziti-edge-tunnel container's network
#   make ziti-up MODE=router      # mode router: a Ziti router in tproxy mode is the gateway for 100.64.0.0/10 + DNS
#   make ziti-case CASE=…         # ok | no-login-host (only the registry is exposed) | host-down (hosting tunneler stopped)
#   make ziti-down                # back to direct access; removes every Ziti container, volume and dev/.ziti
#
# Pieces: owl-ziti (controller + edge router, quickstart), owl-ziti-host on owl-corp next to GitLab (hosts the
# services registry.owl.test:443 and gitlab.owl.test:443 → owl-gitlab:443), and the client side:
#   sidecar: owl-ziti-client (ziti-edge-tunnel, tun + its DNS 100.64.0.2 first in resolv.conf), published :8080;
#            dev/stack-up.sh starts owl-server with --network container:owl-ziti-client.
#   router:  owl-ziti-gw (ziti-router, tproxy, resolver on :53); dev/stack-up.sh starts owl-server with
#            --dns <gw> --dns $OWL_DEV_DNS (the router's resolver REFUSES non-Ziti names, Docker then asks the
#            next one) and a short-lived NET_ADMIN container adds the 100.64.0.0/10 route (Owl itself never gets it).
# Both client tunnellers run as root with NET_ADMIN — dev only.
set -euo pipefail
cd "$(dirname "$0")/.."

ZDIR=dev/.ziti
CLI=openziti/ziti-cli:2.0.8
ROUTER=openziti/ziti-router:2.0.8
TUNNEL=openziti/ziti-edge-tunnel:1.20.0
HOST=openziti/ziti-host:1.20.0
PW=owl-dev-ziti # the dev controller's admin password (owl-net only, nothing published)

Z() { docker exec owl-ziti ziti "$@"; }

controller() {
  docker network create owl-net >/dev/null 2>&1 || true
  docker network create owl-corp >/dev/null 2>&1 || true
  mkdir -p "$ZDIR"; chmod 700 "$ZDIR"
  if [[ -z $(docker ps -q -f name='^owl-ziti$') ]]; then
    docker rm -f owl-ziti >/dev/null 2>&1 || true
    docker volume create owl-ziti >/dev/null
    docker run --rm -v owl-ziti:/v alpine:3.20 chown 2171:2171 /v # the image's user
    docker run -d --name owl-ziti --hostname owl-ziti --network owl-net -v owl-ziti:/persistent "$CLI" \
      edge quickstart --home /persistent --ctrl-address owl-ziti --router-address owl-ziti --password "$PW" >/dev/null
  fi
  echo "waiting for the Ziti controller…"
  for _ in $(seq 1 60); do Z edge login owl-ziti:1280 -u admin -p "$PW" -y >/dev/null 2>&1 && break; sleep 2; done
  for _ in $(seq 1 60); do Z edge list edge-routers 'name="router-instance-1"' 2>/dev/null | grep -q true && break; sleep 2; done
}

# exists TYPE NAME: the entity is there already (every step is idempotent).
exists() { Z edge list "$1" "name=\"$2\"" 2>/dev/null | grep -q "results: 1"; }

model() {
  for s in registry gitlab; do
    exists configs "$s-intercept" || Z edge create config "$s-intercept" intercept.v1 \
      "{\"protocols\":[\"tcp\"],\"addresses\":[\"$s.owl.test\"],\"portRanges\":[{\"low\":443,\"high\":443}]}" >/dev/null
    exists configs "$s-host" || Z edge create config "$s-host" host.v1 '{"protocol":"tcp","address":"owl-gitlab","port":443}' >/dev/null
    exists services "$s" || Z edge create service "$s" --configs "$s-intercept,$s-host" -a gitlab-services >/dev/null
  done
  exists service-policies gitlab-bind || Z edge create service-policy gitlab-bind Bind --service-roles '#gitlab-services' --identity-roles '#gitlab-hosts' >/dev/null
  exists service-policies gitlab-dial || Z edge create service-policy gitlab-dial Dial --service-roles '#gitlab-services' --identity-roles '#owl-clients' >/dev/null
  exists edge-router-policies all-routers || Z edge create edge-router-policy all-routers --edge-router-roles '#all' --identity-roles '#all' >/dev/null
  exists service-edge-router-policies all-services || Z edge create service-edge-router-policy all-services --edge-router-roles '#all' --service-roles '#all' >/dev/null
  for id in gitlab-host:gitlab-hosts owl-client:owl-clients; do
    exists identities "${id%%:*}" || { Z edge create identity "${id%%:*}" -a "${id#*:}" -o "/tmp/${id%%:*}.jwt" >/dev/null
      docker exec owl-ziti cat "/tmp/${id%%:*}.jwt" > "$ZDIR/${id%%:*}.jwt"; }
  done
  if ! exists edge-routers owl-ziti-gw; then
    Z edge create edge-router owl-ziti-gw --tunneler-enabled -o /tmp/gw.jwt >/dev/null
    Z edge update identity owl-ziti-gw -a owl-clients >/dev/null # the router's tunneler dials like a client
    docker exec owl-ziti cat /tmp/gw.jwt > "$ZDIR/owl-ziti-gw.jwt"
  fi
  chmod 600 "$ZDIR"/*.jwt
}

# The tunneller next to GitLab; its identity stays in its volume after the first enrollment.
host_tunneller() {
  [[ -n $(docker ps -q -f name='^owl-ziti-host$') ]] && return
  docker rm -f owl-ziti-host >/dev/null 2>&1 || true
  # created on owl-net (to enroll with the controller), then joined to owl-corp (to reach GitLab)
  docker create --name owl-ziti-host --network owl-net -v owl-ziti-host:/ziti-edge-tunnel -e ZITI_IDENTITY_BASENAME=gitlab-host \
    -e "ZITI_ENROLL_TOKEN=$(cat "$ZDIR/gitlab-host.jwt")" "$HOST" >/dev/null
  docker network connect owl-corp owl-ziti-host
  docker start owl-ziti-host >/dev/null
}

client_sidecar() {
  docker rm -f owl-ziti-gw >/dev/null 2>&1 || true
  docker rm -f owl-server >/dev/null 2>&1 || true # it lives in this container's network: recreated by stack-up
  docker rm -f owl-ziti-client >/dev/null 2>&1 || true
  # The owner of owl-server's network namespace: owl-server's port is published here.
  docker run -d --name owl-ziti-client --network owl-net -p 127.0.0.1:8080:8080 --cap-add NET_ADMIN --device /dev/net/tun \
    -v owl-ziti-client:/ziti-edge-tunnel -e ZITI_IDENTITY_BASENAME=owl-client -e "ZITI_ENROLL_TOKEN=$(cat "$ZDIR/owl-client.jwt")" \
    "$TUNNEL" run >/dev/null
  for _ in $(seq 1 30); do docker exec owl-ziti-client grep -q 100.64.0.2 /etc/resolv.conf 2>/dev/null && break; sleep 1; done
}

client_router() {
  docker rm -f owl-server owl-ziti-client >/dev/null 2>&1 || true
  if [[ -z $(docker ps -q -f name='^owl-ziti-gw$') ]]; then
    docker rm -f owl-ziti-gw >/dev/null 2>&1 || true
    docker volume create owl-ziti-gw >/dev/null
    docker run --rm -v owl-ziti-gw:/v alpine:3.20 chown 2171:2171 /v
    # root: setting up the tproxy rules needs NET_ADMIN, which the image's own user doesn't get
    docker run -d --name owl-ziti-gw --hostname owl-ziti-gw --network owl-net --user 0 --cap-add NET_ADMIN -v owl-ziti-gw:/ziti-router \
      -e ZITI_BOOTSTRAP=true -e "ZITI_ENROLL_TOKEN=$(cat "$ZDIR/owl-ziti-gw.jwt")" -e ZITI_CTRL_ADVERTISED_ADDRESS=owl-ziti \
      -e ZITI_CTRL_ADVERTISED_PORT=1280 -e ZITI_ROUTER_ADVERTISED_ADDRESS=owl-ziti-gw -e ZITI_ROUTER_MODE=tproxy \
      -e ZITI_ROUTER_TPROXY_RESOLVER=udp://0.0.0.0:53 -e ZITI_ROUTER_LAN_INTERFACE=eth0 "$ROUTER" run config.yml >/dev/null
  fi
  for _ in $(seq 1 60); do Z edge list edge-routers 'name="owl-ziti-gw"' 2>/dev/null | grep -q " true " && break; sleep 2; done
}

up() {
  local mode=${MODE:-sidecar}
  [[ $mode == sidecar || $mode == router ]] || { echo "MODE: sidecar or router" >&2; exit 2; }
  [[ -f dev/.gitlab/up ]] || { echo "error: no dev GitLab — run make gitlab-up first" >&2; exit 1; }
  controller
  model
  host_tunneller
  touch "$ZDIR/up"
  echo "$mode" > "$ZDIR/mode"
  dev/gitlab.sh owl-net # GitLab leaves owl-net: from Owl's side its names exist only in Ziti now
  if [[ $mode == sidecar ]]; then client_sidecar; else client_router; fi
  case_ ok
  ready "$mode"
  dev/stack-up.sh
  echo "Owl reaches GitLab only through Ziti ($mode). Try Test in Settings → Registries; make ziti-case CASE=no-login-host|host-down|ok"
}

# ready MODE: wait until the client side resolves the services (a fresh tunneller loads them after enrolling).
ready() {
  local probe
  if [[ $1 == sidecar ]]; then probe=(--network container:owl-ziti-client)
  else probe=(--network owl-net --dns "$(docker inspect -f '{{(index .NetworkSettings.Networks "owl-net").IPAddress}}' owl-ziti-gw)"); fi
  echo "waiting for the Ziti services…"
  for _ in $(seq 1 60); do
    docker run --rm "${probe[@]}" alpine:3.20 getent hosts registry.owl.test >/dev/null 2>&1 && return
    sleep 2
  done
  echo "warning: registry.owl.test doesn't resolve through Ziti yet — docker logs owl-ziti-$([[ $1 == sidecar ]] && echo client || echo gw)" >&2
}

case_() {
  Z edge login owl-ziti:1280 -u admin -p "$PW" -y >/dev/null
  case $1 in
    ok) Z edge update service-policy gitlab-dial --service-roles '#gitlab-services' >/dev/null
        docker start owl-ziti-host >/dev/null
        echo "case ok: registry.owl.test and gitlab.owl.test both reachable through Ziti" ;;
    no-login-host) Z edge update service-policy gitlab-dial --service-roles '@registry' >/dev/null
        docker start owl-ziti-host >/dev/null
        echo "case no-login-host: only registry.owl.test is exposed — GitLab's /jwt/auth on gitlab.owl.test isn't (allow ~1 min for the client to notice)" ;;
    host-down) Z edge update service-policy gitlab-dial --service-roles '#gitlab-services' >/dev/null
        docker stop owl-ziti-host >/dev/null
        echo "case host-down: the hosting tunneller next to GitLab is stopped" ;;
    *) echo "CASE: ok, no-login-host or host-down" >&2; exit 2 ;;
  esac
}

down() {
  docker rm -f owl-ziti owl-ziti-host owl-ziti-client owl-ziti-gw >/dev/null 2>&1 || true
  docker rm -f owl-server >/dev/null 2>&1 || true
  docker volume rm owl-ziti owl-ziti-host owl-ziti-client owl-ziti-gw >/dev/null 2>&1 || true
  rm -rf "$ZDIR"
  [[ -f dev/.gitlab/up ]] && dev/gitlab.sh owl-net
  dev/stack-up.sh
  echo "dev OpenZiti removed; Owl reaches the dev GitLab directly again"
}

case ${1:-up} in
  up) up ;;
  case) case_ "${2:-}" ;;
  down) down ;;
  *) echo "usage: $0 up|case <ok|no-login-host|host-down>|down" >&2; exit 2 ;;
esac
