package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
	"github.com/hoaxisr/awg-manager/internal/warpscout"
)

// Читаем только живого пира/интерфейс, не сохранённый конфиг.
// Ни одна команда не выводит приватные ключи (`show ... dump` не используется).
type warpRuntime struct {
	PublicKey, PeerKey, Endpoint string
	Rx, Tx                       uint64
}

func (o *WarpTunnelOps) readWarpRuntime(ctx context.Context, ref *warpscout.TunnelRef) (warpRuntime, error) {
	if o.Runtime != nil {
		return o.Runtime(ctx, ref)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if ref.Mode != warpscout.TargetKernel {
		if o.Queries == nil || o.Queries.WGServers == nil {
			return warpRuntime{}, errors.New("нет свежего состояния NDMS")
		}
		name := ref.ID
		if ref.Mode == warpscout.TargetNative {
			if o.Store == nil {
				return warpRuntime{}, errors.New("нет хранилища NativeWG")
			}
			tun, e := o.Store.Get(ref.ID)
			if e != nil {
				return warpRuntime{}, e
			}
			if tun.NWGIndex < 0 {
				return warpRuntime{}, errors.New("интерфейс NativeWG не назначен в NDMS")
			}
			name = fmt.Sprintf("Wireguard%d", tun.NWGIndex)
		}
		// GetSystemTunnel обращается к живому ShowInterface в NDMS (без устаревшего list-кэша).
		st, err := o.Queries.WGServers.GetSystemTunnel(ctx, name)
		if err != nil {
			return warpRuntime{}, err
		}
		if st == nil || st.Peer == nil {
			return warpRuntime{}, errors.New("нет данных живого пира WG в NDMS")
		}
		r := warpRuntime{
			PublicKey: strings.TrimSpace(st.PublicKey),
			PeerKey:   strings.TrimSpace(st.Peer.PublicKey),
			Endpoint:  strings.TrimSpace(st.Peer.Endpoint),
		}
		if st.Peer.RxBytes < 0 || st.Peer.TxBytes < 0 || r.Endpoint == "" {
			return warpRuntime{}, errors.New("нет endpoint или счётчиков нужного WG-пира")
		}
		r.Rx = uint64(st.Peer.RxBytes)
		r.Tx = uint64(st.Peer.TxBytes)
		if host, _, e := net.SplitHostPort(r.Endpoint); e == nil {
			if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
				// Старые прошивки NativeWG используют слот awg_proxy: проверяем реальное назначение.
				data, e := kmod.ReadProc("/proc/awg_proxy/list")
				if e != nil {
					return warpRuntime{}, fmt.Errorf("не удалось проверить endpoint NativeWG-прокси: %w", e)
				}
				r.Endpoint, err = warpProxyEndpoint(string(data), r.Endpoint)
				if err != nil {
					return warpRuntime{}, err
				}
			}
		}
		return r, nil
	}
	read := func(field string) (string, error) {
		r, err := exec.RunWithOptions(ctx, "/opt/sbin/awg", []string{"show", ref.Iface, field}, exec.Options{Timeout: 2 * time.Second})
		if err != nil {
			return "", fmt.Errorf("чтение WG %s: %w", field, exec.FormatError(r, err))
		}
		return strings.TrimSpace(r.Stdout), nil
	}
	pub, err := read("public-key")
	if err != nil {
		return warpRuntime{}, err
	}
	eps, err := read("endpoints")
	if err != nil {
		return warpRuntime{}, err
	}
	transfers, err := read("transfer")
	if err != nil {
		return warpRuntime{}, err
	}
	return parseWarpRuntime(pub, eps, transfers, ref.PeerPublicKey)
}

func warpProxyEndpoint(data, listen string) (string, error) {
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for _, field := range fields[1:] {
			if field == "listen="+listen {
				if _, _, err := net.SplitHostPort(fields[0]); err == nil {
					return fields[0], nil
				}
			}
		}
	}
	return "", errors.New("живой слот NativeWG-прокси не найден; endpoint не подтверждён")
}

func parseWarpRuntime(pub, eps, transfers, peer string) (warpRuntime, error) {
	r := warpRuntime{PublicKey: strings.TrimSpace(pub)}
	for _, line := range strings.Split(eps, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == peer {
			r.PeerKey, r.Endpoint = f[0], f[1]
		}
	}
	for _, line := range strings.Split(transfers, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[0] != peer {
			continue
		}
		rx, e1 := strconv.ParseUint(f[1], 10, 64)
		tx, e2 := strconv.ParseUint(f[2], 10, 64)
		if e1 == nil && e2 == nil && r.Endpoint != "" {
			r.Rx, r.Tx = rx, tx
			return r, nil
		}
	}
	return r, errors.New("нет endpoint или счётчиков нужного WG-пира")
}

func confirmWarpRuntime(r warpRuntime, ref *warpscout.TunnelRef) error {
	if r.Endpoint != ref.Endpoint {
		return fmt.Errorf("endpoint в живом WG не совпал с конфигом: ожидается %s, применён %s", ref.Endpoint, r.Endpoint)
	}
	if r.PublicKey != ref.LocalPublicKey || r.PeerKey != ref.PeerPublicKey {
		return errors.New("ключи живого WG не совпали с сохранённым конфигом")
	}
	return nil
}
