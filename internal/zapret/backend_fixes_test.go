package zapret

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

func TestCountHostlistStreamsLongAndCompressedFiles(t *testing.T) {
	dir := t.TempDir()
	longDomain := "long." + strings.Repeat("x", 256*1024) + ".example"
	content := "# comment\n1.2.3.4\n" + longDomain + "\n\n2001:db8::1"

	plain := filepath.Join(dir, "hosts.txt")
	if err := os.WriteFile(plain, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	counts, err := countHostlist(plain, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counts.entries != 3 || counts.ips != 2 {
		t.Fatalf("plain counts = %+v, want 3 entries and 2 IPs", counts)
	}

	gzPath := plain + ".gz"
	file, err := os.Create(gzPath)
	if err != nil {
		t.Fatal(err)
	}
	zipper := gzip.NewWriter(file)
	if _, err := zipper.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zipper.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	compressed, err := countHostlist(gzPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if compressed != counts {
		t.Fatalf("gzip counts = %+v, want %+v", compressed, counts)
	}

	broken := filepath.Join(dir, "broken.gz")
	if err := os.WriteFile(broken, []byte("not gzip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := countHostlist(broken, nil); err == nil {
		t.Fatal("invalid gzip input must return an error")
	}
	if ref := newHostlistRef(broken); ref.countErr == "" {
		t.Fatal("hostlist reference must retain the counting error")
	}
}

func TestHostlistCountsCacheTracksMtimeAndSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.txt")
	if err := os.WriteFile(path, []byte("1.1.1.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := countHostlist(path, nil)
	if err != nil || first.entries != 1 || first.ips != 1 {
		t.Fatalf("first count = %+v, err=%v", first, err)
	}

	if err := os.WriteFile(path, []byte("a.a.a.a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	second, err := countHostlist(path, nil)
	if err != nil || second.entries != 1 || second.ips != 0 {
		t.Fatalf("updated count = %+v, err=%v", second, err)
	}
}

func TestEditableMissingHostlistCanBeCreatedWithAbsentRevision(t *testing.T) {
	base := setupInstall(t)
	svc := NewService(t.TempDir(), nil)
	target := filepath.Join(base, "ipset", "zapret-hosts-user.txt")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	id, path, content, err := svc.ReadHostlist(HostlistUser)
	if err != nil {
		t.Fatal(err)
	}
	if id != HostlistUser || content != "" || path != target {
		t.Fatalf("missing hostlist read = (%q, %q, %q), want user, target, empty", id, path, content)
	}
	missingRevision := Revision(path, content)
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if emptyFileRevision := Revision(path, ""); emptyFileRevision == missingRevision {
		t.Fatal("missing and existing empty hostlists need distinct revisions")
	}
	if err := os.WriteFile(target, []byte("<missing>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if literalRevision := Revision(path, "<missing>"); literalRevision == missingRevision {
		t.Fatal("missing revision collided with a literal file value")
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.WriteHostlist(HostlistUser, "created.example", missingRevision); err != nil {
		t.Fatalf("create missing editable hostlist: %v", err)
	}

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	_, path, content, err = svc.ReadHostlist(HostlistUser)
	if err != nil {
		t.Fatal(err)
	}
	staleRevision := Revision(path, content)
	if err := os.WriteFile(target, []byte("someone-else.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.WriteHostlist(HostlistUser, "overwrite.example", staleRevision); !errors.Is(err, ErrConflict) {
		t.Fatalf("competing creation error = %v, want conflict", err)
	}
}

func TestGetStrategiesWarningsUseHydratedListsAndFlavor(t *testing.T) {
	base := setupInstall(t)
	userList := filepath.Join(base, "ipset", "zapret-hosts-user.txt")
	if err := os.WriteFile(userList, []byte("192.0.2.1\n2001:db8::1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := "NFQWS2_OPT=\"--filter-tcp=443 <HOSTLIST> --lua-desync=fake\"\n"
	if err := os.WriteFile(filepath.Join(base, "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := NewService(t.TempDir(), nil).GetStrategies()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || !hasWarning(got.Items[0], "hostlist_ip_only") {
		t.Fatalf("hydrated placeholder warning missing: %+v", got.Items)
	}

	config = "MODE=tpws\nTPWS_OPT=\"--filter-tcp=443 --methodeol\"\nNFQWS_OPT=\"--filter-tcp=443 --dpi-desync=fake\"\n"
	if err := os.WriteFile(filepath.Join(base, "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = NewService(t.TempDir(), nil).GetStrategies()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || hasWarning(got.Items[0], "strategy_no_lua_desync") {
		t.Fatalf("v1 tpws received an nfqws2 Lua warning: %+v", got.Items)
	}
}

func TestStatusAndControlIgnoreIndependentScanProcesses(t *testing.T) {
	base := setupInstall(t)
	pidDir := filepath.Join(t.TempDir(), "pids")
	if err := os.MkdirAll(pidDir, 0o700); err != nil {
		t.Fatal(err)
	}
	functions := filepath.Join(base, "init.d", "sysv", "functions")
	if err := os.WriteFile(functions, []byte("PIDDIR=\""+pidDir+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	init := filepath.Join(base, "init.d", "sysv", "zapret2")
	script := fmt.Sprintf("#!/bin/sh\nPIDDIR='%s'\ncase \"$1\" in\nstart) echo 42 > \"$PIDDIR/nfqws2_1.pid\" ;;\nstop) rm -f \"$PIDDIR/nfqws2_1.pid\" ;;\nrestart) rm -f \"$PIDDIR/nfqws2_1.pid\"; echo 42 > \"$PIDDIR/nfqws2_1.pid\" ;;\nesac\n", pidDir)
	if err := os.WriteFile(init, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(base, "nfq2", "nfqws2")
	prevProcesses := nfqProcessesHook
	t.Cleanup(func() { nfqProcessesHook = prevProcesses })
	nfqProcessesHook = func() []nfqProc {
		live := []nfqProc{{pid: 99, exe: binary}} // scanner-owned process
		if _, err := os.Stat(filepath.Join(pidDir, "nfqws2_1.pid")); err == nil {
			live = append(live, nfqProc{pid: 42, exe: binary})
		}
		return live
	}

	svc := NewService(t.TempDir(), nil)
	svc.runVersion = func(context.Context, string) (string, error) { return "2.1.0", nil }
	svc.opkgListInstalled = func(context.Context) (string, error) { return "", nil }
	status := svc.GetStatus()
	if status.Running || len(status.PIDs) != 0 || status.ProcessState != StateStopped {
		t.Fatalf("scanner-only status = %+v, want stopped with no managed PIDs", status)
	}

	if err := svc.Control("start", base); err != nil {
		t.Fatalf("start with independent scanner: %v", err)
	}
	status = svc.GetStatus()
	if !status.Running || len(status.PIDs) != 1 || status.PIDs[0] != 42 {
		t.Fatalf("running status = %+v, want only managed PID 42", status)
	}

	if err := svc.Control("stop", base); err != nil {
		t.Fatalf("stop with independent scanner: %v", err)
	}
	status = svc.GetStatus()
	if status.Running || len(status.PIDs) != 0 || status.ProcessState != StateStopped {
		t.Fatalf("post-stop status = %+v, want stopped while scanner remains", status)
	}
}

func TestPlaceholderExcludeIsNotAnIncludeList(t *testing.T) {
	base := setupInstall(t)
	ipset := filepath.Join(base, "ipset")
	if err := os.WriteFile(filepath.Join(ipset, "zapret-hosts-user.txt"), []byte("example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exclude := filepath.Join(ipset, "zapret-hosts-user-exclude.txt")
	if err := os.WriteFile(exclude, []byte("192.0.2.0/24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := "NFQWS2_OPT=\"--filter-tcp=443 <HOSTLIST> --lua-desync=fake\"\n"
	if err := os.WriteFile(filepath.Join(base, "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := NewService(t.TempDir(), nil).GetStrategies()
	if err != nil {
		t.Fatal(err)
	}
	item := got.Items[0]
	if hasWarning(item, "hostlist_ip_only") {
		t.Fatalf("IP-only exclude list raised an include-list warning: %+v", item.Warnings)
	}
	if len(item.HostlistExcludes) != 1 || item.HostlistExcludes[0].Path != exclude || item.HostlistExcludes[0].FromPlaceholder != "<HOSTLIST>" {
		t.Fatalf("placeholder exclude not routed to excludes: %+v", item.HostlistExcludes)
	}
	for _, h := range item.Hostlists {
		if h.Path == exclude {
			t.Fatalf("exclude list listed as include: %+v", item.Hostlists)
		}
	}
}

func TestSplitProfilesKeepsLongLines(t *testing.T) {
	long := "--filter-tcp=443 --lua-desync=fake:blob=" + strings.Repeat("a", 2<<20)
	got := splitProfiles(long + " --new\n--filter-udp=443 --lua-desync=fake\n")
	if len(got) != 2 || got[0] != long {
		t.Fatalf("long profile lost: %d profiles", len(got))
	}
}

func TestPackageProbeRunsOnceInBackgroundAndKeepsErrorsApart(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	svc := &Service{
		now: time.Now,
		opkgListInstalled: func(context.Context) (string, error) {
			calls.Add(1)
			close(entered)
			<-release
			return "", errors.New("probe failed")
		},
	}
	done := make(chan map[string]string, 1)
	go func() { done <- svc.installedPackages() }()
	select {
	case got := <-done:
		if len(got) != 0 {
			t.Fatalf("unexpected package snapshot: %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("status read waited for opkg")
	}
	<-entered
	svc.installedPackages()
	if calls.Load() != 1 {
		t.Fatalf("concurrent package probe calls = %d, want 1", calls.Load())
	}
	close(release)
	svc.probes.Wait()
	if !svc.packages.at.IsZero() || svc.packages.err == "" {
		t.Fatalf("failed opkg probe cached as absence: %+v", svc.packages)
	}
}

func TestVersionProbeRunsInBackgroundAndStartsLazily(t *testing.T) {
	base := setupInstall(t)
	binary := filepath.Join(base, "nfq2", "nfqws2")
	marker := filepath.Join(filepath.Dir(binary), "version-probed")
	script := "#!/bin/sh\ntouch \"$(dirname \"$0\")/version-probed\"\nprintf 'version 2.1.0\\n'\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = NewService(t.TempDir(), nil)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("NewService ran --version: stat err=%v", err)
	}

	release := make(chan struct{})
	svc := &Service{
		now: time.Now,
		runVersion: func(context.Context, string) (string, error) {
			<-release
			return "2.1.0", nil
		},
	}
	if got := svc.cachedVersion(binary); got != "" {
		t.Fatalf("version before probe = %q, want empty", got)
	}
	close(release)
	svc.probes.Wait()
	if got := svc.cachedVersion(binary); got != "2.1.0" {
		t.Fatalf("version after probe = %q, want 2.1.0", got)
	}
}

// GetStatus на слабом MIPS не должен ждать opkg (секунды) и --version:
// он отдаёт кэш, а свежий ответ приходит инвалидацией статуса.
func TestGetStatusDoesNotWaitForProbesAndPublishesTheirResult(t *testing.T) {
	setupResolverInstalls(t, "zapret-probes")
	release := make(chan struct{})
	svc := &Service{
		now: time.Now,
		runVersion: func(context.Context, string) (string, error) {
			<-release
			return "2.1.0", nil
		},
		opkgListInstalled: func(context.Context) (string, error) {
			<-release
			return "zapret2 - 2.1.0\n", nil
		},
	}
	bus := events.NewBus()
	_, ch, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	svc.SetEventBus(bus)

	got := make(chan Status, 1)
	go func() { got <- svc.GetStatus() }()
	select {
	case st := <-got:
		if st.Package != "" || st.Version != "" {
			t.Fatalf("status before probes = %+v", st)
		}
	case <-time.After(time.Second):
		t.Fatal("GetStatus waited for external probes")
	}
	close(release)
	svc.probes.Wait()
	select {
	case ev := <-ch:
		inv, ok := ev.Data.(events.ResourceInvalidatedEvent)
		if !ok || inv.Resource != events.ResourceRoutingZapretStatus {
			t.Fatalf("unexpected event %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("probe result was not published")
	}
	if st := svc.GetStatus(); st.Package != "zapret2" || st.Version != "2.1.0" {
		t.Fatalf("status after probes = %+v", st)
	}
}

func TestPlaceholderAsOptionValueIsExpandedOnce(t *testing.T) {
	base := setupInstall(t)
	config := "NFQWS2_OPT=\"--filter-tcp=443 --hostlist=<HOSTLIST> --lua-desync=fake\"\n"
	if err := os.WriteFile(filepath.Join(base, "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := NewService(t.TempDir(), nil).GetStrategies()
	if err != nil {
		t.Fatal(err)
	}
	item := got.Items[0]
	for _, h := range item.Hostlists {
		if h.Path == "<HOSTLIST>" || strings.Contains(h.Path, "exclude") || strings.Contains(h.Path, "auto") {
			t.Fatalf("placeholder leaked into include lists: %+v", item.Hostlists)
		}
	}
	if len(item.Hostlists) != 2 || len(item.HostlistExcludes) != 1 || len(item.HostlistAuto) != 1 {
		t.Fatalf("expanded lists = %d/%d/%d, want 2/1/1", len(item.Hostlists), len(item.HostlistExcludes), len(item.HostlistAuto))
	}
}
