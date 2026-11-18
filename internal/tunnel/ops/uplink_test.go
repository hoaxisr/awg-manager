package ops

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	sysExec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

type uplinkWGSpy struct {
	MockWGClient
	events *[]string
	down   *bool
}

func (w *uplinkWGSpy) SetConf(ctx context.Context, iface, path string) error {
	if !*w.down {
		return errors.New("unmarked setconf attempted while live")
	}
	*w.events = append(*w.events, "setconf")
	return w.MockWGClient.SetConf(ctx, iface, path)
}

func TestKernelUplinkApplyConfigCannotBringUpBeforeMarkRestore(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "restore-failed"}[failure], func(t *testing.T) {
			var events []string
			down := false
			wg := &uplinkWGSpy{events: &events, down: &down}
			o := NewOperatorOS5(nil, nil, wg, &MockBackend{}, &MockFirewall{})
			o.ipRun = func(_ context.Context, _ string, args ...string) (*sysExec.Result, error) {
				if strings.Contains(strings.Join(args, " "), "down") {
					down = true
					events = append(events, "down")
				} else {
					events = append(events, "up")
				}
				return &sysExec.Result{}, nil
			}
			o.SetUplinkLifecycle(func(string) bool { return true }, func(context.Context, string) error {
				events = append(events, "restore")
				if failure {
					return errors.New("mark restoration refused")
				}
				return nil
			})
			err := o.ApplyConfig(context.Background(), "awg10", "config")
			want := []string{"down", "setconf", "restore"}
			if !failure {
				want = append(want, "up")
			}
			if (err != nil) != failure || !reflect.DeepEqual(events, want) {
				t.Fatal(events, err)
			}
		})
	}
}
