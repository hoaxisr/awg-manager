package wdtt

import (
	"slices"
	"strings"
)

// WdttServerIngressRefs returns sing-box ingress interface refs for a WDTT
// server: kernel WG iface (opkgtunN/wdtt0) and raw relay iface (wdttraw0).
func WdttServerIngressRefs(wgKernelIface string) []string {
	wg := strings.TrimSpace(wgKernelIface)
	if wg == "" {
		wg = DefaultWdttIface
	}
	return []string{"iface:" + wg, "iface:" + DefaultRawServerIface}
}

// EnsureWdttIngressRefs adds iface:wdttraw0 when the WG kernel ref is present
// but raw is missing. Returns the updated slice and whether it changed.
func EnsureWdttIngressRefs(refs []string, wgKernelIface string) ([]string, bool) {
	want := WdttServerIngressRefs(wgKernelIface)
	wgRef := want[0]
	rawRef := want[1]
	hasWG := false
	hasRaw := false
	for _, ref := range refs {
		switch ref {
		case wgRef:
			hasWG = true
		case rawRef:
			hasRaw = true
		}
	}
	if !hasWG || hasRaw {
		return refs, false
	}
	out := append(slices.Clone(refs), rawRef)
	return out, true
}

// RemoveWdttIngressRefs drops both WG and raw ingress refs for a WDTT server.
func RemoveWdttIngressRefs(refs []string, wgKernelIface string) ([]string, bool) {
	want := WdttServerIngressRefs(wgKernelIface)
	remove := map[string]bool{want[0]: true, want[1]: true}
	var out []string
	changed := false
	for _, ref := range refs {
		if remove[ref] {
			changed = true
			continue
		}
		out = append(out, ref)
	}
	if !changed {
		return refs, false
	}
	return out, true
}
