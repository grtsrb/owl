# CLI

One binary, `owl`. Most people only need the **client commands**: they talk to an Owl server over its
API with a [personal API token](#personal-api-tokens), from your laptop or a CI job.

```
owl login          save the server URL and a personal API token
owl logout         revoke the saved token and forget it (--keep-token: only forget it)
owl whoami         show the server, your user and the token's scope
owl status         components per priority level and every cluster's last report
owl outdated       what needs attention, per cluster and workload (--fail-on for CI)
owl component      one component: release lines, EOL, advisories, where it runs
owl clusters       registered clusters
owl changes        recent image changes in the clusters
owl unrecognized   images Owl doesn't recognize yet
owl cluster        add | rotate-token | delete a cluster          (write token)
owl users          list | export | plan | apply | add | set-role | disable | enable
owl backup now     take a backup on the server                    (write token)
owl version        print the version
```

The server image also has the **server-side commands** — `owl server`, `owl agent`, `owl db …`,
`owl upstream`, `owl healthcheck` — [below](#server-side-commands). The download from the UI is the
client only (~7 MB).

## Install

In Owl: **your name (bottom of the sidebar) → Account → Command-line tool** — downloads for macOS and
Linux, with SHA-256 checksums.

```bash
chmod +x owl && sudo mv owl /usr/local/bin/
# macOS: only if it refuses to open ("Apple cannot check it") — the binary isn't signed
xattr -d com.apple.quarantine /usr/local/bin/owl
```

In CI, download it with the token you use anyway:

```bash
curl -fsSL -H "Authorization: Bearer $OWL_TOKEN" "$OWL_SERVER/api/v1/cli/linux-amd64" -o owl && chmod +x owl
```

Platforms: `darwin-arm64`, `darwin-amd64`, `linux-amd64`, `linux-arm64`.

## Personal API tokens

Create one in **Account → API tokens → New token**: a name (where it's used: "laptop", "gitlab-ci"),
a scope and an expiry. It's shown **once** — Owl keeps only a hash.

- **Read-only** (the default): everything you can see in the UI.
- **Read & write** (admins only): also adds clusters, takes backups and manages users. It works only
  while you're an admin — demoted, the token becomes read-only.
- **Expiry**: 30, 90 (default) or 365 days, or never. You get an email a week before (if Owl can send
  email). Expired tokens stay listed for 30 days.
- A token acts as you: changes are in the audit log as `you@example.com (token: gitlab-ci)`. It can
  read what you can, but **change** only what the CLI does (clusters, backups, users) — never your
  password, sign-in settings, policies or settings, and never create tokens. Changing your password
  revokes all your tokens. Disabling your user deletes all your
  tokens; admins can see and revoke anyone's tokens in Settings → Users.

> **Why:** a personal token works for Google users too, can be limited to reading, expires, and can be revoked on its own without touching your password — and every change it makes is audited under your name. The CLI never takes a token as an argument (it would land in shell history and `ps`) and never sends it over plain HTTP or to a redirect target.

## owl login / logout / whoami

```bash
owl login --server https://owl.example.com     # asks for the token (not echoed)
echo "$TOKEN" | owl login --server https://…   # or from stdin
owl whoami                                      # https://owl.example.com
                                                # you@example.com (admin) — token "laptop", read-only, expires 2027-02-16
owl logout                                      # revokes the token on the server, removes it here
owl logout --keep-token                         # only removes it here; it keeps working until it expires
```

- The server and token are saved in `~/.config/owl/config.yaml` (or `$XDG_CONFIG_HOME/owl/`), mode
  0600. You stay signed in across restarts until the token expires or is revoked.
- **CI**: set `OWL_SERVER` and `OWL_TOKEN` instead — they override the file. Keep the token in a
  protected, masked variable. `OWL_SERVER` alone, pointing elsewhere than the saved server, is
  refused: the saved token is never sent to another server.
- The token is never accepted as a command-line argument (it would show in `ps` and shell history).
- `http://` is refused except to `localhost`/`127.0.0.1` — the token would travel unencrypted.
- To log in again after `owl logout`, create a new token in the UI.

## Reading

Every read command takes `-o json` for scripts. Exit codes: 0 OK, 1 an error (including a bad flag)
or `--fail-on` matched, 2 only for drift with `users plan --detailed-exitcode`.

```bash
owl status                         # components per level, unrecognized/unpinned counts, clusters + last report
owl outdated                       # everything at Low or above, most urgent first
owl outdated --level high --cluster it-aws-prod,it-aws-test
owl outdated --tenant IT --all     # --all includes OK and Unknown
owl component coredns              # lines (newest + the ones running), target, instances, drift, advisories
owl clusters
owl changes --cluster it-hetzner --limit 20
owl unrecognized
```

### Failing a pipeline

`--fail-on <level>` exits **1** when anything shown is at that level or above (filters apply first):

```yaml
# .gitlab-ci.yml
owl-check:
  script:
    - curl -fsSL -H "Authorization: Bearer $OWL_TOKEN" "$OWL_SERVER/api/v1/cli/linux-amd64" -o owl && chmod +x owl
    - ./owl outdated --cluster it-aws-prod --fail-on critical
```

## Clusters (write token)

```bash
owl cluster add it-aws-prod --stage live --tenant IT              # prints the agent token once (stdout)
owl cluster add it-aws-prod --stage live --tenant IT -o secret | kubectl --context it-aws-prod apply -f -
owl cluster rotate-token it-aws-prod -o secret | kubectl apply -f -   # old tokens stop working at once
owl cluster delete it-aws-prod                                     # asks; --yes in scripts
```

- `-o text` (default) prints the token alone on stdout, so `token=$(owl cluster add …)` works;
  `-o secret` prints the agent's Secret (server URL from Settings → Clusters); `-o json` both.
- Then install the agent: `kubectl apply -f https://owl.example.com/api/v1/agent/manifest`.

## Users

`owl users list` shows everyone with role, status, whether a users file manages them, and last sign-in.

### From a file (optional)

Keep who has access in a repository, like the rest of your infrastructure:

```yaml
# users.yaml
users:
  - email: nikola@example.com
    role: admin
  - email: dev@example.com
    role: viewer
```

```bash
owl users export > users.yaml                    # start from who's in Owl today
owl users plan -f users.yaml                     # what would change (a read-only token is enough)
owl users apply -f users.yaml                    # shows the plan, asks, applies (--yes in CI)
owl users apply -f users.yaml --prune            # also disables users who aren't in the file
owl users plan -f users.yaml --detailed-exitcode # exit 0 = in sync, 2 = drift, 1 = error or bad flags
```

```
Changes:
  + new@example.com     invite as viewer
  ~ dev@example.com     role viewer → admin
  ~ nikola@example.com  start managing (already matches)

In Owl but not in the file (can still sign in):
  old@example.com
  Add them to the file, or apply with --prune to disable them.
```

- **No state file**: Owl remembers which users came from a file (*managed*, a badge in Settings →
  Users). The server is the state.
- Only `email` and `role` (`admin` or `viewer`). Emails are case-insensitive; duplicates and unknown
  fields are rejected before anything is sent. The weekly-digest email stays each user's own choice.
- New users get the normal **invite** by email. Without email, the CLI tells you to send it from
  Settings → Users → Send invite again (invite links are never handed to a token). Someone listed who
  never signed in and whose invite expired gets a new one (`reinvite`).
- Listed but disabled users are enabled again. Users not in the file are **drift**; `--prune` disables
  them (never deletes). Removing someone from the file without `--prune` stops managing them.
- Safety: `apply` needs a write token, refuses to disable or demote **you** (the token's owner), keeps
  at least one active admin, and refuses if users changed since the plan was shown. Everything lands in
  the audit log with `via=users.yaml`.
- Changes made in the UI (or with the commands below) to a managed user still work — Owl warns that
  they show as drift until the file is updated.

> **Why:** Owl keeps no state file: the server itself marks which users came from the file, so the file in your repository is the whole truth and there's nothing else to lose or keep in sync. `plan` shows every change before `apply`, and `--prune` only disables — it never deletes anyone.

### One-off changes

For teams without a file (write token):

```bash
owl users add dev@example.com --role viewer      # invite
owl users set-role dev@example.com --role admin
owl users disable dev@example.com                # signed out, tokens stop working
owl users enable dev@example.com
```

## owl backup now

Takes a backup on the server now (write token) — e.g. before an upgrade. Restores are done in the UI
(Settings → Backups & restore).

> **Why:** the CLI changes only what it needs to (clusters, backups, users); everything else stays in the UI, where viewers and admins see the same thing and every change is audited.

## Server-side commands

These run on the server host (or `docker compose exec owl /owl …` / `docker exec owl-server /owl …`).

### owl server

Runs the server. Flags and environment: [Configuration → Server](configuration.md#server--owl-server).

```bash
GITHUB_TOKEN=... OWL_PUBLIC_URL=https://owl.example.com owl server --db /data/owl.db --listen 0.0.0.0:8080
```

### owl agent

Runs the agent. Flags and environment: [Configuration → Agent](configuration.md#agent--owl-agent).

```bash
# What would this cluster report? (no server or token needed)
owl agent --print --context my-cluster

# One push from your laptop, with the cluster's agent token
OWL_TOKEN=... owl agent --once --context my-cluster --server https://owl.example.com
```

### owl db cluster / owl db user

Work **directly on the database file** (`--db`, default `$OWL_DB`), so they work when nobody can sign
in — the first admin, and emergencies. They refuse to run when the database file doesn't exist (run by
mistake on a laptop). They bypass the server, so they're **not** in the audit log — prefer the UI or
the client commands.

```bash
owl db user invite --admin you@example.com      # the first admin: prints a one-time link (72 h)
owl db user list                                # EMAIL ROLE STATUS SIGN-IN LAST SIGN-IN
owl db user reset-link --email you@example.com  # emergency: link that sets a password and signs in (1 h)
owl db user delete --email old@example.com      # refuses to delete the last active admin

owl db cluster add --name it-aws-prod --stage live --tenant IT   # prints the agent token once (stdout)
owl db cluster list
owl db cluster rotate-token --name it-aws-prod
owl db cluster delete --name it-aws-prod
```

- Links start with `OWL_PUBLIC_URL` when it's set (inside the server container it is). They're printed
  alone on stdout and are secret until used.
- `reset-link` works even with password sign-in turned off — the way back in when Google sign-in
  breaks. For someone who never accepted their invite it prints a fresh invite instead.
- Until milestone 12 these were `owl user …` and `owl cluster … --name`. The old forms still work for
  one release, with a warning.

### owl upstream

Debugging aid: fetch upstream data for components and print it (uses the database as cache).

```bash
set -a; . ./.env; set +a                     # GITHUB_TOKEN
owl upstream                                 # all components: release lines, EOL, archived, advisories
owl upstream --component redis --version 7.2.4   # plus the advisories affecting 7.2.4
```
