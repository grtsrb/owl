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
