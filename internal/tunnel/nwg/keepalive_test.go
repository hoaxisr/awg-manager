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

// Выключенный keepalive командой не становится: пустой `keepalive-interval`
// прошивке слать не за чем.
func TestSyncPeer_KeepaliveZeroSendsNoInterval(t *testing.T) {
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

// Путь 3 — импорт .conf (прошивки >= 5.01.A.3, основной сценарий стенда).
// Строгий парсер NDMS отвергает диапазон вместе со всем файлом, поэтому
// схлопывать надо в самом .conf, а не только в RCI-командах.
func TestNDMSImportConf_KeepaliveRangeCollapsedToLowerBound(t *testing.T) {
	stored := &storage.AWGTunnel{
		Name: "t1",
		Interface: storage.AWGInterface{
			PrivateKey: "wMvstfyVWYn6WGn5CjVlSsGj/9tzCvdNoIjPB/Vsc1w=",
			Address:    "10.13.14.5",
		},
		Peer: storage.AWGPeer{
			PublicKey:           "GLi3Az9hKTEm2GT4Jlktzy3t0nB6Abb4Svf9JlxS4Q4=",
			Endpoint:            "vpn.example.test:32949",
			PersistentKeepalive: "25-35",
		},
	}

	conf, _ := ndmsImportConf(stored)
	if strings.Contains(conf, "25-35") {
		t.Fatalf("диапазон keepalive попал в .conf для NDMS:\n%s", conf)
	}
	if !strings.Contains(conf, "PersistentKeepalive = 25\n") {
		t.Fatalf("нижняя граница не попала в .conf:\n%s", conf)
	}
	// Запись туннеля остаётся как есть: kernel-режим забирает диапазон целиком.
	if stored.Peer.PersistentKeepalive != "25-35" {
		t.Fatalf("запись туннеля изменена: %q", stored.Peer.PersistentKeepalive)
	}
}
