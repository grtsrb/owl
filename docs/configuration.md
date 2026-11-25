# Configuration

Owl is configured in three places:

1. **Environment variables / flags** — anything about the server's environment and every secret.
   Flags win over environment variables.
2. **Settings in the UI** — stored in the database (and so in backups): stages, tenants, policies,
   catalog overrides, agent image/server URL, backup schedule, log level, retention.
3. **The built-in catalog** — compiled into the binary, see [Catalog](catalog.md).

Secrets (GitHub token, Google client secret, SMTP password, S3 keys, Claude API key, agent token) are
**only** ever read from the environment — never from flags (visible in `ps`), the UI or the database.
With [Docker Compose](install.md#docker-compose-on-one-host-recommended) they all live in `.env`.

> **Why:** secrets in flags show up in `ps`, secrets in the database end up in every backup, and secrets in the UI can be read back by anyone who gets an admin session. The environment keeps them in one place you control (`.env`, a Kubernetes Secret). Everything that isn't secret lives in the UI so it's audited and backed up.

## Server — `owl server`

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `--listen` | `OWL_LISTEN` | `127.0.0.1:8080` | Listen address. Use `0.0.0.0:8080` in a container. |
| `--db` | `OWL_DB` | `owl.db` (image: `/data/owl.db`) | SQLite database file. |
| — | `OWL_CLI_DIR` | `/cli` | Where the downloadable CLI builds are (`owl-<os>-<arch>.gz` or plain binaries, + `SHA256SUMS`; the image has them). Missing = no downloads in Account. |
| `--allowed-hosts` | `OWL_ALLOWED_HOSTS` | — | Comma-separated extra host names the UI/API answer to, besides `localhost`, `127.0.0.1`, `::1` and the `OWL_PUBLIC_URL` host. Ports are ignored. |
| `--internal-registries` | `OWL_INTERNAL_REGISTRIES` | — | Comma-separated registries whose images are **in-house** (listed, not scored), e.g. `registry.gitlab.example.com`. Admins can add more in **Settings → Registries → In-house registries** (no restart); these stay and are shown read-only there. Image scanning pulls from a registry given as an IP address (`10.0.0.5:5000`) only when it's listed (here or in the UI) or has a registry login. |
| `--log-dir` | `OWL_LOG_DIR` | `<db dir>/logs` | Rotated log files. `-` = stderr only. |
| `--backup-dir` | `OWL_BACKUP_DIR` | `<db dir>/backups` | Local backup folder. |
| `--no-upstream` | — | off | Don't contact GitHub, endoflife.date or OSV (offline / tests). |
| — | `OWL_PUBLIC_URL` | — | The address users open, e.g. `https://owl.example.com` (no path). `https://` required, except `http://localhost`. Used for the links in emails and the Google redirect URL, and its host is allowed automatically. Needed for email and Google sign-in. |
| — | `OWL_TRUSTED_PROXIES` | — | Comma-separated IPs/CIDRs of the reverse proxy in front of Owl. Requests from them have their client IP taken from `X-Forwarded-For` (for the logs, the audit log and sign-in rate limits). The Compose setup sets Caddy's address. |
| — | `GITHUB_TOKEN` | — | GitHub token **with no scopes**. Without it: 60 requests/hour, incomplete data. |
| — | `OWL_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. A level saved in the UI overrides it. |
| — | `ANTHROPIC_API_KEY` | — | Claude API key for [upgrade summaries](operations.md#claude-upgrade-summaries). Without it summaries can only run in *estimate* mode. Never logged. |
| — | `OWL_SCAN` | on | `off` switches [image scanning](concepts.md#image-vulnerabilities) off. It's also off with `--no-upstream` or when Trivy isn't found. |
| — | `OWL_TRIVY` | `trivy` in `PATH` (the image has `/usr/local/bin/trivy`) | The Trivy binary. |
| — | `OWL_TRIVY_CACHE` | `<db dir>/trivy` | Trivy's vulnerability DB and cache, about **3.5 GB** (DB 1.7 GB + Java DB 1.5 GB). Not in backups. |
| — | `OWL_REGISTRY_AUTH_FILE` | — | Registry logins for scanning private images from a file (below). Optional: admins can add them in **Settings → Registries** instead. A broken file stops the server. |
| — | `OWL_SECRET_KEY` | — | 32 random bytes, base64 (`openssl rand -base64 32`). Encrypts the registry logins added in Settings → Registries; without it that page is read-only. A malformed key stops the server. **Keep it with your backups' credentials, not in them**: a backup restored on a server with another key can't use its logins (re-enter them). |
| — | `OWL_CLAUDE_MODEL` | `claude-sonnet-5-5` | The *default* model for summaries; admins choose another in Settings → Claude summaries. Must be one Owl has a price for (`claude-opus-5-5`, `claude-opus-5`, `claude-opus-4-8`, `claude-sonnet-5-5`, `claude-sonnet-5`, `claude-haiku-5-5`, `claude-fable-5-2`), otherwise the server refuses to start — it can't enforce the caps for a model it can't price. |

### Registry logins for image scanning

Trivy pulls each running image from its registry. Public images need nothing; for private (in-house)
registries Owl needs a login per image prefix. The longest matching prefix wins; an image with no match is
pulled anonymously.

**In the UI** (admins): **Settings → Registries → Add credential** — prefix, username, password/token and
the token's expiry date (optional). Needs `OWL_SECRET_KEY`. The password is stored encrypted and never shown
again: **Replace** sets a new username + password, **Expiry** changes the date, **Delete** removes it.
**Test** logs in from the Owl server the way a pull would, so it also catches DNS problems (VPN, ziti,
split DNS — the name has to resolve *inside the Owl container*) and untrusted certificates. After a change,
images whose last scan couldn't log in are scanned again right away — no restart. Owl warns 14 days before
an expiry date, on that page, in the Scanner panel and in the weekly digest.

**From a file** (e.g. managed by your config tooling): a YAML list, mounted read-only. Its entries show in
Settings → Registries as *from file* and can only be changed in the file (then restart); the file wins over
a UI entry with the same prefix.

```yaml
# OWL_REGISTRY_AUTH_FILE, e.g. /run/secrets/owl-registries.yaml (mount it read-only, chmod 600)
- prefix: registry.gitlab.example.com/team-a          # team-a and its subgroups
  username: gitlab+deploy-token-12                    # a GitLab group deploy token with read_registry only
  password: <token>
- prefix: registry.gitlab.example.com/data-science
  username: gitlab+deploy-token-40
  password: <token>
- prefix: registry.gitlab.com/other-org
  username: <user>
  password: <token>
  expires: 2027-03-31                                 # optional: warn before the token runs out
```

- A prefix matches at a path boundary: `…/team-a` covers `…/team-a/app` and `…/team-a/sub/app`, never
  `…/team-ab/app`. `docker.io`, `index.docker.io` and Docker Hub's short names are the same registry.
- Use **read-only** credentials: a GitLab **group deploy token** with only `read_registry`, one per group,
  so nobody needs a token that reads every group.
- The login reaches Trivy only for that scan, as environment variables — never as command-line arguments
  (visible in `ps`), never in logs or API answers. UI logins are encrypted (AES-256-GCM, `OWL_SECRET_KEY`) in
  the database and its backups; file logins are never stored. The Scanner panel shows the prefixes and any
  that failed to log in.

### Sign-in

Owl is invite-only: see [Users and sign-in](security.md#users-and-sign-in). Passwords work out of the
box; Google sign-in and email are optional.

| Environment | Example | Meaning |
|---|---|---|
| `OWL_GOOGLE_CLIENT_ID` | `1234…apps.googleusercontent.com` | Google OAuth client ([setup](install.md#8-optional-google-sign-in)). Set both ID and secret, or neither. |
| `OWL_GOOGLE_CLIENT_SECRET` | | Its secret. |
| `OWL_GOOGLE_DOMAIN` | `example.com` | Optional: accept only accounts of this Google Workspace domain. |

### Email (SMTP)

Without `OWL_SMTP_HOST`, nothing is emailed: admins copy invite and reset links themselves.

| Environment | Example | Meaning |
|---|---|---|
| `OWL_SMTP_HOST` | `smtp.example.com` | Mail server. |
| `OWL_SMTP_PORT` | `587` | Default `587` for `starttls`, `465` for `tls`. |
| `OWL_SMTP_TLS` | `starttls` | `starttls` (must succeed — no fallback to plain), `tls` (implicit, port 465), or `none` — only allowed for a mail server on localhost or the dev stack's Mailpit. |
| `OWL_SMTP_USERNAME`, `OWL_SMTP_PASSWORD` | | Set both, or neither (a relay that trusts your IP). Only ever sent over TLS. |
| `OWL_SMTP_FROM` | `Owl <owl@example.com>` | Sender. Required. |

Email needs `OWL_PUBLIC_URL` (the links point there); the server refuses to start without it.

### Mattermost notifications

| Environment | Example | Meaning |
|---|---|---|
| `OWL_MATTERMOST_WEBHOOK_URL` | `https://mattermost.example.com/hooks/abc123…` | Incoming webhook for the digest and alerts ([setup](install.md#9-optional-mattermost-notifications)). A secret: environment only, never shown, logged or stored. Must be `https://` (plain `http://` only to localhost or the dev stack's `owl-mattermost`); the server refuses to start otherwise. Without it nothing is posted, but Owl keeps track, so switching notifications on later starts from the present. |

Links in messages point to `OWL_PUBLIC_URL`; without it, messages have no links. The digest email uses
the SMTP settings above.

### Backups to S3

Backups go to a bucket when `OWL_BACKUP_S3_BUCKET` is set; otherwise they stay local (with a warning).

| Environment | Example | Meaning |
|---|---|---|
| `OWL_BACKUP_S3_ENDPOINT` | `s3.eu-central-1.amazonaws.com`, `fsn1.your-objectstorage.com` | `host[:port]`, no scheme. |
| `OWL_BACKUP_S3_BUCKET` | `owl-backups` | Bucket name. |
| `OWL_BACKUP_S3_PREFIX` | `prod/` | Key prefix (optional). |
| `OWL_BACKUP_S3_REGION` | `eu-central-1` | Defaults to `us-east-1`, which skips the bucket-location lookup that some S3-compatible stores lack. |
| `OWL_BACKUP_S3_ACCESS_KEY` | | Access key. |
| `OWL_BACKUP_S3_SECRET_KEY` | | Secret key. |
| `OWL_BACKUP_S3_INSECURE` | `true` | Plain HTTP — local test servers only. |

Endpoint, bucket, access key and secret key are all required once the bucket is set; the server
refuses to start with a partial configuration.

> **Why:** a partly configured feature (a bucket without keys, mail without a sender, a webhook over plain HTTP) would fail later and quietly — at 3 a.m. during the backup, or when someone needs a reset link. Refusing to start makes the mistake visible at once.

## Agent — `owl agent`

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `--server` | `OWL_SERVER_URL` | — | Server base URL, e.g. `https://owl.internal.example.com`. |
| — | `OWL_TOKEN` | — | Agent token (environment only, never a flag). |
| `--interval` | `OWL_INTERVAL` | `15m` | How often to report (minimum 1 minute). The first report goes out within 30 s of start (random delay, so agents don't all report at once). |
| — | `OWL_ALLOW_INSECURE_HTTP` | `false` | Allow an `http://` server URL. Local development only. |
| `--kubeconfig` | `KUBECONFIG` | in-cluster | Run outside a cluster with a kubeconfig. |
| `--context` | — | current | Kube context (out-of-cluster only). |
| `--once` | — | | Collect and push once, then exit. |
| `--print` | — | | Collect once and print the snapshot JSON instead of pushing. Needs no server or token. |

The agent tries a push up to 4 times (waiting 2 s, 4 s, 8 s in between) on network errors, `5xx`
and `429`; if all fail, the next interval tries again. It never retries `4xx` (bad or revoked token, incompatible schema) — those need a fix.

> **Why:** retrying a `4xx` can't help — a revoked token or an incompatible version needs a person — and hammering the server with it would only fill its logs. Network errors and `5xx` are usually brief, so those are retried.

## CLI — `owl login` and friends

| Environment | Default | Meaning |
|---|---|---|
| `OWL_SERVER` | from the config file | Owl's URL, e.g. `https://owl.example.com`. `http://` only to localhost. |
| `OWL_TOKEN` | from the config file | A personal API token (`owl_pat_…`). Both override `~/.config/owl/config.yaml`, which `owl login` writes (mode 0600; `$XDG_CONFIG_HOME/owl/` when set). |

See [CLI](cli.md).

## Settings in the UI

| Setting | Where | Default |
|---|---|---|
| Stages and their order | Settings → Stages & tenants | `staging`, `prelive`, `live` |
| Tenants | Settings → Stages & tenants | — |
| Agent image, server URL (used in generated manifests) | Settings → Clusters → Agent settings | placeholders |
| Users and their roles | Settings → Users | — (first admin: `owl db user invite --admin`) |
| Password sign-in on/off | Settings → Users | on |
| Global and per-component version policy | Settings → Version policy | `lts-or-n-1` |
| Components added to the catalog | Settings → Catalog, cluster page (New component) | — |
| Catalog overrides (map / in-house / ignore) | Cluster page, Settings → Catalog | — |
| Daily backup time (server local time) | Settings → Backups & restore | `03:00` |
| Local backups to keep — without S3: days | 〃 | 15 days |
| Local backups to keep — with S3: copies | 〃 | 3 copies |
| Log level | Settings → Logs & audit | from `OWL_LOG_LEVEL` |
| Log file retention | 〃 | 5 days (1–365) |
| Audit log retention | 〃 | 365 days (1–3650) |
| Summary mode: off / estimate / manual / automatic | Settings → Claude summaries | **off** |
| Summary model: Claude Haiku 5.5, Sonnet 5.5 or Opus 5.5 | 〃 | the server default (`OWL_CLAUDE_MODEL`, else Sonnet 5.5) |
| Daily / monthly spending cap (UTC) | 〃 | $5 / $20 ($0.01–$1000; daily ≤ monthly) |
| Max input / output tokens per summary | 〃 | 30,000 / 4,000 (2k–200k / 1k–16k) |
| Weekly digest, Critical alert, unrecognized image alert, new cluster message | Settings → Notifications | all **off** |
| Digest day, time, time zone | 〃 | Monday 09:00 Europe/Belgrade |
| Digest by email (per user, for yourself) | Account (your name in the sidebar) | off |

> **Why:** settings that are about *what* Owl tracks and how (stages, policies, schedules) belong to the people using it, so they're in the UI, audited and included in backups. Settings about the host and every secret stay in the environment.

## Data directory

```
/data/
├── owl.db                         SQLite database (WAL mode: also owl.db-wal, owl.db-shm)
├── backups/
│   ├── owl-20261117-030000.db.gz              daily / manual backup (UTC timestamp)
│   ├── owl-20261117-091502-pre-restore.db.gz  safety backup taken before a restore
│   └── .owl-20261117-030000.db.gz.uploaded    marker: this copy is in the bucket
├── logs/
│   ├── server.log                 current log (JSON lines)
│   └── server-20261116-…log.gz    rotated, gzipped
└── trivy/                         Trivy's vulnerability DB and cache (~3.5 GB; not in backups)
```

Back up the whole volume if you like, but the supported way is Owl's own backups — they're
consistent while the server runs. See [Operations](operations.md).

## Limits

| What | Limit |
|---|---|
| Agent snapshot | 5 MiB compressed, 50 MiB uncompressed (~100k containers) → `413` |
| Restore upload | 2 GiB (compressed and uncompressed) |
| Cluster export import | 64 MiB |
| Other JSON request bodies | 16 KiB |
| Passwords | 12–256 characters |
| Failed sign-ins | 10 per account from one IP, 30 per IP, in 15 minutes → `429` |
| "Forgot password?" | 3 emails per account and 10 requests per IP per hour |
| Sessions | end after 7 days without use, and after 30 days in any case |
| Invite / reset links | 72 hours / 1 hour, work once |
| One Claude summary | the input/output token limits above, priced before sending (hard ceiling 200k input) |
| Cluster / stage names | `a-z`, `0-9`, `-`; 1–63 characters; starts and ends with a letter or digit |
| Tenant names | letters, digits, space, `_ . -`; 1–40 characters |

> **Why:** limits keep one bad client from exhausting memory or guessing passwords. They're generous for normal use; hitting one almost always means something is wrong.
