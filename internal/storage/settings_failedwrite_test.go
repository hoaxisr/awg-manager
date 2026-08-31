package storage

import (
	"os"
	"reflect"
	"testing"
)

// failWrites делает каталог настроек read-only: AtomicWrite падает на создании
// temp-файла. Образец root-скипа — awg_store_strict_test.go.
func failWrites(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("под root chmod не запрещает запись")
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// F3: saveUnlocked публиковал s.settings ДО AtomicWrite — при провале записи
// кэш нёс незаписанное.
func TestUpdate_FailedWriteKeepsCache(t *testing.T) {
	dir := t.TempDir()
	store := NewSettingsStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := store.Update(func(s *Settings) error {
		s.DisableMemorySaving = true
		return nil
	}); err != nil {
		t.Fatalf("seed Update: %v", err)
	}
	failWrites(t, dir)
	if err := store.Update(func(s *Settings) error {
		s.DisableMemorySaving = false
		return nil
	}); err == nil {
		t.Fatal("ожидался отказ записи")
	}
	got, err := store.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.DisableMemorySaving {
		t.Fatal("кэш несёт незаписанное: провал AtomicWrite опубликовал мутацию")
	}
}

// F3-B: узкий мутатор при провале записи не оставляет следа в кэше.
// Мутация для пина КАЖДОЙ строки — вернуть соответствующий мутатор к in-place
// форме (`s.settings.X = v; return s.saveUnlocked(s.settings)`): мутант
// компилируется, строка краснеет.
func TestNarrowMutators_FailedWriteKeepsCache(t *testing.T) {
	cases := []struct {
		name      string
		seed      func(*testing.T, *SettingsStore)
		act       func(*SettingsStore) error
		assertOld func(*testing.T, *Settings)
	}{
		{
			name: "SaveManagedServers",
			act: func(s *SettingsStore) error {
				return s.SaveManagedServers([]ManagedServer{{InterfaceName: "Wireguard3"}})
			},
			assertOld: func(t *testing.T, st *Settings) {
				if len(st.ManagedServers) != 0 {
					t.Errorf("ManagedServers = %+v, мутировал в кэше", st.ManagedServers)
				}
			},
		},
		{
			name: "SetSingboxManuallyStopped",
			act:  func(s *SettingsStore) error { return s.SetSingboxManuallyStopped(true) },
			assertOld: func(t *testing.T, st *Settings) {
				if st.SingboxManuallyStopped {
					t.Error("SingboxManuallyStopped мутировал в кэше")
				}
			},
		},
		{
			name: "SetAuthEnabled",
			act: func(s *SettingsStore) error {
				_, err := s.SetAuthEnabled(true)
				return err
			},
			assertOld: func(t *testing.T, st *Settings) {
				if st.AuthEnabled {
					t.Error("AuthEnabled мутировал в кэше")
				}
			},
		},
		{
			name: "SetSingboxCreateNDMSProxy",
			seed: func(t *testing.T, s *SettingsStore) {
				if err := s.SetSingboxCreateNDMSProxy(true); err != nil {
					t.Fatal(err)
				}
			},
			act: func(s *SettingsStore) error { return s.SetSingboxCreateNDMSProxy(false) },
			assertOld: func(t *testing.T, st *Settings) {
				if !st.CreateNDMSProxyForSingbox {
					t.Error("CreateNDMSProxyForSingbox мутировал в кэше")
				}
			},
		},
		{
			// Дефолт этого флага — true (defaultSettings), поэтому пишем false.
			name: "SetManagedPeerAllowIPsMigrated",
			act:  func(s *SettingsStore) error { return s.SetManagedPeerAllowIPsMigrated(false) },
			assertOld: func(t *testing.T, st *Settings) {
				if !st.ManagedPeerAllowIPsMigrated {
					t.Error("ManagedPeerAllowIPsMigrated мутировал в кэше")
				}
			},
		},
		{
			name: "SetOpkgTunState",
			act: func(s *SettingsStore) error {
				return s.SetOpkgTunState(&OpkgTunState{Mode: OpkgTunModeFakeIP, Index: 3})
			},
			assertOld: func(t *testing.T, st *Settings) {
				if st.OpkgTun != nil {
					t.Errorf("OpkgTun = %+v, появился в кэше при провале записи", st.OpkgTun)
				}
			},
		},
		{
			name: "SetOpkgTunNATSegments",
			seed: func(t *testing.T, s *SettingsStore) {
				if err := s.SetOpkgTunState(&OpkgTunState{Mode: OpkgTunModePolicyTun, Index: 2}); err != nil {
					t.Fatal(err)
				}
			},
			act: func(s *SettingsStore) error {
				return s.SetOpkgTunNATSegments([]PolicyTunNATSegment{{Name: "Home", PriorMode: "dynamic"}})
			},
			assertOld: func(t *testing.T, st *Settings) {
				if st.OpkgTun == nil || st.OpkgTun.PolicyTun != nil {
					t.Errorf("payload NAT = %+v, мутировал в кэше", st.OpkgTun)
				}
			},
		},
		{
			name: "SetDNSChainPresetState",
			act: func(s *SettingsStore) error {
				return s.SetDNSChainPresetState(&DNSChainPresetState{})
			},
			assertOld: func(t *testing.T, st *Settings) {
				if st.DNSChainPreset != nil {
					t.Error("DNSChainPreset появился в кэше при провале записи")
				}
			},
		},
		{
			name: "MarkServerInterface",
			act:  func(s *SettingsStore) error { return s.MarkServerInterface("Wireguard7") },
			assertOld: func(t *testing.T, st *Settings) {
				for _, id := range st.ServerInterfaces {
					if id == "Wireguard7" {
						t.Error("ServerInterfaces мутировал в кэше")
					}
				}
			},
		},
		{
			name: "UnmarkServerInterface",
			seed: func(t *testing.T, s *SettingsStore) {
				if err := s.MarkServerInterface("Wireguard7"); err != nil {
					t.Fatal(err)
				}
			},
			act: func(s *SettingsStore) error { return s.UnmarkServerInterface("Wireguard7") },
			assertOld: func(t *testing.T, st *Settings) {
				found := false
				for _, id := range st.ServerInterfaces {
					if id == "Wireguard7" {
						found = true
					}
				}
				if !found {
					t.Error("ServerInterfaces мутировал в кэше: запись пропала при провале записи")
				}
			},
		},
		{
			name: "AddManagedPolicy",
			act:  func(s *SettingsStore) error { return s.AddManagedPolicy("awgm-policy") },
			assertOld: func(t *testing.T, st *Settings) {
				for _, n := range st.ManagedPolicies {
					if n == "awgm-policy" {
						t.Error("ManagedPolicies мутировал в кэше")
					}
				}
			},
		},
		{
			name: "RemoveManagedPolicy",
			seed: func(t *testing.T, s *SettingsStore) {
				if err := s.AddManagedPolicy("awgm-policy"); err != nil {
					t.Fatal(err)
				}
			},
			act: func(s *SettingsStore) error { return s.RemoveManagedPolicy("awgm-policy") },
			assertOld: func(t *testing.T, st *Settings) {
				found := false
				for _, n := range st.ManagedPolicies {
					if n == "awgm-policy" {
						found = true
					}
				}
				if !found {
					t.Error("ManagedPolicies мутировал в кэше: запись пропала при провале записи")
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			store := NewSettingsStore(dir)
			if _, err := store.Load(); err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.seed != nil {
				c.seed(t, store)
			}
			failWrites(t, dir)
			if err := c.act(store); err == nil {
				t.Fatal("ожидался отказ записи")
			}
			got, err := store.Get()
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			c.assertOld(t, got)
		})
	}
}

// F3-C: мутатор, правящий элементы контейнера ВГЛУБЬ, при провале записи тоже
// не оставляет следа. Ключевой случай — Peers: managed/service_peers.go правит
// элементы по месту, поэтому мелкой копии Settings мало, нужен клон Peers.
func TestUpdateManagedServer_FailedWriteKeepsPeers(t *testing.T) {
	dir := t.TempDir()
	store := NewSettingsStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := store.AddManagedServer(ManagedServer{
		InterfaceName: "Wireguard3",
		Peers:         []ManagedPeer{{PublicKey: "pub1", Description: "было"}},
	}); err != nil {
		t.Fatalf("AddManagedServer: %v", err)
	}
	failWrites(t, dir)

	err := store.UpdateManagedServer("Wireguard3", func(sv *ManagedServer) error {
		sv.Peers[0].Description = "стало" // правка ЭЛЕМЕНТА по месту
		return nil
	})
	if err == nil {
		t.Fatal("ожидался отказ записи")
	}

	got, gerr := store.Get()
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if len(got.ManagedServers) != 1 || len(got.ManagedServers[0].Peers) != 1 {
		t.Fatalf("ManagedServers = %+v", got.ManagedServers)
	}
	if d := got.ManagedServers[0].Peers[0].Description; d != "было" {
		t.Errorf("Peers[0].Description = %q, want %q (правка по месту утекла в кэш)", d, "было")
	}
}

// Тот же класс для карты секретов: провал записи не должен оставлять запись.
func TestSetServerPeerSecret_FailedWriteKeepsMap(t *testing.T) {
	dir := t.TempDir()
	store := NewSettingsStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Карта обязана быть НЕПУСТОЙ: на nil-карте мутатор создаёт новую и кладёт
	// её в копию, поэтому отсутствие клона так не ловится.
	if err := store.SetServerPeerSecret("Wireguard3", "pub1", ServerPeerSecret{PrivateKey: "k1"}); err != nil {
		t.Fatalf("seed SetServerPeerSecret: %v", err)
	}
	failWrites(t, dir)

	if err := store.SetServerPeerSecret("Wireguard3", "pub2", ServerPeerSecret{PrivateKey: "k2"}); err == nil {
		t.Fatal("ожидался отказ записи")
	}
	got, err := store.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := got.ServerPeerSecrets["Wireguard3"]["pub2"]; ok {
		t.Errorf("ServerPeerSecrets = %+v, запись появилась при провале записи", got.ServerPeerSecrets)
	}
	if _, ok := got.ServerPeerSecrets["Wireguard3"]["pub1"]; !ok {
		t.Error("прежний секрет пропал из кэша")
	}
}

// Остальные контейнерные мутаторы того же класса: без клона их правка уходит в
// ЖИВОЙ кэш ещё до записи. Ловится только на НЕПУСТОМ контейнере — на nil/пустом
// мутатор заводит новый и кладёт его в копию.
func TestContainerMutators_FailedWriteKeepsCache(t *testing.T) {
	t.Run("DeleteManagedServer не сдвигает разделяемый массив", func(t *testing.T) {
		dir := t.TempDir()
		store := NewSettingsStore(dir)
		if _, err := store.Load(); err != nil {
			t.Fatalf("Load: %v", err)
		}
		for _, name := range []string{"Wireguard1", "Wireguard2", "Wireguard3"} {
			if err := store.AddManagedServer(ManagedServer{InterfaceName: name}); err != nil {
				t.Fatalf("AddManagedServer %s: %v", name, err)
			}
		}
		failWrites(t, dir)

		if err := store.DeleteManagedServer("Wireguard2"); err == nil {
			t.Fatal("ожидался отказ записи")
		}
		got, err := store.Get()
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		var names []string
		for _, sv := range got.ManagedServers {
			names = append(names, sv.InterfaceName)
		}
		want := []string{"Wireguard1", "Wireguard2", "Wireguard3"}
		if !reflect.DeepEqual(names, want) {
			t.Errorf("ManagedServers = %v, want %v (сдвиг по месту утёк в кэш)", names, want)
		}
	})

	t.Run("UpdateServerInterfaceMeta не правит живую карту", func(t *testing.T) {
		dir := t.TempDir()
		store := NewSettingsStore(dir)
		if _, err := store.Load(); err != nil {
			t.Fatalf("Load: %v", err)
		}
		if err := store.UpdateServerInterfaceMeta("Wireguard1", func(m *ServerInterfaceMeta) error {
			m.Endpoint = "было"
			return nil
		}); err != nil {
			t.Fatalf("seed UpdateServerInterfaceMeta: %v", err)
		}
		failWrites(t, dir)

		if err := store.UpdateServerInterfaceMeta("Wireguard1", func(m *ServerInterfaceMeta) error {
			m.Endpoint = "стало"
			return nil
		}); err == nil {
			t.Fatal("ожидался отказ записи")
		}
		got, err := store.Get()
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if d := got.ServerInterfaceMeta["Wireguard1"].Endpoint; d != "было" {
			t.Errorf("Endpoint = %q, want %q (правка карты утекла в кэш)", d, "было")
		}
	})

	t.Run("DeleteServerPeerSecret не правит живую карту", func(t *testing.T) {
		dir := t.TempDir()
		store := NewSettingsStore(dir)
		if _, err := store.Load(); err != nil {
			t.Fatalf("Load: %v", err)
		}
		for _, pub := range []string{"pub1", "pub2"} {
			if err := store.SetServerPeerSecret("Wireguard1", pub, ServerPeerSecret{PrivateKey: pub}); err != nil {
				t.Fatalf("seed SetServerPeerSecret %s: %v", pub, err)
			}
		}
		failWrites(t, dir)

		if err := store.DeleteServerPeerSecret("Wireguard1", "pub2"); err == nil {
			t.Fatal("ожидался отказ записи")
		}
		got, err := store.Get()
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if _, ok := got.ServerPeerSecrets["Wireguard1"]["pub2"]; !ok {
			t.Errorf("ServerPeerSecrets = %+v, секрет пропал из кэша при провале записи", got.ServerPeerSecrets)
		}
	})
}
