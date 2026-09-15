package api

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/opkgtun"
	"github.com/hoaxisr/awg-manager/internal/tunnel/external"
)

type fakeOrphanNDMS struct {
	deleted []string
	err     error
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

// stubLinkGone — устройства в ядре нет: `ip link del` отвечает так же, как
// настоящий, и это для ручки успех.
func stubLinkGone(t *testing.T) *[]string {
	t.Helper()
	prev := linkDelete
	var called []string
	linkDelete = func(_ context.Context, iface string) error {
		called = append(called, iface)
		return errors.New("Cannot find device \"" + iface + "\"")
	}
	t.Cleanup(func() { linkDelete = prev })
	return &called
}

// ГЛАВНОЕ свойство ручки: сиротство перепроверяется на сервере. Отчёт
// диагностики, по которому нажали кнопку, мог устареть, и номер к этому
// моменту мог достаться новому туннелю — удаление по слову клиента снесло бы
// чужой живой интерфейс.
func TestOrphanDelete_RefusesWhenNoLongerOrphan(t *testing.T) {
	stubLinkGone(t)
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
	stubLinkGone(t)
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
	prev := linkDelete
	var deleted string
	linkDelete = func(_ context.Context, iface string) error { deleted = iface; return nil }
	t.Cleanup(func() { linkDelete = prev })

	h := NewOrphanIfaceHandler(listOf("opkgtun10"), &fakeOrphanNDMS{}, nil)

	if rr := orphanReq(t, h, `{"iface":"opkgtun10"}`); rr.Code != 200 {
		t.Fatalf("code = %d, ждали 200 (%s)", rr.Code, rr.Body.String())
	}
	if deleted != "opkgtun10" {
		t.Fatalf("устройство ядра не удалено (deleted=%q)", deleted)
	}
}

// Записи NDMS нет вовсе, устройство в ядре есть — снос обязан дойти до
// `ip link del`. Прежняя форма шла под проверкой наличия и на отказе запуска
// `ip` отвечала успехом, не удалив ничего.
func TestOrphanDelete_RemovesKernelDeviceWhenNoNDMSRecord(t *testing.T) {
	prev := linkDelete
	var deleted string
	linkDelete = func(_ context.Context, iface string) error { deleted = iface; return nil }
	t.Cleanup(func() { linkDelete = prev })

	ndms := &fakeOrphanNDMS{}
	h := NewOrphanIfaceHandler(listKernelOnly("opkgtun10"), ndms, nil)

	if rr := orphanReq(t, h, `{"iface":"opkgtun10"}`); rr.Code != 200 {
		t.Fatalf("code = %d, ждали 200 (%s)", rr.Code, rr.Body.String())
	}
	if deleted != "opkgtun10" {
		t.Fatalf("устройство ядра не снесено при отсутствующей записи NDMS (deleted=%q)", deleted)
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
	prev := linkDelete
	linkDelete = func(context.Context, string) error {
		return errors.New("fork/exec /opt/sbin/ip: no such file or directory")
	}
	t.Cleanup(func() { linkDelete = prev })

	h := NewOrphanIfaceHandler(listOf("opkgtun10"), &fakeOrphanNDMS{}, nil)

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
	prev := linkDelete
	var deleted string
	linkDelete = func(_ context.Context, iface string) error { deleted = iface; return nil }
	t.Cleanup(func() { linkDelete = prev })

	ndms := &fakeOrphanNDMS{}
	h := NewOrphanIfaceHandler(listOf("opkgtun10"), ndms, nil)

	if rr := orphanReq(t, h, `{"iface":"OpkgTun10"}`); rr.Code != 200 {
		t.Fatalf("code = %d, ждали 200 (%s)", rr.Code, rr.Body.String())
	}
	if len(ndms.deleted) != 1 || ndms.deleted[0] != "OpkgTun10" {
		t.Errorf("запись NDMS = %v, ждали [OpkgTun10]", ndms.deleted)
	}
	if deleted != "opkgtun10" {
		t.Errorf("устройство ядра = %q, ждали opkgtun10", deleted)
	}
}

// Запись снята, устройство осталось — номер по-прежнему занят. Отчитаться
// успехом значит соврать: пользователь решит, что убрано всё.
func TestOrphanDelete_ReportsFailureWhenDeviceSurvives(t *testing.T) {
	prev := linkDelete
	linkDelete = func(context.Context, string) error { return errors.New("busy") }
	t.Cleanup(func() { linkDelete = prev })

	h := NewOrphanIfaceHandler(listOf("opkgtun10"), &fakeOrphanNDMS{}, nil)

	rr := orphanReq(t, h, `{"iface":"opkgtun10"}`)
	if rr.Code == 200 {
		t.Fatalf("code = 200, а устройство осталось: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "LINK_DELETE_FAILED") {
		t.Errorf("ответ не называет причину: %s", rr.Body.String())
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

// Формулировки «устройства нет» на роутере три, и все три обязаны считаться
// успехом: снос записи NDMS уносит устройство каскадом, так что к нашему
// `ip link del` его штатно уже нет (стенд 15.09 — ручка отчитывалась отказом
// об успешном сносе).
func TestOrphanDelete_AllAbsentDeviceWordingsAreSuccess(t *testing.T) {
	for _, msg := range []string{
		`Device "opkgtun10" does not exist. (exit status 1)`,
		`Cannot find device "opkgtun10" (exit status 1)`,
		"no such device",
	} {
		t.Run(msg, func(t *testing.T) {
			prev := linkDelete
			linkDelete = func(context.Context, string) error { return errors.New(msg) }
			t.Cleanup(func() { linkDelete = prev })

			h := NewOrphanIfaceHandler(listOf("opkgtun10"), &fakeOrphanNDMS{}, nil)
			if rr := orphanReq(t, h, `{"iface":"opkgtun10"}`); rr.Code != 200 {
				t.Fatalf("code = %d, ждали 200: %s", rr.Code, rr.Body.String())
			}
		})
	}
}
