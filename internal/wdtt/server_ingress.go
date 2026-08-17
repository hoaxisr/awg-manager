package wdtt

import (
	"slices"
	"strings"
)

// WdttServerIngressRefs returns sing-box ingress interface refs for a WDTT
// server: kernel WG iface (opkgtunN/wdtt0) and raw relay iface (opkgtunM/wdttraw0).
func WdttServerIngressRefs(wgKernelIface, rawKernelIface string) []string {
	wg := strings.TrimSpace(wgKernelIface)
	if wg == "" {
		wg = DefaultWdttIface
	}
	raw := strings.TrimSpace(rawKernelIface)
	if raw == "" {
		raw = DefaultRawServerIface
	}
	return []string{"iface:" + wg, "iface:" + raw}
}

// staleWdttIngressRefs — наши прежние имена, которых на роутере больше нет:
// legacy wdtt0/wdttraw0 после переезда интерфейса в OpkgTun. Ссылка на
// несуществующий интерфейс остаётся в настройках ingress навсегда — чистим её
// тем же проходом, что добавляет актуальную.
func staleWdttIngressRefs(want []string) map[string]bool {
	stale := map[string]bool{}
	for _, legacy := range []string{"iface:" + DefaultWdttIface, "iface:" + DefaultRawServerIface} {
		if !slices.Contains(want, legacy) {
			stale[legacy] = true
		}
	}
	return stale
}

// EnsureWdttIngressRefs adds the raw ingress ref when the WG kernel ref is
// present but raw is missing. Returns the updated slice and whether it changed.
func EnsureWdttIngressRefs(refs []string, wgKernelIface, rawKernelIface string) ([]string, bool) {
	want := WdttServerIngressRefs(wgKernelIface, rawKernelIface)
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
	if !hasWG {
		return refs, false
	}
	stale := staleWdttIngressRefs(want)
	out := make([]string, 0, len(refs)+1)
	dropped := false
	for _, ref := range refs {
		if stale[ref] {
			dropped = true
			continue
		}
		out = append(out, ref)
	}
	if hasRaw {
		if !dropped {
			return refs, false
		}
		return out, true
	}
	return append(out, rawRef), true
}

// RemoveWdttIngressRefs drops both WG and raw ingress refs for a WDTT server.
func RemoveWdttIngressRefs(refs []string, wgKernelIface, rawKernelIface string) ([]string, bool) {
	want := WdttServerIngressRefs(wgKernelIface, rawKernelIface)
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
