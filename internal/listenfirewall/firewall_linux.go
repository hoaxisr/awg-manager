//go:build linux

package listenfirewall

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/iptables"
)

const listenNetfilterHookPath = "/opt/etc/ndm/netfilter.d/62-awgm-listen-ports.sh"

// Apply inserts an INPUT accept rule for proto/port.
func Apply(ctx context.Context, port int, proto string) error {
	proto = normalizeProto(proto)
	if port <= 0 {
		return fmt.Errorf("listen firewall: invalid port %d", port)
	}
	if Present(ctx, port, proto) {
		return nil
	}
	// Без -m comment: на Keenetic xt_comment часто не загружен (#666).
	if err := iptables.Run(ctx, "-I", "INPUT", "1",
		"-p", proto, "-m", proto, "--dport", fmt.Sprintf("%d", port),
		"-j", "ACCEPT"); err != nil {
		return fmt.Errorf("INPUT accept %s/%d: %w", proto, port, err)
	}
	return nil
}

// Remove drops managed INPUT rules for proto/port.
func Remove(ctx context.Context, port int, proto string) {
	flush(ctx, port, proto)
}

// Present reports whether a managed INPUT rule exists.
func Present(ctx context.Context, port int, proto string) bool {
	proto = normalizeProto(proto)
	if port <= 0 {
		return false
	}
	return iptables.Run(ctx, "-C", "INPUT",
		"-p", proto, "-m", proto, "--dport", fmt.Sprintf("%d", port),
		"-j", "ACCEPT") == nil
}

// Reconcile ensures desired ports are open and removes stale rules.
func Reconcile(ctx context.Context, desired []PortSpec) {
	desired = MergePortSpecs(desired)
	want := make(map[string]struct{}, len(desired))
	for _, spec := range desired {
		want[portKey(spec.Port, spec.Proto)] = struct{}{}
	}
	for _, spec := range listManaged(ctx) {
		key := portKey(spec.Port, spec.Proto)
		if _, ok := want[key]; ok {
			continue
		}
		flush(ctx, spec.Port, spec.Proto)
	}
	for _, spec := range desired {
		if Present(ctx, spec.Port, spec.Proto) {
			continue
		}
		_ = Apply(ctx, spec.Port, spec.Proto)
	}
	if len(desired) == 0 {
		removeListenNetfilterHook()
		return
	}
	_ = ensureListenNetfilterHook(ctx, desired)
}

func flush(ctx context.Context, port int, proto string) {
	proto = normalizeProto(proto)
	for i := 0; i < 5; i++ {
		_ = iptables.Run(ctx, "-D", "INPUT",
			"-p", proto, "-m", proto, "--dport", fmt.Sprintf("%d", port),
			"-j", "ACCEPT")
	}
	out, err := iptables.RunOutput(ctx, "-S", "INPUT")
	if err != nil {
		return
	}
	portArg := fmt.Sprintf("--dport %d", port)
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, Comment) {
			continue
		}
		if !strings.Contains(line, "-p "+proto) || !strings.Contains(line, portArg) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "-A" {
			continue
		}
		_ = iptables.Run(ctx, append([]string{"-D"}, fields[1:]...)...)
	}
}

func listManaged(ctx context.Context) []PortSpec {
	out, err := iptables.RunOutput(ctx, "-S", "INPUT")
	if err != nil {
		return nil
	}
	return parseManaged(out)
}

// parseManaged выбирает из вывода `iptables -S INPUT` правила, которые ставили
// мы. Владение определяется формой, потому что метку `-m comment` Apply не
// пишет вовсе: на Keenetic xt_comment часто не загружен (#666).
func parseManaged(out string) []PortSpec {
	var specs []PortSpec
	seen := map[string]struct{}{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		spec, ok := bareListenRule(fields)
		if !ok && strings.Contains(line, Comment) {
			// Правило с нашей меткой — наше однозначно, какой бы формы ни
			// было: его писала версия, где xt_comment был доступен.
			spec, ok = commentedListenRule(fields)
		}
		if !ok {
			continue
		}
		key := portKey(spec.Port, spec.Proto)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		specs = append(specs, spec)
	}
	return specs
}

// bareListenRule распознаёт РОВНО ту форму, что печатает `iptables -S` для
// правила из Apply: `-A INPUT -p <proto> -m <proto> --dport <N> -j ACCEPT`.
// Единственный лишний токен (-i, -s, второй -m, диапазон портов) снимает
// признание: такие правила ставит не AWG Manager, а NDM и прочие пакеты, и
// трогать их нельзя — раньше сверка считала их своими, впустую гоняла на них
// удаление каждый тик и могла снести одноимённое правило без уточнений.
func bareListenRule(fields []string) (PortSpec, bool) {
	const wantLen = 10 // -A INPUT -p udp -m udp --dport 500 -j ACCEPT
	if len(fields) != wantLen {
		return PortSpec{}, false
	}
	proto := fields[3]
	if fields[0] != "-A" || fields[1] != "INPUT" ||
		fields[2] != "-p" || fields[4] != "-m" || fields[5] != proto ||
		fields[6] != "--dport" || fields[8] != "-j" || fields[9] != "ACCEPT" {
		return PortSpec{}, false
	}
	port, err := strconv.Atoi(fields[7])
	if err != nil || port <= 0 {
		return PortSpec{}, false
	}
	return PortSpec{Port: port, Proto: normalizeProto(proto)}, true
}

// commentedListenRule достаёт proto/port из ACCEPT-правила INPUT свободной
// формы. Зовётся только для строк с нашей меткой, поэтому нестрогость здесь
// безопасна.
func commentedListenRule(fields []string) (PortSpec, bool) {
	if len(fields) < 2 || fields[0] != "-A" || fields[1] != "INPUT" {
		return PortSpec{}, false
	}
	proto := ""
	port := 0
	isAccept := false
	for i, f := range fields {
		switch {
		case f == "-p" && i+1 < len(fields):
			proto = fields[i+1]
		case f == "--dport" && i+1 < len(fields):
			port, _ = strconv.Atoi(fields[i+1])
		case f == "-j" && i+1 < len(fields) && fields[i+1] == "ACCEPT":
			isAccept = true
		}
	}
	if !isAccept || port <= 0 || proto == "" {
		return PortSpec{}, false
	}
	return PortSpec{Port: port, Proto: normalizeProto(proto)}, true
}

func portKey(port int, proto string) string {
	return normalizeProto(proto) + ":" + fmt.Sprintf("%d", port)
}

func normalizeProto(proto string) string {
	p := strings.ToLower(strings.TrimSpace(proto))
	if p == "" {
		return "udp"
	}
	return p
}

func listenNetfilterHookScript(specs []PortSpec) string {
	specs = MergePortSpecs(specs)
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].Proto != specs[j].Proto {
			return specs[i].Proto < specs[j].Proto
		}
		return specs[i].Port < specs[j].Port
	})
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# AWG Manager: INPUT ACCEPT for proxy listen ports (survives NDMS reload).\n")
	b.WriteString("[ \"$type\" = \"ip6tables\" ] && exit 0\n")
	b.WriteString("[ \"$table\" = \"filter\" ] || exit 0\n")
	b.WriteString("IPTABLES=/opt/sbin/iptables\n")
	b.WriteString("[ -x \"$IPTABLES\" ] || IPTABLES=iptables\n")
	b.WriteString("run() { \"$IPTABLES\" -w \"$@\" 2>/dev/null || \"$IPTABLES\" \"$@\" 2>/dev/null; }\n")
	for _, spec := range specs {
		proto := normalizeProto(spec.Proto)
		fmt.Fprintf(&b, "run -C INPUT -p %s -m %s --dport %d -j ACCEPT || run -I INPUT 1 -p %s -m %s --dport %d -j ACCEPT\n",
			proto, proto, spec.Port, proto, proto, spec.Port)
	}
	b.WriteString("exit 0\n")
	return b.String()
}

func ensureListenNetfilterHook(ctx context.Context, specs []PortSpec) error {
	specs = MergePortSpecs(specs)
	if len(specs) == 0 {
		removeListenNetfilterHook()
		return nil
	}
	script := listenNetfilterHookScript(specs)
	if err := storage.AtomicWritePerm(listenNetfilterHookPath, []byte(script), 0o755); err != nil {
		return err
	}
	_, err := exec.Run(ctx, "sh", "-c", "table=filter type=iptables sh "+listenNetfilterHookPath)
	return err
}

func removeListenNetfilterHook() {
	_ = os.Remove(listenNetfilterHookPath)
}
