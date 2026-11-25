package tgwebproxy

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultDirectWANNetfilterDir = "/opt/etc/ndm/netfilter.d"
	defaultDirectWANHookPath     = "/opt/etc/ndm/netfilter.d/68-awgm-tg-mtproto-wan.sh"
	directWANRuleLabel           = "AWGM_TG_MTPROTO_WAN"
	directWANExecTimeout         = 10 * time.Second
)

var ErrInvalidDirectPort = errors.New("некорректный прямой порт MTProto")

var directReservedPorts = map[int]string{
	1099:  "Mihomo mixed-port",
	2222:  "AWG Manager Web UI",
	2398:  "telemt raw backend",
	3553:  "DNS-прокси",
	8084:  "Telegram Web Proxy backend",
	8085:  "Telegram Web Proxy",
	8086:  "Telegram Web Proxy admin",
	9007:  "VLESS compat inbound",
	9008:  "VLESS XHTTP backend",
	9009:  "CDN-диспетчер",
	51820: "WireGuard (AWGM сервер)",
}

var (
	directWANFirewallMu   sync.Mutex
	directWANNetfilterDir = defaultDirectWANNetfilterDir
	directWANHookPath     = defaultDirectWANHookPath
	directWANStat         = os.Stat
	directWANExecOutput   = func(env []string, name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), directWANExecTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		if len(env) > 0 {
			cmd.Env = env
		}
		return cmd.CombinedOutput()
	}
	directWANIptablesBin = resolveDirectWANIptablesBin
)

// DirectWANFirewallState describes the probe outcome of Keenetic's WAN firewall
// rule for the direct MTProto TCP port.
type DirectWANFirewallState string

const (
	DirectWANFirewallOpen          DirectWANFirewallState = "open"
	DirectWANFirewallClosed        DirectWANFirewallState = "closed"
	DirectWANFirewallNotApplicable DirectWANFirewallState = "not_applicable"
	DirectWANFirewallUnknown       DirectWANFirewallState = "unknown"
)

// ValidateDirectPort verifies that the Telegram MTProto direct TCP port is in
// the unprivileged range 1024–65535 and does not collide with reserved
// NDMS/AWGM service ports before opening it on Keenetic's WAN firewall.
func ValidateDirectPort(port int) error {
	if port < 1024 || port > 65535 {
		return fmt.Errorf("%w: порт MTProto (%d) вне допустимого диапазона 1024–65535", ErrInvalidDirectPort, port)
	}
	if why, reserved := directReservedPorts[port]; reserved {
		return fmt.Errorf("%w: порт %d зарезервирован: %s", ErrInvalidDirectPort, port, why)
	}
	return nil
}

// SyncDirectWANFirewall opens (or closes) the Telegram MTProto Fake-TLS TCP port
// on Keenetic's WAN firewall (_NDM_IP_PUBLIC scoped to public WAN interfaces
// discovered from _NDM_INPUT). Rules carry the dedicated AWGM_TG_MTPROTO_WAN
// comment and are persisted via an NDM netfilter.d hook so they survive
// firewall rebuilds without exposing guest or VPN segments or colliding with
// other AWGM services. Outside Keenetic (when netfilter.d is absent), it is a no-op.
func SyncDirectWANFirewall(port int, enabled bool) error {
	directWANFirewallMu.Lock()
	defer directWANFirewallMu.Unlock()

	st, err := directWANStat(directWANNetfilterDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", directWANNetfilterDir, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", directWANNetfilterDir)
	}

	if enabled {
		if portErr := ValidateDirectPort(port); portErr != nil {
			log.Printf("tgwebproxy: wan firewall: refusing unsafe port %d: %v", port, portErr)
			var removeErr error
			if err := os.Remove(directWANHookPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Printf("tgwebproxy: wan firewall: remove hook %s failed: %v", directWANHookPath, err)
				removeErr = fmt.Errorf("remove hook %s: %w", directWANHookPath, err)
			}
			cleanupErr := cleanupDirectWANRules(0, nil)
			return errors.Join(portErr, removeErr, cleanupErr)
		}
	}

	if !enabled {
		var removeErr error
		if err := os.Remove(directWANHookPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("tgwebproxy: wan firewall: remove hook %s failed: %v", directWANHookPath, err)
			removeErr = fmt.Errorf("remove hook %s: %w", directWANHookPath, err)
		}
		cleanupErr := cleanupDirectWANRules(0, nil)
		return errors.Join(removeErr, cleanupErr)
	}

	ipt := directWANIptablesBin()
	var ndmErr error
	var keepWANIfaces map[string]struct{}
	outNDM, err := runDirectWANIptables(ipt, "-S", "_NDM_INPUT")
	if err == nil {
		keepWANIfaces = sliceToSet(parsePublicWANIfaces(string(outNDM)))
	} else if !isIptablesNoSuchChain(outNDM, err) {
		ndmErr = fmt.Errorf("read _NDM_INPUT rules: %w", wrapIptablesErr(outNDM, err))
	}

	cleanupErr := cleanupDirectWANRules(port, keepWANIfaces)

	desiredHook := renderDirectWANHook(port)
	if hookNeedsRewrite(directWANHookPath, desiredHook) {
		if err := writeHookAtomically(directWANHookPath, desiredHook); err != nil {
			log.Printf("tgwebproxy: wan firewall: write hook %s failed: %v", directWANHookPath, err)
			return errors.Join(ndmErr, cleanupErr, fmt.Errorf("write hook %s: %w", directWANHookPath, err))
		}
	}
	var execErr error
	if out, err := directWANExecOutput(directWANHookEnv(), "/bin/sh", directWANHookPath); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			log.Printf("tgwebproxy: wan firewall: exec hook %s failed: %v (%s)", directWANHookPath, err, msg)
			execErr = fmt.Errorf("exec hook %s: %w (%s)", directWANHookPath, err, msg)
		} else {
			log.Printf("tgwebproxy: wan firewall: exec hook %s failed: %v", directWANHookPath, err)
			execErr = fmt.Errorf("exec hook %s: %w", directWANHookPath, err)
		}
	}
	return errors.Join(ndmErr, cleanupErr, execErr)
}

func directWANHookEnv() []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+2)
	for _, kv := range base {
		if strings.HasPrefix(kv, "type=") || strings.HasPrefix(kv, "table=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "type=iptables", "table=filter")
	return env
}

func hookNeedsRewrite(path, desired string) bool {
	st, err := os.Lstat(path)
	if err != nil {
		return true
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0755 {
		return true
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	return string(existing) != desired
}

func writeHookAtomically(targetPath, content string) error {
	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(targetPath)+".tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0755); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, targetPath)
}

func runDirectWANIptables(ipt string, args ...string) ([]byte, error) {
	w5Args := append([]string{"-w", "5"}, args...)
	if out, err := directWANExecOutput(nil, ipt, w5Args...); err == nil {
		return out, nil
	}
	wArgs := append([]string{"-w"}, args...)
	if out, err := directWANExecOutput(nil, ipt, wArgs...); err == nil {
		return out, nil
	}
	return directWANExecOutput(nil, ipt, args...)
}

func isIptablesNoSuchChain(out []byte, err error) bool {
	msg := strings.ToLower(string(out))
	if err != nil {
		msg += " " + strings.ToLower(err.Error())
	}
	return strings.Contains(msg, "no chain/target/match") || strings.Contains(msg, "no such chain")
}

func wrapIptablesErr(out []byte, err error) error {
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, msg)
}

func sliceToSet(items []string) map[string]struct{} {
	set := make(map[string]struct{}, len(items))
	for _, item := range items {
		set[item] = struct{}{}
	}
	return set
}

func isAllowedDirectWANInterface(ifc string) bool {
	val := strings.ToLower(strings.TrimSpace(ifc))
	if val == "" || val == "lo" {
		return false
	}
	if strings.HasPrefix(val, "br") && len(val) >= 3 && val[2] >= '0' && val[2] <= '9' {
		return false
	}
	for _, pfx := range []string{"opkgtun", "t2s", "tun", "tap", "wg", "nwg", "awg", "sstp", "ovpn"} {
		if strings.HasPrefix(val, pfx) {
			return false
		}
	}
	return true
}

func parsePublicWANIfaces(ndmInputRules string) []string {
	var ifaces []string
	seen := make(map[string]struct{})
	for _, line := range strings.Split(ndmInputRules, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 4 || fields[0] != "-A" || fields[1] != "_NDM_INPUT" {
			continue
		}
		var inIf, target string
		var negIf bool
		for i := 2; i < len(fields); i++ {
			tok := fields[i]
			if tok == "!" {
				continue
			}
			neg := i > 2 && fields[i-1] == "!"
			switch tok {
			case "-i", "--in-interface":
				if i+1 < len(fields) {
					inIf = strings.Trim(fields[i+1], "\"")
					negIf = neg
					i++
				}
			case "-j", "--jump":
				if i+1 < len(fields) {
					target = strings.Trim(fields[i+1], "\"")
					i++
				}
			}
		}
		if target != "_NDM_IP_PUBLIC" || negIf || !isAllowedDirectWANInterface(inIf) {
			continue
		}
		if _, exists := seen[inIf]; exists {
			continue
		}
		seen[inIf] = struct{}{}
		ifaces = append(ifaces, inIf)
	}
	return ifaces
}

// CheckDirectWANFirewall inspects Keenetic's WAN firewall rules for the direct
// MTProto TCP port and distinguishes between open, closed, non-Keenetic hosts
// (not_applicable when netfilter.d does not exist), and unreadable/unqueryable
// environments (unknown with underlying error).
func CheckDirectWANFirewall(port int) (DirectWANFirewallState, error) {
	if port <= 0 || port > 65535 {
		return DirectWANFirewallClosed, ErrInvalidDirectPort
	}
	st, err := directWANStat(directWANNetfilterDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DirectWANFirewallNotApplicable, nil
		}
		return DirectWANFirewallUnknown, fmt.Errorf("stat %s: %w", directWANNetfilterDir, err)
	}
	if !st.IsDir() {
		return DirectWANFirewallUnknown, fmt.Errorf("%s is not a directory", directWANNetfilterDir)
	}

	ipt := directWANIptablesBin()
	outPub, err := runDirectWANIptables(ipt, "-S", "_NDM_IP_PUBLIC")
	if err != nil {
		if isIptablesNoSuchChain(outPub, err) {
			return DirectWANFirewallClosed, nil
		}
		return DirectWANFirewallUnknown, fmt.Errorf("query _NDM_IP_PUBLIC: %w", wrapIptablesErr(outPub, err))
	}

	outNDM, err := runDirectWANIptables(ipt, "-S", "_NDM_INPUT")
	if err != nil {
		if isIptablesNoSuchChain(outNDM, err) {
			return DirectWANFirewallUnknown, fmt.Errorf("chain _NDM_INPUT not found: cannot determine public WAN interfaces")
		}
		return DirectWANFirewallUnknown, fmt.Errorf("query _NDM_INPUT: %w", wrapIptablesErr(outNDM, err))
	}

	wanIfaces := parsePublicWANIfaces(string(outNDM))
	if len(wanIfaces) == 0 {
		return DirectWANFirewallClosed, nil
	}
	if directChainAcceptsTCPPort(string(outPub), port, sliceToSet(wanIfaces)) {
		return DirectWANFirewallOpen, nil
	}
	return DirectWANFirewallClosed, nil
}

// IsDirectWANPortOpen reports whether Keenetic's WAN firewall currently accepts
// TCP connections on the direct MTProto port. Returns false outside Keenetic or
// if iptables inspection fails (use CheckDirectWANFirewall to distinguish states).
func IsDirectWANPortOpen(port int) bool {
	state, _ := CheckDirectWANFirewall(port)
	return state == DirectWANFirewallOpen
}

func resolveDirectWANIptablesBin() string {
	if st, err := os.Stat("/opt/sbin/iptables"); err == nil && !st.IsDir() {
		return "/opt/sbin/iptables"
	}
	return "iptables"
}

func directChainAcceptsTCPPort(rulesOut string, port int, allowedWANIfaces map[string]struct{}) bool {
	if len(allowedWANIfaces) == 0 {
		return false
	}
	targetPort := strconv.Itoa(port)
	for _, line := range strings.Split(rulesOut, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 4 || fields[0] != "-A" {
			continue
		}
		var protoOK, dportOK, jumpAccept, hasValidWANInIf, ruleDisqualified bool
		for i := 2; i < len(fields); i++ {
			tok := fields[i]
			if tok == "!" {
				continue
			}
			neg := i > 2 && fields[i-1] == "!"
			switch tok {
			case "-p", "--protocol":
				if i+1 < len(fields) {
					val := strings.ToLower(strings.Trim(fields[i+1], "\""))
					if neg || val != "tcp" {
						ruleDisqualified = true
					} else {
						protoOK = true
					}
					i++
				}
			case "--dport":
				if i+1 < len(fields) {
					val := strings.Trim(fields[i+1], "\"")
					if neg {
						ruleDisqualified = true
					} else if val == targetPort {
						dportOK = true
					}
					i++
				}
			case "-j", "--jump":
				if i+1 < len(fields) {
					val := strings.Trim(fields[i+1], "\"")
					if val == "ACCEPT" {
						jumpAccept = true
					}
					i++
				}
			case "-s", "--source":
				if i+1 < len(fields) {
					val := strings.ToLower(strings.Trim(fields[i+1], "\""))
					if neg || (val != "0.0.0.0/0" && val != "0.0.0.0/0.0.0.0") {
						ruleDisqualified = true
					}
					i++
				}
			case "-i", "--in-interface":
				if i+1 < len(fields) {
					val := strings.Trim(fields[i+1], "\"")
					if neg || !isAllowedDirectWANInterface(val) {
						ruleDisqualified = true
					} else if _, ok := allowedWANIfaces[val]; !ok {
						ruleDisqualified = true
					} else {
						hasValidWANInIf = true
					}
					i++
				}
			}
		}
		if !ruleDisqualified && protoOK && dportOK && jumpAccept && hasValidWANInIf {
			return true
		}
	}
	return false
}

func ruleHasExactComment(fields []string, label string) bool {
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "--comment" && strings.Trim(fields[i+1], "\"") == label {
			return true
		}
	}
	return false
}

func extractRulePortAndInIface(fields []string) (port int, inIf string, hasNegIf bool) {
	for i := 2; i < len(fields); i++ {
		tok := fields[i]
		if tok == "!" {
			continue
		}
		neg := i > 2 && fields[i-1] == "!"
		switch tok {
		case "--dport":
			if i+1 < len(fields) && !neg {
				if p, err := strconv.Atoi(strings.Trim(fields[i+1], "\"")); err == nil {
					port = p
				}
				i++
			}
		case "-i", "--in-interface":
			if i+1 < len(fields) {
				if neg {
					hasNegIf = true
				} else {
					inIf = strings.Trim(fields[i+1], "\"")
				}
				i++
			}
		}
	}
	return port, inIf, hasNegIf
}

func renderDirectWANHook(port int) string {
	return fmt.Sprintf(`#!/bin/sh
# Managed by awg-manager (Telegram MTProto direct ingress). Do not edit.
[ "$type" = "ip6tables" ] && exit 0
[ "$table" = "filter" ] || [ -z "$table" ] || exit 0
PORT="%d"
CMT="%s"
IPTABLES=/opt/sbin/iptables
[ -x "$IPTABLES" ] || IPTABLES=iptables
KREL="$(uname -r 2>/dev/null || true)"
if [ -n "$KREL" ] && ! grep -q "^xt_comment " /proc/modules 2>/dev/null; then
    for p in "/lib/modules/${KREL}/xt_comment.ko" "/lib/modules/${KREL}/kernel/net/netfilter/xt_comment.ko"; do
        if [ -f "$p" ]; then
            insmod "$p" 2>/dev/null || true
            break
        fi
    done
fi
run() { "$IPTABLES" -w 5 "$@" 2>/dev/null || "$IPTABLES" -w "$@" 2>/dev/null || "$IPTABLES" "$@" 2>/dev/null; }

if ! run -L _NDM_IP_PUBLIC -n >/dev/null 2>&1; then
    logger -t awgm-tg-mtproto-wan "FAILED: chain _NDM_IP_PUBLIC not found for tcp/${PORT}" 2>/dev/null || true
    exit 1
fi

WAN_IFACES=""
NDM_RULES="$(run -S _NDM_INPUT 2>/dev/null || true)"
set -f
while IFS= read -r line; do
    set -- $line
    [ "$1" = "-A" ] && [ "$2" = "_NDM_INPUT" ] || continue
    in_if=""
    target=""
    neg_if=0
    prev=""
    prev2=""
    for tok in "$@"; do
        if [ "$prev" = "-i" ] || [ "$prev" = "--in-interface" ]; then
            if [ "$prev2" = "!" ]; then
                neg_if=1
            else
                in_if="$tok"
            fi
        elif [ "$prev" = "-j" ] || [ "$prev" = "--jump" ]; then
            target="$tok"
        fi
        prev2="$prev"
        prev="$tok"
    done
    [ "$target" = "_NDM_IP_PUBLIC" ] || continue
    [ "$neg_if" = "0" ] || continue
    case "$in_if" in
        ""|lo|br[0-9]*|opkgtun*|t2s*|tun*|tap*|wg*|nwg*|awg*|sstp*|ovpn*) continue ;;
    esac
    case " $WAN_IFACES " in
        *" $in_if "*) ;;
        *) WAN_IFACES="${WAN_IFACES:+$WAN_IFACES }$in_if" ;;
    esac
done <<EOF
$NDM_RULES
EOF
set +f

if [ -z "$WAN_IFACES" ]; then
    logger -t awgm-tg-mtproto-wan "FAILED: no public WAN interfaces in _NDM_INPUT for tcp/${PORT}" 2>/dev/null || true
    exit 1
fi

ok=1
for ifc in $WAN_IFACES; do
    if ! run -C _NDM_IP_PUBLIC -i "$ifc" -p tcp -m tcp --dport "$PORT" -m comment --comment "$CMT" -j ACCEPT; then
        run -I _NDM_IP_PUBLIC 1 -i "$ifc" -p tcp -m tcp --dport "$PORT" -m comment --comment "$CMT" -j ACCEPT || ok=0
    fi
done

if [ "$ok" != "1" ]; then
    logger -t awgm-tg-mtproto-wan "FAILED to apply WAN firewall rule for tcp/${PORT}" 2>/dev/null || true
    exit 1
fi
exit 0
`, port, directWANRuleLabel)
}

func cleanupDirectWANRules(keepPort int, keepWANIfaces map[string]struct{}) error {
	ipt := directWANIptablesBin()
	var errs []error

	for _, chain := range []string{"INPUT", "_NDM_IP_PUBLIC"} {
		out, err := runDirectWANIptables(ipt, "-S", chain)
		if err != nil {
			if isIptablesNoSuchChain(out, err) {
				continue
			}
			errs = append(errs, fmt.Errorf("read %s rules: %w", chain, wrapIptablesErr(out, err)))
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			fields := strings.Fields(trimmed)
			if len(fields) < 2 || fields[0] != "-A" {
				continue
			}
			if !ruleHasExactComment(fields, directWANRuleLabel) {
				continue
			}
			if chain == "_NDM_IP_PUBLIC" && keepPort > 0 {
				rulePort, ruleInIf, hasNegIf := extractRulePortAndInIface(fields)
				if rulePort == keepPort && !hasNegIf && isAllowedDirectWANInterface(ruleInIf) {
					if keepWANIfaces == nil {
						continue
					}
					if _, ok := keepWANIfaces[ruleInIf]; ok {
						continue
					}
				}
			}
			for i := range fields {
				fields[i] = strings.Trim(fields[i], "\"")
			}
			args := append([]string{"-D"}, fields[1:]...)
			if delOut, delErr := runDirectWANIptables(ipt, args...); delErr != nil {
				errs = append(errs, fmt.Errorf("delete %s rule (%s): %w", chain, strings.Join(args, " "), wrapIptablesErr(delOut, delErr)))
			}
		}
	}
	return errors.Join(errs...)
}
