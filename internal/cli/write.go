package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The few changes the CLI makes (decided 2026-11-18): clusters for GitOps bootstrapping, backups,
// and users (users.go). Everything else stays in the web UI.

// agentSecretYAML is the agent's Secret, the same as the UI's (web/src/api.ts agentSecretYAML).
func agentSecretYAML(serverURL, token string) string {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	if serverURL == "" {
		serverURL = "https://SET-SERVER-URL-IN-SETTINGS"
	}
	lines := []string{
		"# Agent token for Owl. Apply, then delete this file:",
		"#   kubectl apply -f <this file>",
		"apiVersion: v1",
		"kind: Secret",
		"metadata:",
		"  name: owl-agent",
		"  namespace: owl",
		"type: Opaque",
		"stringData:",
		"  OWL_SERVER_URL: " + q(serverURL),
		"  OWL_TOKEN: " + q(token),
	}
	if strings.HasPrefix(serverURL, "http://") {
		lines = append(lines, `  OWL_ALLOW_INSECURE_HTTP: "true" # plain HTTP: local development only`)
	}
	return strings.Join(lines, "\n") + "\n"
}

type tokenAnswer struct {
	Cluster clusterJSON `json:"cluster"`
	Token   string      `json:"token"`
}

// printAgentToken prints a new agent token: as text (token alone on stdout, explanation on stderr,
// so `token=$(owl cluster add …)` works), as the agent Secret (-o secret, for `| kubectl apply -f -`),
// or JSON.
func (a *App) printAgentToken(ctx context.Context, c *client, out string, t tokenAnswer) error {
	switch out {
	case "json":
		return a.printJSON(t)
	case "secret":
		var s struct {
			ServerURL string `json:"serverURL"`
		}
		if err := c.do(ctx, http.MethodGet, "/api/v1/settings", nil, &s); err != nil {
			return fmt.Errorf("the token was created but the server URL couldn't be read (%w) — the token is: %s", err, t.Token)
		}
		_, err := fmt.Fprint(a.Stdout, agentSecretYAML(s.ServerURL, t.Token))
		return err
	}
	fmt.Fprintf(a.Stderr, "cluster %s (%s, %s) — agent token, shown only once (store it in the cluster's owl-agent Secret, or use -o secret):\n",
		t.Cluster.Name, t.Cluster.Stage, t.Cluster.Tenant)
	_, err := fmt.Fprintln(a.Stdout, t.Token)
	return err
}

func runClusterCmd(a *App, ctx context.Context, args []string) error {
	const use = "cluster add <name> --stage <stage> --tenant <tenant> [-o text|secret|json]\n       owl cluster rotate-token <name> [-o text|secret|json]\n       owl cluster delete <name> [--yes]"
	if len(args) == 0 {
		return a.usageErr("usage: owl %s", use)
	}
	sub := args[0]
	f := a.newFlags("cluster "+sub, use, false)
	out := f.String("o", "text", "output: text (the token alone), secret (the agent's Secret YAML) or json")
	stage := f.String("stage", "", "a configured stage, e.g. staging, prelive, live")
	tenant := f.String("tenant", "", "the tenant using the cluster, e.g. IT")
	yes := f.Bool("yes", false, "don't ask for confirmation")
	legacyName := f.String("name", "", "")
	legacyDB := f.String("db", "", "")
	pos, err := f.parse(args[1:])
	if err != nil {
		return err
	}
	if *legacyDB != "" || *legacyName != "" || sub == "list" {
		return errors.New("`owl cluster " + sub + "` now talks to the server API. The database-file command moved to `owl db cluster " + sub +
			" --name …`; for the list use `owl clusters`")
	}
	if *out != "text" && *out != "secret" && *out != "json" {
		return a.usageErr("-o: text, secret or json")
	}
	if len(pos) != 1 {
		return a.usageErr("usage: owl %s", use)
	}
	name := pos[0]
	c, err := a.client()
	if err != nil {
		return err
	}
	path := "/api/v1/clusters/" + url.PathEscape(name)
	switch sub {
	case "add":
		if *stage == "" || *tenant == "" {
			return a.usageErr("owl cluster add %s --stage <stage> --tenant <tenant>", name)
		}
		var t tokenAnswer
		if err := c.do(ctx, http.MethodPost, "/api/v1/clusters", map[string]string{"name": name, "stage": *stage, "tenant": *tenant}, &t); err != nil {
			return err
		}
		return a.printAgentToken(ctx, c, *out, t)
	case "rotate-token":
		var t tokenAnswer
		if err := c.do(ctx, http.MethodPost, path+"/rotate-token", nil, &t); err != nil {
			return err
		}
		fmt.Fprintln(a.Stderr, "previous agent tokens revoked: the agent is refused until its Secret has the new token")
		return a.printAgentToken(ctx, c, *out, t)
	case "delete":
		if !*yes {
			ok, err := a.confirm(fmt.Sprintf("Delete cluster %s with its inventory, history and agent tokens?", name))
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("not deleted")
			}
		}
		if err := c.do(ctx, http.MethodDelete, path, nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(a.Stdout, "Deleted", name)
		return nil
	}
	return a.usageErr("usage: owl %s", use)
}

func runBackup(a *App, ctx context.Context, args []string) error {
	f := a.newFlags("backup now", "backup now [-o json]", true)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || pos[0] != "now" {
		return a.usageErr("usage: owl backup now   (restores are done in the web UI: Settings → Backups & restore)")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	var raw json.RawMessage
	// VACUUM INTO + gzip + upload run while we wait: allow a big database on a slow bucket.
	if err := c.doTimeout(ctx, 30*time.Minute, http.MethodPost, "/api/v1/backups", nil, &raw); err != nil {
		return err
	}
	if f.json() {
		_, err := fmt.Fprintln(a.Stdout, string(raw))
		return err
	}
	var b struct {
		Name     string `json:"name"`
		Size     int64  `json:"size"`
		Uploaded bool   `json:"uploaded"`
	}
	_ = json.Unmarshal(raw, &b)
	where := "kept on the server"
	if b.Uploaded {
		where = "uploaded to S3"
	}
	fmt.Fprintf(a.Stdout, "Backup %s (%.1f MB), %s\n", b.Name, float64(b.Size)/1e6, where)
	return nil
}
