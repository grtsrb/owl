//go:build owlclient || owlagent

package main

// The client and agent builds have no database-file commands.
func legacy(string, []string) bool { return false }
