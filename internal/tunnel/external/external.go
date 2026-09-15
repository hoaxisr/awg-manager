// Package external provides functionality for working with external (unmanaged) tunnels.
// External tunnels are AWG interfaces that exist in the system but are not managed by awg-manager.
package external

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/opkgtun"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/sysinfo"
)

// TunnelInfo contains information about an external tunnel.
type TunnelInfo = sysinfo.ExternalTunnelInfo

// Service provides operations for external tunnels.
type Service struct {
	store         *storage.AWGTunnelStore
	settingsStore *storage.SettingsStore
	tunnelService service.Service
	appLog        *logging.ScopedLogger

	// orphans — интерфейсы OpkgTun без владельца, от аллокатора номеров.
	// descriptions — описания записей NDMS по номеру. Обе nil-безопасны:
	// без них список остаётся прежним, просто беднее.
	orphans      func(ctx context.Context) ([]OrphanIface, error)
	descriptions func(ctx context.Context) map[int]string
}

// OrphanIface — интерфейс OpkgTun, за которым не стоит ни одной записи панели.
type OrphanIface struct {
	Iface string
	// NDMSName — ИМЯ ЗАПИСИ, как его отдал роутер, а не собранное из номера.
	// Снос ходит по нему: собирать имя заново значит завести второй разборщик
	// рядом с тем, которым занятость считал пул, и на записи, которую один
	// принимает, а другой нет, снос ушёл бы в никуда и отчитался успехом.
	// Пусто — записи нет вовсе, снимать надо только устройство.
	NDMSName    string
	Description string
	Addrs       []string
	// NDMSRecord / KernelDevice — из каких половин интерфейс состоит. Обе
	// держат номер пула и снимаются независимо, и пользователю в строке важно
	// именно это: «только устройство» — это интерфейс, поднятый мимо NDMS.
	NDMSRecord   bool
	KernelDevice bool
}

// SetOrphanSource подключает поставщиков сирот и описаний. Отдельным сеттером,
// а не аргументом конструктора: аллокатор номеров собирается позже сервиса.
func (s *Service) SetOrphanSource(
	orphans func(ctx context.Context) ([]OrphanIface, error),
	descriptions func(ctx context.Context) map[int]string,
) {
	s.orphans = orphans
	s.descriptions = descriptions
}

// NewService creates a new external tunnel service.
func NewService(
	store *storage.AWGTunnelStore,
	settingsStore *storage.SettingsStore,
	tunnelSvc service.Service,
	appLogger logging.AppLogger,
) *Service {
	return &Service{
		store:         store,
		settingsStore: settingsStore,
		tunnelService: tunnelSvc,
		appLog:        logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubConnectivity),
	}
}

// Швы над sysinfo: без них List непроверяем — он читает интерфейсы МАШИНЫ, на
// которой запущен, и подставить ему расклад роутера нечем.
var (
	listSystemInterfaces = sysinfo.ListSystemInterfaces
	isAWGInterface       = sysinfo.IsAWGInterface
)

// List returns tunnels that exist in the system but are not managed by awg-manager.
func (s *Service) List(ctx context.Context) ([]TunnelInfo, error) {
	// Get all system interfaces
	systemNumbers, err := listSystemInterfaces()
	if err != nil {
		s.appLog.Warn("list", "", "Failed to list system interfaces: "+err.Error())
		systemNumbers = []int{}
	}

	// Get managed tunnel numbers
	managedTunnels, err := s.store.List()
	if err != nil {
		return nil, fmt.Errorf("list managed tunnels: %w", err)
	}

	managed := make(map[int]bool)
	for _, t := range managedTunnels {
		numStr := tunnel.NewNames(t.ID).TunnelNum
		if num, err := strconv.Atoi(numStr); err == nil {
			managed[num] = true
		}
	}

	// Find external tunnels (in system but not managed).
	// Deduplicate by number: opkgtunX and awgX both produce the same number,
	// so without dedup the same interface would appear twice.
	//
	// Карт ДВЕ, и это не дублирование. scanned — дедуп ВНУТРИ перебора,
	// listed — что реально попало в список. Раньше роль была одна, и на ней
	// функция ломалась целиком: перебор помечал каждый просмотренный номер, а
	// дописывание сирот такие номера пропускает. Живой поставщик занятости
	// читает ТУ ЖЕ ListSystemInterfaces, поэтому у сироты с устройством номер
	// всегда оказывался помечен — и до списка доезжали только сироты без
	// устройства, то есть ровно обратный случай.
	scanned := make(map[int]bool)
	listed := make(map[int]bool)
	var external []TunnelInfo
	for _, num := range systemNumbers {
		if managed[num] || scanned[num] {
			continue
		}
		scanned[num] = true

		// Get interface name
		names := tunnel.NewNames(fmt.Sprintf("awg%d", num))
		ifaceName := names.IfaceName

		// Check if it's an AWG interface
		info, isAWG := isAWGInterface(ctx, ifaceName)
		if isAWG && info != nil {
			external = append(external, *info)
			listed[num] = true
		}
	}

	external = s.withOrphans(ctx, external, listed)
	s.annotate(ctx, external, managedTunnels)
	return external, nil
}

// appendOrphans дописывает интерфейсы OpkgTun, которые не принадлежат НИ ОДНОЙ
// записи панели и при этом не являются AWG-туннелями, — поэтому предыдущий
// проход их не увидел.
//
// Почему в этом же списке, а не отдельным экраном: состояние у них одно и то же
// — «интерфейс на роутере, которым панель не владеет». Пока трактовки жили в
// двух местах, один и тот же интерфейс панель предлагала и принять, и удалить
// (стенд 15.09, opkgtun13). Список должен быть один, а различаться должны
// ДЕЙСТВИЯ в строке: принять можно только то, поверх чего есть живой туннель.
//
// Источник сиротства — аллокатор номеров, а не собственная проверка: он один
// знает про записи прокси и режимы роутера, которых перебор выше не видит.
func (s *Service) withOrphans(ctx context.Context, external []TunnelInfo, listed map[int]bool) []TunnelInfo {
	if s.orphans == nil {
		return external
	}
	orphans, err := s.orphans(ctx)
	if err != nil {
		// Не отказ всего списка: внешние туннели уже собраны и полезны сами по
		// себе. Недосчёт сирот молчанием хуже не делает — он лишь не покажет
		// строку, а показанное остаётся верным.
		s.appLog.Warn("list", "", "Не удалось собрать осиротевшие интерфейсы: "+err.Error())
		return external
	}

	// Сироты приходят ДВАЖДЫ: сперва помечают уже собранные строки, потом
	// дописывают недостающие. Пометка обязательна — «можно удалить» решает
	// аллокатор, а не факт присутствия строки в списке: строка, чей номер
	// держит владелец, которого стор туннелей не знает (половина прокси, режим
	// роутера), — это не сирота, и кнопка на ней врала бы (сервер ответит
	// отказом).
	byNum := make(map[int]OrphanIface, len(orphans))
	for _, o := range orphans {
		if num, ok := opkgtun.IndexOf(o.Iface); ok {
			byNum[num] = o
		}
	}
	for i := range external {
		o, isOrphan := byNum[external[i].TunnelNumber]
		if !isOrphan {
			continue
		}
		external[i].Removable = true
		external[i].NDMSRecord = o.NDMSRecord
		external[i].KernelDevice = o.KernelDevice
		if external[i].Description == "" {
			external[i].Description = o.Description
		}
		if len(external[i].Addresses) == 0 {
			external[i].Addresses = o.Addrs
		}
	}

	for _, o := range orphans {
		num, ok := opkgtun.IndexOf(o.Iface)
		if !ok || listed[num] {
			continue
		}
		listed[num] = true
		external = append(external, TunnelInfo{
			Removable:     true,
			InterfaceName: o.Iface,
			TunnelNumber:  num,
			IsAWG:         false,
			Description:   o.Description,
			Addresses:     o.Addrs,
			NDMSRecord:    o.NDMSRecord,
			KernelDevice:  o.KernelDevice,
		})
	}
	return external
}

// annotate дополняет строки тем, что нужно человеку для решения: описанием из
// NDMS и именем туннеля, чей адрес совпал.
//
// Совпадение считается по ВСЕМ записям туннелей, включая остановленные: адрес
// записан за туннелем и тогда, когда он выключен, а конфликт выстрелит ровно в
// момент, когда оба окажутся подняты. Проверка перед стартом туннеля смотрит на
// то же самое (orchestrator.checkSystemAddressConflict).
func (s *Service) annotate(ctx context.Context, external []TunnelInfo, managed []storage.AWGTunnel) {
	if len(external) == 0 {
		return
	}
	owner := make(map[string]string, len(managed))
	for _, t := range managed {
		if addr := addrOnly(t.Interface.Address); addr != "" {
			owner[addr] = t.Name
		}
	}
	var descr map[int]string
	if s.descriptions != nil {
		descr = s.descriptions(ctx)
	}
	for i := range external {
		e := &external[i]
		if e.Description == "" {
			e.Description = descr[e.TunnelNumber]
		}
		if len(e.Addresses) == 0 {
			e.Addresses = ifaceAddresses(e.InterfaceName)
		}
		for _, a := range e.Addresses {
			if name, dup := owner[a]; dup {
				e.ConflictsWith = name
				break
			}
		}
	}
}

// addrOnly отбрасывает маску: адреса туннелей хранятся как "10.8.1.3/32".
func addrOnly(addr string) string {
	return strings.TrimSpace(strings.SplitN(addr, "/", 2)[0])
}

// ifaceAddresses — адреса устройства, если оно есть в ядре.
var ifaceAddresses = func(name string) []string {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok {
			out = append(out, ipNet.IP.String())
		}
	}
	return out
}

// AdoptRequest contains parameters for adopting an external tunnel.
type AdoptRequest struct {
	InterfaceName string // Interface name (e.g., "opkgtun0" or "awg0")
	ConfContent   string // WireGuard .conf file content
	TunnelName    string // Optional name for the tunnel
}

// Adopt takes control of an external tunnel.
// The tunnel must be stopped (no peer in awg show) before adoption.
func (s *Service) Adopt(ctx context.Context, req AdoptRequest) (*service.TunnelWithStatus, error) {
	// Extract tunnel number from interface name
	tunnelNum, ok := sysinfo.ExtractInterfaceNumber(req.InterfaceName)
	if !ok {
		return nil, fmt.Errorf("invalid interface name: %s", req.InterfaceName)
	}

	// Check if tunnel is still running
	info, isAWG := sysinfo.IsAWGInterface(ctx, req.InterfaceName)
	if isAWG && info != nil && info.PublicKey != "" {
		return nil, fmt.Errorf("tunnel is still active - stop it in the external application and try again")
	}

	// Check if this number is already managed
	tunnelID := fmt.Sprintf("awg%d", tunnelNum)
	if s.store.Exists(tunnelID) {
		return nil, fmt.Errorf("tunnel %s is already managed", tunnelID)
	}

	// Parse the config
	t, err := config.Parse(req.ConfContent)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Set the specific ID to match the external interface
	t.ID = tunnelID
	if req.TunnelName != "" {
		t.Name = req.TunnelName
	}
	if t.Name == "" {
		t.Name = fmt.Sprintf("Imported %s", req.InterfaceName)
	}

	// Set defaults
	t.Type = "awg"
	t.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	t.Enabled = true

	// Validate
	if t.Interface.PrivateKey == "" {
		return nil, fmt.Errorf("missing privateKey")
	}
	if t.Peer.PublicKey == "" {
		return nil, fmt.Errorf("missing publicKey")
	}
	if t.Peer.Endpoint == "" {
		return nil, fmt.Errorf("missing endpoint")
	}

	// Initialize PingCheck config if globally enabled
	if t.PingCheck == nil && s.settingsStore != nil {
		settings, err := s.settingsStore.Get()
		if err == nil && settings.PingCheck.Enabled {
			defaults := settings.PingCheck.Defaults
			t.PingCheck = &storage.TunnelPingCheck{
				Enabled:       true,
				Method:        defaults.Method,
				Target:        defaults.Target,
				Interval:      defaults.Interval,
				DeadInterval:  defaults.DeadInterval,
				FailThreshold: defaults.FailThreshold,
				MinSuccess:    1,
				Timeout:       5,
				Restart:       true,
			}
		}
	}

	// Save to storage
	if err := s.store.Create(t); err != nil {
		return nil, fmt.Errorf("save tunnel: %w", err)
	}

	s.appLog.Info("adopt", t.ID, "Adopted external tunnel: "+t.Name)

	// Start the tunnel under awg-manager control
	if err := s.tunnelService.Start(ctx, t.ID); err != nil {
		s.appLog.Warn("adopt", t.ID, "Failed to start: "+err.Error())
		// Don't fail - tunnel is imported, just not started
	}

	return s.tunnelService.Get(ctx, t.ID)
}
