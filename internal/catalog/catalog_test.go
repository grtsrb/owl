package catalog

import "testing"

func TestParseRef(t *testing.T) {
	cases := []struct{ in, registry, repo, tag, digest string }{
		{"nginx", "docker.io", "library/nginx", "latest", ""},
		{"nginx:1.25.4", "docker.io", "library/nginx", "1.25.4", ""},
		{"bitnami/redis:7.0.15-debian-12-r20", "docker.io", "bitnami/redis", "7.0.15-debian-12-r20", ""},
		{"registry.k8s.io/ingress-nginx/controller:v1.11.3@sha256:abc", "registry.k8s.io", "ingress-nginx/controller", "v1.11.3", "sha256:abc"},
		{"localhost:5000/team/app:1.0", "localhost:5000", "team/app", "1.0", ""},
		{"registry.example.com:5000/app", "registry.example.com:5000", "app", "latest", ""},
		{"redis@sha256:def", "docker.io", "library/redis", "", "sha256:def"},
		{"index.docker.io/library/postgres:16", "docker.io", "library/postgres", "16", ""},
	}
	for _, c := range cases {
		r := ParseRef(c.in)
		if r.Registry != c.registry || r.Repository != c.repo || r.Tag != c.tag || r.Digest != c.digest {
			t.Errorf("ParseRef(%q) = %+v", c.in, r)
		}
	}
}

func TestIdentify(t *testing.T) {
	cat, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	cat.InternalRegistries = []string{"registry.gitlab.example"}

	cases := []struct {
		image     string
		status    Status
		component string
		version   string
	}{
		// Plain upstream images.
		{"nginx:1.25.4", StatusIdentified, "nginx", "1.25.4"},
		{"postgres:14.13", StatusIdentified, "postgresql", "14.13"},
		{"grafana/grafana:11.3.0", StatusIdentified, "grafana", "11.3.0"},
		{"haproxy:2.4.24", StatusIdentified, "haproxy", "2.4.24"},
		{"docker.redpanda.com/redpandadata/redpanda:v24.2.7", StatusIdentified, "redpanda", "24.2.7"},
		{"longhornio/longhorn-manager:v1.5.5", StatusIdentified, "longhorn", "1.5.5"},
		{"registry.k8s.io/ingress-nginx/controller:v1.11.3", StatusIdentified, "ingress-nginx", "1.11.3"},
		// Two different HAProxy ingress projects must not be confused.
		{"haproxytech/kubernetes-ingress:1.10.11", StatusIdentified, "haproxy-kubernetes-ingress", "1.10.11"},
		{"quay.io/jcmoraisjr/haproxy-ingress:v0.15.0", StatusIdentified, "haproxy-ingress", "0.15.0"},
		// Vendor builds map back to the upstream version.
		{"rancher/nginx-ingress-controller:v1.10.4-hardened3", StatusIdentified, "ingress-nginx", "1.10.4"},
		{"rancher/hardened-coredns:v1.11.1-build20240305", StatusIdentified, "coredns", "1.11.1"},
		{"rancher/mirrored-coredns-coredns:1.13.1", StatusIdentified, "coredns", "1.13.1"},
		{"bitnami/redis:7.0.15-debian-12-r20", StatusIdentified, "redis", "7.0.15"},
		{"nginx:1.27.3-alpine", StatusIdentified, "nginx", "1.27.3"},
		// Digest pinned alongside a tag keeps the tag's version.
		{"nginx:1.27.3@sha256:abc", StatusIdentified, "nginx", "1.27.3"},
		// Registry mirrors / pull-through caches.
		{"harbor.example.com/dockerhub/library/redis:7.2.4", StatusIdentified, "redis", "7.2.4"},
		{"harbor.example.com/dockerhub/bitnami/redis:7.2.4", StatusIdentified, "redis", "7.2.4"},
		// Unpinned.
		{"redis:latest", StatusUnpinned, "redis", ""},
		{"redis", StatusUnpinned, "redis", ""},
		{"nginx:stable-alpine", StatusUnpinned, "nginx", ""},
		{"redis@sha256:def", StatusUnpinned, "redis", ""},
		{"postgres:16", StatusUnpinned, "postgresql", "16"},
		{"nginx:1.27", StatusUnpinned, "nginx", "1.27"},
		// In-house and unknown.
		{"registry.gitlab.example/it/billing-api:2.3.1", StatusInHouse, "", "2.3.1"},
		{"ghcr.io/acme/mystery-tool:0.4.0", StatusUnrecognized, "", ""},
		{"myorg/redis:7.2.4", StatusUnrecognized, "", ""}, // a fork is not upstream redis
		// Nested forks on public registries are not mirrors.
		{"ghcr.io/myorg/bitnami/redis:7.2.4", StatusUnrecognized, "", ""},
		{"ghcr.io/acme/grafana/grafana:9.0.0", StatusUnrecognized, "", ""},
	}
	for _, c := range cases {
		id := cat.Identify(c.image)
		comp := ""
		if id.Component != nil {
			comp = id.Component.ID
		}
		ver := ""
		if id.Version != nil {
			ver = id.Version.String()
		}
		if id.Status != c.status || comp != c.component || ver != c.version {
			t.Errorf("%s: got (%s, %q, %q, %q) want (%s, %q, %q)", c.image, id.Status, comp, ver, id.Reason,
				c.status, c.component, c.version)
		}
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		tag, want string
		ok        bool
	}{
		{"v1.30.4", "1.30.4", true},
		{"1.30.4+rke2r1", "1.30.4+rke2r1", true},
		{"v1.34.13-rc3+rke2r1", "1.34.13-rc3+rke2r1", true},
		{"v1.12.0-rc.1", "1.12.0-rc.1", true},
		{"3.0.0-beta2", "3.0.0-beta2", true},
		{"8.17.7-SNAPSHOT", "8.17.7-snapshot", true},
		{"0.166.0-nightly.c02ef6a", "0.166.0-nightly", true},
		{"v0.95.2-arm64-distroless", "0.95.2", true},
		{"v1.9.1-flannel3", "1.9.1", true},
		{"latest", "", false},
		{"20240305", "", false},
		{"1.2.3a", "", false},
		{"main", "", false},
	}
	for _, c := range cases {
		v, ok := ParseVersion(c.tag, "")
		if ok != c.ok || (ok && v.String() != c.want) {
			t.Errorf("ParseVersion(%q) = %q, %v", c.tag, v.String(), ok)
		}
	}
	a, _ := ParseVersion("1.12.0-rc.1", "")
	b, _ := ParseVersion("1.12.0", "")
	c, _ := ParseVersion("1.11.9", "")
	if a.Compare(b) >= 0 || b.Compare(c) <= 0 || c.Compare(a) >= 0 {
		t.Error("version ordering wrong")
	}
}

func TestClusterVersions(t *testing.T) {
	cat, _ := Builtin()
	got := map[string]string{}
	for _, id := range cat.ClusterVersions("v1.30.4+rke2r1") {
		got[id.Component.ID] = id.Version.String()
	}
	if got["kubernetes"] != "1.30.4" || got["rke2"] != "1.30.4+rke2r1" || len(got) != 2 {
		t.Errorf("rke2 cluster: %v", got)
	}
	if ids := cat.ClusterVersions("v1.33.6+k3s1"); len(ids) != 2 || ids[1].Component.ID != "k3s" {
		t.Errorf("k3s cluster: %+v", ids)
	}
}

func TestVersionOrderingNatural(t *testing.T) {
	cat, _ := Builtin()
	r1 := cat.ClusterVersions("v1.30.4+rke2r1")[1].Version
	r2 := cat.ClusterVersions("v1.30.4+rke2r2")[1].Version
	if r1.Compare(*r2) >= 0 {
		t.Error("rke2r2 must be newer than rke2r1")
	}
	pairs := [][2]string{{"1.12.0-rc.2", "1.12.0-rc.10"}, {"3.0.0-beta.9", "3.0.0-beta.10"}, {"1.12.0-rc.10", "1.12.0"}}
	for _, p := range pairs {
		a, _ := ParseVersion(p[0], "")
		b, _ := ParseVersion(p[1], "")
		if a.Compare(b) >= 0 {
			t.Errorf("%s should sort before %s", p[0], p[1])
		}
	}
}

// bundledWith marks images a distribution ships itself — also through a mirror — and never the upstream
// image of the same component.
func TestBundledWith(t *testing.T) {
	cat, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ image, comp, bundled string }{
		{"rancher/hardened-coredns:v1.11.1-build20240305", "coredns", "rke2"},
		{"harbor.example.com/proxy/rancher/hardened-coredns:v1.11.1-build20240305", "coredns", "rke2"},
		{"rancher/mirrored-coredns-coredns:1.11.3", "coredns", "k3s"},
		{"coredns/coredns:1.11.3", "coredns", ""},
		{"rancher/hardened-k8s-metrics-server:v0.7.1-build20240910", "metrics-server", "rke2"},
		{"rancher/nginx-ingress-controller:v1.10.4-hardened3", "ingress-nginx", "rke2"},
		{"registry.k8s.io/ingress-nginx/controller:v1.11.3", "ingress-nginx", ""},
	} {
		id := cat.Identify(c.image)
		if id.Component == nil || id.Component.ID != c.comp || id.BundledWith != c.bundled {
			t.Errorf("%s: component %v bundledWith %q, want %s %q", c.image, id.Component, id.BundledWith, c.comp, c.bundled)
		}
	}
}

// Built-in entries may use versionPattern (two versions in one tag): the app version is what's read.
func TestBuiltinVersionPattern(t *testing.T) {
	cat := &Catalog{Components: []Component{{ID: "kafka", Name: "Kafka", Images: []string{"quay.io/strimzi/kafka"},
		GitHub: "apache/kafka", VersionPattern: `-kafka-(\d+\.\d+\.\d+)$`}}}
	for i := range cat.Components {
		if err := cat.Components[i].prepare(); err != nil {
			t.Fatal(err)
		}
	}
	id := cat.Identify("quay.io/strimzi/kafka:0.45.0-kafka-3.9.0")
	if id.Status != StatusIdentified || id.Version == nil || id.Version.String() != "3.9.0" {
		t.Errorf("got %s %v (%s)", id.Status, id.Version, id.Reason)
	}
}

// Batch 1 (milestone 13): real image references, as published (checked against the registries 2026-11-19),
// to component, version and bundling. A floating or non-version tag must not read as a version.
func TestBatch1Images(t *testing.T) {
	cat, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ image, comp, version, bundled string }{
		{"docker.elastic.co/eck/eck-operator:3.5.0", "eck-operator", "3.5.0", ""},
		{"docker.elastic.co/eck/eck-operator-ubi:3.4.1", "eck-operator", "3.4.1", ""},
		{"docker.elastic.co/elasticsearch/elasticsearch:8.19.25", "elasticsearch", "8.19.25", ""},
		{"docker.elastic.co/elasticsearch/elasticsearch:8.17.7-SNAPSHOT", "elasticsearch", "8.17.7-snapshot", ""},
		{"elasticsearch:9.5.5", "elasticsearch", "9.5.5", ""},
		{"bitnamilegacy/elasticsearch:9.1.2-debian-12-r0", "elasticsearch", "9.1.2", ""},
		{"docker.elastic.co/kibana/kibana:9.5.5", "kibana", "9.5.5", ""},
		{"docker.elastic.co/logstash/logstash:8.19.25", "logstash", "8.19.25", ""},
		{"docker.elastic.co/beats/filebeat:8.19.25", "elastic-beats", "8.19.25", ""},
		{"docker.elastic.co/elastic-agent/elastic-agent:9.5.5", "elastic-agent", "9.5.5", ""},
		{"jertel/elastalert2:2.32.0", "elastalert2", "2.32.0", ""},
		{"traefik:v3.7.19", "traefik", "3.7.19", ""},
		{"traefik:3.8.0-rc.3", "traefik", "3.8.0-rc.3", ""},
		{"rancher/mirrored-library-traefik:3.7.19", "traefik", "3.7.19", "k3s"},
		{"rancher/hardened-traefik:v3.7.19-build20261118", "traefik", "3.7.19", "rke2"},
		{"quay.io/cilium/cilium:v1.20.5", "cilium", "1.20.5", ""},
		{"quay.io/cilium/operator-generic:v1.19.11", "cilium", "1.19.11", ""},
		{"rancher/mirrored-cilium-cilium:v1.20.5", "cilium", "1.20.5", "rke2"},
		{"quay.io/cilium/hubble-ui-backend:v0.13.6", "hubble-ui", "0.13.6", ""},
		{"quay.io/cilium/certgen:v0.4.14", "cilium-certgen", "0.4.14", ""},
		{"quay.io/cilium/cilium-envoy:v1.36.9-1782267392-edeb3f2af56c37c407efa1f63f0b32f595399bbc", "envoy", "1.36.9", ""},
		{"rancher/mirrored-cilium-cilium-envoy:v1.39.6-1794557904-dee711ff0e84b106edd0abcdd71961af1b870359", "envoy", "1.39.6", "rke2"},
		{"envoyproxy/envoy:v1.36.4", "envoy", "1.36.4", ""},
		{"calico/node:v3.32.4", "calico", "3.32.4", ""},
		{"quay.io/calico/typha:v3.33.1", "calico", "3.33.1", ""},
		{"rancher/hardened-calico:v3.33.1-build20261118", "calico", "3.33.1", "rke2"},
		{"quay.io/tigera/operator:v1.40.17", "tigera-operator", "1.40.17", ""},
		{"flannel/flannel:v0.28.10", "flannel", "0.28.10", ""},
		{"rancher/hardened-flannel:v0.28.10-build20261119", "flannel", "0.28.10", "rke2"},
		{"flannel/flannel-cni-plugin:v1.9.1-flannel3", "flannel-cni-plugin", "1.9.1", ""},
		{"quay.io/metallb/speaker:v0.16.2", "metallb", "0.16.2", ""},
		{"bitnamilegacy/metallb-controller:0.15.2-debian-12-r7", "metallb", "0.15.2", ""},
		{"quay.io/frrouting/frr:10.4.5", "frr", "10.4.5", ""},
		{"registry.k8s.io/external-dns/external-dns:v0.18.0", "external-dns", "0.18.0", ""},
		{"quay.io/jetstack/cert-manager-cainjector:v1.20.5", "cert-manager", "1.20.5", ""},
		{"bitnamilegacy/acmesolver:1.18.2-debian-12-r5", "cert-manager", "1.18.2", ""},
		{"ghcr.io/external-secrets/external-secrets:v2.15.0", "external-secrets", "2.15.0", ""},
		{"oci.external-secrets.io/external-secrets/external-secrets:v2.14.0", "external-secrets", "2.14.0", ""},
		{"bitnami/sealed-secrets-controller:0.41.2", "sealed-secrets", "0.41.2", ""},
		{"quay.io/prometheus/prometheus:v3.16.0-distroless", "prometheus", "3.16.0", ""},
		{"prom/prometheus:v3.16.0", "prometheus", "3.16.0", ""},
		{"rancher/mirrored-prometheus-prometheus:v3.8.1", "prometheus", "3.8.1", ""},
		{"quay.io/prometheus/alertmanager:v0.35.0", "alertmanager", "0.35.0", ""},
		{"quay.io/prometheus-operator/prometheus-config-reloader:v0.95.2", "prometheus-operator", "0.95.2", ""},
		{"quay.io/prometheus/node-exporter:v1.13.1", "node-exporter", "1.13.1", ""},
		{"registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.18.0", "kube-state-metrics", "2.18.0", ""},
		{"grafana/loki:3.6.22", "loki", "3.6.22", ""},
		{"grafana/promtail:3.6.11", "loki", "3.6.11", ""},
		{"grafana/alloy:v1.22.0-boringcrypto", "alloy", "1.22.0", ""},
		{"grafana/tempo:2.10.10", "tempo", "2.10.10", ""},
		{"grafana/mimir:3.2.6", "mimir", "3.2.6", ""},
		{"grafana/rollout-operator:v0.42.1", "rollout-operator", "0.42.1", ""},
		{"quay.io/thanos/thanos:v0.42.5", "thanos", "0.42.5", ""},
		{"otel/opentelemetry-collector-contrib:0.165.0", "opentelemetry-collector", "0.165.0", ""},
		{"otel/opentelemetry-collector-contrib:0.166.0-nightly.c02ef6a", "opentelemetry-collector", "0.166.0-nightly", ""},
		{"ghcr.io/open-telemetry/opentelemetry-operator/opentelemetry-operator:0.160.0", "opentelemetry-operator", "0.160.0", ""},
		{"fluent/fluent-bit:5.1.5", "fluent-bit", "5.1.5", ""},
		{"cr.fluentbit.io/fluent/fluent-bit:5.1.5-debug", "fluent-bit", "5.1.5", ""},
	} {
		id := cat.Identify(c.image)
		got := ""
		if id.Version != nil {
			got = id.Version.String()
		}
		if id.Component == nil || id.Component.ID != c.comp || id.Status != StatusIdentified || got != c.version || id.BundledWith != c.bundled {
			t.Errorf("%s: %v %s %q bundled %q (%s), want %s %s %q", c.image, id.Component, id.Status, got, id.BundledWith, id.Reason, c.comp, c.version, c.bundled)
		}
	}
	for _, img := range []string{"traefik:v3.7", "jertel/elastalert2:2", "quay.io/metallb/speaker:v0.16", "prom/prometheus:v3"} {
		if id := cat.Identify(img); id.Status != StatusUnpinned {
			t.Errorf("%s: floating tag must be unpinned, got %s", img, id.Status)
		}
	}
}

// No image is claimed by two components (the first would silently win).
func TestBuiltinImagesUnique(t *testing.T) {
	cat, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	owner := map[string]string{}
	for _, c := range cat.Components {
		for _, r := range c.refs {
			if o, ok := owner[r.Name()]; ok {
				t.Errorf("%s is in %s and %s", r.Name(), o, c.ID)
			}
			owner[r.Name()] = c.ID
		}
	}
}
