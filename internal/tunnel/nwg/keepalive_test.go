package nwg

import (
	"context"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// F186: конфиг Amnezia Premium приходит с диапазоном keepalive (AWG 3.0), а
// прошивка знает только число. Все три пути в NDMS обязаны схлопывать его в
// нижнюю границу — иначе keepalive либо теряется, либо валит операцию целиком.

// Путь 1 — RCI-батч правки пира (замена конфига, правка карточки).
func TestSyncPeer_KeepaliveRangeGoesToNDMSAsLowerBound(t *testing.T) {
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)

	stored := &storage.AWGTunnel{
		NWGIndex: 5,
		Peer: storage.AWGPeer{
			PublicKey:           "newkey0000000000000000000000000000000000000=",
			Endpoint:            "192.0.2.7:51820",
			AllowedIPs:          []string{"0.0.0.0/0"},
			PersistentKeepalive: "25-35",
		},
	}
	if err := op.SyncPeer(context.Background(), stored, ""); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}

	joined := strings.Join(cs.bodies, "\n")
	if !strings.Contains(joined, `"keepalive-interval":{"interval":25}`) {
		t.Fatalf("нижняя граница диапазона не ушла в NDMS: %s", joined)
	}
}

// Выключенный keepalive команды не порождает. Команда пира в RCI
// инкрементальная: отсутствие `keepalive-interval` означает «оставить в
// прошивке прежнее значение», а не «выключить» — выключать нам нечем.
func TestSyncPeer_KeepaliveZeroSendsNoIntervalCommand(t *testing.T) {
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)

	stored := &storage.AWGTunnel{
		NWGIndex: 5,
		Peer: storage.AWGPeer{
			PublicKey:           "newkey0000000000000000000000000000000000000=",
			Endpoint:            "192.0.2.7:51820",
			PersistentKeepalive: "0",
		},
	}
	if err := op.SyncPeer(context.Background(), stored, ""); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}
	if joined := strings.Join(cs.bodies, "\n"); strings.Contains(joined, "keepalive-interval") {
		t.Fatalf("keepalive 0 породил команду интервала: %s", joined)
	}
}

// Путь 2 — создание батчем (прошивки до 5.01.A.3).
func TestCreateViaBatch_KeepaliveRangeGoesToNDMSAsLowerBound(t *testing.T) {
	f := newFakeNDMS(t)
	op := newCreateTestOperator(t, f)

	stored := testTunnel("a", "CH")
	stored.Peer.PersistentKeepalive = "25-35"
	if _, err := op.Create(context.Background(), stored); err != nil {
		t.Fatalf("Create: %v", err)
	}

	f.mu.Lock()
	joined := strings.Join(f.bodies, "\n")
	f.mu.Unlock()
	if !strings.Contains(joined, `"keepalive-interval":{"interval":25}`) {
		t.Fatalf("нижняя граница диапазона не ушла в NDMS: %s", joined)
	}
}

// Выключенный keepalive и здесь команды не порождает: батч создания собирает
// того же пира той же командой, что SyncPeer.
func TestCreateViaBatch_KeepaliveZeroSendsNoIntervalCommand(t *testing.T) {
	f := newFakeNDMS(t)
	op := newCreateTestOperator(t, f)

	stored := testTunnel("a", "CH")
	stored.Peer.PersistentKeepalive = "0"
	if _, err := op.Create(context.Background(), stored); err != nil {
		t.Fatalf("Create: %v", err)
	}

	f.mu.Lock()
	joined := strings.Join(f.bodies, "\n")
	f.mu.Unlock()
	if strings.Contains(joined, "keepalive-interval") {
		t.Fatalf("keepalive 0 породил команду интервала: %s", joined)
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

// Fail-open запрещён: значение, которого прошивке не понять, не подменяется
// выдуманным числом. Такой .conf NDMS отвергнет громко и целиком — это лучше
// молча уехавшего keepalive 25 (свойство 3 плана).
//
// До записи туннеля значение доезжает импортом: ServiceImpl.Import keepalive
// не валидирует, а config.Parse кладёт строку из файла как есть.
func TestNDMSImportConf_UnreadableKeepaliveNotReplacedByDefault(t *testing.T) {
	stored := importConfTunnel("70000")

	conf, _ := ndmsImportConf(stored)
	if got := confKeepalive(t, conf); got != "70000" {
		t.Fatalf("нечитаемый keepalive подменён на %q:\n%s", got, conf)
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
