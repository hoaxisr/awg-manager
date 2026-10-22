package zapret

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

func TestDefaultRunVersionReturnsProbeErrors(t *testing.T) {
	_, err := defaultRunVersion(context.Background(), filepath.Join(t.TempDir(), "missing-nfqws2"))
	if err == nil {
		t.Fatal("missing daemon binary should return an error instead of an empty successful version")
	}
}

func TestDefaultRunVersionReturnsTimeout(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "nfqws2")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexec /bin/sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := defaultRunVersion(ctx, binary)
	if !errors.Is(err, sysexec.ErrTimeout) {
		t.Fatalf("version error = %v, want wrapped timeout", err)
	}
}

func TestGetVersionCachedRetriesAfterEmptyProbeWithoutCachingItAsVersion(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "nfqws2")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	calls := 0
	svc := &Service{
		now: func() time.Time { return now },
		runVersion: func(context.Context, string) (string, error) {
			calls++
			if calls == 1 {
				return "", nil
			}
			return "2.1.0", nil
		},
	}
	svc.cachedVersion(binary)
	svc.probes.Wait()
	if got := svc.cachedVersion(binary); got != "" {
		t.Fatalf("first version = %q, want empty after empty probe", got)
	}
	now = now.Add(time.Minute)
	svc.cachedVersion(binary)
	svc.probes.Wait()
	if got := svc.cachedVersion(binary); got != "2.1.0" {
		t.Fatalf("version after retry = %q, want 2.1.0", got)
	}
	if calls != 2 {
		t.Fatalf("version probe calls = %d, want 2", calls)
	}
}

func TestGetStatusReportsVersionProbeFailureAfterPackageProbeSucceeds(t *testing.T) {
	setupResolverInstalls(t, "zapret-version-failure")
	svc := NewService(t.TempDir(), nil)
	svc.runVersion = func(context.Context, string) (string, error) {
		return "", errors.New("probe timed out")
	}
	svc.opkgListInstalled = func(context.Context) (string, error) { return "", nil }
	svc.GetStatus()
	svc.probes.Wait()
	status := svc.GetStatus()
	if !strings.Contains(status.LastError, "zapret version: probe timed out") {
		t.Fatalf("LastError = %q, want version-probe failure", status.LastError)
	}
}

func TestDefaultRunVersionFallsBackWhenVersionFlagHangs(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "nfqws2")
	script := "#!/bin/sh\n[ \"$1\" = --version ] && exec /bin/sleep 10\nprintf 'version 2.1.0\\n'\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := defaultRunVersion(context.Background(), binary)
	if err != nil || got != "2.1.0" {
		t.Fatalf("version = %q, %v; want -v fallback after hung --version", got, err)
	}
}
