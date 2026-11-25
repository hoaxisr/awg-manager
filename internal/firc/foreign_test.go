package firc

import (
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if target, ok := strings.CutPrefix(content, "-> "); ok {
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// foreignTree — роутер, где стоит пакет firc: формат файлов его, а рядом лежат
// остатки движка панели, которые на старте раньше переписывали groups.yaml.
func foreignTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	groups := filepath.Join(root, "opt/etc/firc/groups.yaml")
	writeTree(t, root, map[string]string{
		"opt/bin/fircd":                           "elf-fircd",
		"opt/bin/tunvless":                        "elf-tunvless",
		"opt/etc/init.d/S99firc":                  "#!/bin/sh\necho \"$1\" >> \"$0.ran\"\n",
		"opt/etc/ndm/netfilter.d/100-firc":        "#!/bin/sh\nexit 0\n",
		"opt/etc/ndm/iflayerchanged.d/100-firc":   "-> ../netfilter.d/100-firc",
		"opt/etc/firc/firc.conf":                  "configVersion: 0.7.0\napp:\n  httpWeb:\n    enabled: true\n    host:\n      address: \"[::]\"\n      port: 666\n",
		"opt/etc/firc/tunnels.yaml":               "tunnels:\n- id: aaaa0001\n  device: tunvless0\n  enable: true\n  failback: true\n",
		"opt/etc/firc/groups.yaml":                "groups:\n- id: bbbb0001\n  name: Foreign\n  enable: true\n  interface: tunvless0\n  rules: []\n",
		"opt/etc/firc/awgm-ext.json":              `{"serverRouting":{"enabledServers":["vless_xhttp_server"]}}`,
		"opt/etc/firc/.awgm-uplink-inbounds.json": `{"aaaa0001":2100}`,
		"opt/etc/firc/recovery-state.json":        `{"required":true,"reason":"old switch","timestampUnixMs":1}`,
		"opt/etc/firc/migration-journal.json": fmt.Sprintf(
			`{"version":1,"journalId":"j1","operation":"migrate_from_singbox","stage":"writing_files","backups":[{"path":%q,"exists":true,"mode":420,"dataB64":"Z3JvdXBzOiBbXQo="}]}`, groups),
		"opt/etc/firc/sr-tx/tx-live/manifest.json":             `{"tx_id":"tx-live","server_id":"vless_xhttp_server","prev_ext":{},"candidate_ext":{}}`,
		"opt/etc/firc/sr-tx/tx-orphan/awgm-ext.json.bak":       "{}",
		"opt/etc/firc/snapshots/snap-20261101-000000-a.json":   `{"id":"snap-20261101-000000-a","files":[]}`,
		"opt/var/lib/firc/hwid":                                "hwid\n",
		"opt/var/lib/firc/tunnels/aaaa0001.json":               "{}",
		"opt/lib/opkg/info/firc.list":                          "/opt/bin/fircd\n/opt/bin/tunvless\n/opt/etc/init.d/S99firc\n",
		"opt/etc/awg-manager/settings.json":                    `{"singboxRouter":{"routingEngine":"firc"}}`,
		"opt/etc/awg-manager/singbox/config.d/00-base.json":    `{"log":{"level":"info"}}`,
		"opt/etc/awg-manager/singbox/config.d/10-tunnels.json": `{"outbounds":[{"type":"vless","tag":"sb1","server":"192.0.2.1","server_port":443,"uuid":"11111111-1111-1111-1111-111111111111"}]}`,
	})
	return root
}

// treeDigest — всё дерево побайтово, вместе с правами и отметками времени:
// перезапись теми же байтами и временный файл в каталоге тоже следы.
func treeDigest(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		body := ""
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			body, err = os.Readlink(p)
		case info.Mode().IsRegular():
			var raw []byte
			raw, err = os.ReadFile(p)
			body = string(raw)
		}
		if err != nil {
			return err
		}
		out[strings.TrimPrefix(p, root)] = fmt.Sprintf("%v %d %s", info.Mode(), info.ModTime().UnixNano(), body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertTreeUntouched(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	for p, v := range after {
		if before[p] != v {
			t.Errorf("%s: изменён %s:\n  было  %q\n  стало %q", what, p, before[p], v)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			t.Errorf("%s: удалён %s", what, p)
		}
	}
}

// foreignProbe следит за тем, что не видно по дереву: обращения к сокету демона
// и команды iptables/ipset.
type foreignProbe struct {
	hits atomic.Int32
	mu   sync.Mutex
	cmds []string
}

func (p *foreignProbe) assertForeignRulesUntouched(t *testing.T, what string) {
	t.Helper()
	if n := p.hits.Load(); n != 0 {
		t.Errorf("%s: %d обращений к сокету чужого fircd", what, n)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.cmds {
		if strings.Contains(c, " FIRC_") {
			t.Errorf("%s: команда трогает правила стороннего firc: %s", what, c)
		}
	}
}

func newForeignOperator(t *testing.T, root string) (*Operator, *foreignProbe) {
	t.Helper()
	probe := &foreignProbe{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probe.hits.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	record := func(name string, args []string) {
		probe.mu.Lock()
		probe.cmds = append(probe.cmds, name+" "+strings.Join(args, " "))
		probe.mu.Unlock()
	}
	SetExtIptablesRunner(func(_ context.Context, args ...string) ([]byte, error) {
		record("iptables", args)
		return nil, errors.New("no such rule")
	})
	t.Cleanup(func() { SetExtIptablesRunner(nil) })
	SetExtIpsetRunner(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		record("ipset", args)
		return []byte("FIRC_bbbb0001S4\n"), nil
	})
	t.Cleanup(func() { SetExtIpsetRunner(nil) })

	op := NewOperator(Options{
		ConfigDir:   filepath.Join(root, "opt/etc/firc"),
		FircdBinary: filepath.Join(root, "opt/bin/fircd"),
		TunvlessBin: filepath.Join(root, "opt/bin/tunvless"),
		InitScript:  filepath.Join(root, "opt/etc/init.d/S99firc"),
		Foreign:     NewForeignDetector(root),
	})
	op.SetVarLibDir(filepath.Join(root, "opt/var/lib/firc"))
	op.SetClient(NewHTTPClient(srv.URL, srv.Client()))
	op.SetSingboxValidator(func(context.Context, string) error { return nil })
	return op, probe
}

func TestForeignDetector(t *testing.T) {
	ownList := "/opt/sbin/fircd\n/opt/bin/fircd\n/opt/sbin/tunvless\n/opt/etc/init.d/S99firc\n"
	cases := []struct {
		name  string
		files map[string]string
		want  *ForeignFirc
		err   bool
	}{
		{"пусто", nil, nil, false},
		{"остатки init-скрипта без fircd", map[string]string{"opt/etc/init.d/S99firc": "#!/bin/sh\n"}, nil, false},
		{"пакет firc", map[string]string{"opt/lib/opkg/info/firc.list": "/opt/bin/fircd\n"}, &ForeignFirc{Package: "firc"}, false},
		{"пакет tunvless", map[string]string{"opt/lib/opkg/info/tunvless.list": "/opt/bin/tunvless\n"}, &ForeignFirc{Package: "tunvless"}, false},
		{"посторонний пакет", map[string]string{"opt/lib/opkg/info/curl.list": "/opt/bin/curl\n"}, nil, false},
		{"бинарь руками в /opt/bin", map[string]string{"opt/bin/fircd": "elf"}, &ForeignFirc{Binary: "/opt/bin/fircd"}, false},
		{"бинарь руками в /opt/sbin", map[string]string{"opt/sbin/fircd": "elf"}, &ForeignFirc{Binary: "/opt/sbin/fircd"}, false},
		{"ссылка в сторону", map[string]string{"opt/home/fircd": "elf", "opt/bin/fircd": "-> ../home/fircd"}, &ForeignFirc{Binary: "/opt/bin/fircd"}, false},
		{"висячая ссылка", map[string]string{"opt/bin/fircd": "-> ../sbin/fircd"}, nil, false},
		{"наш рантайм", map[string]string{
			"opt/lib/opkg/info/awg-firc-runtime.list": ownList,
			"opt/sbin/fircd": "elf", "opt/bin/fircd": "-> ../sbin/fircd", "opt/sbin/tunvless": "elf",
		}, nil, false},
		{"наш рантайм, список с режимами", map[string]string{
			"opt/lib/opkg/info/awg-firc-runtime.list": "/opt/sbin/fircd\t0100755\n/opt/bin/fircd\t0120777\t../sbin/fircd\n",
			"opt/sbin/fircd": "elf", "opt/bin/fircd": "-> ../sbin/fircd",
		}, nil, false},
		{"рантайм внутри пакета awg-manager", map[string]string{
			"opt/lib/opkg/info/awg-manager.list": ownList,
			"opt/sbin/fircd":                     "elf", "opt/bin/fircd": "-> ../sbin/fircd",
		}, nil, false},
		{"наш рантайм и чужой бинарь поверх ссылки", map[string]string{
			"opt/lib/opkg/info/awg-firc-runtime.list": ownList,
			"opt/sbin/fircd": "elf", "opt/bin/fircd": "other",
		}, &ForeignFirc{Binary: "/opt/bin/fircd"}, false},
		{"наш рантайм и пакет firc", map[string]string{
			"opt/lib/opkg/info/awg-firc-runtime.list": ownList,
			"opt/lib/opkg/info/firc.list":             "/opt/bin/fircd\n",
			"opt/sbin/fircd":                          "elf", "opt/bin/fircd": "other",
		}, &ForeignFirc{Package: "firc"}, false},
		{"прерванная установка нашего рантайма", map[string]string{
			"opt/etc/awg-manager/firc-runtime-operation.json": `{"action":"install","pending":true}`,
			"opt/sbin/fircd": "elf", "opt/bin/fircd": "-> ../sbin/fircd",
		}, nil, false},
		{"завершённая операция чужой бинарь не прикрывает", map[string]string{
			"opt/etc/awg-manager/firc-runtime-operation.json": `{"action":"uninstall","pending":false}`,
			"opt/sbin/fircd": "elf",
		}, &ForeignFirc{Binary: "/opt/sbin/fircd"}, false},
		{"нечитаемый список пакета", map[string]string{"opt/lib/opkg/info/x.list/y": "z"}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, tc.files)
			got, err := NewForeignDetector(root).Detect()
			if (err != nil) != tc.err || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Detect() = %+v, %v; want %+v, err=%v", got, err, tc.want, tc.err)
			}
		})
	}
	if got, err := (*ForeignDetector)(nil).Detect(); got != nil || err != nil {
		t.Fatalf("nil detector: %+v, %v", got, err)
	}
}

// Нечитаемый владелец файлов — отказ, а не «стороннего нет».
func TestForeignDetector_UnknownOwnerRefuses(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"opt/lib/opkg/info/x.list/y": "z"})
	op := NewOperator(Options{ConfigDir: filepath.Join(root, "opt/etc/firc"), Foreign: NewForeignDetector(root)})
	if err := op.SaveGroupsFile(nil); err == nil || errors.Is(err, ErrForeignFirc) {
		t.Fatalf("SaveGroupsFile = %v, want отказ проверки владельца", err)
	}
	if _, err := os.Stat(op.GroupsPath()); !os.IsNotExist(err) {
		t.Fatal("groups.yaml записан при неизвестном владельце")
	}
}

// Кэш не должен пережить установку и снятие пакета: свежесть отметок времени
// снята нарочно, чтобы проверялся именно ключ кэша.
func TestForeignDetector_CacheFollowsInstallAndRemoval(t *testing.T) {
	root := t.TempDir()
	info := filepath.Join(root, "opt/lib/opkg/info")
	writeTree(t, root, map[string]string{"opt/lib/opkg/info/curl.list": "/opt/bin/curl\n"})
	age := func(d time.Duration) {
		t.Helper()
		at := time.Now().Add(-d)
		if err := os.Chtimes(info, at, at); err != nil {
			t.Fatal(err)
		}
	}
	d := NewForeignDetector(root)
	age(time.Hour)
	if got, err := d.Detect(); got != nil || err != nil || !d.cached {
		t.Fatalf("до установки: %+v, %v, cached=%v", got, err, d.cached)
	}
	writeTree(t, root, map[string]string{"opt/lib/opkg/info/firc.list": "/opt/bin/fircd\n"})
	age(30 * time.Minute)
	if got, _ := d.Detect(); got == nil || got.Package != "firc" || !d.cached {
		t.Fatalf("после установки: %+v", got)
	}
	if err := os.Remove(filepath.Join(info, "firc.list")); err != nil {
		t.Fatal(err)
	}
	age(10 * time.Minute)
	if got, _ := d.Detect(); got != nil {
		t.Fatalf("после снятия: %+v", got)
	}
	// Свежая отметка не кэшируется: вторая правка в тот же такт часов её не сдвинет.
	age(0)
	if _, _ = d.Detect(); d.cached {
		t.Fatal("результат со свежей отметкой времени попал в кэш")
	}
}

func TestForeignFirc_WebPortOnlyWhenConfigSaysSo(t *testing.T) {
	web := func(enabled, host string) string {
		return "app:\n  httpWeb:\n" + enabled + "    host:\n" + host
	}
	on := "    enabled: true\n"
	for _, tc := range []struct {
		name, conf string
		port       int
		host       string
	}{
		{"штатный конфиг", web(on, "      address: \"[::]\"\n      port: 666\n"), 666, ""},
		{"все адреса v4", web(on, "      address: 0.0.0.0\n      port: 8880\n"), 8880, ""},
		{"конкретный адрес", web(on, "      address: 192.168.1.1\n      port: 8880\n"), 8880, "192.168.1.1"},
		{"только loopback", web(on, "      address: 127.0.0.1\n      port: 8880\n"), 0, ""},
		{"localhost", web(on, "      address: localhost\n      port: 8880\n"), 0, ""},
		{"имя", web(on, "      address: router.lan\n      port: 8880\n"), 8880, "router.lan"},
		{"чужой URL в адресе", web(on, "      address: \"evil.example/@192.168.1.1\"\n      port: 8880\n"), 0, ""},
		{"выключен", web("    enabled: false\n", "      port: 666\n"), 0, ""},
		{"включение не указано", web("", "      port: 666\n"), 0, ""},
		{"порт не указан", web(on, "      address: 0.0.0.0\n"), 0, ""},
		{"порт вне диапазона", web(on, "      port: 70000\n"), 0, ""},
		{"не YAML", "app: [", 0, ""},
		{"нет файла", "", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			files := map[string]string{"opt/lib/opkg/info/firc.list": "/opt/bin/fircd\n"}
			if tc.conf != "" {
				files["opt/etc/firc/firc.conf"] = tc.conf
			}
			writeTree(t, root, files)
			op := NewOperator(Options{ConfigDir: t.TempDir(), Foreign: NewForeignDetector(root)})
			got := op.ForeignFirc()
			if got == nil || got.Package != "firc" || got.WebPort != tc.port || got.WebHost != tc.host {
				t.Fatalf("ForeignFirc() = %+v, want port=%d host=%q", got, tc.port, tc.host)
			}
		})
	}
}

func TestForeignWebHost(t *testing.T) {
	for addr, want := range map[string]string{
		"192.168.1.1": "192.168.1.1", "fd00::1": "fd00::1", "[fd00::1]": "fd00::1", "router.lan": "router.lan",
		"firc-box": "firc-box", "": "", "0.0.0.0": "", "::": "", "[::]": "",
	} {
		if host, ok := foreignWebHost(addr); !ok || host != want {
			t.Errorf("foreignWebHost(%q) = %q, %v; want %q", addr, host, ok, want)
		}
	}
	for _, addr := range []string{
		"localhost", "LOCALHOST", "a.localhost", "localhost.", "127.0.0.1", "127.8.8.8", "::1", "[::1]", "::ffff:127.0.0.1",
		"127.1", "0x7f.1", "2130706433", "0x7f000001", "fe80::1", "fe80::1%br0", "192.168.1.1%eth0", "[fd00::1", "fd00::1]",
		"user@host", "host:80", "host/path", "host?x", "host#x", "javascript:alert(1)", "a b", "хост", "a\\b", "-", "a..b", "224.0.0.1",
	} {
		if host, ok := foreignWebHost(addr); ok {
			t.Errorf("foreignWebHost(%q) принят как %q", addr, host)
		}
	}
}

// foreignMutators — каждый публичный метод оператора, который что-то меняет.
// Мутация «снять проверку у метода» роняет тест либо отсутствием ErrForeignFirc,
// либо следом в дереве, на сокете или в iptables.
func foreignMutators(sbDir string, inst *OnDemandInstaller) map[string]func(context.Context, *Operator) error {
	e := func(_ any, err error) error { return err }
	groups := []Group{{ID: "cccc0001", Name: "Ours", Enable: true, Interface: GroupInterface{"br0"}}}
	tunnels := []Tunnel{{ID: "t1", Device: "tunvless9", Enable: true}}
	return map[string]func(context.Context, *Operator) error{
		"AddConfirmedCandidate": func(c context.Context, o *Operator) error {
			_, _, err := o.AddConfirmedCandidate(c, "example.com", "bbbb0001")
			return err
		},
		"ApplyExtConfig":                 func(c context.Context, o *Operator) error { return e(o.ApplyExtConfig(c, ExtConfig{})) },
		"ApplyGroups":                    func(c context.Context, o *Operator) error { return e(o.ApplyGroups(c, groups)) },
		"ApplyGroupsWithLinks":           func(c context.Context, o *Operator) error { return e(o.ApplyGroupsWithLinks(c, groups, nil)) },
		"ApplyTunnels":                   func(c context.Context, o *Operator) error { return e(o.ApplyTunnels(c, tunnels, true)) },
		"CheckRuntimeInstalledForSwitch": func(_ context.Context, o *Operator) error { return o.CheckRuntimeInstalledForSwitch() },
		"ClearRecovery":                  func(_ context.Context, o *Operator) error { return o.ClearRecovery() },
		"CommitServerRouting":            func(c context.Context, o *Operator) error { return o.CommitServerRouting(c, "tx-orphan") },
		"CreateSystemSnapshot":           func(_ context.Context, o *Operator) error { return e(o.CreateSystemSnapshot(sbDir, "", "", "firc")) },
		"DeleteSystemSnapshot":           func(_ context.Context, o *Operator) error { return o.DeleteSystemSnapshot("snap-20261101-000000-a") },
		"EnsureConfigFiles":              func(_ context.Context, o *Operator) error { return o.EnsureConfigFiles() },
		"ForceClearRecoveryAdmin": func(_ context.Context, o *Operator) error {
			return o.ForceClearRecoveryAdmin("FORCE_CLEAR_UNVERIFIED", BreakGlassOptions{Actor: "test"})
		},
		"MigrateFromSingbox": func(c context.Context, o *Operator) error { return e(o.MigrateFromSingbox(c, sbDir)) },
		"MigrateFromSingboxWithOptions": func(c context.Context, o *Operator) error {
			return e(o.MigrateFromSingboxWithOptions(c, sbDir, &MigrationOptions{Mode: "replace"}))
		},
		"PrepareServerRouting": func(c context.Context, o *Operator) error {
			return e(o.PrepareServerRouting(c, "tx-new", "vless_xhttp_server", true))
		},
		"PurgeFircAndTunnels": func(c context.Context, o *Operator) error {
			return e(o.PurgeFircAndTunnels(c, sbDir, "sing-box", inst, nil, EnginePurgeHooks{}))
		},
		"PurgeSingboxAndTunnels": func(c context.Context, o *Operator) error {
			return e(o.PurgeSingboxAndTunnels(c, sbDir, "firc", nil, EnginePurgeHooks{
				PurgeSingbox: func(context.Context) (int, int, error) {
					return 0, 0, errors.New("дошли до удаления sing-box")
				},
			}))
		},
		"ReconcileRecovery": func(c context.Context, o *Operator) error { return e(o.ReconcileRecovery(c, "firc")) },
		"RestoreSavedSingboxFiles": func(c context.Context, o *Operator) error {
			return e(o.RestoreSavedSingboxFiles(c, sbDir, true, "firc"))
		},
		"RestoreSystemSnapshot": func(c context.Context, o *Operator) error {
			return e(o.RestoreSystemSnapshot(c, "snap-20261101-000000-a", sbDir))
		},
		"RestoreSystemSnapshotWithRollback": func(c context.Context, o *Operator) error {
			_, _, err := o.RestoreSystemSnapshotWithRollback(c, "snap-20261101-000000-a", sbDir, true)
			return err
		},
		"ReverseMigrateToSingbox": func(c context.Context, o *Operator) error {
			return e(o.ReverseMigrateToSingbox(c, sbDir, &ReverseMigrationOptions{}, "firc"))
		},
		"SaveExtConfig": func(_ context.Context, o *Operator) error {
			return o.SaveExtConfig(ExtConfig{GlobalTarget: "iface:br0"})
		},
		"SaveGroupsFile": func(_ context.Context, o *Operator) error { return o.SaveGroupsFile(groups) },
		"SaveMigrationWizardState": func(_ context.Context, o *Operator) error {
			return o.SaveMigrationWizardState(MigrationWizardState{LastDirection: "x"})
		},
		"SavePreFircSingboxConfig": func(_ context.Context, o *Operator) error { return o.SavePreFircSingboxConfig(sbDir, true) },
		"SaveTunnelsFile":          func(_ context.Context, o *Operator) error { return o.SaveTunnelsFile(tunnels) },
		"SetRecoveryRequired":      func(_ context.Context, o *Operator) error { return o.SetRecoveryRequired("x") },
		"SetRecoveryRequiredWithMeta": func(_ context.Context, o *Operator) error {
			return o.SetRecoveryRequiredWithMeta(RecoveryMarker{Reason: "x"})
		},
		"StageExtConfigOffline": func(c context.Context, o *Operator) error {
			return e(o.StageExtConfigOffline(c, ExtConfig{GlobalTarget: "iface:br0"}))
		},
		"StageGroupsOffline": func(_ context.Context, o *Operator) error { return e(o.StageGroupsOffline(groups)) },
		"StageGroupsOfflineWithLinks": func(_ context.Context, o *Operator) error {
			return e(o.StageGroupsOfflineWithLinks(groups, map[string]string{}))
		},
		"StageTunnelsOffline": func(_ context.Context, o *Operator) error { return e(o.StageTunnelsOffline(tunnels, true)) },
		"Start":               func(c context.Context, o *Operator) error { return o.Start(c) },
		"StartByUser":         func(c context.Context, o *Operator) error { return o.StartByUser(c) },
		"StopByUser":          func(c context.Context, o *Operator) error { return o.StopByUser(c) },
		"SwitchEngine":        func(c context.Context, o *Operator) error { return o.SwitchEngine(c, "firc") },
		"SwitchEngineWithOptions": func(c context.Context, o *Operator) error {
			return o.SwitchEngineWithOptions(c, "firc", EngineSwitchOptions{AuthorizeDisabledStart: true})
		},
		"SyncGroups":            func(c context.Context, o *Operator) error { return e(o.SyncGroups(c, groups)) },
		"SyncGroupsRuntimeOnly": func(c context.Context, o *Operator) error { return e(o.SyncGroupsRuntimeOnly(c, groups)) },
		"SyncKeenDNSCloud":      func(c context.Context, o *Operator) error { return o.SyncKeenDNSCloud(c, true) },
		"SyncTunnels":           func(c context.Context, o *Operator) error { return e(o.SyncTunnels(c, tunnels, true)) },
		"ValidateBinaries":      func(_ context.Context, o *Operator) error { return o.ValidateBinaries() },
	}
}

// foreignSilent — методы, которым при стороннем отказывать нечем (без ошибки или
// «нечего делать»), но и следов они оставить не должны.
func foreignSilent(sbDir string) map[string]func(context.Context, *Operator) {
	return map[string]func(context.Context, *Operator){
		"CheckDomainRouting":               func(c context.Context, o *Operator) { _, _ = o.CheckDomainRouting(c, "") },
		"CheckDomainsRouting":              func(c context.Context, o *Operator) { o.CheckDomainsRouting(c, nil) },
		"CleanupPoolRejectBarrier":         func(c context.Context, o *Operator) { o.CleanupPoolRejectBarrier(c) },
		"EngageCutoverBarrier":             func(c context.Context, o *Operator) { _ = o.EngageCutoverBarrier(c) },
		"FinalizeServerRouting":            func(_ context.Context, o *Operator) { _ = o.FinalizeServerRouting("tx-orphan") },
		"GetMonitorSnapshot":               func(c context.Context, o *Operator) { o.GetMonitorSnapshot(c, true) },
		"GetOfflineMonitorSnapshot":        func(c context.Context, o *Operator) { o.GetOfflineMonitorSnapshot(c) },
		"GetOfflineStatus":                 func(_ context.Context, o *Operator) { o.GetOfflineStatus(false) },
		"GetSavedSingboxStatus":            func(_ context.Context, o *Operator) { o.GetSavedSingboxStatus(sbDir) },
		"GetStatus":                        func(c context.Context, o *Operator) { o.GetStatus(c, true) },
		"GetSystemSnapshotMeta":            func(_ context.Context, o *Operator) { _, _ = o.GetSystemSnapshotMeta("snap-20261101-000000-a") },
		"IsRunning":                        func(c context.Context, o *Operator) { o.IsRunning(c) },
		"IsServerRoutingEnabled":           func(_ context.Context, o *Operator) { o.IsServerRoutingEnabled("vless_xhttp_server") },
		"ListAvailableServers":             func(c context.Context, o *Operator) { o.ListAvailableServers(c) },
		"ListSystemSnapshots":              func(_ context.Context, o *Operator) { _, _ = o.ListSystemSnapshots() },
		"LoadExtConfig":                    func(_ context.Context, o *Operator) { o.LoadExtConfig() },
		"LoadExtConfigWithStatus":          func(c context.Context, o *Operator) { o.LoadExtConfigWithStatus(c) },
		"LoadGroupsFile":                   func(_ context.Context, o *Operator) { _, _ = o.LoadGroupsFile() },
		"LoadMigrationWizardState":         func(_ context.Context, o *Operator) { o.LoadMigrationWizardState() },
		"LoadTunnelsFile":                  func(_ context.Context, o *Operator) { _, _ = o.LoadTunnelsFile() },
		"PreviewFromSingbox":               func(_ context.Context, o *Operator) { _, _ = o.PreviewFromSingbox(sbDir) },
		"PreviewFromSingboxWithOptions":    func(_ context.Context, o *Operator) { _, _ = o.PreviewFromSingboxWithOptions(sbDir, nil) },
		"PreviewReverseToSingbox":          func(c context.Context, o *Operator) { _, _ = o.PreviewReverseToSingbox(c, sbDir, nil) },
		"PreviewReverseToSingboxOffline":   func(c context.Context, o *Operator) { _, _ = o.PreviewReverseToSingboxOffline(c, sbDir, nil) },
		"ReconcileExt":                     func(c context.Context, o *Operator) { _ = o.ReconcileExt(c) },
		"ReconcileExtNetfilter":            func(c context.Context, o *Operator) { o.ReconcileExtNetfilter(c, ExtConfig{}, nil) },
		"ReconcileExtNetfilterStrict":      func(c context.Context, o *Operator) { _ = o.ReconcileExtNetfilterStrict(c, ExtConfig{}, nil) },
		"RecordMigrationPostAction":        func(_ context.Context, o *Operator) { o.RecordMigrationPostAction("switch_to_singbox") },
		"RecoverIncompleteServerRoutingTx": func(c context.Context, o *Operator) { _ = o.RecoverIncompleteServerRoutingTx(c) },
		"ReleaseCutoverBarrier":            func(c context.Context, o *Operator) { _ = o.ReleaseCutoverBarrier(c) },
		"Reload":                           func(_ context.Context, o *Operator) { _ = o.Reload() },
		"RollbackServerRouting":            func(c context.Context, o *Operator) { _ = o.RollbackServerRouting(c, "tx-live") },
		"RuntimeDependents":                func(_ context.Context, o *Operator) { _, _ = o.RuntimeDependents() },
		"Stop":                             func(c context.Context, o *Operator) { _ = o.Stop(c) },
		"StopDirectChild":                  func(_ context.Context, o *Operator) { o.StopDirectChild() },
		"VerifyLiveSyncedTunnels":          func(c context.Context, o *Operator) { _ = o.VerifyLiveSyncedTunnels(c, []Tunnel{{ID: "t1"}}) },
	}
}

// foreignInert — методы без доступа к диску, сети и ядру.
var foreignInert = map[string]bool{
	"CheckNotForeign": true, "Client": true, "ConfigDir": true, "ConfigPath": true, "DNSProxyEndpoint": true,
	"DefaultEngineHooks": true, "ForeignFirc": true, "GroupsPath": true, "HasProductionEngineHooks": true,
	"InboundPort": true, "IsRecoveryRequired": true, "KeenCloudProvider": true, "LockRuntimeMutation": true,
	"ManuallyDisabled": true, "MigrationJournalPath": true, "RecoveryInfo": true, "RecoveryMarkerInfo": true,
	"RecoveryMarkerPath": true, "RecoveryReason": true, "RecoveryTombstonePath": true, "TunnelsPath": true,
	"SetClient": true, "SetDefaultEngineHooks": true, "SetDirSyncHook": true, "SetForceKernelNetfilter": true,
	"SetGroupsSaveFaultHook": true, "SetKeenCloudProvider": true, "SetLifecycleHooks": true, "SetManagedUplinkConfig": true,
	"SetMigrationJournalCommitFaultHooks": true, "SetMigrationStepHook": true, "SetNetfilterStrictHook": true,
	"SetOnRuntimeChange": true, "SetRecoveryClearFaultHooks": true, "SetRouterPoliciesProvider": true,
	"SetServerRoutingTxFaultHooks": true, "SetServerStatusCallbacks": true, "SetSingboxBinary": true,
	"SetSingboxStageCopyHook": true, "SetSingboxValidator": true, "SetVarLibDir": true, "SetWGServersProvider": true,
}

// Новый публичный метод оператора обязан быть отнесён к одному из трёх видов —
// иначе он мог бы оказаться обходом отказа, о котором никто не подумал.
func TestForeignFirc_EveryOperatorMethodClassified(t *testing.T) {
	mut, silent := foreignMutators("", nil), foreignSilent("")
	typ := reflect.TypeOf(&Operator{})
	for i := range typ.NumMethod() {
		name := typ.Method(i).Name
		n := 0
		if _, ok := mut[name]; ok {
			n++
		}
		if _, ok := silent[name]; ok {
			n++
		}
		if foreignInert[name] {
			n++
		}
		if n != 1 {
			t.Errorf("метод Operator.%s отнесён к %d видам, нужен ровно один", name, n)
		}
	}
}

func TestForeignFirc_OperatorRefusesAndLeavesNoTrace(t *testing.T) {
	ctx := context.Background()
	for name := range foreignMutators("", nil) {
		t.Run("отказ/"+name, func(t *testing.T) {
			root := foreignTree(t)
			sbDir := filepath.Join(root, "opt/etc/awg-manager/singbox/config.d")
			before := treeDigest(t, root)
			op, probe := newForeignOperator(t, root)
			op.SetForceKernelNetfilter(true)
			inst := NewOnDemandInstaller("mipsel-3.4", FircBinarySpec{})
			inst.SetPackageLayout(DefaultPackageLayout(root))
			inst.SetPackageManager(&mockRuntimePackages{root: root})
			if err := foreignMutators(sbDir, inst)[name](ctx, op); !errors.Is(err, ErrForeignFirc) {
				t.Errorf("%s = %v, want ErrForeignFirc", name, err)
			}
			assertTreeUntouched(t, name, before, treeDigest(t, root))
			probe.assertForeignRulesUntouched(t, name)
		})
	}
	for name := range foreignSilent("") {
		t.Run("без следа/"+name, func(t *testing.T) {
			root := foreignTree(t)
			sbDir := filepath.Join(root, "opt/etc/awg-manager/singbox/config.d")
			before := treeDigest(t, root)
			op, probe := newForeignOperator(t, root)
			op.SetForceKernelNetfilter(true)
			foreignSilent(sbDir)[name](ctx, op)
			assertTreeUntouched(t, name, before, treeDigest(t, root))
			probe.assertForeignRulesUntouched(t, name)
		})
	}
}

// Контроль к тесту выше: без стороннего те же вызовы след оставляют, то есть
// слежка за деревом, сокетом и правилами не холостая.
func TestForeignFirc_ControlWithoutForeignLeavesTrace(t *testing.T) {
	root := foreignTree(t)
	for _, rel := range []string{"opt/lib/opkg/info/firc.list", "opt/bin/fircd"} {
		if err := os.Remove(filepath.Join(root, rel)); err != nil {
			t.Fatal(err)
		}
	}
	before := treeDigest(t, root)
	op, probe := newForeignOperator(t, root)
	after := treeDigest(t, root)
	if before["/opt/etc/firc/groups.yaml"] == after["/opt/etc/firc/groups.yaml"] {
		t.Error("журнал миграции на старте должен откатить groups.yaml")
	}
	if !op.IsRunning(context.Background()) || probe.hits.Load() == 0 {
		t.Error("пинг демона должен доходить до сокета")
	}
	_ = op.Stop(context.Background())
	if _, err := os.Stat(filepath.Join(root, "opt/etc/init.d/S99firc.ran")); err != nil {
		t.Errorf("Stop должен звать init-скрипт: %v", err)
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if !strings.Contains(strings.Join(probe.cmds, "\n"), "ipset destroy FIRC_") {
		t.Error("Stop должен чистить ipset нашего рантайма")
	}
}

// Внутренние записи, до которых публичный вызов доходит только в гонке «firc
// поставили посреди операции».
func TestForeignFirc_InternalWritersRefuse(t *testing.T) {
	root := foreignTree(t)
	before := treeDigest(t, root)
	op, _ := newForeignOperator(t, root)
	for name, err := range map[string]error{
		"restoreDisabledMarker":    op.restoreDisabledMarker(),
		"writeMigrationJournal":    op.writeMigrationJournal(&MigrationJournal{Version: 1, Operation: "x", Stage: "prepared"}),
		"saveUplinkInboundRecords": op.saveUplinkInboundRecords(map[string]uint16{"x": 1}),
		"startRuntime":             op.startRuntime(context.Background()),
	} {
		if !errors.Is(err, ErrForeignFirc) {
			t.Errorf("%s = %v, want ErrForeignFirc", name, err)
		}
	}
	assertTreeUntouched(t, "внутренние записи", before, treeDigest(t, root))
	starts := 0
	op.SetLifecycleHooks(func(context.Context) error { starts++; return nil }, nil)
	if err := op.Start(context.Background()); !errors.Is(err, ErrForeignFirc) || starts != 0 {
		t.Errorf("Start = %v, запусков %d", err, starts)
	}
	// Клиент, созданный самим оператором (без SetClient), закрыт так же.
	own := NewOperator(Options{ConfigDir: t.TempDir(), Foreign: NewForeignDetector(root)})
	if _, err := own.Client().Ping(context.Background()); !errors.Is(err, ErrForeignFirc) {
		t.Errorf("Ping = %v, want ErrForeignFirc", err)
	}
}

func TestForeignFirc_ReadsShowNothing(t *testing.T) {
	ctx := context.Background()
	op, _ := newForeignOperator(t, foreignTree(t))
	if tuns, err := op.LoadTunnelsFile(); err != nil || len(tuns) != 0 {
		t.Errorf("туннели стороннего видны: %v, %v", tuns, err)
	}
	if grps, err := op.LoadGroupsFile(); err != nil || len(grps) != 0 {
		t.Errorf("группы стороннего видны: %v, %v", grps, err)
	}
	if op.IsServerRoutingEnabled("vless_xhttp_server") {
		t.Error("маршрутизация серверов через FIRC при стороннем не действует")
	}
	st := op.GetStatus(ctx, true)
	want := Status{Selected: true, Foreign: &ForeignFirc{Package: "firc", WebPort: 666},
		SocketPath: op.socketPath, DNSProxyEndpoint: op.DNSProxyEndpoint(), InboundPort: op.InboundPort()}
	if !reflect.DeepEqual(st, want) {
		t.Errorf("GetStatus = %+v, want %+v", st, want)
	}
	if snap := op.GetMonitorSnapshot(ctx, true); snap.Running || len(snap.Groups) != 0 {
		t.Errorf("монитор показывает стороннего: %+v", snap)
	}
}

// Движок FIRC был выбран, потом поставили сторонний firc: уйти на sing-box можно
// даже с меткой восстановления и недописанным журналом, чужого не трогая.
func TestForeignFirc_LeavingFircEngine(t *testing.T) {
	ctx := context.Background()
	root := foreignTree(t)
	before := treeDigest(t, root)
	op, probe := newForeignOperator(t, root)
	op.SetForceKernelNetfilter(true)
	if !op.IsRecoveryRequired() {
		t.Fatal("метка восстановления должна читаться")
	}
	cur, sbRunning, parked := "firc", false, true
	hooks := newSection3TestSwitchHooks(&cur, &sbRunning, &parked, nil, nil)
	if err := op.SwitchEngine(ctx, "sing-box", hooks); err != nil {
		t.Fatalf("уход с FIRC: %v", err)
	}
	if cur != "sing-box" || !sbRunning || parked {
		t.Fatalf("движок не переключён: cur=%s running=%v parked=%v", cur, sbRunning, parked)
	}
	assertTreeUntouched(t, "SwitchEngine", before, treeDigest(t, root))
	probe.assertForeignRulesUntouched(t, "SwitchEngine")
	probe.mu.Lock()
	cmds := strings.Join(probe.cmds, "\n")
	probe.mu.Unlock()
	if !strings.Contains(cmds, "-X FEXT_DNS") || strings.Contains(cmds, "-N "+cutoverGuardChain) {
		t.Errorf("свои цепочки должны сниматься, барьер — не ставиться:\n%s", cmds)
	}
	if err := op.SwitchEngine(ctx, "firc", hooks); !errors.Is(err, ErrForeignFirc) || cur != "sing-box" {
		t.Fatalf("возврат на FIRC = %v, движок %s", err, cur)
	}
}

// Сорванный уход не должен ни поднимать чужой демон, ни требовать восстановления.
func TestForeignFirc_LeavingFircEngineRollback(t *testing.T) {
	root := foreignTree(t)
	if err := os.Remove(filepath.Join(root, "opt/etc/firc/recovery-state.json")); err != nil {
		t.Fatal(err)
	}
	before := treeDigest(t, root)
	op, probe := newForeignOperator(t, root)
	cur, sbRunning, parked := "firc", false, true
	hooks := newSection3TestSwitchHooks(&cur, &sbRunning, &parked, nil, nil)
	boom := errors.New("activate failed")
	hooks.ActivateTarget = func(_ context.Context, target string) error {
		if target == "sing-box" {
			return boom
		}
		return nil
	}
	if err := op.SwitchEngine(context.Background(), "sing-box", hooks); !errors.Is(err, boom) || errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("SwitchEngine = %v", err)
	}
	if cur != "firc" || op.IsRecoveryRequired() {
		t.Fatalf("после отката: движок %s, recovery=%v", cur, op.IsRecoveryRequired())
	}
	assertTreeUntouched(t, "rollback", before, treeDigest(t, root))
	probe.assertForeignRulesUntouched(t, "rollback")
}

// Сервер с выходом «правила FIRC» остаётся настраиваемым: отключение — не отказ.
func TestForeignFirc_PrepareServerRoutingOff(t *testing.T) {
	root := foreignTree(t)
	before := treeDigest(t, root)
	op, _ := newForeignOperator(t, root)
	id, err := op.PrepareServerRouting(context.Background(), "tx-off", "vless_xhttp_server", false)
	if err != nil || id != "" {
		t.Fatalf("PrepareServerRouting(off) = %q, %v", id, err)
	}
	assertTreeUntouched(t, "PrepareServerRouting(off)", before, treeDigest(t, root))
}

func TestForeignFirc_WatchdogReleasesOwnRulesOnceConfirmed(t *testing.T) {
	root := foreignTree(t)
	op, probe := newForeignOperator(t, root)
	op.SetForceKernelNetfilter(true)
	released := func() int {
		probe.mu.Lock()
		defer probe.mu.Unlock()
		n := 0
		for _, c := range probe.cmds {
			if strings.HasSuffix(c, "-X FEXT_DNS") {
				n++
			}
		}
		return n
	}
	before := treeDigest(t, root)
	for tick, want := range []int{0, 1, 1} {
		op.runExtWatchdogTick(context.Background())
		if got := released(); got != want {
			t.Fatalf("такт %d: снятий %d, want %d", tick+1, got, want)
		}
	}
	assertTreeUntouched(t, "watchdog", before, treeDigest(t, root))
	probe.assertForeignRulesUntouched(t, "watchdog")
}

func TestForeignFirc_InstallerRefuses(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		call func(*OnDemandInstaller, *Operator) error
	}{
		{"Install", func(i *OnDemandInstaller, _ *Operator) error { return i.Install(ctx) }},
		{"Upgrade", func(i *OnDemandInstaller, o *Operator) error { return i.Upgrade(ctx, o) }},
		{"Uninstall", func(i *OnDemandInstaller, o *Operator) error { return i.Uninstall(ctx, o, false, false) }},
		{"RecoverPendingOperationOnStartup", func(i *OnDemandInstaller, o *Operator) error { return i.RecoverPendingOperationOnStartup(ctx, o) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := foreignTree(t)
			// Прерванная операция нашего пакета: без отказа старт доставил бы его поверх.
			writeTree(t, root, map[string]string{"opt/etc/awg-manager/firc-runtime-operation.json": `{"action":"uninstall","pending":true}`})
			before := treeDigest(t, root)
			op, probe := newForeignOperator(t, root)
			inst := NewOnDemandInstaller("mipsel-3.4", FircBinarySpec{
				Version: "0.8.0-awgm.4", PackageURL: "https://example.invalid/r.ipk",
				PackageSHA256: strings.Repeat("b", 64), PackageSize: 1024,
			})
			inst.SetPackageLayout(DefaultPackageLayout(root))
			pm := &mockRuntimePackages{root: root, versions: map[string]string{RuntimePackageName: "0.7.0-1"}}
			inst.SetPackageManager(pm)
			inst.SetFreeDiskFn(func(string) (int64, bool) { return 256 << 20, true })
			downloads := 0
			inst.SetDownloader(&orderedDownloader{onDownload: func(string) { downloads++ }, err: errors.New("offline")})
			if err := tc.call(inst, op); !errors.Is(err, ErrForeignFirc) {
				t.Errorf("= %v, want ErrForeignFirc", err)
			}
			if pm.installs != 0 || pm.removes != 0 || downloads != 0 {
				t.Errorf("installs=%d removes=%d downloads=%d", pm.installs, pm.removes, downloads)
			}
			assertTreeUntouched(t, tc.name, before, treeDigest(t, root))
			probe.assertForeignRulesUntouched(t, tc.name)
			st := inst.GetInstallStatus(ctx, op, true)
			want := InstallStatus{State: InstallStateForeign, ActiveEngine: true, Foreign: &ForeignFirc{Package: "firc", WebPort: 666}}
			if !reflect.DeepEqual(st, want) {
				t.Errorf("GetInstallStatus = %+v, want %+v", st, want)
			}
		})
	}
}

// firc поставили, пока рантайм скачивался под локом: уборка «остатков» снесла бы
// его файлы. Запасной пакет качается после первой проверки — её одной мало.
func TestForeignFirc_AppearsDuringInstallDownload(t *testing.T) {
	ipk := buildMockEntwareFircIPK(t, "mipsel-3.4", "0.8.0-1", elf.ELFCLASS32, elf.ELFDATA2LSB, elf.EM_MIPS)
	spec := FircBinarySpec{
		Version: "0.8.0-1", PackageURL: "https://example.invalid/firc.ipk",
		PackageSHA256: sha256HexBytes(ipk), PackageSize: int64(len(ipk)),
	}
	root := t.TempDir()
	pm := &mockFallbackRuntimePkgMgr{root: root, arch: "mipsel-3.4", expected: "0.8.0-1"}
	inst := NewOnDemandInstaller("mipsel-3.4", FircBinarySpec{})
	inst.SetFallbackPackageSpec(spec)
	inst.SetPackageLayout(DefaultPackageLayout(root))
	inst.SetPackageManager(pm)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 256 << 20, true })
	var before map[string]string
	inst.SetDownloader(&orderedDownloader{
		blobs: map[string][]byte{spec.PackageURL: ipk},
		onDownload: func(string) {
			writeTree(t, root, map[string]string{
				"opt/lib/opkg/info/firc.list": "/opt/bin/fircd\n",
				"opt/bin/fircd":               "foreign",
			})
			before = treeDigest(t, filepath.Join(root, "opt/bin"))
		},
	})
	if err := inst.Install(context.Background()); !errors.Is(err, ErrForeignFirc) {
		t.Fatalf("Install = %v, want ErrForeignFirc", err)
	}
	if before == nil || pm.installs != 0 {
		t.Fatalf("загрузка была: %v, installs=%d", before != nil, pm.installs)
	}
	assertTreeUntouched(t, "install", before, treeDigest(t, filepath.Join(root, "opt/bin")))
}

// То же при обновлении: демон к этому моменту уже чужой, останавливать нельзя.
func TestForeignFirc_AppearsDuringUpgrade(t *testing.T) {
	root, pm, dl, op, running, _, specV2, _, _ := setupUpgradeFixture(t)
	running.Store(true)
	stops := 0
	op.SetLifecycleHooks(
		func(context.Context) error { return nil },
		func(context.Context) error { stops++; return nil },
	)
	inst := NewOnDemandInstaller("aarch64-3.10", specV2)
	inst.SetPackageLayout(DefaultPackageLayout(root))
	inst.SetPackageManager(pm)
	inst.SetDownloader(dl)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 256 << 20, true })
	inst.SetFircRunningChecker(func(context.Context) bool { return running.Load() })
	inst.SetDependencyChecker(func(context.Context) ([]string, error) {
		writeTree(t, root, map[string]string{"opt/lib/opkg/info/firc.list": "/opt/bin/fircd\n"})
		return nil, nil
	})
	installs := pm.installs
	if err := inst.Upgrade(context.Background(), op); !errors.Is(err, ErrForeignFirc) {
		t.Fatalf("Upgrade = %v, want ErrForeignFirc", err)
	}
	if stops != 0 || pm.installs != installs {
		t.Fatalf("остановок %d, установок %d", stops, pm.installs-installs)
	}
}

func TestForeignFirc_UplinkAdapter(t *testing.T) {
	ctx := context.Background()
	target := uplink.Target{Engine: "firc", Ref: "aaaa0001"}
	root := foreignTree(t)
	before := treeDigest(t, root)
	op, probe := newForeignOperator(t, root)
	a := &UplinkAdapter{Operator: op, Active: func() bool { return true }}
	for name, err := range map[string]error{
		"Validate":      a.Validate(ctx, target),
		"PrepareUplink": a.PrepareUplink(ctx, target),
		"Activate":      a.Activate(ctx, target, "", "lo"),
	} {
		if err == nil {
			t.Errorf("%s прошёл при стороннем firc", name)
		}
	}
	// Отвязать Multi-WAN можно: иначе политика держала бы движок FIRC.
	if err := a.ReleaseUplink(ctx, target); err != nil {
		t.Errorf("ReleaseUplink = %v", err)
	}
	assertTreeUntouched(t, "uplink", before, treeDigest(t, root))
	probe.assertForeignRulesUntouched(t, "uplink")
}
