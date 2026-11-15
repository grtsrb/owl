package agent

import (
	"context"
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/pager"

	"service-inventory/internal/snapshot"
)

// Collector builds snapshots using read-only list calls. It needs exactly the permissions in
// deploy/agent/rbac.yaml — keep the two in sync.
type Collector struct {
	Client  kubernetes.Interface
	Version string
}

type ownerKey struct{ kind, namespace, name string }

func (c *Collector) Collect(ctx context.Context) (*snapshot.Snapshot, error) {
	snap := &snapshot.Snapshot{
		SchemaVersion: snapshot.SchemaVersion,
		AgentVersion:  c.Version,
		CollectedAt:   time.Now().UTC(),
	}

	sv, err := c.Client.Discovery().ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("server version: %w", err)
	}
	snap.Cluster.KubernetesVersion = sv.GitVersion

	if err := c.collectNodes(ctx, snap); err != nil {
		return nil, err
	}

	workloads := map[ownerKey]*snapshot.Workload{}
	add := func(kind string, meta metav1.ObjectMeta, replicas int32, spec corev1.PodSpec) {
		workloads[ownerKey{kind, meta.Namespace, meta.Name}] = &snapshot.Workload{
			Kind: kind, Namespace: meta.Namespace, Name: meta.Name,
			DesiredReplicas: replicas, Containers: containersOf(spec),
		}
	}

	// rsOwner / jobOwner resolve intermediate controllers to the top-level workload.
	rsOwner := map[ownerKey]ownerKey{}
	jobOwner := map[ownerKey]ownerKey{}

	apps := c.Client.AppsV1()
	if err := each(ctx, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
		return apps.Deployments("").List(ctx, o)
	}, func(obj runtime.Object) {
		d := obj.(*appsv1.Deployment)
		add("Deployment", d.ObjectMeta, replicas(d.Spec.Replicas), d.Spec.Template.Spec)
	}); err != nil {
		return nil, fmt.Errorf("deployments: %w", err)
	}
	if err := each(ctx, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
		return apps.StatefulSets("").List(ctx, o)
	}, func(obj runtime.Object) {
		s := obj.(*appsv1.StatefulSet)
		add("StatefulSet", s.ObjectMeta, replicas(s.Spec.Replicas), s.Spec.Template.Spec)
	}); err != nil {
		return nil, fmt.Errorf("statefulsets: %w", err)
	}
	if err := each(ctx, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
		return apps.DaemonSets("").List(ctx, o)
	}, func(obj runtime.Object) {
		d := obj.(*appsv1.DaemonSet)
		add("DaemonSet", d.ObjectMeta, d.Status.DesiredNumberScheduled, d.Spec.Template.Spec)
	}); err != nil {
		return nil, fmt.Errorf("daemonsets: %w", err)
	}
	if err := each(ctx, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
		return apps.ReplicaSets("").List(ctx, o)
	}, func(obj runtime.Object) {
		rs := obj.(*appsv1.ReplicaSet)
		key := ownerKey{"ReplicaSet", rs.Namespace, rs.Name}
		if ref := controllerOf(rs.OwnerReferences); ref != nil && ref.Kind == "Deployment" {
			rsOwner[key] = ownerKey{"Deployment", rs.Namespace, ref.Name}
			return
		}
		add("ReplicaSet", rs.ObjectMeta, replicas(rs.Spec.Replicas), rs.Spec.Template.Spec)
	}); err != nil {
		return nil, fmt.Errorf("replicasets: %w", err)
	}

	batch := c.Client.BatchV1()
	if err := each(ctx, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
		return batch.CronJobs("").List(ctx, o)
	}, func(obj runtime.Object) {
		cj := obj.(*batchv1.CronJob)
		add("CronJob", cj.ObjectMeta, 0, cj.Spec.JobTemplate.Spec.Template.Spec)
	}); err != nil {
		return nil, fmt.Errorf("cronjobs: %w", err)
	}
	if err := each(ctx, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
		return batch.Jobs("").List(ctx, o)
	}, func(obj runtime.Object) {
		j := obj.(*batchv1.Job)
		key := ownerKey{"Job", j.Namespace, j.Name}
		if ref := controllerOf(j.OwnerReferences); ref != nil && ref.Kind == "CronJob" {
			jobOwner[key] = ownerKey{"CronJob", j.Namespace, ref.Name}
			return
		}
		add("Job", j.ObjectMeta, 1, j.Spec.Template.Spec)
	}); err != nil {
		return nil, fmt.Errorf("jobs: %w", err)
	}

	core := c.Client.CoreV1()
	if err := each(ctx, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
		return core.Pods("").List(ctx, o)
	}, func(obj runtime.Object) {
		p := obj.(*corev1.Pod)
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			return // finished job pods etc. don't represent what's running
		}
		owner := resolveOwner(p, rsOwner, jobOwner)
		w, ok := workloads[owner]
		if !ok {
			// Bare pod, or owned by a controller we don't list (e.g. an operator CRD). Group by
			// that owner so operator-managed pods don't explode into one entry per pod.
			w = &snapshot.Workload{Kind: owner.kind, Namespace: owner.namespace, Name: owner.name,
				Containers: containersOf(p.Spec)}
			workloads[owner] = w
		}
		recordRunning(w, p)
	}); err != nil {
		return nil, fmt.Errorf("pods: %w", err)
	}

	for _, w := range workloads {
		sort.Slice(w.Running, func(i, j int) bool {
			if w.Running[i].Container != w.Running[j].Container {
				return w.Running[i].Container < w.Running[j].Container
			}
			return w.Running[i].Image < w.Running[j].Image
		})
		snap.Workloads = append(snap.Workloads, *w)
	}
	sort.Slice(snap.Workloads, func(i, j int) bool {
		a, b := snap.Workloads[i], snap.Workloads[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	return snap, nil
}

func (c *Collector) collectNodes(ctx context.Context, snap *snapshot.Snapshot) error {
	return each(ctx, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
		return c.Client.CoreV1().Nodes().List(ctx, o)
	}, func(obj runtime.Object) {
		n := obj.(*corev1.Node)
		i := n.Status.NodeInfo
		snap.Cluster.Nodes = append(snap.Cluster.Nodes, snapshot.Node{
			Name: n.Name, KubeletVersion: i.KubeletVersion, OSImage: i.OSImage,
			KernelVersion: i.KernelVersion, ContainerRuntime: i.ContainerRuntimeVersion,
			Architecture: i.Architecture,
		})
	})
}

// each pages through a list call (500 items per page) so large clusters don't need one huge response.
func each(ctx context.Context, list pager.ListPageFunc, fn func(runtime.Object)) error {
	p := pager.New(list)
	p.PageSize = 500
	return p.EachListItem(ctx, metav1.ListOptions{}, func(obj runtime.Object) error {
		fn(obj)
		return nil
	})
}

func resolveOwner(p *corev1.Pod, rsOwner, jobOwner map[ownerKey]ownerKey) ownerKey {
	ref := controllerOf(p.OwnerReferences)
	if ref == nil {
		return ownerKey{"Pod", p.Namespace, p.Name}
	}
	key := ownerKey{ref.Kind, p.Namespace, ref.Name}
	if top, ok := rsOwner[key]; ok {
		return top
	}
	if top, ok := jobOwner[key]; ok {
		return top
	}
	return key
}

func recordRunning(w *snapshot.Workload, p *corev1.Pod) {
	statuses := map[string]corev1.ContainerStatus{}
	for _, s := range p.Status.ContainerStatuses {
		statuses[s.Name] = s
	}
	for _, s := range p.Status.InitContainerStatuses {
		statuses[s.Name] = s
	}
	all := append(append([]corev1.Container{}, p.Spec.InitContainers...), p.Spec.Containers...)
	for _, ct := range all {
		st := statuses[ct.Name]
		ready := 0
		if st.Ready {
			ready = 1
		}
		found := false
		for i := range w.Running {
			r := &w.Running[i]
			if r.Container == ct.Name && r.Image == ct.Image && r.ImageID == st.ImageID {
				r.Pods++
				r.Ready += ready
				found = true
				break
			}
		}
		if !found {
			w.Running = append(w.Running, snapshot.RunningImage{
				Container: ct.Name, Image: ct.Image, ImageID: st.ImageID, Pods: 1, Ready: ready,
			})
		}
	}
}

func containersOf(spec corev1.PodSpec) []snapshot.Container {
	var out []snapshot.Container
	for _, c := range spec.InitContainers {
		out = append(out, snapshot.Container{Name: c.Name, Image: c.Image, Init: true})
	}
	for _, c := range spec.Containers {
		out = append(out, snapshot.Container{Name: c.Name, Image: c.Image})
	}
	return out
}

func controllerOf(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	return nil
}

func replicas(r *int32) int32 {
	if r == nil {
		return 1
	}
	return *r
}
