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

## Server — `owl server`

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `--listen` | `OWL_LISTEN` | `127.0.0.1:8080` | Listen address. Use `0.0.0.0:8080` in a container. |
| `--db` | `OWL_DB` | `owl.db` (image: `/data/owl.db`) | SQLite database file. |
| `--allowed-hosts` | `OWL_ALLOWED_HOSTS` | — | Comma-separated extra host names the UI/API answer to, besides `localhost`, `127.0.0.1`, `::1` and the `OWL_PUBLIC_URL` host. Ports are ignored. |
| `--internal-registries` | `OWL_INTERNAL_REGISTRIES` | — | Comma-separated registries whose images are **in-house** (listed, not scored), e.g. `registry.gitlab.example.com`. |
| `--log-dir` | `OWL_LOG_DIR` | `<db dir>/logs` | Rotated log files. `-` = stderr only. |
| `--backup-dir` | `OWL_BACKUP_DIR` | `<db dir>/backups` | Local backup folder. |
| `--no-upstream` | — | off | Don't contact GitHub, endoflife.date or OSV (offline / tests). |
| — | `OWL_PUBLIC_URL` | — | The address users open, e.g. `https://owl.example.com` (no path). `https://` required, except `http://localhost`. Used for the links in emails and the Google redirect URL, and its host is allowed automatically. Needed for email and Google sign-in. |
| — | `OWL_TRUSTED_PROXIES` | — | Comma-separated IPs/CIDRs of the reverse proxy in front of Owl. Requests from them have their client IP taken from `X-Forwarded-For` (for the logs, the audit log and sign-in rate limits). The Compose setup sets Caddy's address. |
| — | `GITHUB_TOKEN` | — | GitHub token **with no scopes**. Without it: 60 requests/hour, incomplete data. |
| — | `OWL_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. A level saved in the UI overrides it. |
| — | `ANTHROPIC_API_KEY` | — | Claude API key for [upgrade summaries](operations.md#claude-upgrade-summaries). Without it summaries can only run in *estimate* mode. Never logged. |
| — | `OWL_CLAUDE_MODEL` | `claude-opus-5-5` | Model for summaries. Must be one Owl has a price for (`claude-opus-5-5`, `claude-opus-5`, `claude-opus-4-8`, `claude-sonnet-5-5`, `claude-sonnet-5`, `claude-haiku-5-5`, `claude-fable-5-2`), otherwise the server refuses to start — it can't enforce the caps for a model it can't price. |

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

## Settings in the UI

| Setting | Where | Default |
|---|---|---|
| Stages and their order | Settings → Stages | `staging`, `prelive`, `live` |
| Tenants | Settings → Tenants | — |
| Agent image, server URL (used in generated manifests) | Settings → Clusters → Agent settings | placeholders |
| Users and their roles | Settings → Users | — (first admin: `owl user invite --admin`) |
| Password sign-in on/off | Settings → Users | on |
| Global and per-component version policy | Settings → Version policy | `lts-or-n-1` |
| Catalog overrides (map / in-house / ignore) | Cluster page, Settings → Catalog overrides | — |
| Daily backup time (server local time) | Settings → Backups & restore | `03:00` |
| Local backups to keep — without S3: days | 〃 | 15 days |
| Local backups to keep — with S3: copies | 〃 | 3 copies |
| Log level | Settings → Logs & audit | from `OWL_LOG_LEVEL` |
| Log file retention | 〃 | 5 days (1–365) |
| Audit log retention | 〃 | 365 days (1–3650) |
| Summary mode: off / estimate / manual / automatic | Settings → Summaries | **off** |
| Daily / monthly spending cap (UTC) | 〃 | $5 / $20 ($0.01–$1000; daily ≤ monthly) |
| Max input / output tokens per summary | 〃 | 30,000 / 4,000 (2k–200k / 1k–16k) |

## Data directory

```
/data/
├── owl.db                         SQLite database (WAL mode: also owl.db-wal, owl.db-shm)
├── backups/
│   ├── owl-20261117-030000.db.gz              daily / manual backup (UTC timestamp)
│   ├── owl-20261117-091502-pre-restore.db.gz  safety backup taken before a restore
│   └── .owl-20261117-030000.db.gz.uploaded    marker: this copy is in the bucket
└── logs/
    ├── server.log                 current log (JSON lines)
    └── server-20261116-…log.gz    rotated, gzipped
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
