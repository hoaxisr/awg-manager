package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// os5WithRecorders — оператор OS5, у которого видно и RCI-команды, и вызовы ip.
func os5WithRecorders(t *testing.T) (*OperatorOS5Impl, *recordingPoster, *[][]string) {
	t.Helper()
	poster := &recordingPoster{}
	queries := ndmsquery.NewQueries(ndmsquery.Deps{
		Getter: ndmsquery.NewFakeGetter(), Logger: ndmsquery.NopLogger(), IsOS5: func() bool { return true },
	})
	cmds := ndmscommand.NewCommands(ndmscommand.Deps{
		Poster:  poster,
		Queries: queries,
		Save:    ndmscommand.NewSaveCoordinator(poster, nil, time.Hour, time.Hour, 0, queries.RunningConfig),
		IsOS5:   func() bool { return true },
	})
	impl := NewOperatorOS5(nil, cmds, &MockWGClient{}, &MockBackend{}, &MockFirewall{})
	var calls [][]string
	impl.ipRun = func(_ context.Context, name string, args ...string) (*exec.Result, error) {
		calls = append(calls, append([]string{name}, args...))
		return &exec.Result{}, nil
	}
	return impl, poster, &calls
}

// ipv6InPayloads — ушла ли в роутер команда установки v6-адреса.
func ipv6InPayloads(payloads []any) bool {
	for _, p := range payloads {
		root, _ := p.(map[string]any)
		ifaces, _ := root["interface"].(map[string]any)
		for _, v := range ifaces {
			body, _ := v.(map[string]any)
			v6, ok := body["ipv6"].(map[string]any)
			if !ok {
				continue
			}
			// Снятие ({"address":{"no":true}}) установкой не считается —
			// оно как раз и убирает неработающую запись.
			if list, isList := v6["address"].([]any); isList && len(list) > 0 {
				return true
			}
		}
	}
	return false
}

func ipCallsWith(calls [][]string, needle string) []string {
	var out []string
	for _, c := range calls {
		joined := strings.Join(c, " ")
		if strings.Contains(joined, needle) {
			out = append(out, joined)
		}
	}
	return out
}

// Правка адреса живого туннеля: v6 обязан лечь на устройство через ip, а в
// NDMS уходить не должен — применить его к нашему kernel-интерфейсу роутер не
// может (стенд 5.01: Ip6Tools отвечает `no such device` на любую попытку).
// До этой правки смена v6 у живого туннеля не применялась вовсе до рестарта.
func TestSyncAddress_IPv6GoesToKernelNotNDMS(t *testing.T) {
	o, poster, calls := os5WithRecorders(t)

	if err := o.SyncAddress(context.Background(), "awg10", "10.8.0.2", 32, "2001:db8::2"); err != nil {
		t.Fatalf("SyncAddress: %v", err)
	}

	got := ipCallsWith(*calls, "2001:db8::2/128")
	if len(got) == 0 {
		t.Fatalf("v6-адрес не лёг на устройство: %v", *calls)
	}
	if !strings.Contains(got[0], "replace") || !strings.Contains(got[0], "opkgtun10") {
		t.Fatalf("не та команда для устройства: %q", got[0])
	}
	if ipv6InPayloads(poster.payloads) {
		t.Fatalf("v6-адрес ушёл в NDMS, хотя роутер применить его не может: %+v", poster.payloads)
	}
}

// Пустой v6 — адрес снимается с устройства: NDMS этого сделать не может, а
// прежняя запись осталась бы висеть на интерфейсе.
func TestSyncAddress_EmptyIPv6FlushesDevice(t *testing.T) {
	o, _, calls := os5WithRecorders(t)

	if err := o.SyncAddress(context.Background(), "awg10", "10.8.0.2", 32, ""); err != nil {
		t.Fatalf("SyncAddress: %v", err)
	}

	if got := ipCallsWith(*calls, "flush"); len(got) == 0 {
		t.Fatalf("v6 не снят с устройства: %v", *calls)
	}
}

// Слой ipv6 в NDMS не трогаем вовсе — ни установкой, ни снятием: любое
// обращение к нему будит роутер, он подхватывает адрес с устройства, заводит
// запись и снова упирается в `no such device` (стенд 5.01).
func TestSyncAddress_LeavesNDMSIPv6LayerAlone(t *testing.T) {
	o, poster, _ := os5WithRecorders(t)

	if err := o.SyncAddress(context.Background(), "awg10", "10.8.0.2", 32, "2001:db8::2"); err != nil {
		t.Fatalf("SyncAddress: %v", err)
	}

	for _, p := range poster.payloads {
		root, _ := p.(map[string]any)
		ifaces, _ := root["interface"].(map[string]any)
		for name, v := range ifaces {
			body, _ := v.(map[string]any)
			if _, touched := body["ipv6"]; touched {
				t.Fatalf("слой ipv6 у %s тронут: %+v", name, p)
			}
		}
	}
}
