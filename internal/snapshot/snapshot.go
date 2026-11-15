// Package snapshot defines the wire format the agent sends to the server.
//
// It is the contract between agent and server: keep it backwards compatible and bump
// SchemaVersion on breaking changes. The cluster's identity is NOT part of the payload —
// the server derives it from the agent's token.
package snapshot

import "time"

const SchemaVersion = 1

// IngestPath is the server endpoint agents POST snapshots to.
const IngestPath = "/api/v1/snapshots"

type Snapshot struct {
	SchemaVersion int        `json:"schemaVersion"`
	AgentVersion  string     `json:"agentVersion"`
	CollectedAt   time.Time  `json:"collectedAt"`
	Cluster       Cluster    `json:"cluster"`
	Workloads     []Workload `json:"workloads"`
}

type Cluster struct {
	// KubernetesVersion is the API server's gitVersion, e.g. "v1.30.4+rke2r1".
	KubernetesVersion string `json:"kubernetesVersion"`
	Nodes             []Node `json:"nodes"`
}

type Node struct {
	Name             string `json:"name"`
	KubeletVersion   string `json:"kubeletVersion"`
	OSImage          string `json:"osImage"`
	KernelVersion    string `json:"kernelVersion"`
	ContainerRuntime string `json:"containerRuntime"`
	Architecture     string `json:"architecture"`
}

// Workload is a top-level controller (Deployment, StatefulSet, DaemonSet, CronJob), a Job without
// a CronJob owner, or a bare Pod without any owner.
type Workload struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// DesiredReplicas is the spec's replica count (DaemonSet: desired scheduled pods; CronJob/Job/Pod: 0/1).
	DesiredReplicas int32 `json:"desiredReplicas"`
	// Containers come from the workload's pod template: what *should* run.
	Containers []Container `json:"containers"`
	// Running aggregates what pods owned by this workload actually run right now.
	Running []RunningImage `json:"running,omitempty"`
}

type Container struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	Init  bool   `json:"init,omitempty"`
}

// RunningImage is one distinct image observed in this workload's pods.
// Several entries with different images for the same container = rollout in progress.
type RunningImage struct {
	Container string `json:"container"`
	Image     string `json:"image"`
	// ImageID is the resolved digest reference reported by the kubelet (may be empty while pulling).
	ImageID string `json:"imageID,omitempty"`
	Pods    int    `json:"pods"`
	Ready   int    `json:"ready"`
}
