// Runtime field-level sync methods for OperatorNativeWG.
//
// These are called from service.applyDiffNWG to push specific stored
// fields (DNS, address/MTU, peer, AWG params, description) to a running
// NDMS interface without restarting it. They are decoupled from the
// lifecycle (Create/Start/Stop/Delete), which lives in operator.go and
// owns the heavier orchestration around kmod, peer-via, etc.
package nwg

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ndms/payloads"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// SyncDNS reconciles DNS servers for a NativeWG tunnel: clears oldDNS
// from NDMS, then applies newDNS. Either side may be nil/empty —
// passing both lists explicitly avoids needing applied-state tracking.
//
// Contract asymmetry vs OperatorOS5Impl.SyncDNS(ctx, id, dns): the OS5
// path tracks applied DNS internally and computes its own diff. The NWG
// path takes both lists as parameters and is stateless. This is
// deliberate — caller already knows oldDNS (it's the previous stored
// value), so the diff naturally lives at the call site.
//
// Use cases:
//   - Start tunnel: SyncDNS(ctx, stored, nil, tunnel.ParseDNSList(stored.Interface.DNS))
//   - Stop tunnel:  SyncDNS(ctx, stored, tunnel.ParseDNSList(stored.Interface.DNS), nil)
//   - Update DNS:   SyncDNS(ctx, stored, oldList, newList)
func (o *OperatorNativeWG) SyncDNS(ctx context.Context, stored *storage.AWGTunnel, oldDNS, newDNS []string) error {
	names := NewNWGNames(stored.NWGIndex)
	if len(oldDNS) > 0 {
		if err := o.commands.Interfaces.ClearDNS(ctx, names.NDMSName, oldDNS); err != nil {
			o.appLog.Warn("clear-dns", names.NDMSName, err.Error())
		}
	}
	if len(newDNS) > 0 {
		if err := o.commands.Interfaces.SetDNS(ctx, names.NDMSName, newDNS); err != nil {
			return fmt.Errorf("set DNS: %w", err)
		}
	}
	return nil
}

// SyncAWGParams applies AmneziaWG obfuscation parameters (Jc, Jmin,
// Jmax, S1-S4, H1-H4, I1-I5, Qlen) to a running NativeWG tunnel via
// RCI. Best-effort: if NDMS rejects (some firmware versions require
// interface down for ASC changes), failures bubble up so the caller
// can log a Warn and instruct the user to restart the tunnel.
func (o *OperatorNativeWG) SyncAWGParams(ctx context.Context, stored *storage.AWGTunnel) error {
	if !o.supportsASC() {
		return fmt.Errorf("ASC not supported by firmware; restart tunnel to apply")
	}
	// Туннель 3.x на прошивке с ASC 2.0 идёт через awg_proxy — параметры ему
	// довозит SyncKmodSlot, а не эта запись. Слать их в NDMS нельзя: прошивка
	// обфусцирует сама, kmod наложит обфускацию поверх, и на выходе мусор.
	if !o.useASC(&stored.Interface) {
		return nil
	}
	names := NewNWGNames(stored.NWGIndex)
	ascJSON, err := buildASCJSON(&stored.Interface)
	if err != nil {
		return fmt.Errorf("build ASC params: %w", err)
	}
	if ascJSON == nil {
		return nil
	}
	if err := o.commands.Wireguard.SetASCParams(ctx, names.NDMSName, ascJSON); err != nil {
		return fmt.Errorf("set ASC params: %w", err)
	}
	return nil
}

// SyncAddressMTU pushes the stored address and MTU to the NDMS interface.
// Called on Start (to override any changes made via the router UI)
// and on Update (to hot-apply changes to a running tunnel).
func (o *OperatorNativeWG) SyncAddressMTU(ctx context.Context, stored *storage.AWGTunnel) error {
	ndmsName := NewNWGNames(stored.NWGIndex).NDMSName

	// extractIPv4 сохраняет CIDR-суффикс — маска пользователя доезжает до
	// RCI, а не заменяется дефолтным /32 (issue #531).
	addr, mask := splitAddressMask(extractIPv4(stored.Interface.Address))
	if err := o.commands.Interfaces.SetAddress(ctx, ndmsName, addr, mask); err != nil {
		return fmt.Errorf("sync address: %w", err)
	}

	ipv6 := extractIPv6(stored.Interface.Address)
	if ipv6 != "" {
		if err := o.commands.Interfaces.SetIPv6Address(ctx, ndmsName, ipv6); err != nil {
			o.appLog.Warn("sync-address-mtu", ndmsName, "ipv6: "+err.Error())
		}
	} else {
		_ = o.commands.Interfaces.ClearIPv6Address(ctx, ndmsName)
	}

	if err := o.commands.Interfaces.SetMTU(ctx, ndmsName, stored.Interface.MTU); err != nil {
		return fmt.Errorf("sync mtu: %w", err)
	}

	o.appLog.Info("sync-address-mtu", ndmsName, fmt.Sprintf("address=%s mask=%s ipv6=%s mtu=%d", addr, mask, ipv6, stored.Interface.MTU))
	return nil
}

// SyncPrivateKey pushes stored.Interface.PrivateKey to NDMS.
//
// Required when the interface section is replaced wholesale (ReplaceConfig)
// or its PrivateKey changes via Update. CmdWireguardPrivateKey is otherwise
// only emitted in Create — without explicit re-sync, NDMS keeps the original
// key from import. WG kernel then signs handshake initiators with the
// old identity; the new server (whose peer entry expects the public key
// derived from the NEW private key) silently drops them → handshake never
// completes. Symptom: tx grows, rx stays at 0, last-handshake never updates.
func (o *OperatorNativeWG) SyncPrivateKey(ctx context.Context, stored *storage.AWGTunnel) error {
	ndmsName := NewNWGNames(stored.NWGIndex).NDMSName
	cmds := []any{
		payloads.CmdWireguardPrivateKey(ndmsName, stored.Interface.PrivateKey),
		payloads.CmdSave(),
	}
	if _, err := o.transport.PostBatch(ctx, cmds); err != nil {
		return fmt.Errorf("sync private-key: %w", err)
	}
	o.appLog.Info("sync-private-key", ndmsName, "private-key synced")
	return nil
}

// SyncPeer доводит до интерфейса NDMS сохранённые параметры пира:
// ключ, allowed-ips, keepalive, preshared-key.
//
// Endpoint'ом функция распоряжается отдельно. Доменное имя в конфиг NDMS
// не уходит НИКОГДА: роутер резолвит его сам и при неудаче молча не
// поднимает интерфейс (#702). Наружу отдаётся только IP — свежерезолвенный
// v4, либо заглушка вместо v6, который NDMS не принимает. Когда отдать
// нечего (резолв не удался, адрес v6), endpoint из команды опускается и в
// конфиге остаётся прежний, а актуальный адрес доводят wg set ниже, страж
// endpoint'а или следующий Start. На прошивках без нативного WireGuard
// (proxy-путь) endpoint не отправляется вовсе: в конфиге NDMS должен
// стоять адрес слота awg_proxy.ko, и владеют им startProxy/SyncKmodSlot.
//
// previousPublicKey lets callers atomically replace the peer when the
// public key changes (e.g. ReplaceConfig from a fresh .conf). If non-
// empty AND different from stored.Peer.PublicKey, the old peer entry is
// removed from NDMS in the same batch as the new one is added — without
// this, NDMS keeps both peers (it indexes by key) and the interface
// ends up with an orphan from the previous config. Pass "" when there
// is no previous peer to remove (e.g. fresh tunnel start).
func (o *OperatorNativeWG) SyncPeer(ctx context.Context, stored *storage.AWGTunnel, previousPublicKey string) error {
	ndmsName := NewNWGNames(stored.NWGIndex).NDMSName
	o.appLog.Full("replace-config", stored.Name, "Syncing peer parameters to NDMS")

	// NDMS отвергает IPv6-endpoint в peer-командах: в RCI уходит заглушка,
	// реальный endpoint живёт в ядре (wg set ниже + endpoint-страж).
	// Hostname резолвим здесь же (v4 предпочтителен — netutil.preferIPv4),
	// причём СВЕЖИМ резолвом, без кэш-фолбэка: кэш может нести адрес
	// прежнего endpoint'а. AAAA-only-имя NDMS резолвить не умеет — ему
	// тоже нужна заглушка.
	rciEndpoint := stored.Peer.Endpoint
	kernelEndpoint, kernelV6 := canonicalV6Endpoint(stored.Peer.Endpoint)
	v4Confirmed := false
	resolvedV4 := "" // резолв hostname'а: и в RCI, и в реестр стража (#702)
	if !kernelV6 {
		host, ok := splitEndpointHost(stored.Peer.Endpoint)
		switch {
		case !ok:
			// Пустой/мусорный endpoint — резолвить нечего.
		case net.ParseIP(host) != nil:
			// IP-литерал. Форма с двоеточиями — v6 без валидного порта
			// (canonicalV6Endpoint дал false): в ядро её не поставить,
			// v4 она не является. v4Confirmed — только настоящий v4.
			v4Confirmed = !strings.Contains(host, ":")
		default:
			if ip, port, err := o.resolveEndpointFresh(stored.Peer.Endpoint); err == nil {
				if parsed := net.ParseIP(ip); parsed != nil && parsed.To4() == nil {
					kernelEndpoint = net.JoinHostPort(ip, strconv.Itoa(port))
					kernelV6 = true
				} else {
					v4Confirmed = true
					resolvedV4 = net.JoinHostPort(ip, strconv.Itoa(port))
					rciEndpoint = resolvedV4
				}
			} else if !o.useASC(&stored.Interface) {
				// Proxy-путь: endpoint в конфиге NDMS всё равно не наш —
				// см. общее правило ниже. Неудача резолва здесь ничего
				// не решает, ошибку не поднимаем.
				rciEndpoint = ""
			} else if previousPublicKey != "" && previousPublicKey != stored.Peer.PublicKey {
				// Особый угол: ключ пира сменился, значит батч ниже
				// УДАЛИТ старого пира (CmdWireguardPeerNo) и создаст
				// нового. Опустить endpoint здесь нельзя — прежнего
				// адреса не останется вовсе, пир будет без endpoint'а,
				// и туннель умрёт до ручного вмешательства. Отдавать имя
				// тоже нельзя (см. ниже). Единственный честный выход —
				// не применять смену: handler fail-closed не сохранит
				// storage, и пользователь увидит ошибку вместо тихой
				// поломки.
				return fmt.Errorf("sync peer: резолв %s не удался, смена ключа пира не применена: %w",
					stored.Peer.Endpoint, err)
			} else if e, guarded := o.guardGet(stored.ID); guarded && !e.viaNDMS && !e.viaKmod &&
				e.spec != stored.Peer.Endpoint {
				// v6-история: в конфиге NDMS лежит заглушка 127.0.0.1:1,
				// а не рабочий адрес (запись v6-стража — единственное
				// состояние, в котором она там стоит). Молча принять
				// нерезолвимое имя значит оставить туннель мёртвым до
				// ручного старта — отказываем, как и при смене ключа (#702).
				// Отказ только на СМЕНУ endpoint'а: при прежнем spec это
				// транзиентный сбой DNS, и ронять из-за него правку
				// соседних полей пира нельзя.
				return fmt.Errorf("sync peer: резолв %s не удался, а в конфиге NDMS заглушка v6 — смена endpoint не применена: %w",
					stored.Peer.Endpoint, err)
			} else {
				// Резолв не удался, но ключ прежний — пир в конфиге NDMS
				// останется вместе со своим endpoint'ом. Имя не отдаём:
				// при неудаче собственного резолва NDMS молча не поднимает
				// интерфейс, без единой строки в журнале роутера (#702).
				// Пустой endpoint команда просто опустит
				// (payloads.CmdWireguardPeer) — в конфиге останется
				// прежний адрес, а актуальный доведёт страж или Start.
				o.appLog.Warn("sync-peer", ndmsName,
					"резолв "+stored.Peer.Endpoint+" не удался — endpoint в NDMS оставлен прежним: "+err.Error())
				rciEndpoint = ""
			}
		}
	}
	if kernelV6 {
		rciEndpoint = ndmsEndpointPlaceholder
	}
	// Proxy-путь: в конфиге NDMS обязан стоять 127.0.0.1:<порт слота>, а
	// реальный адрес сервера живёт в слоте awg_proxy.ko. Endpoint'ом там
	// владеют startProxy и SyncKmodSlot — SyncPeer его не трогает вовсе.
	// Раньше сюда уходило доменное имя, и правка полей пира, которых нет
	// в kmodShapingChanged (AllowedIPs, keepalive, preshared-key), уводила
	// ядро слать хендшейки мимо прокси — без обфускации и без лечения.
	if !o.useASC(&stored.Interface) {
		rciEndpoint = ""
	}
	peerCfg := payloads.PeerConfig{
		PublicKey: stored.Peer.PublicKey,
		Endpoint:  rciEndpoint,
	}
	// NDMS принимает keepalive числом; диапазон AWG 3.0 сюда попасть не должен
	// (запрещён валидацией), а если попал — оставляем поле пустым, чтобы не
	// подсунуть прошивке мусор.
	if n, ok := stored.Peer.PersistentKeepalive.Single(); ok && n > 0 {
		peerCfg.KeepaliveInterval = n
	}
	if stored.Peer.PresharedKey != "" {
		peerCfg.PresharedKey = stored.Peer.PresharedKey
	}

	for _, raw := range stored.Peer.AllowedIPs {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if _, netw, err := net.ParseCIDR(s); err == nil && netw != nil {
			ones, _ := netw.Mask.Size()
			item := payloads.AllowedIP{Address: netw.IP.String(), Mask: strconv.Itoa(ones)}
			if netw.IP.To4() != nil {
				peerCfg.AllowedIPv4 = append(peerCfg.AllowedIPv4, item)
			} else {
				peerCfg.AllowedIPv6 = append(peerCfg.AllowedIPv6, item)
			}
			continue
		}
		if ip := net.ParseIP(s); ip != nil {
			if v4 := ip.To4(); v4 != nil {
				peerCfg.AllowedIPv4 = append(peerCfg.AllowedIPv4, payloads.AllowedIP{
					Address: v4.String(),
					Mask:    "32",
				})
			} else {
				peerCfg.AllowedIPv6 = append(peerCfg.AllowedIPv6, payloads.AllowedIP{
					Address: ip.String(),
					Mask:    "128",
				})
			}
		}
	}

	cmds := make([]any, 0, 3)
	if previousPublicKey != "" && previousPublicKey != stored.Peer.PublicKey {
		cmds = append(cmds, payloads.CmdWireguardPeerNo(ndmsName, previousPublicKey))
	}
	cmds = append(cmds, payloads.CmdWireguardPeer(ndmsName, peerCfg), payloads.CmdSave())
	_, err := o.transport.PostBatch(ctx, cmds)
	if err != nil {
		return fmt.Errorf("sync peer: %w", err)
	}

	if stored.ISPInterface != "" {
		if _, err := o.transport.Post(ctx, payloads.CmdWireguardPeerConnect(ndmsName, stored.Peer.PublicKey, stored.ISPInterface)); err != nil {
			o.appLog.Warn("sync-peer", ndmsName, "peer connect via: "+err.Error())
		}
	}

	// Смена ключа/endpoint'а должна доехать до ядра сразу, а реестр стража —
	// обновиться (устаревшая запись не просто восстановит старые значения:
	// wg set по старому ключу ВОСКРЕШАЕТ удалённого RCI-батчем пира).
	switch {
	case kernelV6 && !o.useASC(&stored.Interface):
		// Proxy-путь: v6-адрес живёт в слоте awg_proxy.ko (kmod умеет v6
		// с 1.3.0), а не в ядре. Писать его через wg set значило бы гнать
		// хендшейки мимо прокси — без обфускации. Endpoint в конфиге NDMS
		// тоже не наш: там 127.0.0.1:<порт слота>. Делаем то же, что для
		// v4 на этом пути — только освежаем запись стража (#702).
		// endpoint — из текущей записи, не из свежего резолва: в слоте
		// лежит прежний адрес, и запись «желаемого» навсегда увела бы
		// sweep в «адрес не менялся» (та же ловушка, что в v4-ветке ниже).
		// v6-литерал под стражем не держим — резолвить нечего, адрес
		// доводят startProxy/SyncKmodSlot.
		if guard, _ := guardModeForEndpoint(stored.Peer.Endpoint, false); !guard {
			o.guardUnregister(stored.ID)
		} else if cur, ok := o.guardGet(stored.ID); ok && cur.spec == stored.Peer.Endpoint {
			o.guardReplaceIfPresent(stored.ID, guardEntry{
				iface:    NewNWGNames(stored.NWGIndex).IfaceName,
				pubkey:   stored.Peer.PublicKey,
				endpoint: cur.endpoint,
				spec:     stored.Peer.Endpoint,
				name:     ndmsName,
				viaKmod:  true,
			})
		}
	case kernelV6:
		ifaceName := NewNWGNames(stored.NWGIndex).IfaceName
		entry := guardEntry{
			iface:    ifaceName,
			pubkey:   stored.Peer.PublicKey,
			endpoint: kernelEndpoint,
			spec:     stored.Peer.Endpoint,
			name:     ndmsName,
		}
		// Реестр — ДО wg set и независимо от его исхода: RCI-батч выше уже
		// заменил пира, и упавший wg set не повод оставить в страже старый
		// ключ. Свежая запись при упавшем wg set безопасна — страж сам
		// доведёт endpoint на ближайшем проходе (≤guardInterval).
		// Replace-if-present, не register: параллельный Stop/Delete
		// (оркестратор, другой лок-домен) мог снять туннель со стражи —
		// безусловный register воскресил бы запись навсегда.
		guarded := o.guardReplaceIfPresent(stored.ID, entry)
		err := setKernelPeerEndpoint(ctx, ifaceName, entry.pubkey, kernelEndpoint)
		switch {
		case err != nil && guarded:
			o.appLog.Warn("sync-peer", ndmsName, "kernel endpoint: "+err.Error()+" — страж доведёт на ближайшем проходе")
		case err != nil:
			// Устройства нет (туннель не запускался) — endpoint доедет
			// при старте, как и раньше.
			o.appLog.Full("sync-peer", ndmsName, "kernel endpoint отложен до старта: "+err.Error())
		case !guarded:
			// Живой переход v4→v6 (реестр был пуст, устройство есть):
			// заглушка уже ушла в RCI, без стража NDMS затрёт ею
			// kernel-endpoint при первом же переприменении конфига.
			o.guardRegister(stored.ID, entry)
			o.appLog.Info("sync-peer", ndmsName,
				fmt.Sprintf("endpoint теперь IPv6 — %s выставлен в ядро (%s), взят под endpoint-страж", kernelEndpoint, ifaceName))
		}
	case v4Confirmed:
		// ASC-only. На proxy-прошивках endpoint в конфиге NDMS обязан быть
		// 127.0.0.1:<порт слота> (его держат SyncKmodSlot и RestoreKmodTunnel,
		// operator.go:807/849) — страж, доводящий туда реальный адрес сервера,
		// сломал бы proxy-туннель. Non-ASC вне объёма этого плана.
		guard, viaNDMS := guardModeForEndpoint(stored.Peer.Endpoint, false)
		if guard && !o.useASC(&stored.Interface) {
			// Proxy-путь: за адресом следит режим viaKmod (Task A2), его
			// ставит startProxy. Здесь только освежаем запись — ключ или
			// имя могли смениться, а протухший spec в реестре заставил бы
			// стража резолвить не тот домен. Снимать стража нельзя:
			// SyncPeer вызывается при любой правке пира, и снятие оставило
			// бы туннель без защиты до следующего startProxy.
			// ВАЖНО: endpoint берём из текущей записи, а НЕ из свежего
			// резолва. Свежий резолв здесь отражал бы желаемое, а не
			// действительное: слот-то мы не трогали, в нём остался прежний
			// адрес. Записав новый, мы бы навсегда увели sweep в «адрес не
			// менялся» — слот никогда бы не пересобрался. Когда spec
			// сменился, слот пересоберёт SyncKmodSlot в том же
			// applyDiffNWG (kmodShapingChanged включает Endpoint), и он же
			// зарегистрирует свежую запись.
			cur, ok := o.guardGet(stored.ID)
			if !ok || cur.spec != stored.Peer.Endpoint {
				break
			}
			o.guardReplaceIfPresent(stored.ID, guardEntry{
				iface:    NewNWGNames(stored.NWGIndex).IfaceName,
				pubkey:   stored.Peer.PublicKey,
				endpoint: cur.endpoint, // прежний резолв — он же в слоте
				spec:     stored.Peer.Endpoint,
				name:     ndmsName,
				viaKmod:  true,
			})
			break
		}
		if guard && o.useASC(&stored.Interface) {
			// Hostname→v4: адрес за именем может смениться, а NDMS
			// хранит литерал — оставляем под стражем в режиме NDMS (#702).
			entry := guardEntry{
				iface:    NewNWGNames(stored.NWGIndex).IfaceName,
				pubkey:   stored.Peer.PublicKey,
				endpoint: resolvedV4, // резолв, НЕ rciEndpoint (там имя)
				spec:     stored.Peer.Endpoint,
				name:     ndmsName,
				viaNDMS:  viaNDMS,
			}
			// Register безусловный — в отличие от v6-ветки выше, где он
			// воскресил бы запись, снятую параллельным Stop/Delete, и страж
			// начал бы делать wg set по мёртвому устройству. Здесь цена
			// гонки другая: viaNDMS правит конфиг NDMS, а он у остановленного
			// туннеля живёт и должен быть актуален. Delete снимает запись
			// (operator.go:561-568), так что вечной она не станет.
			o.guardRegister(stored.ID, entry)
			break
		}
		// v4-литерал — резолвить нечего, стражу тут делать нечего.
		if o.guardHas(stored.ID) {
			o.guardUnregister(stored.ID)
			o.appLog.Info("sync-peer", ndmsName, "endpoint теперь v4-литерал — endpoint-страж снят, адресом управляет NDMS")
		}
	default:
		// Резолв hostname'а не удался (или endpoint непригоден). Если ключ
		// или endpoint изменились, реестр стража устарел — снять, иначе он
		// воскресит старого пира. При неизменном пире транзиентный сбой
		// DNS защиту не снимает.
		if e, ok := o.guardGet(stored.ID); ok &&
			(e.pubkey != stored.Peer.PublicKey || e.spec != stored.Peer.Endpoint) {
			o.guardUnregister(stored.ID)
			o.appLog.Warn("sync-peer", ndmsName,
				"endpoint не резолвится, параметры пира изменились — endpoint-страж снят; если имя резолвится только в IPv6, перезапустите туннель")
		}
	}

	o.appLog.Full("replace-config", stored.Name, "Peer sync complete")
	o.appLog.Info("sync-peer", ndmsName, fmt.Sprintf("allowed v4=%d, v6=%d", len(peerCfg.AllowedIPv4), len(peerCfg.AllowedIPv6)))
	return nil
}

// UpdateDescription updates the NDMS interface description.
func (o *OperatorNativeWG) UpdateDescription(ctx context.Context, stored *storage.AWGTunnel, name string) error {
	return o.commands.Interfaces.SetDescription(ctx, NewNWGNames(stored.NWGIndex).NDMSName, name)
}
