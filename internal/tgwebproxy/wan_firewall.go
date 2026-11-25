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

// ValidateDirectPortTransition проверяет прямой порт там, где он меняется или
// где прямое подключение включается. Порт, сохранённый прежней версией, не
// мешает остальным правкам: удалить пользователя, сменить секрет, остановить.
func ValidateDirectPortTransition(old, next Config) error {
	if next.DirectPort != old.DirectPort && next.DirectPort != 0 {
		return ValidateDirectPort(next.DirectPort)
	}
	if next.IsDirectEnabled() && !old.IsDirectEnabled() {
		if err := ValidateDirectPort(next.effectiveDirectPort()); err != nil {
			return fmt.Errorf("%w. Смените прямой порт MTProto в настройках Telegram и запустите снова", err)
		}
	}
	return nil
}

// SyncDirectWANFirewall opens (or closes) the Telegram MTProto Fake-TLS TCP port
// on Keenetic's WAN firewall (INPUT and _NDM_IP_PUBLIC). Rules carry the dedicated
// AWGM_TG_MTPROTO_WAN comment and are persisted via an NDM netfilter.d hook so
// they survive firewall rebuilds without colliding with other AWGM services.
// Outside Keenetic (when the netfilter.d directory is absent), it is a no-op.
func SyncDirectWANFirewall(port int, enabled bool) {
	directWANFirewallMu.Lock()
	defer directWANFirewallMu.Unlock()

	if _, err := os.Stat(directWANNetfilterDir); err != nil {
		return
	}

	if enabled {
		if err := ValidateDirectPort(port); err != nil {
			log.Printf("tgwebproxy: wan firewall: refusing unsafe port %d: %v", port, err)
			enabled = false
		}
	}

	if !enabled {
		if err := os.Remove(directWANHookPath); err != nil && !os.IsNotExist(err) {
			log.Printf("tgwebproxy: wan firewall: remove hook %s failed: %v", directWANHookPath, err)
		}
		cleanupDirectWANRules(0)
		return
	}

	cleanupDirectWANRules(port)

	desiredHook := renderDirectWANHook(port)
	if hookNeedsRewrite(directWANHookPath, desiredHook) {
		if err := writeHookAtomically(directWANHookPath, desiredHook); err != nil {
			log.Printf("tgwebproxy: wan firewall: write hook %s failed: %v", directWANHookPath, err)
			return
		}
	}
	if out, err := directWANExecOutput(directWANHookEnv(), "/bin/sh", directWANHookPath); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			log.Printf("tgwebproxy: wan firewall: exec hook %s failed: %v (%s)", directWANHookPath, err, msg)
		} else {
			log.Printf("tgwebproxy: wan firewall: exec hook %s failed: %v", directWANHookPath, err)
		}
	}
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

// IsDirectWANPortOpen reports whether Keenetic's WAN firewall currently accepts
// TCP connections on the direct MTProto port. Returns true outside Keenetic,
// and fails closed (false) on Keenetic if iptables inspection fails.
func IsDirectWANPortOpen(port int) bool {
	if port <= 0 || port > 65535 {
		return false
	}
	if _, err := os.Stat(directWANNetfilterDir); err != nil {
		return true
	}

	ipt := directWANIptablesBin()
	outInput, err := runDirectWANIptables(ipt, "-S", "INPUT")
	if err != nil {
		return false
	}
	if !directChainAcceptsTCPPort(string(outInput), port) {
		return false
	}

	outPub, err := runDirectWANIptables(ipt, "-S", "_NDM_IP_PUBLIC")
	if err == nil {
		return directChainAcceptsTCPPort(string(outPub), port)
	}
	return isIptablesNoSuchChain(outPub, err)
}

func resolveDirectWANIptablesBin() string {
	if st, err := os.Stat("/opt/sbin/iptables"); err == nil && !st.IsDir() {
		return "/opt/sbin/iptables"
	}
	return "iptables"
}

func directChainAcceptsTCPPort(rulesOut string, port int) bool {
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
		var protoOK, dportOK, jumpAccept, ruleDisqualified bool
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
					val := strings.ToLower(strings.Trim(fields[i+1], "\""))
					if neg || val == "lo" ||
						strings.HasPrefix(val, "br") ||
						strings.HasPrefix(val, "wg") ||
						strings.HasPrefix(val, "nwg") ||
						strings.HasPrefix(val, "awg") {
						ruleDisqualified = true
					}
					i++
				}
			}
		}
		if !ruleDisqualified && protoOK && dportOK && jumpAccept {
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

ok=1
if ! run -C INPUT -p tcp -m tcp --dport "$PORT" -m comment --comment "$CMT" -j ACCEPT; then
    run -I INPUT 1 -p tcp -m tcp --dport "$PORT" -m comment --comment "$CMT" -j ACCEPT || ok=0
fi

if run -L _NDM_IP_PUBLIC -n >/dev/null 2>&1; then
    if ! run -C _NDM_IP_PUBLIC -p tcp -m tcp --dport "$PORT" -m comment --comment "$CMT" -j ACCEPT; then
        run -I _NDM_IP_PUBLIC 1 -p tcp -m tcp --dport "$PORT" -m comment --comment "$CMT" -j ACCEPT || ok=0
    fi
fi

if [ "$ok" != "1" ]; then
    logger -t awgm-tg-mtproto-wan "FAILED to apply WAN firewall rule for tcp/${PORT}" 2>/dev/null || true
    exit 1
fi
exit 0
`, port, directWANRuleLabel)
}

func cleanupDirectWANRules(keepPort int) {
	ipt := directWANIptablesBin()
	keepFlag := ""
	if keepPort > 0 {
		keepFlag = "--dport " + strconv.Itoa(keepPort)
	}

	for _, chain := range []string{"INPUT", "_NDM_IP_PUBLIC"} {
		out, err := runDirectWANIptables(ipt, "-S", chain)
		if err != nil {
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
			if keepFlag != "" && (strings.Contains(trimmed, keepFlag+" ") || strings.HasSuffix(trimmed, keepFlag)) {
				continue
			}
			for i := range fields {
				fields[i] = strings.Trim(fields[i], "\"")
			}
			args := append([]string{"-D"}, fields[1:]...)
			_, _ = runDirectWANIptables(ipt, args...)
		}
	}
}
