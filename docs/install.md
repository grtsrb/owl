# Installation

Owl is one binary/image with two modes: `owl server` (central, with the UI) and `owl agent` (one per
cluster). The same image `owl:<version>` runs both.

- [Requirements](#requirements)
- [1. Build the image](#1-build-the-image)
- [2. Run the server](#2-run-the-server)
- [3. First-time setup](#3-first-time-setup)
- [4. Connect a cluster](#4-connect-a-cluster)
- [5. Configure backups](#5-configure-backups)
- [6. Optional: Claude upgrade summaries](#6-optional-claude-upgrade-summaries)
- [Upgrading](#upgrading)
- [Uninstalling an agent](#uninstalling-an-agent)

## Requirements

- **Server**: Docker (or any container runtime) and a persistent volume for `/data`. It is light:
  about 40 MB of memory with three clusters.
  Outbound HTTPS to `api.github.com`, `endoflife.date` and `api.osv.dev`.
- **Agents**: Kubernetes with the `apps/v1` and `batch/v1` APIs (developed on k3s 1.33–1.36, used on
  RKE2). Outbound HTTPS from the cluster to the server.
  The agent requests 10m CPU / 32 Mi memory.
- A **GitHub token with no scopes** (public read only) — without it GitHub allows 60 requests/hour
  and upstream data will be incomplete.
- Optional: an **S3-compatible bucket** for backups (AWS S3, Hetzner Object Storage, MinIO, …).

> ⚠️ **Network exposure.** Until sign-in lands (milestone 9) the read API and UI are unauthenticated.
> Run the server on a private network or behind a VPN/reverse proxy with authentication. Never
> publish it to the internet. See [Security](security.md).

## 1. Build the image

```bash
make image                       # owl:dev
make image IMAGE=registry.example.com/team/owl:0.7.0 VERSION=0.7.0
docker push registry.example.com/team/owl:0.7.0
```

The image is distroless, runs as non-root (UID 65532) and contains only `/owl`.

## 2. Run the server

Generate an admin token (at least 24 characters) and keep it in a secret store:

```bash
openssl rand -base64 33 | tr -d '/+=\n'
```

Run the container. A minimal example:

```bash
docker run -d --name owl-server --restart unless-stopped \
  -p 10.0.0.5:8080:8080 \
  -v owl-data:/data \
  --log-opt max-size=10m --log-opt max-file=3 \
  -e OWL_LISTEN=0.0.0.0:8080 \
  -e OWL_ALLOWED_HOSTS=owl.internal.example.com \
  -e OWL_ADMIN_TOKEN -e GITHUB_TOKEN \
  -e OWL_INTERNAL_REGISTRIES=registry.gitlab.example.com \
  registry.example.com/team/owl:0.7.0 server
```

- `OWL_LISTEN=0.0.0.0:8080` is needed inside a container (the default is `127.0.0.1:8080`). Restrict
  exposure with the `-p <ip>:` part.
- `OWL_ALLOWED_HOSTS` must list every host name you use in the browser (localhost is always allowed).
  Anything else gets `421 host not allowed` — this blocks DNS-rebinding attacks.
- Pass secrets **by name** (`-e OWL_ADMIN_TOKEN`) from the environment or an env file, never inline.
- All state lives in `/data`: `owl.db`, `backups/`, `logs/`.

Check it:

```bash
curl -s http://10.0.0.5:8080/healthz     # ok
docker logs owl-server | head
```

The full list of settings is in [Configuration](configuration.md).

### TLS

The server speaks plain HTTP. Put a TLS-terminating reverse proxy (nginx, Caddy, Traefik, an ALB) in
front of it. Agents refuse `http://` server URLs unless `OWL_ALLOW_INSECURE_HTTP=true` (meant for
local development only).

## 3. First-time setup

1. Open the UI, go to **Settings → Admin token**, paste the admin token, click **Use**.
2. **Settings → Stages**: adjust the stage list and order (default `staging → prelive → live`).
3. **Settings → Tenants**: add your tenants (e.g. `IT`, `DS`).
4. **Settings → Clusters → Agent settings**: set the **agent image** (as pushed above) and the
   **server URL** agents use (e.g. `https://owl.internal.example.com`).
5. Optional: **Settings → Version policy** — change the global default if `lts-or-n-1` doesn't fit.

## 4. Connect a cluster

### From the UI (recommended)

**Settings → Clusters → Add cluster**: enter name, stage, tenant. Download both files and apply them
with the cluster's kube context:

```bash
kubectl apply -f agent.yaml
kubectl apply -f agent-secret-<cluster>.yaml
rm agent-secret-<cluster>.yaml          # the token is in the cluster now
```

The first report arrives within about 30 seconds; the cluster then shows its workload count in
**Clusters**.

### From the command line

On the server host:

```bash
docker exec owl-server /owl cluster add --name it-aws-prod --stage live --tenant IT
# prints the token once
```

Then in the cluster:

```bash
curl -s http://<server>/api/v1/agent/manifest > agent.yaml && kubectl apply -f agent.yaml
kubectl -n owl create secret generic owl-agent \
  --from-literal=OWL_SERVER_URL=https://owl.internal.example.com \
  --from-literal=OWL_TOKEN=<token>
```

(The `create secret` form leaves the token in your shell history; prefer a file you apply and delete.)

### What the agent manifest contains

- namespace `owl`, service account `owl-agent`,
- ClusterRole `owl-agent-read`: `get`/`list` on pods, nodes, deployments, statefulsets, daemonsets,
  replicasets, jobs, cronjobs — **no Secrets, no ConfigMaps, no write verbs**,
- Deployment `owl-agent` (1 replica, non-root, read-only root filesystem, all capabilities dropped),
  reporting every 15 minutes (`OWL_INTERVAL`), reading `OWL_SERVER_URL` and `OWL_TOKEN` from the
  `owl-agent` Secret.

## 5. Configure backups

Without a bucket, backups stay on the server's own disk and the UI warns that this isn't safe. Set
the `OWL_BACKUP_S3_*` variables and restart — see [Operations → Backups](operations.md#backups),
including a bucket policy that doesn't allow deleting.

## 6. Optional: Claude upgrade summaries

Summaries cost money per use and are **off** until you switch them on. Set up the hard ceiling on
Anthropic's side first:

1. In the Claude Console create a **dedicated workspace** for Owl, with its own API key.
2. Give that workspace a **monthly spend limit**, use **prepaid credits** and turn **auto-reload off**.
3. Pass the key as `ANTHROPIC_API_KEY` (environment only) and restart the server.
4. In **Settings → Summaries**, start with **Estimate only** to see what each summary would cost, then
   **Manual**, and **Automatic** only once you're comfortable. Check the daily/monthly caps there.

Details: [Operations → Claude upgrade summaries](operations.md#claude-upgrade-summaries).

## Upgrading

1. Take a backup (**Settings → Backups & restore → Back up now**).
2. Pull/build the new image, recreate the server container with the same volume and environment.
   Database migrations run automatically at startup. A database written by a *newer* Owl refuses to
   start with an older binary — roll back by restoring the pre-upgrade backup, not by downgrading
   over the migrated database.
3. Update the agents: set the new agent image in **Agent settings**, re-download `agent.yaml`, apply it
   in each cluster (or `kubectl -n owl set image deploy/owl-agent agent=<image>`).

Agents and servers of different versions work together as long as the snapshot schema version is
the same; the server rejects an incompatible agent with a clear `400` in the agent's logs.

## Uninstalling an agent

```bash
kubectl delete -f agent.yaml     # namespace, RBAC, Deployment (and the Secret, via the namespace)
```

Then delete the cluster in **Settings → Clusters** (export it first if you want to keep its history),
or **Rotate token** if you only want to cut it off and keep the data.
