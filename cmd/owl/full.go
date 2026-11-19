//go:build !owlclient

package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
)

func init() {
	fullCommands["agent"] = runAgent
	fullCommands["server"] = runServer
	fullCommands["db"] = runDB
	fullCommands["healthcheck"] = runHealthcheck
	fullCommands["upstream"] = runUpstream
	fullCommands["catalog-check"] = runCatalogCheck
	fullUsage = `
server-side commands:
  agent        collect workload inventory from a cluster and push it to the server
  server       run the central inventory server
  db           operator commands on the database file: cluster (add, list, rotate-token, delete),
               user (invite, list, reset-link, delete) — the first admin and emergencies
  healthcheck  exit 0 if the local server answers /healthz (for Docker HEALTHCHECK)
  upstream     show upstream release lines, EOL, archived status and advisories for components
  catalog-check  verify the built-in catalog against GitHub, endoflife.date and OSV (needs GITHUB_TOKEN)
`
}

// runDB is `owl db cluster …` / `owl db user …`: works on the SQLite file directly, on the server host
// (or `docker exec` into the server container).
func runDB(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: owl db <cluster|user> <command> [flags]")
	}
	switch args[0] {
	case "cluster":
		return runCluster(args[1:])
	case "user":
		return runUser(args[1:])
	}
	return fmt.Errorf("unknown db command %q: cluster or user", args[0])
}

// legacy reports whether this is a pre-milestone-12 database-file command (`owl user …`,
// `owl cluster list`, `owl cluster add --name …`), which still works for one release with a warning.
// `owl cluster add <name>` (no --name/--db) is the client command.
func legacy(name string, args []string) bool {
	old := false
	switch name {
	case "user":
		old = true
	case "cluster":
		old = len(args) > 0 && args[0] == "list" || slices.ContainsFunc(args, func(a string) bool {
			for _, f := range []string{"-name", "--name", "-db", "--db"} {
				if a == f || len(a) > len(f) && a[:len(f)+1] == f+"=" {
					return true
				}
			}
			return false
		})
	}
	if old {
		fmt.Fprintf(os.Stderr, "warning: `owl %s` on the database file moved to `owl db %s` — the old form goes away in the next release\n", name, name)
	}
	return old
}
