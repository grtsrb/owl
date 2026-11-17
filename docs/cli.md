# CLI

One binary, `owl`, with these commands:

```
owl agent      collect workload inventory from a cluster and push it to the server
owl server     run the central server (API + UI)
owl cluster    manage registered clusters and agent tokens (add, list, rotate-token, delete)
owl upstream   show upstream release lines, EOL, archived status and advisories
owl version    print the version
```

In Docker, run them in the server container: `docker exec owl-server /owl <command>`.

## owl server

Runs the server. Flags and environment: [Configuration → Server](configuration.md#server--owl-server).

```bash
OWL_ADMIN_TOKEN=... GITHUB_TOKEN=... owl server --db /data/owl.db --listen 0.0.0.0:8080
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

## owl upstream

Debugging aid: fetch upstream data for components and print it (uses the database as cache).

```bash
set -a; . ./.env; set +a                     # GITHUB_TOKEN
owl upstream                                 # all components: release lines, EOL, archived, advisories
owl upstream --component redis --version 7.2.4   # plus the advisories affecting 7.2.4
```
