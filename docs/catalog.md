# Catalog

The catalog tells Owl which images belong to which upstream project and where to look up releases,
end-of-life dates and advisories. It's `internal/catalog/catalog.yaml`, compiled into the binary.

## Built-in components

| id | Component | Release lines from | Advisories from |
|---|---|---|---|
| `kubernetes` | Kubernetes *(from nodes)* | endoflife.date | OSV |
| `rke2` | RKE2 *(from nodes)* | GitHub releases | OSV |
| `k3s` | k3s *(from nodes)* | GitHub releases | OSV |
| `ingress-nginx` | ingress-nginx controller | GitHub releases (`controller-` tags) | OSV |
| `nginx` | nginx | endoflife.date | none published |
| `haproxy` | HAProxy | endoflife.date | none published |
| `haproxy-kubernetes-ingress` | HAProxy Kubernetes Ingress (HAProxy Technologies) | GitHub releases | none published |
| `haproxy-ingress` | HAProxy Ingress (jcmoraisjr) | endoflife.date | GitHub |
| `grafana` | Grafana | endoflife.date | OSV |
| `coredns` | CoreDNS | GitHub releases | OSV |
| `longhorn` | Longhorn | endoflife.date | GitHub |
| `redis` | Redis | endoflife.date | GitHub |
| `valkey` | Valkey | endoflife.date | GitHub |
| `postgresql` | PostgreSQL | endoflife.date | none published |
| `redpanda` | Redpanda | GitHub releases | GitHub |
| `metrics-server` | metrics-server | GitHub releases (`v` tags) | none published |
| `eck-operator` | Elastic Cloud on Kubernetes (ECK) operator | GitHub releases | none published |
| `elasticsearch` | Elasticsearch | endoflife.date | OSV (Maven) |
| `kibana` | Kibana | endoflife.date | none published |
| `logstash` | Logstash | endoflife.date | none published |
| `elastic-beats` | Elastic Beats | GitHub releases | none published |
| `elastic-agent` | Elastic Agent | GitHub releases | none published |
| `elastalert2` | ElastAlert 2 | GitHub releases | none published |
| `traefik` | Traefik *(RKE2/k3s builds marked)* | endoflife.date | GitHub |
| `cilium` | Cilium *(RKE2/k3s builds marked)* | endoflife.date | OSV |
| `hubble-ui` | Hubble UI *(RKE2/k3s builds marked)* | GitHub releases | none published |
| `cilium-certgen` | Cilium certgen *(RKE2/k3s builds marked)* | GitHub releases | none published |
| `envoy` | Envoy *(RKE2/k3s builds marked)* | endoflife.date | GitHub |
| `calico` | Calico *(RKE2/k3s builds marked)* | endoflife.date | OSV |
| `tigera-operator` | Tigera operator (Calico) | GitHub releases | none published |
| `flannel` | flannel *(RKE2/k3s builds marked)* | GitHub releases | OSV |
| `flannel-cni-plugin` | flannel CNI plugin | GitHub releases | none published |
| `metallb` | MetalLB | endoflife.date | none published |
| `frr` | FRRouting (FRR) | GitHub releases (`frr-` tags) | none published |
| `external-dns` | ExternalDNS | GitHub releases (`v` tags) | none published |
| `cert-manager` | cert-manager | endoflife.date | OSV |
| `external-secrets` | External Secrets Operator | GitHub releases (`v` tags) | OSV |
| `sealed-secrets` | Sealed Secrets | GitHub releases (`v` tags) | OSV |
| `prometheus` | Prometheus | endoflife.date | OSV |
| `alertmanager` | Alertmanager | GitHub releases | OSV |
| `prometheus-operator` | Prometheus Operator | GitHub releases | none published |
| `node-exporter` | Prometheus node exporter | GitHub releases | none published |
| `kube-state-metrics` | kube-state-metrics | GitHub releases | none published |
| `loki` | Grafana Loki | endoflife.date | OSV |
| `alloy` | Grafana Alloy | GitHub releases | OSV |
| `tempo` | Grafana Tempo | GitHub releases | OSV |
| `mimir` | Grafana Mimir | GitHub releases (`mimir-` tags) | none published |
| `rollout-operator` | Grafana rollout-operator | GitHub releases | none published |
| `thanos` | Thanos | GitHub releases | none published |
| `opentelemetry-collector` | OpenTelemetry Collector | GitHub releases (`v` tags) | none published |
| `opentelemetry-operator` | OpenTelemetry Operator | GitHub releases | GitHub |
| `fluent-bit` | Fluent Bit | endoflife.date | GitHub |
| `gitlab-runner` | GitLab Runner | endoflife.date | none published |
| `actions-runner-controller` | Actions Runner Controller (scale sets) | GitHub releases (`gha-runner-scale-set-` tags) | none published |
| `actions-runner-controller-legacy` | Actions Runner Controller (legacy, summerwind) | GitHub releases (`v` tags) | none published |
| `actions-runner` | GitHub Actions runner | GitHub releases (`v` tags) | GitHub |
| `velero` | Velero | GitHub releases | OSV |
| `velero-plugin-for-aws` | Velero plugin for AWS | GitHub releases | none published |
| `velero-plugin-for-microsoft-azure` | Velero plugin for Microsoft Azure | GitHub releases | none published |
| `velero-plugin-for-gcp` | Velero plugin for GCP | GitHub releases | none published |
| `rook` | Rook (Ceph operator) | GitHub releases | none published |
| `ceph` | Ceph | endoflife.date | none published |
| `ceph-csi` | Ceph CSI | GitHub releases | none published |
| `local-path-provisioner` | Local Path Provisioner | GitHub releases | OSV |
| `csi-snapshotter` | CSI snapshotter and snapshot controller | GitHub releases (`v` tags) | none published |
| `csi-provisioner` | CSI external-provisioner | GitHub releases | none published |
| `csi-attacher` | CSI external-attacher | GitHub releases | none published |
| `csi-resizer` | CSI external-resizer | GitHub releases | none published |
| `csi-node-driver-registrar` | CSI node-driver-registrar | GitHub releases | none published |
| `csi-livenessprobe` | CSI livenessprobe | GitHub releases | none published |
| `csi-external-health-monitor` | CSI external-health-monitor | GitHub releases | none published |

Images RKE2 or k3s ship themselves (`rancher/hardened-*`, `rancher/mirrored-*`) are listed under the
component with `bundledWith` — see Settings → Catalog for the full image lists.

"None published" means the project doesn't publish GitHub security advisories; Owl shows **no
advisory data** for it rather than implying it's clean.

Owl fetches upstream data only for components that **run in at least one cluster** (plus every
component added in the UI), so a long catalog costs nothing until you deploy something from it. Settings →
Catalog lists every built-in component with a green dot (running, and in how many clusters) or a grey
one (not running).

## Format

```yaml
components:
  - id: postgresql                    # stable id: used in URLs, policies and overrides
    name: PostgreSQL
    images: [postgres, bitnami/postgresql, bitnamilegacy/postgresql]
    github: postgres/postgres         # releases, archived status, advisories
    eol: postgresql                   # endoflife.date slug — only if it's verified to be the same project
    versionParts: 2                   # 17.2 is fully pinned; "postgres:17" is floating
```

| Field | Meaning |
|---|---|
| `id`, `name` | Identifier and display name. Never change an `id` once used: policies and overrides refer to it. |
| `images` | Image names without tag: `registry/repository`, or a bare repository for Docker Hub (`redis` = `docker.io/library/redis`). Mirrors are matched by path suffix. |
| `github` | `owner/repo` for releases, archived/retired detection and repository advisories. |
| `eol` | endoflife.date product slug. **Set only when verified to be the same project** — e.g. endoflife.date `haproxy-ingress` is jcmoraisjr's project, not HAProxy Technologies'. |
| `versionParts` | How many numeric parts a pinned version has: 1–3 (default 3). Tags with fewer parts are "unpinned (floating)". |
| `tagPrefix` | Prefix stripped from image tags before parsing (`controller-v1.10.4`). |
| `releaseTagPrefix` | Only GitHub release tags with this prefix count (repositories that release several things). |
| `osv` | `{ecosystem: Go, name: <module>}` — use OSV.dev for advisories instead of the repository's. Any OSV ecosystem works (`PyPI`, `Maven`, `npm` …); set it only when the package is the same project **and** its versions match the image tags. |
| `bundledWith` | `{<image>: rke2 \| k3s \| <component id>}` — what ships and upgrades that image itself: a distribution (`rancher/hardened-*`, `rancher/mirrored-*`; applies in clusters of that distribution) or another built-in component (`quay.io/cilium/cilium-envoy: cilium`; applies everywhere). The recommended action becomes "Comes with RKE2 — upgrade RKE2 …" and the UI tags it *RKE2-managed* / *Cilium-managed*; the level is unchanged. Each image must also be in `images`. |
| `source: cluster` | Not matched by image: the version comes from the cluster's nodes. |
| `notes` | Shown with the component. |
| `versionPattern` | A regular expression for tags `ParseVersion` can't read: its groups, joined with ".", are the version (`^(\d+\.\d+)_p(\d+)`: `9.7_p1-r4-ls163` → 9.7.1). Also for tags with two versions: `-kafka-(\d+\.\d+\.\d+)$` reads Strimzi's `0.45.0-kafka-3.9.0` as Kafka 3.9.0. |
| `releaseNotes` | Where [upgrade summaries](concepts.md#upgrade-summaries) read release notes when the GitHub releases don't carry them: `kubernetes-changelog`, `redis-releasenotes`, `haproxy-changelog`, `postgres-release-notes`. Empty = GitHub release notes. A new source needs code in `internal/upstream/notes.go`. |

`releaseTagPrefix` is also stripped before reading a version from a GitHub release tag (nginx tags
releases `release-1.27.4`).

## How tags become versions

- A leading `v` is dropped; vendor suffixes are stripped: `-hardened3`, `-build20240305`,
  `-debian-12-r20`, `-alpine` … (`rancher/hardened-coredns:v1.11.1-build20240305` → 1.11.1).
- Real pre-releases are kept (`1.12.0-rc.1`).
- `latest`, digests only, non-version tags and too few version parts → **unpinned**.

## Adding a component

What gets a built-in entry: it runs as a container image in the cluster, has a public GitHub repository
with tagged releases, and its image names and tag format were checked against real published images.
Anything else is better added from the UI.

1. Add an entry to `catalog.yaml` with **every** image the tool ships — upstream, Bitnami
   (`bitnami/*` and `bitnamilegacy/*`), vendor mirrors, and its helper images. Helpers that share the
   tool's version (cert-manager's webhook) belong to the same entry; helpers with their own release cycle
   (CSI sidecars) get their own. A standard install should leave no unrecognized images.
2. Check the endoflife.date product (and the OSV package) is the same project before setting `eol` (`osv`).
3. Check it against the live upstreams:
   ```bash
   make catalog-check COMPONENTS=<id>[,<id>…]    # needs GITHUB_TOKEN in .env
   ./bin/owl upstream --component <id> --version <a version you run>
   ```
   `catalog-check` fails on a missing repository, endoflife.date product or release data, and warns
   about an archived repository, endoflife.date and GitHub disagreeing about the newest release line,
   and an OSV package without advisories. Warnings need a human look (ingress-nginx *is* archived);
   it's a manual check, not part of CI.
4. Add real image references to `internal/catalog/catalog_test.go` (image → component and version).
5. `make test`, rebuild, and check the component in the UI.

Or add a component **from the UI** (Settings → Catalog, or **New component** on an unrecognized image):
see [User guide → Catalog](user-guide.md#catalog). Those live in the database (restored with backups),
can't set `osv` or `releaseNotes`, and a built-in component with the same id or image always wins.
**Copy as YAML** turns one into an entry for this file.

## Overrides (in the UI)

For images the catalog doesn't recognize — your mirror with an odd path, a fork, your own software —
use the actions on a cluster's **Unrecognized images** card instead of editing the catalog:

| Override | Effect |
|---|---|
| map → component | Treat this repository as that component (scored normally, flagged "mapped"). |
| in-house | Our own software: listed, never scored. |
| ignore | Hidden from the unrecognized list. |

Overrides are per repository (across all clusters), stored in the database, included in backups and
listed with **Undo** in Settings → Catalog. **In-house** and **ignore** apply even after a newer Owl's
built-in catalog learns the image — an upgrade never undoes your choice (Settings shows "Owl now
recognizes this as …"); only a component you add yourself for that repository takes precedence. **Map**
applies only while the catalog doesn't recognize the image; once a component covers it, that one wins
and Settings shows "Now covered by … — remove?". Registries passed
in `--internal-registries` are in-house without any override.
