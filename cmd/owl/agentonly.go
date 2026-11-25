//go:build owlagent && !owlclient

package main

// The agent build (slim agent image) has only `owl agent`.
func init() { commandUsage = agentUsage }
