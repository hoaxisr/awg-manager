package orchestrator

import (
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Зеркальные записи raw-выходов оркестратору не принадлежат: их жизненным
// циклом ведает прокси-рантайм. Обоими путями загрузки кэша они обязаны
// отсеиваться — иначе первая же ветка default в switch по Backend начала бы
// стартовать и останавливать чужой ресурс.
func TestStateSkipsWdttRawMirrors(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	for _, tun := range []*storage.AWGTunnel{
		{ID: "awg10", Name: "Свой", Backend: "kernel",
			Interface: storage.AWGInterface{Address: "10.0.0.1/32", MTU: 1420}},
		{ID: "wdttraw-de", Name: "Зеркало", Backend: "wdtt-raw",
			Interface: storage.AWGInterface{Address: "10.70.0.2/32", MTU: 1420}},
	} {
		if err := store.Create(tun); err != nil {
			t.Fatal(err)
		}
	}

	s := &State{tunnels: map[string]*tunnelState{}}
	s.loadFromStore(store)
	if _, ok := s.tunnels["awg10"]; !ok {
		t.Fatal("свой туннель обязан попасть в кэш")
	}
	if _, ok := s.tunnels["wdttraw-de"]; ok {
		t.Fatal("зеркальная запись попала в кэш через loadFromStore")
	}

	if s.ensureTunnel("wdttraw-de", store) {
		t.Fatal("ensureTunnel загрузил зеркальную запись: второй путь сводит фильтр на нет")
	}
	if _, ok := s.tunnels["wdttraw-de"]; ok {
		t.Fatal("зеркальная запись осела в кэше после ensureTunnel")
	}
	if !s.ensureTunnel("awg10", store) {
		t.Fatal("свой туннель обязан грузиться через ensureTunnel")
	}
}
