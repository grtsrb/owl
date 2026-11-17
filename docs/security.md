# Security

What Owl can see, what it stores and sends, and how to run it safely.

## Current limits — read first

- **The UI and read API have no login yet** (sign-in is planned for milestone 9). Anyone who can
  reach the server can read the inventory: cluster names, namespaces, workloads, images, versions.
- **Only expose the server on a private network** (VPN, internal load balancer, or a reverse proxy
  with its own authentication). Never on the internet.
- Writes and admin-only reads (backups, audit log, logs settings, exports) need the **admin token**.

## The agent

- **Read-only**: its ClusterRole allows only `get` and `list` on pods, nodes, deployments,
  statefulsets, daemonsets, replicasets, jobs and cronjobs. **No Secrets, no ConfigMaps, no write
  verbs.** The role is defined once in `deploy/agent/agent.yaml` and must match exactly what the
  collector reads.
- **Push-only**: the agent connects out to the server. The server never connects into a cluster and
  never stores kubeconfigs.
- Sends only: Kubernetes version, node info (name, kubelet/OS/kernel/runtime version, architecture),
  and per workload its kind, namespace, name, replica count, container names and images, running
  image digests and pod counts. **No environment variables, no configs, no logs.**
- Runs as non-root with a read-only root filesystem, no privilege escalation, all capabilities dropped.
- Refuses an `http://` server URL unless `OWL_ALLOW_INSECURE_HTTP=true`.
- The token is read from the environment only (a flag would show in `ps`).

## Tokens

| Token | Where it lives | Protection |
|---|---|---|
| Agent token (per cluster) | the cluster's `owl-agent` Secret | Random, shown once, stored as a SHA-256 hash. Revocations are permanent, even across restores. |
| Admin token | server environment (`OWL_ADMIN_TOKEN`) | ≥ 24 characters, compared in constant time. Unset = writes disabled. In the browser: session storage of one tab only, sent only with admin requests, never in a URL. |
| GitHub token | server environment | Needs **no scopes** (public data only). Sent only to `api.github.com`. |
| Anthropic API key | server environment (`ANTHROPIC_API_KEY`) | Never in a flag, the UI, the database or logs. Put it in a dedicated Console workspace with a monthly spend limit, so a leak or a bug has a hard ceiling. |
| S3 keys | server environment | Should not allow deleting (see [Operations](operations.md#with-s3-recommended)). |

The admin token is an interim measure until sign-in: everyone who has it has full control. Keep it in
a password manager or secret store, and rotate it (change the variable and restart) if it may have
leaked.

## Server protections

- **Host allowlist**: the UI and API answer only to `localhost` and `OWL_ALLOWED_HOSTS`
  (`421` otherwise) — this stops DNS-rebinding attacks from a malicious web page against an internal
  server. Agent ingest and `/healthz` are exempt.
- **Writes are JSON-only** (and restore uploads accept only gzip/octet-stream/sqlite types). Since Owl
  sends no CORS headers, a cross-site web page can't make the browser send such requests.
- **Strict Content-Security-Policy** (`default-src 'self'`, no inline scripts, no third-party origins),
  `X-Frame-Options: DENY`, `nosniff`, `no-referrer`.
- **Size limits** on every body; gzip bombs are caught (see [Configuration → Limits](configuration.md#limits)).
- **Strict validation** of names that end up in YAML or `kubectl` commands.
- **Logs never contain** headers, query strings or tokens.
- **Audit log** of every admin action, kept across restores.

## What leaves the server

| To | What |
|---|---|
| GitHub API, endoflife.date, OSV.dev | Public project names (e.g. `grafana/grafana`) — never cluster data. |
| Your S3 bucket | Backups (contain the whole database: inventory, token hashes, audit log with IPs). Treat the bucket as sensitive. |
| Claude API (only when summaries are set to manual or automatic) | Only public data: component name and upstream repository, the two versions, upstream release notes. Never cluster names, namespaces, hosts, workloads, tenants, stages, configs or tokens (a test checks every request). |
| GitHub (raw files), haproxy.org, postgresql.org | Release-note fetches for summaries: public URLs built from the component and its release lines only. |

## Reporting a problem

Internal project: report security issues directly to the maintainer, not in a public channel.
Include the request id (`X-Request-ID`) if relevant.
