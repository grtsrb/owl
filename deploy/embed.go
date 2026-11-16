// Package deploy embeds deployment manifests so the server can hand them out (Settings → Clusters →
// Download agent.yaml). The repo file stays the single definition of the agent's RBAC.
package deploy

import _ "embed"

// AgentManifest is deploy/agent/agent.yaml: namespace, read-only RBAC and the agent Deployment. It
// contains no token; the token goes into the inventory-agent Secret separately.
//
//go:embed agent/agent.yaml
var AgentManifest string

// AgentImagePlaceholder is the image line in AgentManifest that the server replaces with the
// configured agent image.
const AgentImagePlaceholder = "image: service-inventory:dev # replace with your registry image"
