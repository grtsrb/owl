#!/usr/bin/env bash
# Seeds the local k3d clusters with third-party components at deliberately different/old versions,
# so the inventory has realistic drift, EOL and vendor-image cases to show.
#
#   dev/seed/seed.sh            # seed all three clusters
#   dev/seed/seed.sh it-hetzner # seed one
#
# Heavy components (Longhorn, Redpanda, ingress controllers, HAProxy) are scaled to 0: the agent
# reads workload templates, so their versions still count without running anything.
set -euo pipefail

# name | kind | replicas | image          (one table per cluster)
staging=(
  "web|Deployment|1|nginx:1.27.3"
  "cache|Deployment|1|redis:7.4.1"
  "db|StatefulSet|1|postgres:17.2"
  "grafana|Deployment|1|grafana/grafana:11.3.0"
  "ingress-nginx-controller|Deployment|0|registry.k8s.io/ingress-nginx/controller:v1.11.3"
  "haproxy|Deployment|0|haproxy:3.0.5"
  "haproxy-ingress|Deployment|0|haproxytech/kubernetes-ingress:3.0.1"
  "longhorn-manager|Deployment|0|longhornio/longhorn-manager:v1.7.2"
  "redpanda|StatefulSet|0|docker.redpanda.com/redpandadata/redpanda:v24.2.7"
  "elastic-operator|StatefulSet|0|docker.elastic.co/eck/eck-operator:3.4.1"
  "logs-es|StatefulSet|0|docker.elastic.co/elasticsearch/elasticsearch:8.19.25"
  "logs-kb|Deployment|0|docker.elastic.co/kibana/kibana:8.19.25"
  "elastalert|Deployment|0|jertel/elastalert2:2.32.0"
  "cert-manager|Deployment|0|quay.io/jetstack/cert-manager-controller:v1.21.3"
  "prometheus|StatefulSet|0|quay.io/prometheus/prometheus:v3.16.0"
  "gitlab-runner|Deployment|0|gitlab/gitlab-runner:alpine-v18.5.0"
  "velero|Deployment|0|velero/velero:v1.18.4"
  "billing-api|Deployment|0|registry.gitlab.example/it/billing-api:2.4.0"
)
test=(
  "web|Deployment|1|nginx:1.26.2"
  "cache|Deployment|1|redis:7.2.4"
  "db|StatefulSet|1|postgres:16.4"
  "grafana|Deployment|0|grafana/grafana:10.4.2"
  "ingress-nginx-controller|Deployment|0|rancher/nginx-ingress-controller:v1.10.4-hardened3"
  "haproxy|Deployment|0|haproxy:2.8.5"
  "haproxy-ingress|Deployment|0|haproxytech/kubernetes-ingress:1.11.4"
  "longhorn-manager|Deployment|0|longhornio/longhorn-manager:v1.6.2"
  "redpanda|StatefulSet|0|docker.redpanda.com/redpandadata/redpanda:v23.3.13"
  "elastic-operator|StatefulSet|0|docker.elastic.co/eck/eck-operator:3.3.2"
  "logs-es|StatefulSet|0|docker.elastic.co/elasticsearch/elasticsearch:8.19.22"
  "logs-kb|Deployment|0|docker.elastic.co/kibana/kibana:8.19.22"
  "elastalert|Deployment|0|jertel/elastalert2:2.31.0"
  "cert-manager|Deployment|0|quay.io/jetstack/cert-manager-controller:v1.20.4"
  "prometheus|StatefulSet|0|prom/prometheus:v3.13.4"
  "traefik|Deployment|0|traefik:v3.7.18"
  "gitlab-runner|Deployment|0|gitlab/gitlab-runner:v18.3.0"
  "velero|Deployment|0|velero/velero:v1.17.1"
  "billing-api|Deployment|0|registry.gitlab.example/it/billing-api:2.4.0"
  "session-cache|Deployment|0|redis:latest"
)
prod=(
  "web|Deployment|1|nginx:1.25.4"
  "cache|Deployment|1|redis:7.2.4"
  "legacy-cache|Deployment|0|bitnami/redis:7.0.15-debian-12-r20"
  "db|StatefulSet|1|postgres:14.13"
  "db-backup|CronJob|0|postgres:14.13"
  "grafana|Deployment|0|grafana/grafana:9.5.3"
  "ingress-nginx-controller|Deployment|0|rancher/nginx-ingress-controller:v1.11.5-hardened1"
  "haproxy|Deployment|0|haproxy:2.4.24"
  "haproxy-ingress|Deployment|0|haproxytech/kubernetes-ingress:1.10.11"
  "longhorn-manager|Deployment|0|longhornio/longhorn-manager:v1.5.5"
  "redpanda|StatefulSet|0|docker.redpanda.com/redpandadata/redpanda:v23.2.24"
  "elastic-operator|StatefulSet|0|docker.elastic.co/eck/eck-operator:3.0.0"
  "logs-es|StatefulSet|0|docker.elastic.co/elasticsearch/elasticsearch:8.19.21"
  "logs-kb|Deployment|0|docker.elastic.co/kibana/kibana:8.19.21"
  "elastalert|Deployment|0|jertel/elastalert2:2.28.0"
  "cert-manager|Deployment|0|quay.io/jetstack/cert-manager-controller:v1.19.6"
  "prometheus|StatefulSet|0|prom/prometheus:v3.13.4"
  "alertmanager|StatefulSet|0|quay.io/prometheus/alertmanager:v0.33.1"
  "traefik|Deployment|0|traefik:v2.11.63"
  "fluent-bit|Deployment|0|fluent/fluent-bit:5.1.5"
  "gitlab-runner|Deployment|0|gitlab/gitlab-runner:v17.11.0"
  "velero|Deployment|0|velero/velero:v1.16.0"
  "velero-aws|Deployment|0|velero/velero-plugin-for-aws:v1.12.0"
  "longhorn-csi-attacher|Deployment|0|longhornio/csi-attacher:v4.8.0"
  "coredns-hardened|Deployment|0|rancher/hardened-coredns:v1.11.1-build20240305"
  "billing-api|Deployment|0|registry.gitlab.example/it/billing-api:2.3.1"
  "mystery-tool|Deployment|0|ghcr.io/acme/mystery-tool:0.4.0"
)

manifest() { # name kind replicas image
  local name=$1 kind=$2 replicas=$3 image=$4 env=""
  [[ $image == postgres:* ]] && env='
          env: [{name: POSTGRES_PASSWORD, value: dev-only}]'
  local pod="
      containers:
        - name: $name
          image: $image${env}
          resources: {requests: {cpu: 10m, memory: 32Mi}}"
  case $kind in
    Deployment|StatefulSet)
      cat <<EOF
---
apiVersion: apps/v1
kind: $kind
metadata: {name: $name, namespace: seed, labels: {app: $name}}
spec:
  replicas: $replicas
$( [[ $kind == StatefulSet ]] && echo "  serviceName: $name" )
  selector: {matchLabels: {app: $name}}
  template:
    metadata: {labels: {app: $name}}
    spec:${pod}
EOF
      ;;
    CronJob)
      local nl=$'\n' jobpod
      jobpod=${pod//$nl/$nl    } # $'..' isn't expanded inside heredocs, so indent here
      cat <<EOF
---
apiVersion: batch/v1
kind: CronJob
metadata: {name: $name, namespace: seed}
spec:
  schedule: "0 3 * * *"
  suspend: true
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: OnFailure${jobpod}
EOF
      ;;
  esac
}

seed() { # cluster tableName
  local cluster=$1 rows; eval "rows=(\"\${$2[@]}\")" # bash 3.2 (macOS) has no namerefs
  echo "==> seeding $cluster"
  {
    echo "apiVersion: v1
kind: Namespace
metadata: {name: seed}"
    for row in "${rows[@]}"; do IFS='|' read -r n k r i <<<"$row"; manifest "$n" "$k" "$r" "$i"; done
  } | kubectl --context "k3d-$cluster" apply -f -
}

if [[ $# -eq 0 ]]; then set -- it-hetzner it-aws-test it-aws-prod; fi
for c in "$@"; do
  case $c in
    it-hetzner)  seed it-hetzner staging ;;
    it-aws-test) seed it-aws-test test ;;
    it-aws-prod) seed it-aws-prod prod ;;
    *) echo "unknown cluster $c" >&2; exit 1 ;;
  esac
done
