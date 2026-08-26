package api

import (
	"errors"
	"net/http"
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

// Гейт «проверять только при реальной смене»: наш sing-box в момент проверки
// слушает СТАРЫЙ порт, поэтому пересохранение того же значения не должно
// упираться в собственную занятость.
func TestUpdate_SingboxClashPort_ValidatesOnlyOnChange(t *testing.T) {
	h, _ := newSettingsHandlerFromRaw(t, `{"schemaVersion":2,"singboxClashPort":9500}`)
	h.SetClashPortInspector(stubInspector{bindings: []sysports.Binding{{
		Port: 9500, ProcessName: "sing-box", PID: 42,
	}}})

	// Тот же порт при занятом сокете — проходит: смены нет.
	if rec := postSettingsUpdate(t, h, `{"singboxClashPort":9500}`); rec.Code != http.StatusOK {
		t.Fatalf("пересохранение того же порта: status %d (%s)", rec.Code, rec.Body.String())
	}
	// Смена на занятый порт — отказ с именем держателя.
	rec := postSettingsUpdate(t, h, `{"singboxClashPort":9600}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("смена на занятый порт: status %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "sing-box") {
		t.Errorf("отказ должен называть держателя порта: %s", rec.Body.String())
	}
}

// Отвергнутый валидацией порт не должен оседать в сторадже: иначе он молча
// применился бы на следующем старте awgm.
func TestUpdate_SingboxClashPort_RejectedValueNotStored(t *testing.T) {
	h, store := newSettingsHandlerFromRaw(t, `{"schemaVersion":2}`)
	if rec := postSettingsUpdate(t, h, `{"singboxClashPort":80}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	got, err := store.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SingboxClashPort != 0 {
		t.Errorf("отвергнутый порт сохранён: %d", got.SingboxClashPort)
	}
}

func TestUpdate_SingboxClashPort_AppliesOnChange(t *testing.T) {
	h, _ := newSettingsHandlerFromRaw(t, `{"schemaVersion":2}`)
	var applied []int
	h.SetApplyClashPort(func(p int) error {
		applied = append(applied, p)
		return nil
	})

	if rec := postSettingsUpdate(t, h, `{"singboxClashPort":9500}`); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(applied) != 1 || applied[0] != 9500 {
		t.Fatalf("applied = %#v, want [9500]", applied)
	}

	// Повтор без смены значения sing-box дёргать не должен.
	if rec := postSettingsUpdate(t, h, `{"singboxClashPort":9500}`); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if len(applied) != 1 {
		t.Errorf("applied = %#v, повторное применение без смены", applied)
	}
}

// Провал применения отдаёт ошибку, но настройки к этому моменту УЖЕ сохранены
// (общий для всех applyX-хуков порядок в Update). Тест фиксирует расхождение
// явно: пользователь видит отказ, а порт уже в сторадже и оживёт на следующем
// старте. Отдельная задача — общая для clash-порта, bootstrap-DNS и уровня
// лога; пока поведение зафиксировано, чтобы не поменялось незамеченным.
func TestUpdate_SingboxClashPort_ApplyFailureStillPersists(t *testing.T) {
	h, store := newSettingsHandlerFromRaw(t, `{"schemaVersion":2}`)
	h.SetApplyClashPort(func(int) error { return errors.New("orchestrator down") })

	rec := postSettingsUpdate(t, h, `{"singboxClashPort":9500}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("ожидалась ошибка применения, got 200")
	}
	got, err := store.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SingboxClashPort != 9500 {
		t.Errorf("SingboxClashPort = %d; тест фиксирует, что настройка сохраняется ДО применения", got.SingboxClashPort)
	}
}
