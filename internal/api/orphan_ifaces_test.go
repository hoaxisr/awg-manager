package api

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/opkgtun"
	"github.com/hoaxisr/awg-manager/internal/tunnel/external"
)

type fakeOrphanNDMS struct {
	deleted []string
	err     error
	stopped []string // StopIfPresent: устройство снято
	stopErr error
}

func (f *fakeOrphanNDMS) StopIfPresent(_ context.Context, iface string) error {
	if f.stopErr != nil {
		return f.stopErr
	}
	f.stopped = append(f.stopped, iface)
	return nil
}

func (f *fakeOrphanNDMS) ReplaceWithTun(context.Context, string) error {
	panic("ReplaceWithTun: порядок сироты с ним — Task 61")
}

func (f *fakeOrphanNDMS) DeleteOpkgTun(_ context.Context, name string) error {
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
	if len(ndms.deleted) != 0 {
		t.Fatalf("ходили в NDMS без записи: %v", ndms.deleted)
	}
}

// Отказ ЗАПУСКА `ip` — это «мы не проверили», а не «устройства нет». Ответить
// на него успехом значит соврать: номер остался занятым.
func TestOrphanDelete_ExecFailureIsNotTreatedAsAbsentDevice(t *testing.T) {
	ndms := &fakeOrphanNDMS{stopErr: errors.New("fork/exec /opt/sbin/ip: no such file or directory")}
	h := NewOrphanIfaceHandler(listOf("opkgtun10"), ndms, nil)

	rr := orphanReq(t, h, `{"iface":"opkgtun10"}`)
	if rr.Code == 200 {
		t.Fatalf("отказ запуска ip принят за отсутствие устройства: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "LINK_DELETE_FAILED") {
		t.Errorf("ответ не называет причину: %s", rr.Body.String())
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

// Запись снята, устройство осталось — номер по-прежнему занят. Отчитаться
// успехом значит соврать: пользователь решит, что убрано всё.
func TestOrphanDelete_ReportsFailureWhenDeviceSurvives(t *testing.T) {
	ndms := &fakeOrphanNDMS{stopErr: errors.New("busy")}
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

// oracleOrphanNDMS — снос записи через оракул FakeNDMS: C/E видны там.
type oracleOrphanNDMS struct{ f *ndmsquery.FakeNDMS }

// StopIfPresent — снос устройства, видимый оракулу.
func (o oracleOrphanNDMS) StopIfPresent(_ context.Context, iface string) error {
	o.f.SetNetdev(iface, false)
	o.f.SetAmneziaWG(iface, false)
	return nil
}

func (o oracleOrphanNDMS) ReplaceWithTun(context.Context, string) error {
	panic("ReplaceWithTun: порядок сироты с ним — Task 61")
}

func (o oracleOrphanNDMS) DeleteOpkgTun(ctx context.Context, name string) error {
	_, err := o.f.Post(ctx, map[string]any{"interface": map[string]any{name: map[string]any{"no": true}}})
	return err
}

// N1/F598: сирота OpkgTun10 с живым amneziawg opkgtun10 (например, после
// потери стора туннелей). Устройство снимается до записи — иначе снос записи
// при живом устройстве даёт C 0xcffd003b (стенд A7/B).
func TestOrphanDelete_LiveAmneziaWG_DeviceBeforeRecord_NoC(t *testing.T) {
	f := ndmsquery.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"})
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
