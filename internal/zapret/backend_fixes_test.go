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
	if ref := newHostlistRef(broken, false); !strings.HasPrefix(ref.Note, hostlistCountErrorNotePrefix) {
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

func TestPackageProbeDoesNotHoldServiceMutex(t *testing.T) {
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
	done := make(chan struct{})
	go func() {
		svc.listInstalledPackages(context.Background())
		close(done)
	}()
	<-entered
	locked := make(chan struct{})
	go func() {
		svc.mu.Lock()
		svc.mu.Unlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("package probe held the service mutex")
	}
	if got := svc.listInstalledPackages(context.Background()); len(got) != 0 {
		t.Fatalf("unexpected package snapshot: %v", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("concurrent package probe calls = %d, want 1", calls.Load())
	}
	close(release)
	<-done
	if !svc.packagesAt.IsZero() {
		t.Fatal("failed opkg probe was cached as package absence")
	}
}

func TestVersionProbeDoesNotHoldServiceMutexAndStartsLazily(t *testing.T) {
	base := setupInstall(t)
	binary := filepath.Join(base, "nfq2", "nfqws2")
	marker := filepath.Join(filepath.Dir(binary), "version-probed")
	script := "#!/bin/sh\ntouch \"$(dirname \"$0\")/version-probed\"\nprintf 'version 2.1.0\\n'\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = NewService(t.TempDir(), nil)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("NewService synchronously ran --version: stat err=%v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	svc := &Service{
		now: time.Now,
		runVersion: func(context.Context, string) (string, error) {
			close(entered)
			<-release
			return "2.1.0", nil
		},
	}
	versionDone := make(chan string, 1)
	go func() { versionDone <- svc.getVersionCached(binary) }()
	<-entered
	locked := make(chan struct{})
	go func() {
		svc.mu.Lock()
		svc.mu.Unlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("version probe held the service mutex")
	}
	close(release)
	if got := <-versionDone; got != "2.1.0" {
		t.Fatalf("version = %q, want 2.1.0", got)
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

func TestGetStatusDoesNotCutOpkgToVersionBudget(t *testing.T) {
	setupResolverInstalls(t, "zapret-opkg")
	var deadline time.Duration
	svc := &Service{
		now:        time.Now,
		runVersion: func(context.Context, string) (string, error) { return "2.1.0", nil },
		opkgListInstalled: func(ctx context.Context) (string, error) {
			if d, ok := ctx.Deadline(); ok {
				deadline = time.Until(d)
			}
			return "zapret2 - 2.1.0\n", nil
		},
	}
	st := svc.GetStatus()
	if st.Package != "zapret2" {
		t.Fatalf("package = %q, want zapret2", st.Package)
	}
	if deadline != 0 && deadline <= versionProbeTimeout {
		t.Fatalf("opkg got %v, which is the version probe budget, not opkgTimeout", deadline)
	}
}
