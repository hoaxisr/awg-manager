package router

import (
	"context"
	"fmt"
	"os"

	"github.com/hoaxisr/awg-manager/internal/proxyengine"
)

// ReadinessProbeResult provides structured and granular diagnostic details
// for the readiness check of the active routing engine.
type ReadinessProbeResult struct {
	Ready           bool
	EngineName      string
	Mode            string
	MissingCriteria []string
	Details         map[string]bool
}

func checkTCPRedirect() bool {
	tcp, _ := os.ReadFile("/proc/net/tcp")
	tcp6, _ := os.ReadFile("/proc/net/tcp6")
	return localPortInState(string(tcp), RedirectPort, tcpStateListen) ||
		localPortInState(string(tcp6), RedirectPort, tcpStateListen)
}

func checkUDPTProxy() bool {
	udp, _ := os.ReadFile("/proc/net/udp")
	udp6, _ := os.ReadFile("/proc/net/udp6")
	return localPortInState(string(udp), TPROXYPort, udpStateBound) ||
		localPortInState(string(udp6), TPROXYPort, udpStateBound)
}

// CheckEngineReadiness evaluates the readiness of the given engine for the specified mode.
func CheckEngineReadiness(ctx context.Context, engine proxyengine.Engine, engineName, mode string, tunMode bool, iface string, isMihomo bool) ReadinessProbeResult {
	res := ReadinessProbeResult{
		EngineName: engineName,
		Mode:       mode,
		Details:    make(map[string]bool),
	}

	if engine == nil {
		res.MissingCriteria = append(res.MissingCriteria, "engine not configured")
		return res
	}

	running, pid := engine.IsRunning()
	res.Details["process"] = running && pid > 0
	if !res.Details["process"] {
		res.MissingCriteria = append(res.MissingCriteria, "process not running")
	}

	if tunMode || usesTunInbound(mode) || iface != "" {
		if iface == "" {
			res.Details["tun_carrier"] = false
			res.MissingCriteria = append(res.MissingCriteria, "tun interface not provisioned")
		} else {
			carrierUp := tunReadyProbe(iface)
			res.Details["tun_carrier"] = carrierUp
			if !carrierUp {
				res.MissingCriteria = append(res.MissingCriteria, fmt.Sprintf("tun carrier=0 (%s)", iface))
			}
		}
	} else {
		// TPROXY / REDIRECT mode
		listening := singboxListeningProbe()
		res.Details["inbound_sockets"] = listening
		if !listening {
			if isMihomo {
				tcp, _ := os.ReadFile("/proc/net/tcp")
				tcp6, _ := os.ReadFile("/proc/net/tcp6")
				ctrlOK := localPortInState(string(tcp), 9090, tcpStateListen) ||
					localPortInState(string(tcp6), 9090, tcpStateListen)
				mixedOK := localPortInState(string(tcp), 1099, tcpStateListen) ||
					localPortInState(string(tcp6), 1099, tcpStateListen)
				res.Details["controller"] = ctrlOK
				res.Details["mixed_port"] = mixedOK
				if ctrlOK || mixedOK {
					res.Details["mihomo_listeners"] = true
				} else {
					res.MissingCriteria = append(res.MissingCriteria, "mihomo controller/mixed-port not ready")
				}
			}
			tcpOK := checkTCPRedirect()
			udpOK := checkUDPTProxy()
			res.Details["tcp_redirect"] = tcpOK
			res.Details["udp_tproxy"] = udpOK
			if !tcpOK {
				res.MissingCriteria = append(res.MissingCriteria, fmt.Sprintf("tcp redirect:%d not listening", RedirectPort))
			}
			if !udpOK {
				res.MissingCriteria = append(res.MissingCriteria, fmt.Sprintf("udp tproxy:%d not bound", TPROXYPort))
			}
			if tcpOK && udpOK {
				res.MissingCriteria = append(res.MissingCriteria, "inbound sockets not ready")
			}
		} else {
			res.Details["tcp_redirect"] = true
			res.Details["udp_tproxy"] = true
		}
	}

	res.Ready = len(res.MissingCriteria) == 0
	return res
}
