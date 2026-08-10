package accesspolicy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

const hotspotPath = "/show/ip/hotspot"

type fakeEvictor struct {
	got []string
	// ctxErr — состояние контекста на момент вызова: вытеснение обязано
	// получать неотменяемый контекст.
	ctxErr error
}

func (f *fakeEvictor) EvictFlows(ctx context.Context, ips ...string) {
	f.ctxErr = ctx.Err()
	f.got = append(f.got, ips...)
}

// hotspotQueries собирает Queries с настоящим HotspotStore поверх фейкового
// геттера — свой шов резолва в проде ради этого не нужен.
func hotspotQueries(fg *query.FakeGetter) *query.Queries {
	return &query.Queries{Hotspot: query.NewHotspotStore(fg, query.NopLogger())}
}

// Регистр MAC приходит от NDMS и от вызывающего независимо, поэтому нормализуются
// обе стороны сравнения.
func TestEvictDeviceFlowsResolvesAddressByMAC(t *testing.T) {
	cases := []struct {
		name       string
		callerMAC  string
		hotspotMAC string
	}{
		{"вызывающий в верхнем регистре", "AA:BB:CC:DD:EE:FF", "aa:bb:cc:dd:ee:ff"},
		{"хотспот в верхнем регистре", "aa:bb:cc:dd:ee:ff", "AA:BB:CC:DD:EE:FF"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fg := query.NewFakeGetter()
			fg.SetJSON(hotspotPath, fmt.Sprintf(`{"host":[
				{"ip":"192.168.1.12","mac":"11:22:33:44:55:66"},
				{"ip":"192.168.1.55","mac":%q}
			]}`, tc.hotspotMAC))

			ev := &fakeEvictor{}
			svc := &ServiceImpl{queries: hotspotQueries(fg), evictor: ev}

			svc.evictDeviceFlows(context.Background(), tc.callerMAC)

			if len(ev.got) != 1 || ev.got[0] != "192.168.1.55" {
				t.Fatalf("вытеснение не вызвано для адреса устройства: %q", ev.got)
			}
		})
	}
}

func TestEvictDeviceFlowsSkipsUnknownAddress(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON(hotspotPath, `{"host":[{"ip":"192.168.1.12","mac":"11:22:33:44:55:66"}]}`)

	ev := &fakeEvictor{}
	svc := &ServiceImpl{queries: hotspotQueries(fg), evictor: ev}

	svc.evictDeviceFlows(context.Background(), "AA:BB:CC:DD:EE:FF")

	if len(ev.got) != 0 {
		t.Fatalf("вытеснение вызвано без известного адреса: %q", ev.got)
	}
}

// Настоящий резолв обязан ходить за свежим хотспотом: устройство могло сменить
// аренду, а прежний адрес — достаться соседу, которому вытеснение снесло бы
// его собственные соединения.
func TestEvictDeviceFlowsRefreshesHotspotCache(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON(hotspotPath, `{"host":[{"ip":"192.168.1.10","mac":"aa:bb:cc:dd:ee:ff"}]}`)

	ev := &fakeEvictor{}
	svc := &ServiceImpl{queries: hotspotQueries(fg), evictor: ev}

	svc.evictDeviceFlows(context.Background(), "AA:BB:CC:DD:EE:FF")

	// Аренда сменилась: устройство переехало, прежний адрес занял сосед.
	fg.SetJSON(hotspotPath, `{"host":[{"ip":"192.168.1.77","mac":"aa:bb:cc:dd:ee:ff"}]}`)
	svc.evictDeviceFlows(context.Background(), "AA:BB:CC:DD:EE:FF")

	want := []string{"192.168.1.10", "192.168.1.77"}
	if len(ev.got) != len(want) || ev.got[0] != want[0] || ev.got[1] != want[1] {
		t.Fatalf("вытеснение ушло по кэшу вместо свежего хотспота: got %q, want %q", ev.got, want)
	}
}

// Отмена HTTP-запроса не должна отменять вытеснение: политика в роутере уже
// сменена. Фейки контекст не читают, поэтому проверяем сам контракт — вниз
// уходит неотменённый контекст.
func TestEvictDeviceFlowsSurvivesRequestCancel(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON(hotspotPath, `{"host":[{"ip":"192.168.1.55","mac":"aa:bb:cc:dd:ee:ff"}]}`)

	ev := &fakeEvictor{}
	svc := &ServiceImpl{queries: hotspotQueries(fg), evictor: ev}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.evictDeviceFlows(ctx, "AA:BB:CC:DD:EE:FF")

	if len(ev.got) != 1 || ev.got[0] != "192.168.1.55" {
		t.Fatalf("вытеснение не вызвано: %q", ev.got)
	}
	if ev.ctxErr != nil {
		t.Fatalf("вытеснение получило отменённый контекст: %v", ev.ctxErr)
	}
}

func TestEvictDeviceFlowsSilentWithoutEvictor(t *testing.T) {
	svc := &ServiceImpl{}
	svc.evictDeviceFlows(context.Background(), "aa:bb:cc:dd:ee:ff") // не должно паниковать
}

// capturingAppLog собирает строки журнала приложения целиком: уровень здесь
// значим не меньше текста — Warn на «адрес не найден» был бы ложной тревогой,
// а его отсутствие — молчанием там, где гарантия не сработала.
type capturingAppLog struct{ lines []string }

func (c *capturingAppLog) AppLog(level logging.Level, _, _, action, target, message string) {
	c.lines = append(c.lines, string(level)+" "+action+" "+target+" "+message)
}

func (c *capturingAppLog) find(substr string) string {
	for _, l := range c.lines {
		if strings.Contains(l, substr) {
			return l
		}
	}
	return ""
}

// Неизвестный адрес — не редкость: записи нет у устройства за нижестоящим
// роутером, а записи без аренды бывают у статического хоста и у только что
// выключенного. Вытеснение в этом случае не происходит, и без строки в журнале
// это неотличимо от успешного прохода.
func TestEvictDeviceFlowsLogsUnknownAddress(t *testing.T) {
	cases := []struct {
		name  string
		hosts string
	}{
		{"записи нет вовсе", `{"host":[{"ip":"192.168.1.12","mac":"11:22:33:44:55:66"}]}`},
		{"запись есть, аренды нет", `{"host":[{"ip":"","mac":"aa:bb:cc:dd:ee:ff"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fg := query.NewFakeGetter()
			fg.SetJSON(hotspotPath, tc.hosts)

			log := &capturingAppLog{}
			ev := &fakeEvictor{}
			svc := &ServiceImpl{
				queries: hotspotQueries(fg),
				evictor: ev,
				appLog:  logging.NewScopedLogger(log, logging.GroupRouting, logging.SubAccessPolicy),
			}

			svc.evictDeviceFlows(context.Background(), "AA:BB:CC:DD:EE:FF")

			if len(ev.got) != 0 {
				t.Fatalf("вытеснение вызвано без известного адреса: %q", ev.got)
			}
			line := log.find("не найден в хотспоте")
			if line == "" {
				t.Fatalf("ненайденный адрес прошёл молча — след обязателен, записано: %q", log.lines)
			}
			if !strings.HasPrefix(line, string(logging.LevelInfo)+" ") {
				t.Errorf("уровень строки обязан быть info, получено %q", line)
			}
			if !strings.Contains(line, "AA:BB:CC:DD:EE:FF") {
				t.Errorf("в строке нет MAC устройства, по которому её ищут: %q", line)
			}
		})
	}
}

// fakePoster проглатывает мутации NDMS: настоящему PolicyCommands нужен только
// успешный Post, а нам — доказательство, что смена политики дошла до роутера.
type fakePoster struct{ payloads []any }

func (f *fakePoster) Post(_ context.Context, payload any) (json.RawMessage, error) {
	f.payloads = append(f.payloads, payload)
	return json.RawMessage(`{}`), nil
}

type nopPublisher struct{}

func (nopPublisher) Publish(string, any) {}

// Единственный триггер фичи — вызовы из Assign/UnassignDevice. Без этого теста
// удаление обеих строк вызова оставляет пакет зелёным, а гарантию «член
// политики всегда через sing-box» — мёртвой: перехват действует на новые
// потоки, а установившиеся дожили бы своё мимо движка.
func TestPolicyMembershipChangeEvictsDeviceFlows(t *testing.T) {
	cases := []struct {
		name string
		call func(*ServiceImpl, context.Context) error
	}{
		{"вступление", func(s *ServiceImpl, ctx context.Context) error {
			return s.AssignDevice(ctx, "AA:BB:CC:DD:EE:FF", "Policy0")
		}},
		{"выход", func(s *ServiceImpl, ctx context.Context) error {
			return s.UnassignDevice(ctx, "AA:BB:CC:DD:EE:FF")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fg := query.NewFakeGetter()
			fg.SetJSON(hotspotPath, `{"host":[{"ip":"192.168.1.55","mac":"aa:bb:cc:dd:ee:ff"}]}`)
			// Настоящий PolicyCommands поверх фейкового Poster: он трогает
			// Hotspot и RunningConfig, поэтому Queries нужны полные, а не
			// урезанные hotspotQueries.
			q := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
			poster := &fakePoster{}
			sc := command.NewSaveCoordinator(poster, nopPublisher{}, time.Hour, time.Hour, 0, nil)
			ev := &fakeEvictor{}
			svc := &ServiceImpl{policies: command.NewPolicyCommands(poster, sc, q, nil), queries: q, evictor: ev}

			if err := tc.call(svc, context.Background()); err != nil {
				t.Fatalf("смена состава политики: %v", err)
			}
			if len(poster.payloads) == 0 {
				t.Fatal("предусловие: мутация до роутера не дошла — тест проверял бы не тот путь")
			}
			if len(ev.got) != 1 || ev.got[0] != "192.168.1.55" {
				t.Fatalf("смена состава политики не вытеснила потоки устройства: %q", ev.got)
			}
		})
	}
}
