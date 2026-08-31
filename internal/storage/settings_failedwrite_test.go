package storage

import (
	"os"
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
