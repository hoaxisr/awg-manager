package api

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/netdev"
	"github.com/hoaxisr/awg-manager/internal/opkgtun"
	"github.com/hoaxisr/awg-manager/internal/tunnel/external"
)

type fakeOrphanNDMS struct {
	deleted    []string
	err        error
	stopped    []string // StopIfPresent: устройство снято
	stopErr    error
	replaceErr error
	// replaceGone — подмена сорвалась после del: устройство снято (каталог
	// в временном netdev.SysClassNet удаляется), tun не встал.
	replaceGone bool
	downErr     error
	calls       []string // все шаги по порядку
}

func (f *fakeOrphanNDMS) InterfaceDownIfUp(_ context.Context, name string) error {
	f.calls = append(f.calls, "down "+name)
	return f.downErr
}

func (f *fakeOrphanNDMS) StopIfPresent(_ context.Context, iface string) error {
	f.calls = append(f.calls, "stop "+iface)
	if f.stopErr != nil {
		return f.stopErr
	}
	f.stopped = append(f.stopped, iface)
	return nil
}

func (f *fakeOrphanNDMS) ReplaceWithTun(_ context.Context, iface string) error {
	f.calls = append(f.calls, "replace "+iface)
	if f.replaceGone {
		_ = os.Remove(filepath.Join(netdev.SysClassNet, iface))
	}
	return f.replaceErr
}

// orphanDevice — устройство iface есть для netdev (временный SysClassNet):
// ручка решает «устройства нет → сразу `no interface`» по stat (M1).
func orphanDevice(t *testing.T, iface string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, iface), 0o755); err != nil {
		t.Fatal(err)
	}
	old := netdev.SysClassNet
	netdev.SysClassNet = root
	t.Cleanup(func() { netdev.SysClassNet = old })
}

// noOrphanDevice — устройства нет (пустой временный SysClassNet).
func noOrphanDevice(t *testing.T) {
	t.Helper()
	old := netdev.SysClassNet
	netdev.SysClassNet = t.TempDir()
	t.Cleanup(func() { netdev.SysClassNet = old })
}

func (f *fakeOrphanNDMS) DeleteOpkgTun(_ context.Context, name string) error {
	f.calls = append(f.calls, "delete "+name)
	if f.err != nil {
		return f.err
	}
	f.deleted = append(f.deleted, name)
	return nil
}

func orphanReq(t *testing.T, h *OrphanIfaceHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/tunnels/orphans/delete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.Delete(rr, req)
	return rr
}

// listOf — сироты «обе половины на месте»: имя записи NDMS приходит от роутера
// и несётся до сноса как есть.
func listOf(ifaces ...string) func(context.Context) ([]external.OrphanIface, error) {
	return func(context.Context) ([]external.OrphanIface, error) {
		out := make([]external.OrphanIface, 0, len(ifaces))
		for _, i := range ifaces {
			num, _ := opkgtun.IndexOf(i)
			out = append(out, external.OrphanIface{
				Iface:        i,
				NDMSName:     fmt.Sprintf("OpkgTun%d", num),
				NDMSRecord:   true,
				KernelDevice: true,
			})
		}
		return out, nil
	}
}

// listKernelOnly — устройство есть, записи NDMS нет вовсе.
func listKernelOnly(iface string) func(context.Context) ([]external.OrphanIface, error) {
	return func(context.Context) ([]external.OrphanIface, error) {
		return []external.OrphanIface{{Iface: iface, KernelDevice: true}}, nil
	}
}

// ГЛАВНОЕ свойство ручки: сиротство перепроверяется на сервере. Отчёт
// диагностики, по которому нажали кнопку, мог устареть, и номер к этому
// моменту мог достаться новому туннелю — удаление по слову клиента снесло бы
// чужой живой интерфейс.
func TestOrphanDelete_RefusesWhenNoLongerOrphan(t *testing.T) {
	ndms := &fakeOrphanNDMS{}
	h := NewOrphanIfaceHandler(listOf("opkgtun11"), ndms, nil)

	rr := orphanReq(t, h, `{"iface":"opkgtun10"}`)

	if rr.Code != 409 {
		t.Fatalf("code = %d, ждали 409", rr.Code)
	}
	if len(ndms.deleted) != 0 {
		t.Fatalf("удалено %v, а туннель уже не сирота", ndms.deleted)
	}
}

func TestOrphanDelete_RemovesNDMSRecordByNDMSName(t *testing.T) {
	ndms := &fakeOrphanNDMS{}
	h := NewOrphanIfaceHandler(listOf("opkgtun10"), ndms, nil)

	if rr := orphanReq(t, h, `{"iface":"opkgtun10"}`); rr.Code != 200 {
		t.Fatalf("code = %d, ждали 200 (%s)", rr.Code, rr.Body.String())
	}
	// Ядро зовёт его opkgtun10, NDMS — OpkgTun10. Снимать надо запись NDMS.
	if len(ndms.deleted) != 1 || ndms.deleted[0] != "OpkgTun10" {
		t.Fatalf("удалено %v, ждали [OpkgTun10]", ndms.deleted)
	}
}

// Устройство переживает снос записи, когда его держали открытым. Не добить
// его значит занять номер пула навсегда.
func TestOrphanDelete_AlsoRemovesSurvivingKernelDevice(t *testing.T) {
	ndms := &fakeOrphanNDMS{}
	h := NewOrphanIfaceHandler(listOf("opkgtun10"), ndms, nil)

	if rr := orphanReq(t, h, `{"iface":"opkgtun10"}`); rr.Code != 200 {
		t.Fatalf("code = %d, ждали 200 (%s)", rr.Code, rr.Body.String())
	}
	if len(ndms.stopped) != 1 || ndms.stopped[0] != "opkgtun10" {
		t.Fatalf("устройство ядра не удалено (stopped=%v)", ndms.stopped)
	}
}

// Записи NDMS нет вовсе, устройство в ядре есть — снос обязан дойти до
// бэкенда.
func TestOrphanDelete_RemovesKernelDeviceWhenNoNDMSRecord(t *testing.T) {
	ndms := &fakeOrphanNDMS{}
	h := NewOrphanIfaceHandler(listKernelOnly("opkgtun10"), ndms, nil)

	if rr := orphanReq(t, h, `{"iface":"opkgtun10"}`); rr.Code != 200 {
		t.Fatalf("code = %d, ждали 200 (%s)", rr.Code, rr.Body.String())
	}
	if len(ndms.stopped) != 1 || ndms.stopped[0] != "opkgtun10" {
		t.Fatalf("устройство ядра не снесено при отсутствующей записи NDMS (stopped=%v)", ndms.stopped)
	}
	// Записи не было — в NDMS ходить незачем: снос имени, собранного из номера,
	// ушёл бы мимо и был бы принят за успех.
	if len(ndms.deleted) != 0 || strings.Join(ndms.calls, ",") != "stop opkgtun10" {
		t.Fatalf("ходили в NDMS без записи: deleted=%v шаги=%v", ndms.deleted, ndms.calls)
	}
}

// Отказ ЗАПУСКА `ip` — это «мы не проверили», а не «устройства нет». Ответить
// на него успехом значит соврать: номер остался занятым.
func TestOrphanDelete_ExecFailureIsNotTreatedAsAbsentDevice(t *testing.T) {
	ndms := &fakeOrphanNDMS{stopErr: errors.New("fork/exec /opt/sbin/ip: no such file or directory")}
	h := NewOrphanIfaceHandler(listKernelOnly("opkgtun10"), ndms, nil)

	rr := orphanReq(t, h, `{"iface":"opkgtun10"}`)
	if rr.Code == 200 {
		t.Fatalf("отказ запуска ip принят за отсутствие устройства: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "LINK_DELETE_FAILED") {
		t.Errorf("ответ не называет причину: %s", rr.Body.String())
	}
}

// D-N2 (C3a, стенд Task 59): запись есть — down, подмена устройства на tun,
// снос записи, затем остаток устройства. Прежний порядок «устройство, затем
// запись» оставлял запись без устройства до `no interface` (0767, X5b).
func TestOrphanDelete_Order_DownTunRecordStop(t *testing.T) {
	orphanDevice(t, "opkgtun10")
	ndms := &fakeOrphanNDMS{}
	h := NewOrphanIfaceHandler(listOf("opkgtun10"), ndms, nil)

	if rr := orphanReq(t, h, `{"iface":"opkgtun10"}`); rr.Code != 200 {
		t.Fatalf("code = %d, ждали 200 (%s)", rr.Code, rr.Body.String())
	}
	want := []string{"down OpkgTun10", "replace opkgtun10", "delete OpkgTun10", "stop opkgtun10"}
	if strings.Join(ndms.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("шаги %v, want %v", ndms.calls, want)
	}
}

// Ядро зовёт интерфейс opkgtun10, NDMS — OpkgTun10, клиент мог прислать любое
// написание. Сверка по строке отвечала на «OpkgTun10» отказом «у номера есть
// владелец», что неправда, и сносила бы имя, которого в ядре нет.
func TestOrphanDelete_AcceptsNDMSSpellingAndDeletesCanonicalNames(t *testing.T) {
	ndms := &fakeOrphanNDMS{}
	h := NewOrphanIfaceHandler(listOf("opkgtun10"), ndms, nil)

	if rr := orphanReq(t, h, `{"iface":"OpkgTun10"}`); rr.Code != 200 {
		t.Fatalf("code = %d, ждали 200 (%s)", rr.Code, rr.Body.String())
	}
	if len(ndms.deleted) != 1 || ndms.deleted[0] != "OpkgTun10" {
		t.Errorf("запись NDMS = %v, ждали [OpkgTun10]", ndms.deleted)
	}
	if len(ndms.stopped) != 1 || ndms.stopped[0] != "opkgtun10" {
		t.Errorf("устройство ядра = %v, ждали [opkgtun10]", ndms.stopped)
	}
}

// Запись не опущена (RCI отказал / список не прочитан) — дальше не идём:
// подмена под up-записью — 0ba1, снос записи при живом устройстве — 003b.
func TestOrphanDelete_DownFails_NothingTouched(t *testing.T) {
	orphanDevice(t, "opkgtun10")
	ndms := &fakeOrphanNDMS{downErr: errors.New("injected: down")}
	h := NewOrphanIfaceHandler(listOf("opkgtun10"), ndms, nil)

	rr := orphanReq(t, h, `{"iface":"opkgtun10"}`)
	if rr.Code == 200 || !strings.Contains(rr.Body.String(), "NDMS_DOWN_FAILED") {
		t.Fatalf("code=%d body=%s, want отказ NDMS_DOWN_FAILED", rr.Code, rr.Body.String())
	}
	if strings.Join(ndms.calls, ",") != "down OpkgTun10" {
		t.Fatalf("шаги после отказа down: %v", ndms.calls)
	}
}

// Устройство под записью не подменено (отказ ip, чужой держатель tun) —
// запись не трогаем: снос записи при живом устройстве — C. Отчитаться
// успехом значит соврать: номер по-прежнему занят.
func TestOrphanDelete_ReportsFailureWhenDeviceSurvives(t *testing.T) {
	orphanDevice(t, "opkgtun10")
	ndms := &fakeOrphanNDMS{replaceErr: errors.New("busy")}
	h := NewOrphanIfaceHandler(listOf("opkgtun10"), ndms, nil)

	rr := orphanReq(t, h, `{"iface":"opkgtun10"}`)
	if rr.Code == 200 {
		t.Fatalf("code = 200, а устройство осталось: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "LINK_DELETE_FAILED") {
		t.Errorf("ответ не называет причину: %s", rr.Body.String())
	}
	if len(ndms.deleted) != 0 {
		t.Errorf("запись снята при живом устройстве: %v", ndms.deleted)
	}
}

func TestOrphanDelete_RejectsNonOpkgTunName(t *testing.T) {
	h := NewOrphanIfaceHandler(listOf("opkgtun10"), &fakeOrphanNDMS{}, nil)
	if rr := orphanReq(t, h, `{"iface":"br0"}`); rr.Code == 200 {
		t.Fatalf("имя br0 принято: %s", rr.Body.String())
	}
}

// Занятость не собрана — удалять нельзя: неполный список делает живой
// интерфейс сиротой.
func TestOrphanDelete_RefusesWhenOccupancyUnavailable(t *testing.T) {
	ndms := &fakeOrphanNDMS{}
	h := NewOrphanIfaceHandler(func(context.Context) ([]external.OrphanIface, error) {
		return nil, errors.New("RCI молчит")
	}, ndms, nil)

	if rr := orphanReq(t, h, `{"iface":"opkgtun10"}`); rr.Code == 200 {
		t.Fatalf("удалили при несобранной занятости: %s", rr.Body.String())
	}
	if len(ndms.deleted) != 0 {
		t.Fatalf("удалено %v при несобранной занятости", ndms.deleted)
	}
}

// oracleOrphanNDMS — шаги сироты через оракул FakeNDMS: C/E видны там.
// Опускание — без предиката State (его держит адаптер в cmd, свой тест).
type oracleOrphanNDMS struct{ f *ndmsquery.FakeNDMS }

func (o oracleOrphanNDMS) InterfaceDownIfUp(ctx context.Context, name string) error {
	_, err := o.f.Post(ctx, map[string]any{"interface": map[string]any{name: map[string]any{"up": false}}})
	return err
}

// StopIfPresent — снос устройства, видимый оракулу.
func (o oracleOrphanNDMS) StopIfPresent(_ context.Context, iface string) error {
	o.f.SetNetdev(iface, false)
	o.f.SetAmneziaWG(iface, false)
	return nil
}

// ReplaceWithTun — как в ядре: живое устройство снято, затем plain tun.
func (o oracleOrphanNDMS) ReplaceWithTun(_ context.Context, iface string) error {
	o.f.SetNetdev(iface, false)
	o.f.SetNetdev(iface, true)
	o.f.SetAmneziaWG(iface, false)
	return nil
}

func (o oracleOrphanNDMS) DeleteOpkgTun(ctx context.Context, name string) error {
	_, err := o.f.Post(ctx, map[string]any{"interface": map[string]any{name: map[string]any{"no": true}}})
	return err
}

// N1/D-N2: сирота OpkgTun10 в up с живым amneziawg opkgtun10 (например,
// после потери стора туннелей). C3a — 0 C: запись раньше tun — 003b, подмена
// под up — 0ba1 (стенд Task 59).
func TestOrphanDelete_LiveAmneziaWG_TunThenRecord_NoC(t *testing.T) {
	orphanDevice(t, "opkgtun10")
	f := ndmsquery.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun", State: "up"})
	f.SetNetdev("opkgtun10", true)
	f.SetAmneziaWG("opkgtun10", true)
	h := NewOrphanIfaceHandler(listOf("opkgtun10"), oracleOrphanNDMS{f}, nil)

	if rr := orphanReq(t, h, `{"iface":"opkgtun10"}`); rr.Code != 200 {
		t.Fatalf("code = %d, ждали 200 (%s)", rr.Code, rr.Body.String())
	}
	if f.Has("OpkgTun10") || f.C != 0 || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("запись есть=%v C=%d E=%d фантомов=%d, want false/0/0/0; posts=%v", f.Has("OpkgTun10"), f.C, f.E, f.Phantoms, f.Posts)
	}
}

// M1: устройства нет (внешний `ip link del`/rmmod — запись в state error при
// conf running) — ни down, ни подмены: сразу `no interface` (0 C ×12, стенд
// Task 59), затем остаток устройства (StopIfPresent — nil). Подмена здесь
// была бы голым `tuntap add` под running-записью.
// Мутация: снять ветку «устройства нет» → down/replace в шагах, красный.
func TestOrphanDelete_DeviceAbsent_DirectNoInterface(t *testing.T) {
	noOrphanDevice(t)
	ndms := &fakeOrphanNDMS{}
	h := NewOrphanIfaceHandler(listOf("opkgtun10"), ndms, nil)

	if rr := orphanReq(t, h, `{"iface":"opkgtun10"}`); rr.Code != 200 {
		t.Fatalf("code = %d, ждали 200 (%s)", rr.Code, rr.Body.String())
	}
	want := []string{"delete OpkgTun10", "stop opkgtun10"}
	if strings.Join(ndms.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("шаги %v, want %v", ndms.calls, want)
	}
}

// L2: подмена сорвалась после del (tun не встал) — устройства нет, запись
// снимается: оставленная без устройства, она давала бы 0767 на каждом нашем
// списке. Живое устройство после отказа — отказ (ReportsFailureWhenDeviceSurvives).
// Мутация: отказ подмены всегда без сноса записи → красный.
func TestOrphanDelete_ReplaceFailedDeviceGone_RecordRemoved(t *testing.T) {
	orphanDevice(t, "opkgtun10")
	ndms := &fakeOrphanNDMS{replaceErr: errors.New("injected: tuntap"), replaceGone: true}
	h := NewOrphanIfaceHandler(listOf("opkgtun10"), ndms, nil)

	if rr := orphanReq(t, h, `{"iface":"opkgtun10"}`); rr.Code != 200 {
		t.Fatalf("code = %d, ждали 200 (%s)", rr.Code, rr.Body.String())
	}
	want := []string{"down OpkgTun10", "replace opkgtun10", "delete OpkgTun10", "stop opkgtun10"}
	if strings.Join(ndms.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("шаги %v, want %v", ndms.calls, want)
	}
}
