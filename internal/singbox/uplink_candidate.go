package singbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os/exec"
	"reflect"
	"runtime"
	"time"

	"github.com/hoaxisr/awg-manager/internal/uplink"
	"github.com/hoaxisr/awg-manager/internal/uplink/sbconfig"
)

func (a *UplinkAdapter) UplinkCapabilities(t uplink.Target) uplink.Capabilities {
	supported := t.Kind == "" && runtime.GOOS == "linux"
	return uplink.Capabilities{ProviderDNS: true, IndependentProbe: supported, AutomaticFailback: supported, ControlledFailback: supported, RestartScope: "engine"}
}

// The candidate process contains one proxy, one loopback SOCKS listener and
// only its explicitly bound resolver. No TUN, API, direct outbound, user rules,
// cache file or shared process is copied into it.
func (a *UplinkAdapter) ProbeCandidate(ctx context.Context, t uplink.Target, device string) error {
	if !a.UplinkCapabilities(t).IndependentProbe {
		return uplink.ErrUnsupported
	}
	a.Operator.migrationMu.Lock()
	defer a.Operator.migrationMu.Unlock()
	ob, _, err := a.read(ctx, t)
	if err != nil {
		return err
	}
	binding, err := uplink.InspectBinding(device)
	if err != nil {
		return err
	}
	if server, _ := ob["server"].(string); net.ParseIP(server) == nil {
		binding.DNSServers, err = a.DNSForDevice(ctx, device)
		if err != nil {
			return err
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	doc, err := sbconfig.Apply(map[string]any{
		"log":       map[string]any{"disabled": true},
		"outbounds": []any{ob},
		"inbounds":  []any{map[string]any{"type": "socks", "tag": "candidate-in", "listen": "127.0.0.1", "listen_port": port}},
		"route":     map[string]any{"final": t.Ref},
	}, []sbconfig.Spec{{Tag: t.Ref, Binding: binding}})
	if err != nil {
		return err
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	// Keenetic does not provide /dev/stdin. procfs is already required to
	// verify candidate socket ownership; keep credentials in the pipe.
	check := exec.CommandContext(ctx, a.Operator.binary, "check", "-c", "/proc/self/fd/0")
	check.Stdin = bytes.NewReader(data)
	setCandidateParentDeath(check)
	if err = check.Run(); err != nil {
		return fmt.Errorf("candidate configuration rejected: %w", err)
	}
	cmd := exec.CommandContext(ctx, a.Operator.binary, "run", "-c", "/proc/self/fd/0")
	cmd.Stdin = bytes.NewReader(data)
	setCandidateParentDeath(cmd)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return err
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	for {
		if !candidateOwnsPort(cmd.Process.Pid, port) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
				continue
			}
		}
		// A fresh SOCKS connection is mandatory; no sockets from the live process.
		// A cold proxy may need DNS, transport and TLS handshakes. Honor the
		// configured probe deadline instead of silently limiting each HTTPS
		// request to less than a second on slower routers/transports.
		err = uplink.ProbeSOCKS(ctx, uint16(port), "", "")
		if err == nil && candidateOwnsPort(cmd.Process.Pid, port) {
			fresh, _, err := a.read(ctx, t)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(ob, fresh) {
				return uplink.ErrConflict
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
