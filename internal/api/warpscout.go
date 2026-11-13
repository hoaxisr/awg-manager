package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/systemtunnel"
	"github.com/hoaxisr/awg-manager/internal/warpscout"
)

// ── Адаптер туннелей для автопилота ─────────────────────────────

// WarpTunnelOps реализует warpscout.TunnelOps поверх сервисов AWGM:
// kernel/nativewg — через TunnelService (Update под замком оркестратора),
// system — через NDMS RCI. Все проверки защиты собраны здесь, чтобы ни
// ручное «Подменить», ни автопилот не дотянулись до серверных и служебных
// интерфейсов (Wireguard2/AWGM, managedServers, встроенный VPN-сервер,
// Phobos, связанные прокси-клиенты).
type WarpTunnelOps struct {
	Svc      TunnelService
	Store    *storage.AWGTunnelStore
	Settings *storage.SettingsStore
	System   systemtunnel.Service
	Commands *ndmscommand.Commands
	Queries  *ndmsquery.Queries
	Tunnels  *TunnelsHandler // штатная правка туннеля (EditTunnelWith)
	// RotateKernelPort переоткрывает эфемерный UDP-порт kernel-туннеля
	// (`awg set <iface> listen-port 0`), чтобы сбросить блокировку ТСПУ
	// по паре (IP клиента, исходный UDP-порт). В тестах подменяется.
	RotateKernelPort func(ctx context.Context, iface string) error
}

var _ warpscout.TunnelOps = (*WarpTunnelOps)(nil)

func modeOfBackend(backend string) (warpscout.TargetMode, bool) {
	switch backend {
	case "nativewg":
		return warpscout.TargetNative, true
	case "kernel", "":
		return warpscout.TargetKernel, true
	}
	return "", false
}

func managedProtectReason(t *storage.AWGTunnel) string {
	switch {
	case t.Locked:
		return "туннель защищён от изменений (замок на карточке)"
	case t.Obfuscator != nil:
		return "endpoint обфусцированного туннеля не правится"
	case strings.TrimSpace(t.WdttClientID) != "", strings.TrimSpace(t.FreeTurnClientID) != "":
		return "туннель принадлежит прокси-клиенту"
	}
	return ""
}

func (o *WarpTunnelOps) managedRef(ctx context.Context, t *storage.AWGTunnel) (warpscout.TunnelRef, bool) {
	mode, ok := modeOfBackend(t.Backend)
	if !ok {
		return warpscout.TunnelRef{}, false
	}
	pub, _ := warpscout.PublicKeyOf(t.Interface.PrivateKey)
	ref := warpscout.TunnelRef{
		Mode: mode, ID: t.ID, Name: t.Name, Endpoint: t.Peer.Endpoint,
		AddressV4:      bareIPv4(t.Interface.Address),
		LocalPublicKey: pub,
		PeerPublicKey:  t.Peer.PublicKey,
		IsWarp:         strings.TrimSpace(t.Peer.PublicKey) == warpscout.WarpPeerPublicKey,
	}
	if reason := managedProtectReason(t); reason != "" {
		ref.Protected, ref.ProtectReason = true, reason
	}
	if o.Svc != nil {
		if tw, err := o.Svc.Get(ctx, t.ID); err == nil && tw != nil {
			ref.Iface = tw.InterfaceName
			// NativeWG без живого рукопожатия переходит в StateStarting / StateBroken,
			// хотя интерфейс административно включён — для автопилота это именно
			// повод проверить и подменить мёртвый endpoint, а не «туннель выключен».
			ref.Running = tw.State == tunnel.StateRunning ||
				(t.Enabled && (tw.State == tunnel.StateStarting || tw.State == tunnel.StateBroken))
		}
	}
	return ref, true
}

// systemProtectReason — почему системный интерфейс трогать нельзя.
func (o *WarpTunnelOps) systemProtectReason(t *ndms.SystemWireguardTunnel) string {
	if t.Description == ndms.BuiltInVPNServerDescription {
		return "встроенный VPN-сервер Keenetic"
	}
	if t.External != "" {
		return "интерфейс стороннего установщика (" + t.External + ")"
	}
	if strings.HasPrefix(t.Description, "AWGM WDTT") {
		return "служебный интерфейс WDTT"
	}
	if o.Settings != nil {
		if o.Settings.IsServerInterface(t.ID) {
			return "интерфейс отмечен как WG-сервер"
		}
		for _, ms := range o.Settings.GetManagedServers() {
			if ms.InterfaceName == t.ID {
				return "сервер AWGM (managedServers)"
			}
		}
	}
	for _, name := range managedNativeWGNames(o.Store) {
		if name == t.ID {
			return "туннель управляется AWGM — выберите тип native"
		}
	}
	return ""
}

func (o *WarpTunnelOps) systemRef(t *ndms.SystemWireguardTunnel) warpscout.TunnelRef {
	ref := warpscout.TunnelRef{
		Mode: warpscout.TargetSystem, ID: t.ID, Name: t.Description, Iface: t.InterfaceName,
		AddressV4:      bareIPv4(t.Address),
		LocalPublicKey: strings.TrimSpace(t.PublicKey),
		Running:        t.Status == "up",
	}
	if ref.Name == "" {
		ref.Name = t.ID
	}
	if t.Peer != nil {
		ref.Endpoint = t.Peer.Endpoint
		ref.PeerPublicKey = t.Peer.PublicKey
		ref.IsWarp = strings.TrimSpace(t.Peer.PublicKey) == warpscout.WarpPeerPublicKey
	}
	if reason := o.systemProtectReason(t); reason != "" {
		ref.Protected, ref.ProtectReason = true, reason
	}
	return ref
}

type parentTunnelInfo struct {
	mode        warpscout.TargetMode
	id          string
	name        string
	kernelIface string
	endpoint    string
	running     bool
}

func (o *WarpTunnelOps) populateViaInfo(ctx context.Context, refs []warpscout.TunnelRef) {
	o.populateViaInfoSeeded(ctx, refs, nil)
}

func (o *WarpTunnelOps) populateViaInfoSeeded(ctx context.Context, refs []warpscout.TunnelRef, seedVia map[string]string) {
	if len(refs) == 0 {
		return
	}
	byKey := map[string]parentTunnelInfo{}
	viaRaw := map[string]string{}

	if o.Store != nil {
		if list, err := o.Store.List(); err == nil {
			for i := range list {
				t := &list[i]
				m, ok := modeOfBackend(t.Backend)
				if !ok {
					continue
				}
				var kIface string
				var running bool
				if o.Svc != nil {
					if tw, err := o.Svc.Get(ctx, t.ID); err == nil && tw != nil {
						kIface = tw.InterfaceName
						running = tw.State == tunnel.StateRunning ||
							(t.Enabled && (tw.State == tunnel.StateStarting || tw.State == tunnel.StateBroken))
					}
				}
				if kIface == "" {
					if m == warpscout.TargetNative && t.NWGIndex >= 0 {
						kIface = fmt.Sprintf("nwg%d", t.NWGIndex)
					} else if m == warpscout.TargetKernel {
						kIface = tunnel.NewNames(t.ID).IfaceName
					}
				}
				info := parentTunnelInfo{
					mode: m, id: t.ID, name: t.Name,
					kernelIface: kIface, endpoint: t.Peer.Endpoint, running: running,
				}
				byKey["tunnel:"+t.ID] = info
				if m == warpscout.TargetNative && t.NWGIndex >= 0 {
					byKey[fmt.Sprintf("Wireguard%d", t.NWGIndex)] = info
				} else if m == warpscout.TargetKernel {
					byKey[tunnel.NewNames(t.ID).NDMSName] = info
				}
				if kIface != "" {
					byKey[kIface] = info
				}
				if via := strings.TrimSpace(t.ISPInterface); via != "" {
					viaRaw[string(m)+":"+t.ID] = via
				}
			}
		}
	}

	if o.System != nil {
		if sys, err := o.System.List(ctx); err == nil {
			for i := range sys {
				s := &sys[i]
				if _, exists := byKey[s.ID]; !exists && o.systemProtectReason(s) == "" {
					name := s.Description
					if name == "" {
						name = s.ID
					}
					ep := ""
					if s.Peer != nil {
						ep = s.Peer.Endpoint
					}
					info := parentTunnelInfo{
						mode: warpscout.TargetSystem, id: s.ID, name: name,
						kernelIface: s.InterfaceName, endpoint: ep, running: s.Status == "up",
					}
					byKey[s.ID] = info
					if s.InterfaceName != "" {
						byKey[s.InterfaceName] = info
					}
				}
				if s.Peer != nil && strings.TrimSpace(s.Peer.Via) != "" {
					viaRaw[string(warpscout.TargetSystem)+":"+s.ID] = strings.TrimSpace(s.Peer.Via)
				}
			}
		}
	}

	for k, v := range seedVia {
		if v := strings.TrimSpace(v); v != "" {
			viaRaw[k] = v
		}
	}

	for i := range refs {
		viaKey := viaRaw[string(refs[i].Mode)+":"+refs[i].ID]
		if viaKey == "" {
			continue
		}
		p, ok := byKey[viaKey]
		if !ok || (p.mode == refs[i].Mode && p.id == refs[i].ID) {
			continue
		}
		refs[i].ViaMode = p.mode
		refs[i].ViaTunnelID = p.id
		refs[i].ViaIface = p.kernelIface
		refs[i].ViaLabel = p.name
		refs[i].ViaEndpoint = p.endpoint
		refs[i].ViaRunning = p.running
	}
}

// List — managed-туннели и системные WG-клиенты. Серверы и служебные
// интерфейсы в выбор не попадают вовсе.
func (o *WarpTunnelOps) List(ctx context.Context) ([]warpscout.TunnelRef, error) {
	out := []warpscout.TunnelRef{}
	if o.Store != nil {
		list, err := o.Store.List()
		if err != nil {
			return nil, err
		}
		for i := range list {
			if ref, ok := o.managedRef(ctx, &list[i]); ok {
				out = append(out, ref)
			}
		}
	}
	if o.System != nil {
		sys, err := o.System.List(ctx)
		if err == nil {
			for i := range sys {
				ref := o.systemRef(&sys[i])
				if ref.Protected {
					continue
				}
				out = append(out, ref)
			}
		}
	}
	o.populateViaInfo(ctx, out)
	return out, nil
}

// bareIPv4 вытаскивает первый IPv4-хост (без /маски и без IPv6) из строки адресов интерфейса.
func bareIPv4(raw string) string {
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == ';'
	}) {
		host := strings.TrimSpace(strings.SplitN(part, "/", 2)[0])
		if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
			return ip.To4().String()
		}
	}
	return ""
}

// UsedIPv4 возвращает отсортированный список IPv4-адресов, уже занятых на роутере
// (все туннели AWGM, все системные WG-интерфейсы включая серверные, а также
// активные сетевые интерфейсы ядра). Нужен UI для предупреждения о конфликте
// ещё до создания WARP-туннеля.
func (o *WarpTunnelOps) UsedIPv4(ctx context.Context) []string {
	seen := map[string]bool{}
	addRaw := func(raw string) {
		if ip := bareIPv4(raw); ip != "" {
			seen[ip] = true
		}
	}
	if o.Store != nil {
		if list, err := o.Store.List(); err == nil {
			for i := range list {
				addRaw(list[i].Interface.Address)
			}
		}
	}
	if o.System != nil {
		if sys, err := o.System.List(ctx); err == nil {
			for i := range sys {
				addRaw(sys[i].Address)
			}
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP == nil || ipNet.IP.IsLoopback() || ipNet.IP.IsUnspecified() {
				continue
			}
			if ip4 := ipNet.IP.To4(); ip4 != nil {
				seen[ip4.String()] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for ip := range seen {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}

// Get — один туннель.
func (o *WarpTunnelOps) Get(ctx context.Context, mode warpscout.TargetMode, id string) (*warpscout.TunnelRef, error) {
	switch mode {
	case warpscout.TargetKernel, warpscout.TargetNative:
		if o.Store == nil {
			return nil, errors.New("хранилище туннелей недоступно")
		}
		t, err := o.Store.Get(id)
		if err != nil {
			return nil, fmt.Errorf("туннель %s не найден", id)
		}
		ref, ok := o.managedRef(ctx, t)
		if !ok || ref.Mode != mode {
			return nil, fmt.Errorf("туннель %s не типа %s", id, mode)
		}
		slice := []warpscout.TunnelRef{ref}
		// Сторож зовёт Get на каждой проверке; populateViaInfo опрашивает
		// состояние ВСЕХ туннелей, поэтому без привязки его не зовём.
		if strings.TrimSpace(t.ISPInterface) != "" {
			o.populateViaInfo(ctx, slice)
		}
		return &slice[0], nil
	case warpscout.TargetSystem:
		if o.System == nil {
			return nil, errors.New("системные туннели недоступны")
		}
		if !isValidWireguardName(id) {
			return nil, fmt.Errorf("некорректное имя интерфейса %q", id)
		}
		t, err := o.System.Get(ctx, id)
		if err != nil || t == nil {
			return nil, fmt.Errorf("системный туннель %s не найден", id)
		}
		slice := []warpscout.TunnelRef{o.systemRef(t)}
		var seedVia map[string]string
		if t.Peer != nil && strings.TrimSpace(t.Peer.Via) != "" {
			seedVia = map[string]string{
				string(warpscout.TargetSystem) + ":" + t.ID: strings.TrimSpace(t.Peer.Via),
			}
			o.populateViaInfoSeeded(ctx, slice, seedVia)
		}
		return &slice[0], nil
	}
	return nil, fmt.Errorf("неизвестный тип туннеля %q", mode)
}

func (o *WarpTunnelOps) resolveOuterNDMS(ctx context.Context, viaMode warpscout.TargetMode, viaID string) (ndmsName, label string, err error) {
	ref, err := o.Get(ctx, viaMode, viaID)
	if err != nil {
		return "", "", err
	}
	if ref.Protected {
		return "", "", fmt.Errorf("%w: %s", warpscout.ErrProtected, ref.ProtectReason)
	}
	if !ref.IsWarp {
		return "", "", warpscout.ErrNotWarp
	}
	switch viaMode {
	case warpscout.TargetSystem:
		return viaID, ref.Name, nil
	case warpscout.TargetNative:
		if o.Store == nil {
			return "", "", errors.New("хранилище туннелей недоступно")
		}
		st, err := o.Store.Get(viaID)
		if err != nil {
			return "", "", fmt.Errorf("внешний туннель %s не найден", viaID)
		}
		if st.NWGIndex < 0 {
			return "", "", fmt.Errorf("у внешнего туннеля %s не назначен интерфейс Wireguard", st.Name)
		}
		return fmt.Sprintf("Wireguard%d", st.NWGIndex), st.Name, nil
	case warpscout.TargetKernel:
		return tunnel.NewNames(viaID).NDMSName, ref.Name, nil
	default:
		return "", "", fmt.Errorf("неизвестный тип внешнего туннеля %q", viaMode)
	}
}

func extractPeerPublicKey(conf string) string {
	inPeer := false
	for _, line := range strings.Split(conf, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, ";") {
			continue
		}
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			inPeer = strings.EqualFold(strings.TrimSpace(s[1:len(s)-1]), "peer")
			continue
		}
		if !inPeer {
			continue
		}
		if k, v, ok := strings.Cut(s, "="); ok && strings.EqualFold(strings.TrimSpace(k), "PublicKey") {
			return strings.TrimSpace(v)
		}
	}
	return warpscout.WarpPeerPublicKey
}

// bindManagedVia привязывает только что импортированный NativeWG-туннель к
// внешнему: запись (ISPInterface) и `connect via` у пира в NDMS.
func (o *WarpTunnelOps) bindManagedVia(ctx context.Context, id, outerNDMS, outerLabel string) error {
	if o.Store == nil || o.Commands == nil || o.Commands.Wireguard == nil || o.Queries == nil || o.Queries.Interfaces == nil {
		return errors.New("NDMS недоступен")
	}
	if err := o.Store.Update(id, func(t *storage.AWGTunnel) error {
		t.ISPInterface = outerNDMS
		t.ISPInterfaceLabel = "Через " + outerLabel
		return nil
	}); err != nil {
		return fmt.Errorf("сохранить маршрут внешнего туннеля: %w", err)
	}
	created, err := o.Store.Get(id)
	if err != nil {
		return err
	}
	if created.NWGIndex < 0 {
		return errors.New("туннелю не назначен интерфейс Wireguard")
	}
	ndmsName := fmt.Sprintf("Wireguard%d", created.NWGIndex)
	iface, _, present, err := o.Queries.Interfaces.Confirm(ctx, ndmsName)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("интерфейс %s не найден на роутере", ndmsName)
	}
	return o.Commands.Wireguard.SetPeerConnectVia(ctx, iface, created.Peer.PublicKey, outerNDMS)
}

// Create — новый туннель из .conf (в т.ч. каскадный WARP-in-WARP при opts.ViaID != "").
func (o *WarpTunnelOps) Create(ctx context.Context, mode warpscout.TargetMode, name, conf string, opts warpscout.CreateOptions) (*warpscout.TunnelRef, error) {
	var outerNDMS, outerLabel string
	if strings.TrimSpace(opts.ViaID) != "" {
		if mode == warpscout.TargetKernel {
			return nil, fmt.Errorf("%w: для WARP-in-WARP выберите тип Native WG или Системный", warpscout.ErrInvalidInput)
		}
		var err error
		outerNDMS, outerLabel, err = o.resolveOuterNDMS(ctx, opts.ViaMode, strings.TrimSpace(opts.ViaID))
		if err != nil {
			return nil, err
		}
	}
	switch mode {
	case warpscout.TargetKernel, warpscout.TargetNative:
		if o.Svc == nil {
			return nil, errors.New("сервис туннелей недоступен")
		}
		backend := "kernel"
		if mode == warpscout.TargetNative {
			backend = "nativewg"
		}
		tw, err := o.Svc.Import(ctx, conf, name, backend, service.ImportLink{})
		if err != nil {
			return nil, err
		}
		if outerNDMS != "" {
			// Без привязки к внешнему туннелю это был бы обычный WARP под
			// именем каскадного, поэтому сбой привязки отменяет создание.
			if err := o.bindManagedVia(ctx, tw.ID, outerNDMS, outerLabel); err != nil {
				if derr := o.Svc.Delete(ctx, tw.ID); derr != nil {
					o.warn(name, "Туннель не привязан к внешнему и не удалён: "+derr.Error())
				}
				return nil, fmt.Errorf("настроить маршрут через %s: %w", outerNDMS, err)
			}
		}
		if err := o.Svc.Start(ctx, tw.ID); err != nil {
			// Туннель заведён — отказ старта не отменяет импорт.
			o.warn(name, "Туннель создан, но не запущен: "+err.Error())
		}
		if o.Tunnels != nil {
			o.Tunnels.publishTunnelList(ctx)
		}
		return o.Get(ctx, mode, tw.ID)
	case warpscout.TargetSystem:
		if o.Commands == nil || o.Commands.Wireguard == nil {
			return nil, errors.New("NDMS недоступен")
		}
		res, err := o.Commands.Wireguard.ImportWireguardConfig(ctx, []byte(conf), sanitizeConfName(name)+".conf")
		if err != nil {
			return nil, err
		}
		if o.Commands.Interfaces != nil && name != "" {
			if err := o.Commands.Interfaces.SetDescription(ctx, res.Created, name); err != nil {
				o.warn(name, "Туннель создан, но описание не задано: "+err.Error())
			}
		}
		if outerNDMS != "" {
			peerKey := extractPeerPublicKey(conf)
			if err := o.Commands.Wireguard.SetPeerConnectVia(ctx, res.Created, peerKey, outerNDMS); err != nil {
				// Интерфейс уже создан импортом; оставленный, он копился бы
				// на роутере с каждым повтором.
				if o.Commands.Interfaces == nil {
					o.warn(name, "Интерфейс "+res.Created.Name()+" не привязан к внешнему туннелю и не удалён")
				} else if derr := o.Commands.Interfaces.DeleteOpkgTun(ctx, res.Created); derr != nil {
					o.warn(name, "Интерфейс "+res.Created.Name()+" не привязан к внешнему туннелю и не удалён: "+derr.Error())
				}
				return nil, fmt.Errorf("настроить маршрут через %s: %w", outerNDMS, err)
			}
		}
		if o.Commands.Interfaces != nil {
			if err := o.Commands.Interfaces.InterfaceUp(ctx, res.Created); err != nil {
				o.warn(name, "Туннель создан, но не включён: "+err.Error())
			}
		}
		// Даём NDMS поднять интерфейс, чтобы ответ уже нёс его состояние.
		time.Sleep(500 * time.Millisecond)
		return o.Get(ctx, mode, res.Created.Name())
	}
	return nil, fmt.Errorf("неизвестный тип туннеля %q", mode)
}

// ObfuscationSources возвращает список существующих AWG-туннелей роутера,
// из которых можно скопировать Jc/Jmin/Jmax и I1..I5 для скана или нового WARP.
func (o *WarpTunnelOps) ObfuscationSources(ctx context.Context) []warpscout.ObfuscationSource {
	var out []warpscout.ObfuscationSource
	sanitizeAWG := func(jc, jmin, jmax int, i1, i2, i3, i4, i5 string) (warpscout.AWGParams, bool) {
		p := warpscout.AWGParams{Jc: jc, Jmin: jmin, Jmax: jmax}
		if p.Jc < 0 {
			p.Jc = 0
		}
		if p.Jc > 128 {
			p.Jc = 128
		}
		if p.Jmin < 0 {
			p.Jmin = 0
		}
		if p.Jmax < p.Jmin {
			p.Jmax = p.Jmin
		}
		if p.Jmax > 1280 {
			p.Jmax = 1280
		}
		var hasSig bool
		vals := []string{i1, i2, i3, i4, i5}
		dst := []*string{&p.I1, &p.I2, &p.I3, &p.I4, &p.I5}
		for idx, raw := range vals {
			s := strings.TrimSpace(raw)
			if s == "" {
				continue
			}
			if _, err := warpscout.CPSToRaw(s); err == nil {
				*dst[idx] = s
				hasSig = true
			}
		}
		if p.Jc == 0 && !hasSig {
			return p, false
		}
		if hasSig {
			p.SignatureProfile = warpscout.SignatureCustom
		} else {
			p.SignatureProfile = "none"
		}
		return p, true
	}

	if o.Store != nil {
		if list, err := o.Store.List(); err == nil {
			for i := range list {
				t := &list[i]
				if t.Obfuscator != nil || strings.TrimSpace(t.WdttClientID) != "" ||
					strings.TrimSpace(t.FreeTurnClientID) != "" {
					continue
				}
				awg, ok := sanitizeAWG(t.Interface.Jc, t.Interface.Jmin, t.Interface.Jmax,
					t.Interface.I1, t.Interface.I2, t.Interface.I3, t.Interface.I4, t.Interface.I5)
				if !ok {
					continue
				}
				mode, mok := modeOfBackend(t.Backend)
				if !mok {
					continue
				}
				iface := ""
				running := false
				if o.Svc != nil {
					if tw, err := o.Svc.Get(ctx, t.ID); err == nil && tw != nil {
						iface = tw.InterfaceName
						running = tw.State == tunnel.StateRunning
					}
				}
				out = append(out, warpscout.ObfuscationSource{
					ID:      t.ID,
					Name:    t.Name,
					Iface:   iface,
					Mode:    mode,
					Running: running,
					IsWarp:  strings.TrimSpace(t.Peer.PublicKey) == warpscout.WarpPeerPublicKey,
					AWG:     awg,
				})
			}
		}
	}

	if o.System != nil {
		if sys, err := o.System.List(ctx); err == nil {
			for i := range sys {
				s := &sys[i]
				if o.systemProtectReason(s) != "" {
					continue
				}
				raw, gerr := o.System.GetASCParams(ctx, s.ID)
				if gerr != nil || len(raw) == 0 {
					continue
				}
				var asc struct {
					Jc   int    `json:"jc"`
					Jmin int    `json:"jmin"`
					Jmax int    `json:"jmax"`
					I1   string `json:"i1"`
					I2   string `json:"i2"`
					I3   string `json:"i3"`
					I4   string `json:"i4"`
					I5   string `json:"i5"`
				}
				if json.Unmarshal(raw, &asc) != nil {
					continue
				}
				awg, ok := sanitizeAWG(asc.Jc, asc.Jmin, asc.Jmax, asc.I1, asc.I2, asc.I3, asc.I4, asc.I5)
				if !ok {
					continue
				}
				name := s.Description
				if name == "" {
					name = s.ID
				}
				out = append(out, warpscout.ObfuscationSource{
					ID:      s.ID,
					Name:    name,
					Iface:   s.InterfaceName,
					Mode:    warpscout.TargetSystem,
					Running: s.Status == "up",
					IsWarp:  s.Peer != nil && strings.TrimSpace(s.Peer.PublicKey) == warpscout.WarpPeerPublicKey,
					AWG:     awg,
				})
			}
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Running != out[j].Running {
			return out[i].Running
		}
		if out[i].IsWarp != out[j].IsWarp {
			return !out[i].IsWarp
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (o *WarpTunnelOps) warn(target, msg string) {
	if o.Tunnels != nil {
		o.Tunnels.log.Warn("warpscout", target, msg)
	}
}

func sanitizeConfName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 128 && (r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "warp"
	}
	return b.String()
}

// SwapEndpoint — горячая подмена endpoint без пересоздания интерфейса.
func (o *WarpTunnelOps) SwapEndpoint(ctx context.Context, mode warpscout.TargetMode, id, endpoint string) error {
	ref, err := o.Get(ctx, mode, id)
	if err != nil {
		return err
	}
	if ref.Protected {
		return fmt.Errorf("%w: %s", warpscout.ErrProtected, ref.ProtectReason)
	}
	if !ref.IsWarp {
		return warpscout.ErrNotWarp
	}
	switch mode {
	case warpscout.TargetKernel, warpscout.TargetNative:
		return o.swapManaged(ctx, id, endpoint)
	case warpscout.TargetSystem:
		if o.Commands == nil || o.Commands.Wireguard == nil || o.Queries == nil || o.Queries.Interfaces == nil {
			return errors.New("NDMS недоступен")
		}
		iface, _, present, err := o.Queries.Interfaces.Confirm(ctx, id)
		if err != nil {
			return err
		}
		if !present {
			return fmt.Errorf("интерфейс %s не найден в NDMS", id)
		}
		return o.Commands.Wireguard.SetPeerEndpoint(ctx, iface, ref.PeerPublicKey, endpoint)
	}
	return fmt.Errorf("неизвестный тип туннеля %q", mode)
}

// Rename переименовывает WARP-туннель (карточку и описание интерфейса в NDMS)
// без пересоздания или рестарта живого соединения.
func (o *WarpTunnelOps) Rename(ctx context.Context, mode warpscout.TargetMode, id, name string) error {
	name = strings.TrimSpace(name)
	if err := tunnel.ValidateName(name); err != nil {
		return err
	}
	ref, err := o.Get(ctx, mode, id)
	if err != nil {
		return err
	}
	if ref.Protected {
		return fmt.Errorf("%w: %s", warpscout.ErrProtected, ref.ProtectReason)
	}
	if !ref.IsWarp {
		return warpscout.ErrNotWarp
	}
	if ref.Name == name {
		return nil
	}
	switch mode {
	case warpscout.TargetKernel, warpscout.TargetNative:
		if o.Tunnels == nil {
			return errors.New("сервис туннелей недоступен")
		}
		_, err := o.Tunnels.EditTunnelWith(ctx, id, func(cur storage.AWGTunnel) (storage.AWGTunnel, error) {
			if cur.Name == name {
				return storage.AWGTunnel{}, nil
			}
			return storage.AWGTunnel{Name: name}, nil
		})
		return err
	case warpscout.TargetSystem:
		if o.Commands == nil || o.Commands.Interfaces == nil || o.Queries == nil || o.Queries.Interfaces == nil {
			return errors.New("NDMS недоступен")
		}
		iface, _, present, err := o.Queries.Interfaces.Confirm(ctx, id)
		if err != nil {
			return err
		}
		if !present {
			return fmt.Errorf("интерфейс %s не найден в NDMS", id)
		}
		if err := o.Commands.Interfaces.SetDescription(ctx, iface, name); err != nil {
			return err
		}
		if o.Tunnels != nil {
			o.Tunnels.publishTunnelList(ctx)
		}
		return nil
	}
	return fmt.Errorf("неизвестный тип туннеля %q", mode)
}

// swapManaged — штатная правка карточки (TunnelsHandler.EditTunnelWith) с
// одним изменённым полем: она проверяет замок и endpoint, применяет diff к
// живому интерфейсу под замком оркестратора, пишет запись транзакцией стора
// и публикует список.
//
// Две тонкости живого переключения под ТСПУ:
//  1. `awg setconf` не трогает `listen-port`, если поле опущено в .conf, и
//     kernel-туннель продолжает слать рукопожатия со старого UDP-порта. Если
//     ТСПУ заблокировал поток по `(src_ip, sport)`, все новые endpoint'ы будут
//     молча терять пакеты, пока не сменится исходный порт (`listen-port 0`).
//  2. Kernel-туннель без линка (`StateStarting` / `StateBroken`) правка в
//     живой интерфейс не синхронизирует (`shouldSyncRuntime`: только Running),
//     поэтому такой включённый туннель перезапускается. NativeWG в этих
//     состояниях синхронизируется самой правкой — перезапуск ему не нужен.
func (o *WarpTunnelOps) swapManaged(ctx context.Context, id, endpoint string) error {
	if o.Svc == nil || o.Store == nil || o.Tunnels == nil {
		return errors.New("сервис туннелей недоступен")
	}
	existing, err := o.Store.Get(id)
	if err != nil {
		return fmt.Errorf("туннель %s не найден", id)
	}
	tw, _ := o.Svc.Get(ctx, id)

	if existing.Peer.Endpoint != endpoint {
		if _, err := o.Tunnels.EditTunnelWith(ctx, id, func(cur storage.AWGTunnel) (storage.AWGTunnel, error) {
			peer := cur.Peer
			peer.Endpoint = endpoint
			return storage.AWGTunnel{Peer: peer}, nil
		}); err != nil {
			return err
		}
	}

	if existing.Backend == "nativewg" || tw == nil {
		return nil
	}
	switch tw.State {
	case tunnel.StateStarting, tunnel.StateBroken:
		// Рестарт безусловно включает туннель: если его успели остановить
		// руками, пока шла правка, — не поднимаем обратно.
		if cur, err := o.Store.Get(id); err != nil || !cur.Enabled {
			return nil
		}
		if err := o.Svc.Restart(ctx, id); err != nil {
			return fmt.Errorf("перезапуск зависшего туннеля: %w", err)
		}
		o.Tunnels.publishTunnelList(ctx)
	case tunnel.StateRunning:
		iface := tw.InterfaceName
		if iface == "" {
			iface = tunnel.NewNames(id).IfaceName
		}
		o.rotateKernelPort(ctx, iface)
	}
	return nil
}

func (o *WarpTunnelOps) rotateKernelPort(ctx context.Context, iface string) {
	if iface == "" {
		return
	}
	fn := o.RotateKernelPort
	if fn == nil {
		fn = defaultRotateKernelPort
	}
	if err := fn(ctx, iface); err != nil && o.Tunnels != nil {
		o.Tunnels.log.Warn("warpscout", iface, "Не удалось сменить UDP-порт интерфейса: "+err.Error())
	}
}

func defaultRotateKernelPort(ctx context.Context, iface string) error {
	res, err := exec.Run(ctx, "/opt/sbin/awg", "set", iface, "listen-port", "0")
	if err != nil {
		return exec.FormatError(res, err)
	}
	return nil
}

// WarpHTTPClientFactory — клиент Cloudflare API с прямым выходом через WAN.
// Короткий таймаут рукопожатия позволяет быстро перейти на резервный выход
// (прокси/туннель), если провайдер блокирует домен регистрации на сыром WAN.
func WarpHTTPClientFactory(wanIface string) (*http.Client, error) {
	tr, err := httpclient.NewTransport(httpclient.TransportConfig{Interface: wanIface, Proxy: httpclient.ProxyDirect})
	if err != nil {
		return nil, err
	}
	tr.TLSHandshakeTimeout = 6 * time.Second
	tr.ResponseHeaderTimeout = 10 * time.Second
	return &http.Client{Transport: tr, Timeout: 12 * time.Second}, nil
}

// WarpFallbackClients собирает резервные HTTP-клиенты для регистрации WARP и
// привязки WARP+ на случай блокировки API Cloudflare на прямом WAN:
//  1. настроенный маршрут загрузок (если выбран не direct);
//  2. локальный «Прокси для устройств» (deviceproxy, включая авторизацию инстанса);
//  3. до двух запущенных незащищённых не-WARP VPN-туннелей.
func WarpFallbackClients(ctx context.Context, dp *deviceproxy.Service, ops *WarpTunnelOps, dl *downloader.Service) []*http.Client {
	var out []*http.Client

	if dl != nil {
		if lease, err := dl.ResolveClient(ctx, nil); err == nil && lease != nil && lease.Client != nil {
			if tag := strings.TrimSpace(lease.Route.Tag); tag != "" && tag != "direct" {
				out = append(out, lease.Client)
			} else {
				lease.Close()
			}
		}
	}

	if dp != nil {
		for _, inst := range dp.GetSnapshot().Instances {
			if !inst.Enabled || inst.Port <= 0 || inst.Port > 65535 || strings.EqualFold(strings.TrimSpace(inst.SelectedOutbound), "direct") {
				continue
			}
			addr := findListeningLocalProxy(inst.Port)
			if addr == "" {
				continue
			}
			u := &url.URL{Scheme: "http", Host: addr}
			if inst.Auth.Enabled && inst.Auth.Username != "" {
				u.User = url.UserPassword(inst.Auth.Username, inst.Auth.Password)
			}
			if tr, err := httpclient.NewTransport(httpclient.TransportConfig{ProxyURL: u.String()}); err == nil {
				tr.TLSHandshakeTimeout = 8 * time.Second
				tr.ResponseHeaderTimeout = 12 * time.Second
				out = append(out, &http.Client{Transport: tr, Timeout: 15 * time.Second})
				break
			}
		}
	}

	if ops != nil {
		if list, err := ops.List(ctx); err == nil {
			added := 0
			for _, ref := range list {
				if !ref.Running || ref.Protected || ref.IsWarp || ref.Iface == "" {
					continue
				}
				tr, err := httpclient.NewTransport(httpclient.TransportConfig{Interface: ref.Iface, Proxy: httpclient.ProxyDirect})
				if err != nil {
					continue
				}
				tr.TLSHandshakeTimeout = 6 * time.Second
				tr.ResponseHeaderTimeout = 10 * time.Second
				out = append(out, &http.Client{Transport: tr, Timeout: 12 * time.Second})
				added++
				if added >= 2 {
					break
				}
			}
		}
	}

	return out
}

func findListeningLocalProxy(port int) string {
	p := strconv.Itoa(port)
	candidates := []string{"127.0.0.1:" + p}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP == nil || ipNet.IP.IsLoopback() {
				continue
			}
			if ip4 := ipNet.IP.To4(); ip4 != nil && ip4.IsPrivate() {
				candidates = append(candidates, net.JoinHostPort(ip4.String(), p))
			}
		}
	}
	for _, addr := range candidates {
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return addr
		}
	}
	return ""
}

// ── HTTP-ручки ──────────────────────────────────────────────────

// WarpScoutHandler — REST модуля «WARP Сканер» (раздел «Инструменты»).
// Доступен на всех уровнях интерфейса.
type WarpScoutHandler struct {
	svc *warpscout.Service
	log *logging.ScopedLogger
}

// NewWarpScoutHandler создаёт хендлер.
func NewWarpScoutHandler(svc *warpscout.Service, appLog logging.AppLogger) *WarpScoutHandler {
	return &WarpScoutHandler{svc: svc, log: logging.NewScopedLogger(appLog, logging.GroupTunnel, logging.SubWarpScout)}
}

func (h *WarpScoutHandler) fail(w http.ResponseWriter, err error) {
	var editErr *TunnelEditError
	switch {
	case errors.Is(err, warpscout.ErrNoAccount):
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "WARP_NO_ACCOUNT")
	case errors.Is(err, warpscout.ErrScanRunning), errors.Is(err, warpscout.ErrEngineBusy):
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "WARP_BUSY")
	case errors.Is(err, warpscout.ErrEngineMissing):
		// 409, а не 503: фронт на 503 оставляет только текст и теряет код.
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "WARP_ENGINE_MISSING")
	case errors.Is(err, warpscout.ErrProtected):
		response.ErrorWithStatus(w, http.StatusForbidden, err.Error(), "WARP_PROTECTED")
	case errors.Is(err, warpscout.ErrNotWarp):
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "WARP_NOT_WARP")
	case errors.Is(err, warpscout.ErrInvalidInput):
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "INVALID_INPUT")
	case errors.Is(err, tunnel.ErrOperationInProgress):
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "OPERATION_IN_PROGRESS")
	case errors.As(err, &editErr):
		// Отказ штатной правки туннеля (подмена endpoint) — её статус и код.
		response.ErrorWithStatus(w, editErr.Status, editErr.Message, editErr.Code)
	default:
		response.Error(w, err.Error(), "WARP_FAILED")
	}
}

// State — снимок модуля.
//
//	@Summary	WARP scanner state
//	@Tags		warpscout
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/state [get]
func (h *WarpScoutHandler) State(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	response.Success(w, h.svc.State())
}

// Targets — туннели для выбора цели автопилота.
//
//	@Summary	WARP autopilot targets
//	@Tags		warpscout
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/targets [get]
func (h *WarpScoutHandler) Targets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	list, err := h.svc.Targets(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	response.Success(w, list)
}

// Register — регистрация учётной записи WARP.
//
//	@Summary	Register Cloudflare WARP account
//	@Tags		warpscout
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/account/register [post]
func (h *WarpScoutHandler) Register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	acct, err := h.svc.Register(r.Context())
	if err != nil {
		h.log.Warn("register", "warp", err.Error())
		h.fail(w, err)
		return
	}
	response.Success(w, acct)
}

type warpLicenseRequest struct {
	License string `json:"license"`
}

// License — привязка ключа WARP+.
//
//	@Summary	Apply WARP+ license
//	@Tags		warpscout
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/account/license [post]
func (h *WarpScoutHandler) License(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[warpLicenseRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	acct, err := h.svc.SetLicense(r.Context(), req.License)
	if err != nil {
		h.fail(w, err)
		return
	}
	response.Success(w, acct)
}

type warpAddressRequest struct {
	AddressV4 string `json:"addressV4"`
}

// Address — задать пользовательский локальный IPv4 туннеля до создания туннеля.
//
//	@Summary	Set WARP account tunnel IPv4
//	@Tags		warpscout
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/account/address [post]
func (h *WarpScoutHandler) Address(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[warpAddressRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	acct, err := h.svc.SetAccountAddress(req.AddressV4)
	if err != nil {
		h.fail(w, err)
		return
	}
	response.Success(w, acct)
}

// DeleteAccount — забыть учётную запись.
//
//	@Summary	Delete WARP account
//	@Tags		warpscout
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/account/delete [post]
func (h *WarpScoutHandler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if err := h.svc.DeleteAccountLocal(r.Context()); err != nil {
		h.fail(w, err)
		return
	}
	response.Success(w, map[string]bool{"ok": true})
}

// ScanStart — запуск ручного скана.
//
//	@Summary	Start WARP endpoint scan
//	@Tags		warpscout
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/scan/start [post]
func (h *WarpScoutHandler) ScanStart(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[warpscout.ScanRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	if err := h.svc.StartScan(req); err != nil {
		h.fail(w, err)
		return
	}
	response.Success(w, h.svc.ScanSnapshot())
}

// ScanStop — остановка ручного скана.
//
//	@Summary	Stop WARP endpoint scan
//	@Tags		warpscout
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/scan/stop [post]
func (h *WarpScoutHandler) ScanStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	h.svc.StopScan()
	response.Success(w, map[string]bool{"ok": true})
}

// Apply — создать туннель из узла или подменить endpoint.
//
//	@Summary	Apply WARP endpoint
//	@Tags		warpscout
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/apply [post]
func (h *WarpScoutHandler) Apply(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[warpscout.ApplyRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	res, err := h.svc.Apply(r.Context(), req)
	if err != nil {
		h.log.Warn("apply", req.Endpoint, err.Error())
		h.fail(w, err)
		return
	}
	response.Success(w, res)
}

// Config — сохранить профили автопилота и параметры AWG.
//
//	@Summary	Save WARP autopilot config
//	@Tags		warpscout
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/config [post]
func (h *WarpScoutHandler) Config(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[warpscout.Config](w, r, http.MethodPost)
	if !ok {
		return
	}
	cfg, err := h.svc.SaveConfig(r.Context(), req)
	if err != nil {
		h.fail(w, err)
		return
	}
	response.Success(w, cfg)
}

type warpResetStatsRequest struct {
	ProfileID string `json:"profileId"`
}

// ResetStats — сбросить накопленную статистику профиля автопилота (или всех профилей).
//
//	@Summary	Reset WARP autopilot stats
//	@Tags		warpscout
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Success	200	{object}	map[string]interface{}
//	@Router		/warpscout/stats/reset [post]
func (h *WarpScoutHandler) ResetStats(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[warpResetStatsRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	if err := h.svc.ResetStats(req.ProfileID); err != nil {
		h.fail(w, err)
		return
	}
	response.Success(w, map[string]bool{"ok": true})
}
