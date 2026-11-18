// Command owl is the owl binary. Client commands (owl login, status, outdated, …) talk to an Owl
// server with a personal API token (internal/cli). The full build also has `owl agent` (runs in each
// cluster), `owl server` (the central service) and `owl db …` (operator commands on the database
// file); the downloadable client build (-tags owlclient) has only the client commands.
package main

import (
	"fmt"
	"os"

	"owl/internal/cli"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

// fullCommands are the server-side commands, registered by full.go (not in the client build).
var fullCommands = map[string]func(args []string) error{}

// fullUsage is their part of the usage text.
var fullUsage string

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
	if run, ok := fullCommands[name]; ok {
		if err := run(args); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if cli.Has(name) {
		os.Exit(cli.NewApp(version).Run(name, args))
	}
	usage()
	os.Exit(2)
}

func usage() {
	fmt.Fprint(os.Stderr, "usage: owl <command> [flags]\n\nclient commands (sign in once with `owl login`; OWL_SERVER/OWL_TOKEN for CI):\n"+
		cli.Usage()+fullUsage+"  version      print version\n")
}
