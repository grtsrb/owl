// Command owl is the owl binary. Three builds share this package:
//   - full (no tag): client commands, `owl agent`, `owl server`, `owl db …` and the debug commands;
//   - client (-tags owlclient, the downloadable CLI): client commands only (internal/cli);
//   - agent (-tags owlagent, the slim agent image): `owl agent` only — no server, SQLite, UI, Claude,
//     S3 or CLI code (checked by TestBuildDeps).
//
// Each command registers itself from a file whose build tags say which builds have it.
package main

import (
	"fmt"
	"os"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

// commands are the commands of this build that aren't client commands, registered by agent.go and full.go.
var commands = map[string]func(args []string) error{}

// commandUsage is their part of the usage text: agentUsage alone (agentonly.go) or followed by the rest (full.go).
var commandUsage string

const agentUsage = `
server-side commands:
  agent        collect workload inventory from a cluster and push it to the server
`

// client runs a client command (clientcmds.go; nil in the agent build): ok=false if name isn't one.
var client func(name string, args []string) (code int, ok bool)

// clientUsage is the client commands' part of the usage text ("" in the agent build).
var clientUsage string

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	name, args := os.Args[1], os.Args[2:]
	if name == "version" || name == "--version" {
		fmt.Println(version)
		return
	}
	if name == "help" || name == "-h" || name == "--help" {
		usage()
		return
	}
	if legacy(name, args) {
		name = "db"
		args = append([]string{os.Args[1]}, args...)
	}
	if run, ok := commands[name]; ok {
		if err := run(args); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if client != nil {
		if code, ok := client(name, args); ok {
			os.Exit(code)
		}
	}
	usage()
	os.Exit(2)
}

func usage() {
	fmt.Fprint(os.Stderr, "usage: owl <command> [flags]\n"+clientUsage+commandUsage+"  version      print version\n")
}
