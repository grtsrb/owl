# Installation

Owl is one binary with two modes: `owl server` (central, with the UI) and `owl agent` (one per
cluster). It ships as two images with the same version tag: `owl:<version>` for the server and the slim
`owl-agent:<version>` for the clusters.

- [Requirements](#requirements)
- [1. Build and push the images](#1-build-and-push-the-images)
- [2. Run the server](#2-run-the-server) — [Docker Compose on one host](#docker-compose-on-one-host-recommended),
  [inside a cluster](#inside-a-cluster), [plain `docker run`](#plain-docker-run)
- [3. First admin and first-time setup](#3-first-admin-and-first-time-setup)
- [4. Connect a cluster](#4-connect-a-cluster)
- [5. Configure backups](#5-configure-backups)
- [6. Optional: Claude upgrade summaries](#6-optional-claude-upgrade-summaries)
- [7. Optional: email](#7-optional-email)
- [8. Optional: Google sign-in](#8-optional-google-sign-in)
- [9. Optional: Mattermost notifications](#9-optional-mattermost-notifications)
- [10. Optional: the CLI](#10-optional-the-cli)
- [Upgrading](#upgrading)
- [Uninstalling an agent](#uninstalling-an-agent)

## Requirements

- **Server**: a host with Docker (or a Kubernetes cluster) and a persistent volume for `/data`. It is
  light: about 40 MB of memory with three clusters (plus up to 128 MB briefly while checking
  passwords). Outbound HTTPS to `api.github.com`, `endoflife.date` and `api.osv.dev`.
- **A domain name and HTTPS.** Sign-in cookies are `Secure`, so browsers only keep you signed in over
  HTTPS (plain `http://` works only on `localhost`). The Compose setup gets a certificate from Let's
  Encrypt automatically; it needs a DNS record pointing at the host and ports 80 and 443 reachable.
- **Agents**: Kubernetes with the `apps/v1` and `batch/v1` APIs (developed on k3s 1.33–1.36, used on
  RKE2). Outbound HTTPS from the cluster to the server. The agent requests 10m CPU / 32 Mi memory.
- A **GitHub token with no scopes** (public read only) — without it GitHub allows 60 requests/hour
  and upstream data will be incomplete.
- Optional: an **S3-compatible bucket** for backups (AWS S3, Hetzner Object Storage, …), an **SMTP
  server** for invite and reset emails, a **Google OAuth client** for Google sign-in.

> **Why:** Owl decides who may sign in with `Secure` cookies and rate limits per client, and agents send tokens; all of that is only safe over HTTPS. A GitHub token with no scopes can read nothing private — it only raises the rate limit.

## 1. Build and push the images

Both images are distroless and run as non-root (UID 65532):

- `owl` — the server: `/owl` (UI, database, backups, Claude summaries; `owl agent` too), the CLI
  downloads for four platforms in `/cli`, and the data directory `/data`.
- `owl-agent` — what every cluster runs: only `/owl`, built with nothing but `owl agent`.

Build them from a checkout of the Owl source, for both CPU architectures (amd64, and arm64 for Graviton
instances like `t4g`) and push them to your registry (log in first, e.g. `docker login registry.gitlab.example.com`):

```bash
make image-push REGISTRY=registry.gitlab.example.com/devops VERSION=0.9.0
# -> registry.gitlab.example.com/devops/owl:0.9.0       (linux/amd64 + linux/arm64)
# -> registry.gitlab.example.com/devops/owl-agent:0.9.0 (linux/amd64 + linux/arm64)
```

`make image` builds local single-architecture images (`owl:dev`, `owl-agent:dev`) instead.

> **Why:** every cluster pulls the agent image, and image scanners in the clusters report what's inside it. With one shared image they pulled the whole server (UI, CLI downloads for four platforms) and reported findings for libraries the agent never runs — S3 client, Claude SDK, SQLite. The agent image has none of them.

## 2. Run the server

### Docker Compose on one host (recommended)

This setup (`deploy/compose/` in the source) runs Owl behind **Caddy**, which gets and renews the HTTPS
certificate from Let's Encrypt. Owl itself isn't published on any port; only Caddy is.

**The host** (example: AWS EC2):

- A **`t4g.small`** (2 vCPU, 2 GB, arm64) is plenty. Amazon Linux 2023 or Ubuntu 24.04.
- A separate **EBS volume** (gp3, 10 GB) for the data, mounted at `/srv/owl` — the database, backups
  and logs survive replacing the instance. Enable EBS snapshots if you like; Owl's own backups go to S3.
- **Security group**: 443 and 80 open (users, agents, and Let's Encrypt's HTTP check; port 80 only
  redirects to HTTPS), SSH only from your network (or use SSM instead).
- A **DNS record** (`owl.example.com`) pointing at the instance's Elastic IP.
- Docker with the Compose plugin.

**Set it up:**

```bash
sudo mkdir -p /srv/owl && sudo chown 65532:65532 /srv/owl   # Owl runs as UID 65532
mkdir ~/owl && cd ~/owl
# create compose.yaml, Caddyfile and owl.env.example here (below), then:
cp owl.env.example .env && chmod 600 .env
vi .env           # OWL_IMAGE, OWL_DOMAIN, OWL_PUBLIC_URL, GITHUB_TOKEN, S3, SMTP, …
docker compose up -d
docker compose logs -f owl      # "server listening", then "no users yet: create the first admin …"
```

`.env` holds every setting and secret (see [Configuration](configuration.md)); it never goes into
Git. Both containers restart automatically (also after a reboot), and Docker's own log copies are
capped at 3 × 10 MB.

Check it: `curl -s https://owl.example.com/healthz` prints `ok`. The first start can take a minute
while Caddy gets the certificate.

The three files, exactly as shipped with this version (in the source:
[`deploy/compose/`](../deploy/compose)):

<!-- include deploy/compose/compose.yaml -->

<!-- include deploy/compose/Caddyfile -->

<!-- include deploy/compose/owl.env.example -->

> **Why:** Compose on its own host keeps Owl up when a cluster it watches is down, and Caddy handles certificates without anything else to run. Owl itself listens only inside the Compose network; only Caddy is reachable.

### Inside a cluster

`server.yaml` (below) runs the server as a one-replica StatefulSet with its own volume
(SQLite has one writer: never scale it up), plus an example Ingress for ingress-nginx and cert-manager.

```bash
kubectl apply -f deploy/server/server.yaml        # edit the image first
kubectl -n owl create secret generic owl-server --from-env-file=owl.env
kubectl apply -f deploy/server/ingress.example.yaml   # edit host, issuer, class
```

`owl.env` here is like the Compose one, without `OWL_IMAGE`/`OWL_DOMAIN`/`OWL_DATA_DIR`, and with
`OWL_TRUSTED_PROXIES` set to the ingress controller's pod network (RKE2 default `10.42.0.0/16`).
The trade-off: Owl then runs inside one of the clusters it watches — if that cluster is down, so is
Owl. Prefer a separate host for the production instance.

The two files, exactly as shipped with this version (in the source:
[`deploy/server/`](../deploy/server)):

<!-- include deploy/server/server.yaml -->

<!-- include deploy/server/ingress.example.yaml -->

### Plain `docker run`

On any other host, run the container behind your own TLS-terminating reverse proxy (nginx, Traefik,
an ALB):

```bash
docker run -d --name owl-server --restart unless-stopped \
  -p 127.0.0.1:8080:8080 -v /srv/owl:/data --env-file owl.env \
  --log-opt max-size=10m --log-opt max-file=3 \
  -e OWL_LISTEN=0.0.0.0:8080 -e OWL_TRUSTED_PROXIES=127.0.0.1 \
  registry.gitlab.example.com/devops/owl:0.9.0 server
```

- `OWL_LISTEN=0.0.0.0:8080` is needed inside a container (the default is `127.0.0.1:8080`); the
  `-p 127.0.0.1:` part keeps it reachable only by the proxy on the same host.
- `OWL_PUBLIC_URL` (in `owl.env`) is the `https://` address users open. Its host is allowed
  automatically; other names you use need `OWL_ALLOWED_HOSTS`, anything else gets `421 host not
  allowed` (this blocks DNS-rebinding attacks).
- `OWL_TRUSTED_PROXIES` lists the proxy's address so the audit log and the sign-in rate limits see the
  real client IP (from `X-Forwarded-For`) instead of the proxy's.
- Agents refuse `http://` server URLs unless `OWL_ALLOW_INSECURE_HTTP=true` (local development only).

## 3. First admin and first-time setup

Owl is invite-only. Nobody can sign in until the first admin exists, so create that invite on the
server (it works directly on the database):

```bash
docker compose exec owl /owl db user invite --admin you@example.com        # Compose
kubectl -n owl exec owl-server-0 -- /owl db user invite --admin you@example.com   # in a cluster
```

It prints a one-time link (valid 72 hours). Open it, choose a password (at least 12 characters), and
you're signed in. Then:

1. **Settings → Users → Invite user** for everyone else (viewer or admin). With email set up they get
   the link by email; otherwise copy it and send it yourself (e.g. on Mattermost).
2. **Settings → Stages & tenants**: adjust the stage list and order (default `staging → prelive → live`).
3. On the same page, add your tenants (e.g. `IT`, `DS`).
4. **Settings → Clusters → Agent settings**: set the **agent image** (`owl-agent`, as pushed above) and the
   **server URL** agents use (usually the same as `OWL_PUBLIC_URL`).
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

With the [CLI](cli.md) and a read & write token, from your machine — straight into the cluster:

```bash
owl cluster add it-aws-prod --stage live --tenant IT -o secret | kubectl --context it-aws-prod apply -f -
```

(On the server host without a token: `docker compose exec owl /owl db cluster add --name it-aws-prod
--stage live --tenant IT` prints the token once.)

Then in the cluster:

```bash
curl -s https://owl.example.com/api/v1/agent/manifest > agent.yaml && kubectl apply -f agent.yaml
kubectl -n owl create secret generic owl-agent \
  --from-literal=OWL_SERVER_URL=https://owl.example.com \
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

> **Why:** the agent only pushes and only reads. The server never connects into your clusters and stores no kubeconfigs, so a compromised Owl can't reach them, and the agent's role can't read Secrets or change anything. The token sits in a Secret you apply from a file, never in a URL or a command line.

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

> **Why:** Owl's own daily and monthly caps protect you from Owl's mistakes; the workspace spend limit and prepaid credits protect you from everything else, including a leaked key. Starting with *Estimate only* shows the cost before anything is spent.

## 7. Optional: email

With SMTP configured, Owl emails invite links, password-reset links (admin-triggered and
"Forgot password?" on the sign-in page) and test messages. Without it, everything still works: admins
copy the links and send them themselves.

Set `OWL_SMTP_HOST`, `OWL_SMTP_PORT`, `OWL_SMTP_TLS`, `OWL_SMTP_USERNAME`, `OWL_SMTP_PASSWORD` and
`OWL_SMTP_FROM` (and `OWL_PUBLIC_URL`, which the links point to), restart, then **Settings → Users →
Send me a test email**. Owl only talks to the mail server over TLS (port 465, or STARTTLS on 587) and
refuses to send a password over an unencrypted connection. With Google Workspace, use
`smtp-relay.gmail.com` or `smtp.gmail.com` with an app password; with AWS, Amazon SES SMTP credentials.

## 8. Optional: Google sign-in

Invited users can then sign in with the Google account of their invited email address. You don't need
to be a Workspace admin to set this up, only access to a Google Cloud project:

1. [Google Cloud console](https://console.cloud.google.com/) → **APIs & Services → OAuth consent
   screen**: user type **Internal** if you use Google Workspace (only your organisation's accounts),
   otherwise **External**. App name "Owl"; no extra scopes are needed (Owl asks only for `openid` and
   `email`, which need no Google review).
2. **Credentials → Create credentials → OAuth client ID → Web application**. Authorized redirect URI:
   `https://owl.example.com/auth/google/callback` (your `OWL_PUBLIC_URL` + `/auth/google/callback`;
   Settings → Users shows the exact value).
3. Put the client ID and secret in `OWL_GOOGLE_CLIENT_ID` / `OWL_GOOGLE_CLIENT_SECRET`. Optionally set
   `OWL_GOOGLE_DOMAIN=example.com` so only accounts of that Workspace domain are accepted. Restart.
4. Sign in with Google once as an admin (the first Google sign-in links the Google account to the
   invited user). Optionally then **Settings → Users → Password sign-in → Turn off**, so everyone uses
   Google (and its 2-step verification).

If Google sign-in ever breaks while passwords are off, `owl db user reset-link --email <email>` on the server
prints a link that sets a password and signs you in — see [Troubleshooting](troubleshooting.md#locked-out-no-admin-can-sign-in).

## 9. Optional: Mattermost notifications

1. In Mattermost: **Integrations → Incoming Webhooks → Add Incoming Webhook**, pick the channel (e.g.
   `#owl`), save, copy the URL. Optionally let integrations override usernames (System Console →
   Integrations) so posts show as "Owl".
2. Put it in `OWL_MATTERMOST_WEBHOOK_URL` (environment only) and restart.
3. **Settings → Notifications → Send test message**, then switch on the weekly digest and the alerts
   you want. Everyone in the channel sees cluster and component names — pick the channel accordingly.

Details: [Operations → Notifications](operations.md#notifications).

## 10. Optional: the CLI

Account (your name in the sidebar) → **Command-line tool**: download `owl` for your machine, create an
API token under **API tokens**, then `owl login --server https://owl.example.com`. See [CLI](cli.md) —
including keeping users in a `users.yaml` (`owl users plan/apply`) and failing CI on Critical findings.

## Upgrading

1. Take a backup (**Settings → Backups & restore → Back up now**).
2. Push the new image, set it in `.env` (`OWL_IMAGE`) and run `docker compose up -d` (or change the image of the StatefulSet). Database migrations run automatically at startup.
   A database written by a *newer* Owl refuses to start with an older binary — roll back by restoring
   the pre-upgrade backup, not by downgrading over the migrated database.
3. Update the agents: set the new agent image in **Agent settings**, re-download `agent.yaml`, apply it
   in each cluster (or `kubectl -n owl set image deploy/owl-agent agent=<image>`).
   Agents set up before the slim image still run the full `owl` image — that keeps working; switch them
   to `owl-agent` with the same command.

Agents and servers of different versions work together as long as the snapshot schema version is
the same; the server rejects an incompatible agent with a clear `400` in the agent's logs.

> **Why:** a migrated database can contain things an older Owl doesn't understand, so an older binary refuses to open it rather than silently damage it — the pre-upgrade backup is the way back.

## Uninstalling an agent

```bash
kubectl delete -f agent.yaml     # namespace, RBAC, Deployment (and the Secret, via the namespace)
```

Then delete the cluster in **Settings → Clusters** (export it first if you want to keep its history),
or **Rotate token** if you only want to cut it off and keep the data.
