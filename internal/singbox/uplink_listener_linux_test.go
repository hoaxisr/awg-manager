//go:build linux

package singbox

import (
	"context"
	"errors"
	"github.com/hoaxisr/awg-manager/internal/uplink"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestUplinkOwnedSOCKSWaitsForDelayedListener(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	op := &Operator{proc: &Process{pidPath: path, matchBinaryFn: func(pid int) bool { return pid == os.Getpid() }}}
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	reservation.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		pid, err := op.waitUplinkSOCKS(ctx, uint16(port))
		if err == nil && pid != os.Getpid() {
			err = errors.New("wrong process")
		}
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("probe returned before listener startup: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err = <-result; err != nil {
		t.Fatal(err)
	}
}

func TestUplinkOwnedSOCKSRejectsForeignListenerAndChangedPID(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	child := exec.Command("sleep", "30")
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { child.Process.Kill(); child.Wait() }()
	path := filepath.Join(t.TempDir(), "pid")
	if err = os.WriteFile(path, []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	op := &Operator{proc: &Process{pidPath: path, matchBinaryFn: func(pid int) bool { return pid == os.Getpid() || pid == child.Process.Pid }}}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	_, err = op.waitUplinkSOCKS(ctx, port)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("accepted another process's socket: %v", err)
	}
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unused := uint16(reservation.Addr().(*net.TCPAddr).Port)
	reservation.Close()
	if err = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := op.waitUplinkSOCKS(ctx, unused); result <- err }()
	select {
	case err := <-result:
		t.Fatalf("did not wait: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	replacement := path + ".next"
	if err = os.WriteFile(replacement, []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, uplink.ErrConflict) {
		t.Fatalf("adopted a different process: %v", err)
	}
}
