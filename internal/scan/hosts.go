package scan

import (
	"net/netip"
	"slices"
	"strings"
)

// The server pulls whatever registry an agent's image ID names. A leaked agent token must not turn that into
// requests to the server's own network (the cloud metadata service, internal hosts): registry hosts that are
// IP literals, localhost or metadata names are refused unless an admin configured them
// (OWL_INTERNAL_REGISTRIES or a prefix in OWL_REGISTRY_AUTH_FILE). Names that resolve to private addresses
// are allowed — in-house registries usually do.

var metadataHosts = []string{"metadata", "metadata.google.internal", "metadata.azure.com", "instance-data", "instance-data.ec2.internal"}

// RegistryAllowed reports whether images from host (as in a reference: may carry a port) may be pulled;
// reason says why not. allowed are configured registry hosts (port optional).
func RegistryAllowed(host string, allowed []string) (ok bool, reason string) {
	h := strings.ToLower(host)
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSpace(a))
		if a != "" && (h == a || hostOnly(h) == a) {
			return true, ""
		}
	}
	name := strings.TrimSuffix(hostOnly(h), ".")
	if name == "localhost" || strings.HasSuffix(name, ".localhost") || slices.Contains(metadataHosts, name) {
		return false, "registry host " + host + " is local or a metadata service — add it to OWL_INTERNAL_REGISTRIES if it's really a registry"
	}
	if _, err := netip.ParseAddr(strings.Trim(name, "[]")); err == nil {
		return false, "registry host " + host + " is an IP address — add it to OWL_INTERNAL_REGISTRIES if it's really a registry"
	}
	return true, ""
}

func hostOnly(h string) string {
	if strings.HasPrefix(h, "[") {
		if i := strings.Index(h, "]"); i > 0 {
			return h[:i+1]
		}
	}
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[:i], ":") {
		return h[:i]
	}
	return h
}

// knownArch: the architectures a platform flag may name (anything else from an agent is ignored).
var knownArch = []string{"amd64", "arm64", "arm", "386", "ppc64le", "s390x", "riscv64"}
