package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
	"github.com/hoaxisr/awg-manager/internal/warpscout"
)

// SwapVerified применяет новый endpoint (и опциональную маскировку AWG) на
// настоящем интерфейсе, проверяет живой WG и HTTPS/MTProto трафик, а при
// ошибке автоматически откатывает точную прежнюю конфигурацию.
func (o *WarpTunnelOps) SwapVerified(ctx context.Context, mode warpscout.TargetMode, id, endpoint string, opts warpscout.SwapOptions, filters warpscout.Filters) (*warpscout.VerificationResult, error) {
	o.swapMu.Lock()
	defer o.swapMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ref, err := o.Get(ctx, mode, id)
	if err != nil {
		return nil, err
	}
	if ref == nil || ref.Protected || !ref.IsWarp || !ref.Running {
		return nil, errors.New("целевой WARP-туннель выключен или защищён")
	}
	var restore func(context.Context) error
	if mode == warpscout.TargetSystem {
		if o.System == nil || o.Queries == nil || o.Queries.Interfaces == nil || o.Queries.WGServers == nil || o.Commands == nil || o.Commands.Wireguard == nil {
			return nil, errors.New("NDMS недоступен для безопасной замены")
		}
		asc, e := o.System.GetASCParams(ctx, id)
		if e != nil {
			return nil, fmt.Errorf("не сохранена маскировка для отката: %w", e)
		}
		if st, e := o.Queries.WGServers.GetSystemTunnel(ctx, id); e != nil {
			return nil, e
		} else if st == nil {
			return nil, errors.New("интерфейс исчез")
		}
		restore = func(undo context.Context) error {
			current, e := o.Get(undo, mode, id)
			if e != nil {
				return e
			}
			if current == nil || current.PeerPublicKey != ref.PeerPublicKey || (current.Endpoint != endpoint && current.Endpoint != ref.Endpoint) {
				return errors.New("системный туннель изменён вручную; откат отменён")
			}
			iface, _, present, e := o.Queries.Interfaces.Confirm(undo, id)
			if e != nil {
				return e
			}
			if !present {
				return errors.New("????????? ????? ????? ???????")
			}
			restart := current.Running
			if restart && o.Commands.Interfaces == nil {
				return errors.New("NDMS недоступен для перезапуска")
			}
			if restart {
				if e = o.Commands.Interfaces.InterfaceDown(undo, iface); e != nil {
					return e
				}
			}
			raised := false
			if restart {
				defer func() {
					if !raised {
						_ = o.Commands.Interfaces.InterfaceUp(undo, iface)
					}
				}()
			}
			if len(asc) == 0 {
				if e = o.Commands.Wireguard.ResetASCParams(undo, iface); e != nil {
					return e
				}
			} else {
				if e = o.System.SetASCParams(undo, id, asc); e != nil {
					return e
				}
			}
			if e = o.Commands.Wireguard.SetPeerEndpoint(undo, iface, ref.PeerPublicKey, ref.Endpoint); e != nil {
				return e
			}
			if restart {
				raised = true
				return o.Commands.Interfaces.InterfaceUp(undo, iface)
			}
			return nil
		}
	} else {
		if o.Store == nil || o.Svc == nil {
			return nil, errors.New("хранилище туннелей недоступно")
		}
		old, e := o.Store.Get(id)
		if e != nil {
			return nil, e
		}
		backup := config.GenerateForExport(old)
		restore = func(undo context.Context) error {
			cur, e := o.Store.Get(id)
			if e != nil {
				return e
			}
			if cur.Peer.Endpoint != endpoint && cur.Peer.Endpoint != old.Peer.Endpoint {
				return errors.New("туннель изменён вручную; автоматический откат отменён")
			}
			if cur.Interface.PrivateKey != old.Interface.PrivateKey {
				return errors.New("ключ изменён вручную; автоматический откат отменён")
			}
			enabled := cur.Enabled
			if e = o.Svc.ReplaceConfig(undo, id, backup, ""); e != nil {
				return e
			}
			if enabled {
				return o.Svc.Restart(undo, id)
			}
			return nil
		}
	}
	err = o.SwapEndpoint(ctx, mode, id, endpoint, opts)
	var vr *warpscout.VerificationResult
	if err == nil {
		vr, err = o.verifyWarpTraffic(ctx, mode, id, filters)
	}
	if err == nil && vr.Endpoint != endpoint {
		err = errors.New("???????? ?????? endpoint")
	}
	if err == nil {
		return vr, nil
	}
	undo, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if e := restore(undo); e != nil {
		return nil, fmt.Errorf("%v; %w: %v", err, warpscout.ErrRollback, e)
	}
	return nil, fmt.Errorf("%w; прежний конфиг восстановлен", err)
}

func (o *WarpTunnelOps) verifyWarpTraffic(ctx context.Context, mode warpscout.TargetMode, id string, filters warpscout.Filters) (*warpscout.VerificationResult, error) {
	ref, err := o.Get(ctx, mode, id)
	if err != nil {
		return nil, err
	}
	if ref == nil || !ref.Running || ref.Iface == "" {
		return nil, errors.New("созданный интерфейс не запущен")
	}
	before, err := o.readWarpRuntime(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err = confirmWarpRuntime(before, ref); err != nil {
		return nil, err
	}
	verify := o.Verify
	if verify == nil {
		verify = func(ctx context.Context, iface string, full bool) (*warpscout.VerificationResult, error) {
			return warpscout.VerifyInterfaceChecks(ctx, iface, full, filters.RequireTelegram)
		}
	}
	result, err := verify(ctx, ref.Iface, true)
	if err != nil {
		after, readErr := o.readWarpRuntime(ctx, ref)
		if readErr == nil && confirmWarpRuntime(after, ref) == nil && after.Rx >= before.Rx && after.Tx >= before.Tx {
			return nil, fmt.Errorf("проверка интерфейса %s (за время проверки WG RX +%d, TX +%d байт): %w",
				ref.Iface, after.Rx-before.Rx, after.Tx-before.Tx, err)
		}
		return nil, fmt.Errorf("проверка интерфейса %s: %w", ref.Iface, err)
	}
	if err = warpscout.CheckMeasurement(result, filters); err != nil {
		return nil, err
	}
	after, err := o.readWarpRuntime(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err = confirmWarpRuntime(after, ref); err != nil {
		return nil, err
	}
	if after.Rx < before.Rx || after.Rx-before.Rx < uint64(result.Bytes) || after.Tx <= before.Tx {
		return nil, errors.New("HTTPS ответил, но выбранный WG-пир не принял проверочный объём: работа этого туннеля не подтверждена")
	}
	current, err := o.Get(ctx, mode, id)
	if err != nil {
		return nil, err
	}
	if current == nil || !current.Running || current.Iface != ref.Iface || current.Endpoint != ref.Endpoint || current.LocalPublicKey != ref.LocalPublicKey {
		return nil, errors.New("туннель остановлен или изменён во время проверки")
	}
	if mode != warpscout.TargetSystem && o.Store != nil {
		tun, e := o.Store.Get(id)
		if e != nil {
			return nil, e
		}
		if !tun.Enabled {
			return nil, errors.New("туннель выключен во время проверки")
		}
	}
	if result.Endpoint == "" {
		result.Endpoint = ref.Endpoint
	}
	return result, nil
}
