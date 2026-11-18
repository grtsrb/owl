//go:build owlclient

package main

// The client build has no database-file commands.
func legacy(string, []string) bool { return false }
