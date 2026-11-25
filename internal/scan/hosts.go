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

// metadataIPs are cloud metadata services outside the link-local range (AWS IPv6 IMDS, Alibaba Cloud).
var metadataIPs = []netip.Addr{netip.MustParseAddr("fd00:ec2::254"), netip.MustParseAddr("100.100.100.200")}

// NeverRegistry says why host (may carry a port) can never be a registry an admin configures in the UI
// ("" = it can be): localhost and metadata names, loopback, link-local, unspecified and multicast
// addresses, and known metadata IPs. Allowing them would let a leaked agent token point scans at the
// server's own network.
func NeverRegistry(host string) string {
	name := strings.TrimSuffix(hostOnly(strings.ToLower(strings.TrimSpace(host))), ".")
	if name == "localhost" || strings.HasSuffix(name, ".localhost") || slices.Contains(metadataHosts, name) {
		return "localhost and metadata names are never a registry"
	}
	if ip, err := netip.ParseAddr(strings.Trim(name, "[]")); err == nil {
		ip = ip.Unmap()
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || slices.Contains(metadataIPs, ip) {
			return "a local, link-local or metadata address is never a registry"
		}
	}
	return ""
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
