//go:build !owlagent

package main

import "owl/internal/cli"

// The client commands are in the full and the client build, never in the agent image.
func init() {
	client = func(name string, args []string) (int, bool) {
		if !cli.Has(name) {
			return 0, false
		}
		return cli.NewApp(version).Run(name, args), true
	}
	clientUsage = "\nclient commands (sign in once with `owl login`; OWL_SERVER/OWL_TOKEN for CI):\n" + cli.Usage()
}
