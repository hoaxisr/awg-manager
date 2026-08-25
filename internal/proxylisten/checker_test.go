package proxylisten

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// newRecords кладёт записи ЧЕРЕЗ хранилище: нормализация и инварианты те же,
// что у прода, и фикстура не может застыть в форме, которой store уже не
// отдаёт.
func newRecords(t *testing.T, recs ...instancestore.Record) *instancestore.Store {
	t.Helper()
	store := instancestore.New(t.TempDir())
	if _, err := store.Replace(func(st *instancestore.State) error {
		st.Records = recs
		return nil
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	return store
}

func TestCrossChecker_IncludesClientsOfBothSubsystems(t *testing.T) {
	checker := &CrossChecker{Records: newRecords(t,
		instancestore.Record{ID: "wd-a", Kind: instancestore.KindWdttClient,
			WdttClient: &roles.WdttClientConfig{Mode: "wg", Listen: "127.0.0.1:9000"}},
		instancestore.Record{ID: "ft-a", Kind: instancestore.KindFreeTurnClient,
			FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9001"}},
	)}

	used, err := checker.OccupiedLocalListenPorts("", "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]bool{9000: true, 9001: true}
	if !reflect.DeepEqual(used, want) {
		t.Fatalf("занятые порты = %v, ждали %v", used, want)
	}
}

func TestCrossChecker_IncludesServerPorts(t *testing.T) {
	checker := &CrossChecker{Records: newRecords(t,
		instancestore.Record{ID: "wd-s", Kind: instancestore.KindWdttServer,
			WdttServer: &roles.WdttServerConfig{
				Listen: "0.0.0.0:56002", WgPort: 56001,
				NdmsIface: "OpkgTun20", WgIface: "opkgtun20",
				RawNdmsIface: "OpkgTun21", RawIface: "opkgtun21",
			}},
		instancestore.Record{ID: "ft-s", Kind: instancestore.KindFreeTurnServer,
			FreeTurnServer: &roles.FreeTurnServerConfig{Listen: "0.0.0.0:56003"}},
	)}

	used, err := checker.OccupiedLocalListenPorts("", "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]bool{56001: true, 56002: true, 56003: true}
	if !reflect.DeepEqual(used, want) {
		t.Fatalf("занятые порты = %v, ждали %v", used, want)
	}
}

func TestCrossChecker_IncludesAWGTunnelEndpoint(t *testing.T) {
	dir := t.TempDir()
	awgStore := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := awgStore.Save(&storage.AWGTunnel{
		ID:   "awg10",
		Name: "linked",
		Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9002"},
	}); err != nil {
		t.Fatal(err)
	}

	checker := &CrossChecker{AWGStore: awgStore}
	used, err := checker.OccupiedLocalListenPorts("", "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]bool{9002: true}
	if !reflect.DeepEqual(used, want) {
		t.Fatalf("занятые порты = %v, ждали %v", used, want)
	}
}

// Ни туннель, привязанный к самому клиенту, ни его собственная запись не
// должны считаться занятыми: иначе клиент на каждом старте видит свой же порт
// занятым и переезжает, а endpoint туннеля остаётся на старом — связка
// разваливается без шанса на самолечение.
func TestCrossChecker_SkipsOwnTunnelAndOwnRecord(t *testing.T) {
	dir := t.TempDir()
	awgStore := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	for _, tun := range []storage.AWGTunnel{
		{ID: "awg11", Name: "linked-wdtt", WdttClientID: "client-a",
			Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9000"}},
		{ID: "awg12", Name: "linked-freeturn", FreeTurnClientID: "client-b",
			Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9001"}},
	} {
		if err := awgStore.Save(&tun); err != nil {
			t.Fatal(err)
		}
	}

	checker := &CrossChecker{
		AWGStore: awgStore,
		Records: newRecords(t,
			instancestore.Record{ID: "client-a", Kind: instancestore.KindWdttClient,
				WdttClient: &roles.WdttClientConfig{Mode: "wg", Listen: "127.0.0.1:9000"}},
			instancestore.Record{ID: "client-b", Kind: instancestore.KindFreeTurnClient,
				FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9001"}},
		),
	}

	used, err := checker.OccupiedLocalListenPorts("client-a", "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]bool{9001: true}
	if !reflect.DeepEqual(used, want) {
		t.Fatalf("для wdtt-клиента client-a занятые порты = %v, ждали %v", used, want)
	}

	used, err = checker.OccupiedLocalListenPorts("", "client-b")
	if err != nil {
		t.Fatal(err)
	}
	want = map[int]bool{9000: true}
	if !reflect.DeepEqual(used, want) {
		t.Fatalf("для freeturn-клиента client-b занятые порты = %v, ждали %v", used, want)
	}
}
