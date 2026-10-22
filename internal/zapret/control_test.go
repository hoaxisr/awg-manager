package zapret

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func controlFixture(t *testing.T, running bool) (*Service, string, string) {
	t.Helper()
	base := setupResolverInstalls(t, "zapret-live")[0]
	state := filepath.Join(t.TempDir(), "pid")
	if running {
		if err := os.WriteFile(state, []byte("41"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	nfqProcessesHook = func() []nfqProc {
		body, _ := os.ReadFile(state)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(body)))
		if pid == 0 {
			return nil
		}
		return []nfqProc{{pid: pid, exe: filepath.Join(base, "nfq2", "nfqws2")}}
	}
	return NewService(t.TempDir(), nil), base, state
}

func TestControlRejectsSuccessfulScriptWithoutExpectedProcessState(t *testing.T) {
	for _, action := range []string{"start", "stop", "restart"} {
		t.Run(action, func(t *testing.T) {
			svc, base, _ := controlFixture(t, action != "start")
			if err := svc.Control(action, base); err == nil {
				t.Fatalf("%s reported success although the init script changed no processes", action)
			}
		})
	}
}

func TestControlConfirmsStartStopAndReplacedProcessesOnRestart(t *testing.T) {
	for _, action := range []string{"start", "stop", "restart"} {
		t.Run(action, func(t *testing.T) {
			svc, base, state := controlFixture(t, action != "start")
			quoted := "'" + strings.ReplaceAll(state, "'", "'\\''") + "'"
			script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\nstart|restart) printf 102 > %s;;\nstop) rm -f %s;;\nesac\n", quoted, quoted)
			if err := os.WriteFile(filepath.Join(base, "init.d", "sysv", "zapret2"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := svc.Control(action, base); err != nil {
				t.Fatalf("%s: %v", action, err)
			}
			running, pids := runningPIDsForBase(base)
			if action == "stop" {
				if running {
					t.Fatalf("stop left processes: %v", pids)
				}
			} else if !running || len(pids) != 1 || pids[0] != 102 {
				t.Fatalf("%s did not produce the expected processes: %v", action, pids)
			}
		})
	}
}

func TestTerminatedProcessIsNotAnUnknownLiveOwner(t *testing.T) {
	for _, state := range []string{"Z (zombie)", "X (dead)", "S (sleeping)"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "comm"), []byte("nfqws2\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "status"), []byte("State:\t"+state+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, found := nfqExeInDir(dir)
			if found != strings.HasPrefix(state, "S") {
				t.Fatalf("process %s without an executable: found=%v", state, found)
			}
		})
	}
}

func TestHostlistSaveRestartsOnlyRunningDaemons(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(strconv.FormatBool(running), func(t *testing.T) {
			svc, base, state := controlFixture(t, running)
			calls := filepath.Join(t.TempDir(), "actions")
			script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\nrestart-daemons|restart_daemons) printf '%%s\\n' \"$1\" >> %q; printf 102 > %q;;\n*) exit 9;;\nesac\n", calls, state)
			if err := os.WriteFile(filepath.Join(base, "init.d", "sysv", "zapret2"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(base, "ipset", "zapret-hosts-user.txt")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("before.example\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := svc.WriteHostlist(HostlistID("user"), "after.example\n", Revision(path, "before.example\n")); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(calls)
			if running && (err != nil || string(body) != "restart-daemons\n") {
				t.Fatalf("expected only daemon restart, got %q, %v", body, err)
			}
			if !running && !os.IsNotExist(err) {
				t.Fatalf("stopped installation was started: %q, %v", body, err)
			}
			got, _ := os.ReadFile(path)
			if string(got) != "after.example\n" {
				t.Fatalf("save did not persist: %q", got)
			}
		})
	}
}

func TestHostlistSaveNeverFallsBackToFullRestart(t *testing.T) {
	svc, base, _ := controlFixture(t, true)
	calls := filepath.Join(t.TempDir(), "actions")
	script := fmt.Sprintf("#!/bin/sh\nprintf 'called' > %q\n", calls)
	if err := os.WriteFile(filepath.Join(base, "init.d", "sysv", "zapret2"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "ipset", "zapret-hosts-user.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("before.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := svc.WriteHostlist(HostlistID("user"), "after.example\n", Revision(path, "before.example\n"))
	var applyErr *HostlistApplyError
	if !errors.As(err, &applyErr) {
		t.Fatalf("expected saved-but-not-applied error, got %v", err)
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatal("unsupported script must not be executed")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "after.example\n" {
		t.Fatalf("saved content lost: %q", got)
	}
}

func TestHostlistSaveLeavesIndependentScanRunning(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(strconv.FormatBool(running), func(t *testing.T) {
			svc, base, state := controlFixture(t, running)
			workers := nfqProcessesHook
			nfqProcessesHook = func() []nfqProc {
				return append(workers(), nfqProc{pid: 55, exe: filepath.Join(base, "nfq2", "nfqws2")})
			}
			pidDir := t.TempDir()
			pidfile := filepath.Join(pidDir, "nfqws2_1.pid")
			initDir := filepath.Join(base, "init.d", "sysv")
			if err := os.WriteFile(filepath.Join(initDir, "functions"), []byte("PIDDIR="+pidDir+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if running {
				if err := os.WriteFile(pidfile, []byte("41"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			calls := filepath.Join(t.TempDir(), "actions")
			script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\nrestart-daemons|restart_daemons) printf '%%s\\n' \"$1\" > %q; printf 102 > %q; printf 102 > %q;;\n*) exit 9;;\nesac\n", calls, state, pidfile)
			if err := os.WriteFile(filepath.Join(initDir, "zapret2"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(base, "ipset", "zapret-hosts-user.txt")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("before.example\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := svc.WriteHostlist(HostlistUser, "after.example\n", Revision(path, "before.example\n")); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(calls)
			if running && (err != nil || string(body) != "restart-daemons\n") {
				t.Fatalf("managed workers did not restart: %q, %v", body, err)
			}
			if !running && !os.IsNotExist(err) {
				t.Fatal("a scan alone must not cause stopped production daemons to start")
			}
			_, live := runningPIDsForBase(base)
			foundScan := false
			for _, pid := range live {
				foundScan = foundScan || pid == 55
			}
			if !foundScan {
				t.Fatal("independent scan disappeared")
			}
		})
	}
}

// z2k ставит S99zapret2 обычным файлом в /opt/etc/init.d с литеральным
// ZAPRET_BASE и знает только restart_daemons (files/S99zapret2.new).
func TestHostlistSaveRestartsZ2KStyleGlobalInit(t *testing.T) {
	svc, base, state := controlFixture(t, true)
	if err := os.Remove(filepath.Join(base, "init.d", "sysv", "zapret2")); err != nil {
		t.Fatal(err)
	}
	globalDir := t.TempDir()
	calls := filepath.Join(t.TempDir(), "actions")
	script := fmt.Sprintf("#!/bin/sh\nZAPRET_BASE=%s # z2k\ncase \"$1\" in\n    start)\n        exit 9\n        ;;\n    restart_daemons)\n        printf '%%s\\n' \"$1\" >> %q; printf 102 > %q\n        ;;\nesac\n", base, calls, state)
	if err := os.WriteFile(filepath.Join(globalDir, "S99zapret2"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Сортируются раньше S99zapret2: чужая установка и помощник без
	// глаголов init-скрипта, но с той же ZAPRET_BASE.
	foreign := "#!/bin/sh\nZAPRET_BASE=/opt/zapret-other\ncase \"$1\" in\nstart) exit 9;;\nesac\n"
	if err := os.WriteFile(filepath.Join(globalDir, "S98zapret"), []byte(foreign), 0o755); err != nil {
		t.Fatal(err)
	}
	helper := fmt.Sprintf("#!/bin/sh\nZAPRET_BASE=%s\nexec /bin/false\n", base)
	if err := os.WriteFile(filepath.Join(globalDir, "S50zapret-gui"), []byte(helper), 0o755); err != nil {
		t.Fatal(err)
	}
	initDirs = []string{globalDir}

	path := filepath.Join(base, "ipset", "zapret-hosts-user.txt")
	if _, _, err := svc.WriteHostlist(HostlistUser, "after.example\n", missingRevision(path)); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(calls)
	if err != nil || string(body) != "restart_daemons\n" {
		t.Fatalf("expected z2k daemon restart, got %q, %v", body, err)
	}
}

func TestDaemonRestartVerb(t *testing.T) {
	for body, want := range map[string]string{
		"case \"$1\" in\nrestart-daemons|restart_daemons) x;;\nesac\n":        "restart-daemons",
		"case \"$1\" in\n    restart_daemons)\n        x\n        ;;\nesac\n": "restart_daemons",
		"case \"$1\" in\nrestart) x;;\nrestart_fw) y;;\nesac\n":               "",
	} {
		if got := daemonRestartVerb([]byte(body)); got != want {
			t.Errorf("daemonRestartVerb(%q) = %q, want %q", body, got, want)
		}
	}
}
