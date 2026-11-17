# CLI

One binary, `owl`, with these commands:

```
owl agent      collect workload inventory from a cluster and push it to the server
owl server     run the central server (API + UI)
owl cluster    manage registered clusters and agent tokens (add, list, rotate-token, delete)
owl user       manage who can sign in (invite, list, reset-link, delete) — first admin, emergencies
owl upstream   show upstream release lines, EOL, archived status and advisories
owl healthcheck  exit 0 if the local server answers /healthz (the image's Docker HEALTHCHECK)
owl version    print the version
```

In Docker, run them in the server container: `docker compose exec owl /owl <command>` (Compose) or
`docker exec owl-server /owl <command>`.

## owl server

Runs the server. Flags and environment: [Configuration → Server](configuration.md#server--owl-server).

```bash
GITHUB_TOKEN=... OWL_PUBLIC_URL=https://owl.example.com owl server --db /data/owl.db --listen 0.0.0.0:8080
```

## owl agent

Runs the agent. Flags and environment: [Configuration → Agent](configuration.md#agent--owl-agent).

```bash
# What would this cluster report? (no server or token needed)
owl agent --print --context k3d-it-aws-prod

# One push from your laptop, against a dev server
OWL_TOKEN=... OWL_ALLOW_INSECURE_HTTP=true owl agent --once --context k3d-it-hetzner --server http://127.0.0.1:8080
```

## owl cluster

Works **directly on the database file** — run it on the server host (`--db`, default `$OWL_DB`).
It's a fallback for the UI (Settings → Clusters), handy for scripting and recovery.

```bash
owl cluster add --name it-aws-prod --stage live --tenant IT   # prints the agent token once (stdout)
owl cluster list                                              # NAME STAGE TENANT KUBERNETES AGENT LAST SEEN
owl cluster rotate-token --name it-aws-prod                   # revokes old tokens, prints a new one
owl cluster delete --name it-aws-prod                         # removes the cluster and all its data
```

- The token is printed alone on **stdout** (messages go to stderr), so `token=$(owl cluster add …)` works.
- `--stage` must be a configured stage; `--tenant` is created if needed.
- CLI actions are **not** in the audit log (they bypass the server) — prefer the UI.

## owl user

Also works **directly on the database file**, so it works when nobody can sign in. Run it on the
server host (`--db`, default `$OWL_DB`). Links start with `OWL_PUBLIC_URL` when it's set (inside the
server container it is); otherwise you get a path to put Owl's address in front of.

```bash
owl user invite --admin you@example.com    # the first admin: prints a one-time link (72 h)
owl user invite viewer@example.com         # a viewer (the UI is the usual way: Settings → Users)
owl user list                              # EMAIL ROLE STATUS SIGN-IN LAST SIGN-IN
owl user reset-link --email you@example.com   # emergency: link that sets a password and signs in (1 h)
owl user delete --email old@example.com    # refuses to delete the last active admin
```

- `reset-link` works even with password sign-in turned off — it's the way back in when Google sign-in
  breaks. For someone who never accepted their invite it prints a fresh invite instead.
- The link is printed alone on **stdout**. It's a secret until used: don't paste it into shared chats
  you don't trust.
- Like `owl cluster`, these actions bypass the server and aren't in the audit log (using a link is).

## owl upstream

Debugging aid: fetch upstream data for components and print it (uses the database as cache).

```bash
set -a; . ./.env; set +a                     # GITHUB_TOKEN
owl upstream                                 # all components: release lines, EOL, archived, advisories
owl upstream --component redis --version 7.2.4   # plus the advisories affecting 7.2.4
```
