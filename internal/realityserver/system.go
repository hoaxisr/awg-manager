package realityserver

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

const (
	ndmNetfilterDir         = "/opt/etc/ndm/netfilter.d"
	realityWANHookPath      = "/opt/etc/ndm/netfilter.d/67-awgm-reality-wan.sh"
	realityWANRuleLabel     = "AWGM_REALITY_WAN"
	defaultIptablesFallback = "iptables"
)

var wanFirewallMu sync.Mutex

// SyncWANFirewall opens (or closes) the REALITY TCP port on Keenetic's WAN
// firewall. The rules are re-applied by an NDM netfilter hook after every NDMS
// firewall rebuild and always carry the AWGM_REALITY_WAN comment so they never
// collide with other AWGM-managed rules. No-op outside Keenetic.
func SyncWANFirewall(port int, enabled bool) {
	wanFirewallMu.Lock()
	defer wanFirewallMu.Unlock()

	if _, err := os.Stat(ndmNetfilterDir); err != nil {
		return
	}
	if !enabled || port <= 0 {
		_ = os.Remove(realityWANHookPath)
		cleanupWANRules(0)
		return
	}
	cleanupWANRules(port)
	desired := renderWANHook(port)
	if existing, err := os.ReadFile(realityWANHookPath); err != nil || string(existing) != desired {
		_ = storage.AtomicWritePerm(realityWANHookPath, []byte(desired), 0755) // ndm не должен прочесть недописанный хук
	}
	_ = exec.Command("/bin/sh", realityWANHookPath).Run()
}

// IsWANPortOpen reports whether the WAN firewall currently accepts TCP `port`.
// Returns true outside Keenetic (nothing to check).
func IsWANPortOpen(port int) bool {
	if port <= 0 {
		return false
	}
	if _, err := os.Stat(ndmNetfilterDir); err != nil {
		return true
	}
	ipt := iptablesBin()
	flag := fmt.Sprintf("--dport %d", port)
	out, err := exec.Command(ipt, "-w", "-S", "INPUT").Output()
	if err != nil {
		return true
	}
	if !chainAccepts(string(out), flag) {
		return false
	}
	if pub, err := exec.Command(ipt, "-w", "-S", "_NDM_IP_PUBLIC").Output(); err == nil {
		return chainAccepts(string(pub), flag)
	}
	return true
}

func chainAccepts(rules, flag string) bool {
	for _, line := range strings.Split(rules, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "-p tcp") &&
			(strings.Contains(line, flag+" ") || strings.HasSuffix(line, flag)) &&
			strings.Contains(line, "-j ACCEPT") {
			return true
		}
	}
	return false
}

func iptablesBin() string {
	if st, err := os.Stat("/opt/sbin/iptables"); err == nil && !st.IsDir() {
		return "/opt/sbin/iptables"
	}
	return defaultIptablesFallback
}

func renderWANHook(port int) string {
	return fmt.Sprintf(`#!/bin/sh
# Managed by awg-manager (VLESS REALITY direct ingress). Do not edit.
[ "$type" = "ip6tables" ] && exit 0
[ "$table" = "filter" ] || [ -z "$table" ] || exit 0
PORT="%d"
CMT="%s"
IPTABLES=/opt/sbin/iptables
[ -x "$IPTABLES" ] || IPTABLES=iptables
run() { "$IPTABLES" -w "$@" 2>/dev/null || "$IPTABLES" "$@" 2>/dev/null; }

run -C INPUT -p tcp -m tcp --dport "$PORT" -m comment --comment "$CMT" -j ACCEPT || \
    run -I INPUT 1 -p tcp -m tcp --dport "$PORT" -m comment --comment "$CMT" -j ACCEPT

if run -L _NDM_IP_PUBLIC -n >/dev/null 2>&1; then
    run -C _NDM_IP_PUBLIC -p tcp -m tcp --dport "$PORT" -m comment --comment "$CMT" -j ACCEPT || \
        run -I _NDM_IP_PUBLIC 1 -p tcp -m tcp --dport "$PORT" -m comment --comment "$CMT" -j ACCEPT
fi
exit 0
`, port, realityWANRuleLabel)
}

func cleanupWANRules(keepPort int) {
	ipt := iptablesBin()
	keep := ""
	if keepPort > 0 {
		keep = "--dport " + strconv.Itoa(keepPort) + " "
	}
	for _, chain := range []string{"INPUT", "_NDM_IP_PUBLIC"} {
		out, err := exec.Command(ipt, "-w", "-S", chain).Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.Contains(line, realityWANRuleLabel) {
				continue
			}
			if keep != "" && strings.Contains(line+" ", keep) {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "-A" {
				args := append([]string{"-w", "-D"}, fields[1:]...)
				_ = exec.Command(ipt, args...).Run()
			}
		}
	}
}

// DestCheck is the result of probing a camouflage target.
type DestCheck struct {
	Dest       string `json:"dest"`
	OK         bool   `json:"ok"`
	TLS13      bool   `json:"tls13"`
	H2         bool   `json:"h2"`
	X25519     bool   `json:"x25519"`
	LatencyMs  int64  `json:"latency_ms"`
	ServerName string `json:"server_name"`
	Error      string `json:"error,omitempty"`
}

// CheckDest verifies that dest supports what REALITY needs: TLS 1.3 with X25519
// key exchange (mandatory) and ideally HTTP/2 (recommended, matches browsers).
func CheckDest(ctx context.Context, dest, serverName string) DestCheck {
	res := DestCheck{Dest: dest}
	norm, err := NormalizeDest(dest)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Dest = norm
	host, _, _ := net.SplitHostPort(norm)
	sni := strings.TrimSpace(serverName)
	if sni == "" {
		sni = host
	}
	res.ServerName = sni

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	start := time.Now()
	d := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 6 * time.Second},
		Config: &tls.Config{
			ServerName:       sni,
			MinVersion:       tls.VersionTLS13,
			MaxVersion:       tls.VersionTLS13,
			NextProtos:       []string{"h2", "http/1.1"},
			CurvePreferences: []tls.CurveID{tls.X25519},
		},
	}
	conn, err := d.DialContext(ctx, "tcp", norm)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer conn.Close()
	res.LatencyMs = time.Since(start).Milliseconds()
	tc, ok := conn.(*tls.Conn)
	if !ok {
		res.Error = "unexpected connection type"
		return res
	}
	st := tc.ConnectionState()
	res.TLS13 = st.Version == tls.VersionTLS13
	res.X25519 = st.CurveID == tls.X25519
	res.H2 = st.NegotiatedProtocol == "h2"
	res.OK = res.TLS13 && res.X25519
	if !res.OK {
		res.Error = "dest must support TLS 1.3 with X25519"
	}
	return res
}

// IsPortFree reports whether a TCP port can be bound on all interfaces right now.
func IsPortFree(port int) bool {
	if port <= 0 || port > 65535 {
		return false
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
