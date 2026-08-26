package api

import (
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	sysports "github.com/hoaxisr/awg-manager/internal/sys/ports"
)

type stubInspector struct {
	bindings []sysports.Binding
	err      error
}

func (s stubInspector) InspectPort(int, string) ([]sysports.Binding, error) {
	return s.bindings, s.err
}

func TestValidateClashPort(t *testing.T) {
	const serverPort = 8080
	busy := stubInspector{bindings: []sysports.Binding{{
		Port: 9500, ProcessName: "wt-client", PID: 1234, Service: "S99wt",
	}}}
	free := stubInspector{}

	cases := []struct {
		name string
		port int
		insp clashPortInspector
		want string // подстрока; "" — порт должен быть принят
	}{
		{"свободный порт принимается", 9500, free, ""},
		{"0 = дефолт, принимается", 0, free, ""},
		{"ниже диапазона", 80, free, "диапазоне"},
		{"выше диапазона", 70000, free, "диапазоне"},
		{"порт веб-интерфейса", serverPort, free, "веб-интерфейс"},
		{"TPROXY sing-box", router.TPROXYPort, free, "TPROXY"},
		{"REDIRECT sing-box", router.RedirectPort, free, "REDIRECT"},
		{"первый порт QoS TPROXY", router.QoSTPROXYPortBase, free, "QoS"},
		{"последний порт QoS TPROXY", router.QoSTPROXYPortBase + router.MaxQoSClasses - 1, free, "QoS"},
		{"за границей диапазона QoS", router.QoSTPROXYPortBase + router.MaxQoSClasses, free, ""},
		{"первый порт QoS REDIRECT", router.QoSRedirectPortBase, free, "QoS"},
		{"вход туннеля", 1080, free, "входы туннелей"},
		{"занят чужим процессом", 9500, busy, "wt-client"},
		{"ошибка скана не блокирует", 9500, stubInspector{err: errors.New("no /proc")}, ""},
		{"без сканера проверяется только карта резервов", 9500, nil, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := validateClashPort(c.port, serverPort, c.insp)
			if c.want == "" {
				if got != "" {
					t.Errorf("порт должен быть принят, отказ: %q", got)
				}
				return
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("отказ должен содержать %q, got %q", c.want, got)
			}
		})
	}
}

// Дефолтный порт не должен попадать в собственную карту резервов — иначе
// свежая установка не смогла бы сохранить настройки.
func TestDefaultClashPortNotReserved(t *testing.T) {
	if owner := reservedClashPort(singbox.DefaultClashPort, 8080); owner != "" {
		t.Errorf("дефолтный порт зарезервирован под %q", owner)
	}
}

func TestDescribeBinding(t *testing.T) {
	cases := []struct {
		name string
		in   sysports.Binding
		want string
	}{
		{"полная запись", sysports.Binding{ProcessName: "wt-client", PID: 7, Service: "S99wt"}, "wt-client (PID 7), сервис S99wt"},
		{"только exe", sysports.Binding{Exe: "/opt/bin/x", PID: 7}, "/opt/bin/x (PID 7)"},
		{"пустая запись", sysports.Binding{}, "неизвестный процесс"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := describeBinding(c.in); got != c.want {
				t.Errorf("want %q, got %q", c.want, got)
			}
		})
	}
}
