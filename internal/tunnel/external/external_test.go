package external

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/sysinfo"
)

// validAdoptConf — минимальный .conf, который config.Parse принимает без
// ошибок (см. internal/tunnel/config/config_test.go). Без него Adopt падал бы
// на разборе конфига и до новых проверок не доходил бы — тест был бы холостым.
const validAdoptConf = `[Interface]
PrivateKey = aPrivateKey123=
Address = 10.0.0.2/32

[Peer]
PublicKey = aPublicKey456=
Endpoint = vpn.example.com:51820
AllowedIPs = 0.0.0.0/0, ::/0
`

func withSettings(t *testing.T, s *Service) *storage.SettingsStore {
	t.Helper()
	st := storage.NewSettingsStore(t.TempDir())
	if _, err := st.Load(); err != nil {
		t.Fatal(err)
	}
	s.settingsStore = st
	return st
}

func TestList_ForeignOpkgTunShownNotRemovable(t *testing.T) {
	stubScan(t, []int{7}, nil) // номер 7 есть в системе, AWG на нём нет
	s := newTestService(t)
	st := withSettings(t, s)
	_ = st.MarkForeignInterface("opkgtun7")
	_ = st.MarkForeignInterface("csqtt0")
	s.SetOrphanSource(
		func(context.Context) ([]OrphanIface, error) { return nil, nil }, // отмеченный уже не сирота
		func(context.Context) map[int]string { return map[int]string{7: "csqtt"} },
	)
	got, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]TunnelInfo{}
	for _, g := range got {
		by[g.InterfaceName] = g
	}
	o, ok := by["opkgtun7"]
	if !ok || !o.Foreign || o.Removable || o.Description != "csqtt" || o.TunnelNumber != 7 {
		t.Fatalf("opkgtun7 = %+v (ok=%v)", o, ok)
	}
	k, ok := by["csqtt0"]
	if !ok || !k.Foreign || k.Removable || k.TunnelNumber != -1 {
		t.Fatalf("csqtt0 = %+v (ok=%v)", k, ok)
	}
}

// Отмеченный OpkgTun, который источник сирот ещё отдаёт (кэш с TTL), уже
// дописан withOrphans с Removable=true — withForeign обязан снять кнопку.
func TestList_ForeignOverridesOrphanRemovable(t *testing.T) {
	stubScan(t, []int{7}, nil)
	s := newTestService(t)
	_ = withSettings(t, s).MarkForeignInterface("opkgtun7")
	s.SetOrphanSource(
		func(context.Context) ([]OrphanIface, error) {
			return []OrphanIface{{Iface: "opkgtun7", KernelDevice: true}}, nil
		},
		nil,
	)
	got, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].InterfaceName != "opkgtun7" || !got[0].Foreign || got[0].Removable {
		t.Fatalf("список = %+v, ждали одну opkgtun7: Foreign, не Removable", got)
	}
}

func stubLinkExists(t *testing.T, v bool) {
	t.Helper()
	prev := linkExists
	linkExists = func(string) bool { return v }
	t.Cleanup(func() { linkExists = prev })
}

func TestAdopt_RefusesLiveNonAWG(t *testing.T) {
	stubScan(t, nil, nil) // isAWGInterface → не AWG
	stubLinkExists(t, true)
	s := newTestService(t)
	withSettings(t, s)
	_, err := s.Adopt(context.Background(), AdoptRequest{InterfaceName: "opkgtun7", ConfContent: validAdoptConf})
	if !errors.Is(err, ErrAdoptNotAWG) {
		t.Fatalf("err = %v, want ErrAdoptNotAWG", err)
	}
}

func TestAdopt_RefusesForeign(t *testing.T) {
	stubScan(t, nil, nil)
	stubLinkExists(t, false)
	s := newTestService(t)
	withSettings(t, s).MarkForeignInterface("opkgtun8")
	_, err := s.Adopt(context.Background(), AdoptRequest{InterfaceName: "opkgtun8", ConfContent: validAdoptConf})
	if !errors.Is(err, ErrAdoptForeign) {
		t.Fatalf("err = %v, want ErrAdoptForeign", err)
	}
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	return &Service{
		store:  storage.NewAWGTunnelStoreWithLockDir(dir, dir),
		appLog: logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubConnectivity),
	}
}

// stubScan подменяет оба шва над sysinfo: список номеров в системе и ответ
// «это AWG-интерфейс».
func stubScan(t *testing.T, nums []int, awg map[string]*sysinfo.ExternalTunnelInfo) {
	t.Helper()
	prevList, prevAWG := listSystemInterfaces, isAWGInterface
	listSystemInterfaces = func() ([]int, error) { return nums, nil }
	isAWGInterface = func(_ context.Context, iface string) (*sysinfo.ExternalTunnelInfo, bool) {
		info, ok := awg[iface]
		return info, ok
	}
	t.Cleanup(func() { listSystemInterfaces, isAWGInterface = prevList, prevAWG })
}

// РЕГРЕСС. Сирота с устройством в ядре — главный случай всей функции, и она
// выпадала целиком: перебор системных номеров помечал КАЖДЫЙ просмотренный
// номер как «видели», а дописывание сирот такие номера пропускает. Живой
// поставщик занятости читает ТУ ЖЕ ListSystemInterfaces, поэтому у сироты с
// устройством номер всегда оказывался помечен. В списке оставались только
// сироты без устройства — ровно обратный случай.
func TestList_OrphanWithKernelDeviceReachesTheList(t *testing.T) {
	stubScan(t, []int{11}, nil) // 11 есть в системе, но AWG на нём нет
	s := newTestService(t)
	s.SetOrphanSource(
		func(context.Context) ([]OrphanIface, error) {
			return []OrphanIface{{
				Iface:        "opkgtun11",
				Addrs:        []string{"10.8.1.4"},
				KernelDevice: true,
			}}, nil
		},
		nil,
	)

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("список = %+v, ждали opkgtun11", got)
	}
	if got[0].InterfaceName != "opkgtun11" || !got[0].KernelDevice {
		t.Fatalf("строка = %+v, ждали opkgtun11 с устройством в ядре", got[0])
	}
}

// Дедупликация внутри перебора обязана остаться: opkgtunX и awgX дают один
// номер, и без неё один интерфейс приехал бы дважды.
func TestList_DeduplicatesSameNumberWithinScan(t *testing.T) {
	awg := map[string]*sysinfo.ExternalTunnelInfo{
		"opkgtun9": {InterfaceName: "opkgtun9", TunnelNumber: 9, IsAWG: true},
	}
	stubScan(t, []int{9, 9}, awg)
	s := newTestService(t)

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("список = %+v, ждали одну строку", got)
	}
}

// Сирота не должна дублировать строку, которую уже дал перебор как AWG.
func TestList_OrphanDoesNotDuplicateAdoptableRow(t *testing.T) {
	awg := map[string]*sysinfo.ExternalTunnelInfo{
		"opkgtun13": {InterfaceName: "opkgtun13", TunnelNumber: 13, IsAWG: true},
	}
	stubScan(t, []int{13}, awg)
	s := newTestService(t)
	s.SetOrphanSource(
		func(context.Context) ([]OrphanIface, error) {
			return []OrphanIface{{Iface: "opkgtun13", KernelDevice: true}}, nil
		},
		nil,
	)

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("список = %+v, ждали одну строку", got)
	}
	if !got[0].IsAWG {
		t.Errorf("строка потеряла признак AWG: %+v", got[0])
	}
}

// Совпадение адреса с адресом записанного туннеля — то, ради чего строка
// красится предупреждением.
func TestAnnotate_MarksAddressCollisionWithManagedTunnel(t *testing.T) {
	s := newTestService(t)
	list := []TunnelInfo{{
		InterfaceName: "opkgtun11",
		TunnelNumber:  11,
		Addresses:     []string{"10.8.1.3"},
	}}
	managed := []storage.AWGTunnel{
		{Name: "Germany_AWG_3.1", Interface: storage.AWGInterface{Address: "10.8.1.3/32"}},
	}

	s.annotate(context.Background(), list, managed)

	if list[0].ConflictsWith != "Germany_AWG_3.1" {
		t.Fatalf("ConflictsWith = %q, ждали имя туннеля", list[0].ConflictsWith)
	}
}

// Взятие стороннего туннеля — тоже создание записи: имя упирается в тот же
// предел описания NDMS, что и у импорта (256 байт).
func TestAdopt_RefusesNameOverByteLimit(t *testing.T) {
	stubScan(t, nil, nil)
	stubLinkExists(t, false)
	s := newTestService(t)
	_, err := s.Adopt(context.Background(), AdoptRequest{InterfaceName: "opkgtun9", ConfContent: validAdoptConf,
		TunnelName: strings.Repeat("ж", 128) + "a"})
	if !errors.Is(err, tunnel.ErrNameTooLong) {
		t.Fatalf("err = %v, want ErrNameTooLong", err)
	}
	if list, _ := s.store.List(); len(list) != 0 {
		t.Fatalf("запись заведена при отказе: %d", len(list))
	}
}

// fakeTunnelSvc — служба туннелей для Adopt: записывает порядок вызовов.
// Остальные методы интерфейса Adopt не зовёт (встроенный nil уронил бы тест).
type fakeTunnelSvc struct {
	service.Service
	calls []string
}

func (f *fakeTunnelSvc) SyncDescription(_ context.Context, id, name string) {
	f.calls = append(f.calls, "describe:"+id+":"+name)
}
func (f *fakeTunnelSvc) Start(_ context.Context, id string) error {
	f.calls = append(f.calls, "start:"+id)
	return nil
}
func (f *fakeTunnelSvc) Get(_ context.Context, id string) (*service.TunnelWithStatus, error) {
	return &service.TunnelWithStatus{}, nil
}

// Взятая запись OpkgTunN несёт описание сторонней программы. F517 признаёт
// запись своей по равенству описания имени туннеля — без переписывания
// описания до первого старта туннель без живого устройства не стартовал бы
// никогда (а с устройством — до первого ребута).
func TestAdopt_SyncsDescriptionBeforeStart(t *testing.T) {
	stubScan(t, nil, nil)
	stubLinkExists(t, false)
	s := newTestService(t)
	svc := &fakeTunnelSvc{}
	s.tunnelService = svc
	if _, err := s.Adopt(context.Background(), AdoptRequest{InterfaceName: "opkgtun9", ConfContent: validAdoptConf,
		TunnelName: "Германия"}); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if strings.Join(svc.calls, ",") != "describe:awg9:Германия,start:awg9" {
		t.Fatalf("вызовы: %v", svc.calls)
	}
}
