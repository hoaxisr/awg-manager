package nwg

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// F186: конфиг Amnezia Premium приходит с диапазоном keepalive (AWG 3.0), а
// прошивка знает только число. Все три пути в NDMS обязаны схлопывать его в
// нижнюю границу — иначе keepalive либо теряется, либо валит операцию целиком.

// keepaliveTable — значения, достижимые в записи туннеля, и то, что nwg обязан
// положить в конфигурацию пира. Пустое ожидание означает «команду интервала
// собирать не из чего»: keepalive, которого прошивке не отправить, не
// подменяется ни дефолтом, ни нулём.
//
// Все значения достижимы: формат их принимает (config.ValidateKeepalive),
// поэтому они попадают в запись импортом .conf, а всё, кроме "0-80", — ещё и
// правкой карточки (её проверяет ValidateKeepaliveSubmitted).
var keepaliveTable = []struct {
	stored storage.Keepalive
	want   int // 0 — команды интервала быть не должно
}{
	{"25", 25},
	{"25-35", 25},
	{" 25 - 35 ", 25},
	{"0", 0},
	{"0-80", 0},
	{"", 0},
}

// assertKeepaliveInBatch проверяет команду интервала в отправленных телах.
func assertKeepaliveInBatch(t *testing.T, bodies []string, want int) {
	t.Helper()
	joined := strings.Join(bodies, "\n")
	if want == 0 {
		if strings.Contains(joined, "keepalive-interval") {
			t.Fatalf("keepalive породил команду интервала: %s", joined)
		}
		return
	}
	fragment := `"keepalive-interval":{"interval":` + strconv.Itoa(want) + `}`
	if !strings.Contains(joined, fragment) {
		t.Fatalf("в NDMS не ушёл %s: %s", fragment, joined)
	}
}

// Путь 1 — RCI-батч правки пира (замена конфига, правка карточки).
func TestSyncPeer_KeepaliveGoesToNDMSAsNumber(t *testing.T) {
	for _, tc := range keepaliveTable {
		t.Run(string(tc.stored), func(t *testing.T) {
			cs := newCaptureServer(t)
			op := newSyncTestOperator(t, cs.srv.URL)

			stored := &storage.AWGTunnel{
				NWGIndex: 5,
				Peer: storage.AWGPeer{
					PublicKey:           "newkey0000000000000000000000000000000000000=",
					Endpoint:            "192.0.2.7:51820",
					AllowedIPs:          []string{"0.0.0.0/0"},
					PersistentKeepalive: tc.stored,
				},
			}
			if err := op.SyncPeer(context.Background(), stored, ""); err != nil {
				t.Fatalf("SyncPeer: %v", err)
			}
			assertKeepaliveInBatch(t, cs.bodies, tc.want)
		})
	}
}

// Путь 2 — создание батчем (прошивки до 5.01.A.3).
func TestCreateViaBatch_KeepaliveGoesToNDMSAsNumber(t *testing.T) {
	for _, tc := range keepaliveTable {
		t.Run(string(tc.stored), func(t *testing.T) {
			f := newFakeNDMS(t)
			op := newCreateTestOperator(t, f)

			stored := testTunnel("a", "CH")
			stored.Peer.PersistentKeepalive = tc.stored
			if _, err := op.Create(context.Background(), stored); err != nil {
				t.Fatalf("Create: %v", err)
			}

			f.mu.Lock()
			bodies := append([]string(nil), f.bodies...)
			f.mu.Unlock()
			assertKeepaliveInBatch(t, bodies, tc.want)
		})
	}
}

// Путь 3 — импорт .conf (прошивки >= 5.01.A.3, основной сценарий стенда).
// Строгий парсер NDMS отвергает диапазон вместе со всем файлом, поэтому
// схлопывать надо в самом .conf, а не только в RCI-командах.
//
// Нижняя граница взята заведомо не равной DefaultPersistentKeepalive ("25"):
// на 25 тест зелёный и у реализации «просто стереть поле» — недостающее
// значение генератор подставляет сам (config.GenerateForExport).
func TestNDMSImportConf_KeepaliveRangeCollapsedToLowerBound(t *testing.T) {
	stored := importConfTunnel("40-50")

	conf, _ := ndmsImportConf(stored)
	if got := confKeepalive(t, conf); got != "40" {
		t.Fatalf("PersistentKeepalive в .conf = %q, ждали \"40\":\n%s", got, conf)
	}
	// Запись туннеля остаётся как есть: kernel-режим забирает диапазон целиком.
	if stored.Peer.PersistentKeepalive != "40-50" {
		t.Fatalf("запись туннеля изменена: %q", stored.Peer.PersistentKeepalive)
	}
}

// Диапазон с нулевой нижней границей достижим: config.ValidateKeepalive его
// принимает, поэтому он приезжает импортом .conf (ServiceImpl.Import keepalive
// не проверяет, а config.Parse кладёт всё, что валидатор принял) и лежит в
// записях, сохранённых до запрета на присланное значение.
//
// В .conf такое уходить не должно ни в каком виде: строгий парсер NDMS
// отвергает файл целиком, и туннель не создаётся вовсе. Проверяем свойство, а
// не конкретное число: файл обязан нести keepalive, который прошивка примет.
func TestNDMSImportConf_ZeroLowerBoundRangeNotInConf(t *testing.T) {
	for _, ka := range []storage.Keepalive{"0-80", "0-0"} {
		t.Run(string(ka), func(t *testing.T) {
			conf, _ := ndmsImportConf(importConfTunnel(ka))
			got := confKeepalive(t, conf)
			if _, err := strconv.ParseUint(got, 10, 16); err != nil {
				t.Fatalf("прошивка не примет PersistentKeepalive = %q:\n%s", got, conf)
			}
		})
	}
}

func importConfTunnel(keepalive storage.Keepalive) *storage.AWGTunnel {
	return &storage.AWGTunnel{
		Name: "t1",
		Interface: storage.AWGInterface{
			PrivateKey: "wMvstfyVWYn6WGn5CjVlSsGj/9tzCvdNoIjPB/Vsc1w=",
			Address:    "10.13.14.5",
		},
		Peer: storage.AWGPeer{
			PublicKey:           "GLi3Az9hKTEm2GT4Jlktzy3t0nB6Abb4Svf9JlxS4Q4=",
			Endpoint:            "vpn.example.test:32949",
			PersistentKeepalive: keepalive,
		},
	}
}

// confKeepalive возвращает значение PersistentKeepalive целиком. Проверка
// вхождением подстроки здесь бесполезна: "25" содержится и в "25-35".
func confKeepalive(t *testing.T, conf string) string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(conf, "\n") {
		if v, ok := strings.CutPrefix(line, "PersistentKeepalive = "); ok {
			found = append(found, v)
		}
	}
	if len(found) != 1 {
		t.Fatalf("строк PersistentKeepalive в .conf: %d, ждали одну:\n%s", len(found), conf)
	}
	return found[0]
}
