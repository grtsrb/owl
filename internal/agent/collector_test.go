package agent

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"service-inventory/internal/snapshot"
)

func ctrl(kind, name string) []metav1.OwnerReference {
	t := true
	return []metav1.OwnerReference{{Kind: kind, Name: name, Controller: &t}}
}

func podSpec(image string) corev1.PodSpec {
	return corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: image}}}
}

func pod(name, ownerKind, owner, image, imageID string, ready bool) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Spec:       podSpec(image),
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{
			{Name: "main", Image: image, ImageID: imageID, Ready: ready}}},
	}
	if owner != "" {
		p.OwnerReferences = ctrl(ownerKind, owner)
	}
	return p
}

func find(t *testing.T, s *snapshot.Snapshot, kind, name string) snapshot.Workload {
	t.Helper()
	for _, w := range s.Workloads {
		if w.Kind == kind && w.Name == name {
			return w
		}
	}
	t.Fatalf("workload %s/%s not found in %+v", kind, name, s.Workloads)
	return snapshot.Workload{}
}

func TestCollect(t *testing.T) {
	two := int32(2)
	zero := int32(0)
	objs := []runtime.Object{
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"},
			Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.30.4+rke2r1"}}},

		// Deployment mid-rollout: old RS pod still running, new RS pod not ready yet.
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "ns"},
			Spec: appsv1.DeploymentSpec{Replicas: &two, Template: corev1.PodTemplateSpec{Spec: podSpec("nginx:1.27.4")}}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-old", Namespace: "ns", OwnerReferences: ctrl("Deployment", "web")}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-new", Namespace: "ns", OwnerReferences: ctrl("Deployment", "web")}},
		pod("web-a", "ReplicaSet", "web-old", "nginx:1.27.3", "docker.io/library/nginx@sha256:old", true),
		pod("web-b", "ReplicaSet", "web-new", "nginx:1.27.4", "", false),

		// Scaled to zero: still listed from the template, nothing running.
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "redpanda", Namespace: "ns"},
			Spec: appsv1.StatefulSetSpec{Replicas: &zero, Template: corev1.PodTemplateSpec{Spec: podSpec("redpanda:v24.2.7")}}},

		// CronJob → Job → Pod resolves to the CronJob.
		&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "ns"},
			Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{Spec: podSpec("postgres:14.13")}}}}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "backup-123", Namespace: "ns", OwnerReferences: ctrl("CronJob", "backup")}},
		pod("backup-123-x", "Job", "backup-123", "postgres:14.13", "sha", true),

		// Operator-managed pods (CRD owner we don't list) group under the owner.
		pod("im-1", "InstanceManager", "instance-manager-a", "longhornio/longhorn-instance-manager:v1.7.2", "s", true),
		pod("im-2", "InstanceManager", "instance-manager-a", "longhornio/longhorn-instance-manager:v1.7.2", "s", true),

		// Bare pod, plus a finished pod that must be ignored.
		pod("debug", "", "", "busybox:latest", "s", true),
	}
	done := pod("old-job-pod", "", "", "busybox:1.36", "s", false)
	done.Status.Phase = corev1.PodSucceeded
	objs = append(objs, done)

	snap, err := (&Collector{Client: fake.NewClientset(objs...), Version: "test"}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	web := find(t, snap, "Deployment", "web")
	if web.DesiredReplicas != 2 || web.Containers[0].Image != "nginx:1.27.4" {
		t.Errorf("web template wrong: %+v", web)
	}
	if len(web.Running) != 2 {
		t.Fatalf("want 2 running images during rollout, got %+v", web.Running)
	}
	if r := web.Running[0]; r.Image != "nginx:1.27.3" || r.Pods != 1 || r.Ready != 1 {
		t.Errorf("old image entry wrong: %+v", r)
	}
	if r := web.Running[1]; r.Image != "nginx:1.27.4" || r.Ready != 0 {
		t.Errorf("new image entry wrong: %+v", r)
	}

	if rp := find(t, snap, "StatefulSet", "redpanda"); rp.DesiredReplicas != 0 || len(rp.Running) != 0 {
		t.Errorf("redpanda: %+v", rp)
	}
	if cj := find(t, snap, "CronJob", "backup"); len(cj.Running) != 1 {
		t.Errorf("cronjob pod not attributed to CronJob: %+v", cj)
	}
	if im := find(t, snap, "InstanceManager", "instance-manager-a"); len(im.Running) != 1 || im.Running[0].Pods != 2 {
		t.Errorf("operator pods not grouped: %+v", im)
	}
	find(t, snap, "Pod", "debug")
	for _, w := range snap.Workloads {
		if w.Kind == "ReplicaSet" || w.Kind == "Job" || w.Name == "old-job-pod" {
			t.Errorf("unexpected workload %s/%s", w.Kind, w.Name)
		}
	}
	if len(snap.Cluster.Nodes) != 1 || snap.Cluster.Nodes[0].KubeletVersion != "v1.30.4+rke2r1" {
		t.Errorf("nodes: %+v", snap.Cluster.Nodes)
	}
}
