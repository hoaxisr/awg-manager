package ops

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
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

// Reconcile — путь, ради которого заведён F268: адрес кладётся на устройство,
// слой ipv6 в NDMS не трогается вовсе. ColdStart в юнит-тесте недостижим
// (упирается в заглушки бэкенда раньше), его закрывает TestOS5_NoNDMSIPv6Calls.
func TestReconcile_LeavesNDMSIPv6LayerAlone(t *testing.T) {
	o, poster, calls := os5WithRecorders(t)
	cfg := tunnel.Config{
		ID: "awg10", Address: "10.8.0.2", AddressPrefix: 32,
		AddressIPv6: "2001:db8::2", MTU: 1420,
	}

	// Отказ пути не важен: важно, что ушло в роутер и в ip до того, как он
	// упёрся в заглушки.
	_ = o.Reconcile(context.Background(), cfg)

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
	if got := ipCallsWith(*calls, "2001:db8::2/128"); len(got) == 0 {
		t.Fatalf("v6-адрес не лёг на устройство: %v", *calls)
	}
}

// Прежний адрес снимается ПЕРЕД установкой нового: `replace` его не убирает,
// и на устройстве остались бы два /128.
func TestApplyKernelAddresses_FlushPrecedesReplace(t *testing.T) {
	o, _, calls := os5WithRecorders(t)

	o.applyKernelAddresses(context.Background(), "проба", tunnel.Config{
		ID: "awg10", AddressIPv6: "2001:db8::2",
	}, "opkgtun10")

	var flushAt, replaceAt = -1, -1
	for i, c := range *calls {
		joined := strings.Join(c, " ")
		if strings.Contains(joined, "flush") {
			flushAt = i
		}
		if strings.Contains(joined, "replace") && strings.Contains(joined, "2001:db8::2/128") {
			replaceAt = i
		}
	}
	if flushAt < 0 {
		t.Fatalf("прежний v6 не снят: %v", *calls)
	}
	if replaceAt < 0 {
		t.Fatalf("новый v6 не поставлен: %v", *calls)
	}
	if flushAt > replaceAt {
		t.Fatalf("снятие идёт после установки — новый адрес стёрт: %v", *calls)
	}
}

// Правка адреса живого туннеля кладёт на устройство и v4: до перевода на
// applyKernelAddresses его ставил только NDMS.
func TestSyncAddress_IPv4AlsoGoesToKernel(t *testing.T) {
	o, _, calls := os5WithRecorders(t)

	if err := o.SyncAddress(context.Background(), "awg10", "10.8.0.2", 32, ""); err != nil {
		t.Fatalf("SyncAddress: %v", err)
	}

	if got := ipCallsWith(*calls, "10.8.0.2/32"); len(got) == 0 {
		t.Fatalf("v4-адрес не лёг на устройство: %v", *calls)
	}
}

// Ни один путь OS5 не обращается к слою ipv6 в NDMS: роутер не может
// применить адрес к нашему kernel-устройству (стенд 5.01), команда только
// порождает две строки уровня C на каждый старт. Проверка по исходнику —
// ColdStart юнит-тестом не достаётся, а возврат вызова туда должен падать.
func TestOS5_NoNDMSIPv6Calls(t *testing.T) {
	src, err := os.ReadFile("operator_os5.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SetIPv6Address", "ClearIPv6Address"} {
		if strings.Contains(string(src), "Interfaces."+name) {
			t.Errorf("operator_os5.go снова зовёт %s: роутер не может применить v6 к нашему устройству", name)
		}
	}
}
