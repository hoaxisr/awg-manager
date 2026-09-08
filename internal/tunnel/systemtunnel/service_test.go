package systemtunnel

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// capturingPoster записывает payload каждой мутации RCI.
type capturingPoster struct {
	mu       sync.Mutex
	payloads []string
}

func (p *capturingPoster) Post(_ context.Context, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.payloads = append(p.payloads, string(body))
	p.mu.Unlock()
	return json.RawMessage(`{}`), nil
}

func (p *capturingPoster) last() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.payloads) == 0 {
		return ""
	}
	return p.payloads[len(p.payloads)-1]
}

// markedServers — фейковый ServerMarker: перенятые пользователем интерфейсы.
type markedServers map[string]bool

func (m markedServers) IsServerInterface(id string) bool { return m[id] }

// newASCHarness — сервис поверх фейкового RCI. listen-port у интерфейса есть
// ВСЕГДА (у поднятого туннеля-клиента он тоже непустой — см.
// internal/tunnel/nwg/peer_via_test.go), сервером интерфейс делает описание
// встроенного сервера или пометка в сторе.
func newASCHarness(t *testing.T, name, description string, marked bool) (*ServiceImpl, *capturingPoster) {
	t.Helper()
	fg := query.NewFakeGetter()
	fg.SetPostInterface(name, `{"show":{"interface":{"id":"`+name+`","interface-name":"nwg0","type":"Wireguard",`+
		`"state":"up","description":"`+description+`","wireguard":{"public-key":"PUB=","listen-port":43328}}}}`)
	queries := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	poster := &capturingPoster{}
	cmds := command.NewCommands(command.Deps{
		Poster:  poster,
		Queries: queries,
		Save:    command.NewSaveCoordinator(poster, nil, time.Hour, time.Hour, 0, nil),
	})
	return New(queries, cmds, markedServers{name: marked}, logging.NewScopedLogger(nil, "tunnels", "asc")), poster
}

// Сигнатура интерфейса-сервера принадлежит его пирам: форма ASC сервера
// её больше не задаёт, и ключи i1..i5 до NDMS не доходят.
func TestSetASCParams_StripsSignatureForBuiltInServer(t *testing.T) {
	svc, poster := newASCHarness(t, "Wireguard0", ndms.BuiltInVPNServerDescription, false)

	err := svc.SetASCParams(context.Background(), "Wireguard0",
		json.RawMessage(`{"jc":3,"s1":18,"i1":"<b 0x01>","i5":"<r 32>"}`))
	if err != nil {
		t.Fatalf("SetASCParams: %v", err)
	}
	got := poster.last()
	for _, unwanted := range []string{`"i1"`, `"i5"`} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("ключ %s ушёл в NDMS для интерфейса-сервера:\n%s", unwanted, got)
		}
	}
	// Вырезаем, а не обнуляем: пустая строка на 4.x — тоже неизвестный ключ.
	if strings.Contains(got, `""`) {
		t.Fatalf("сигнатура обнулена вместо вырезания:\n%s", got)
	}
	if !strings.Contains(got, `"jc":3`) || !strings.Contains(got, `"s1":18`) {
		t.Fatalf("числовые параметры не доехали:\n%s", got)
	}
}

// Перенятый пользователем сервер — тоже сервер: сигнатуру режем и у него.
func TestSetASCParams_StripsSignatureForMarkedServer(t *testing.T) {
	svc, poster := newASCHarness(t, "Wireguard2", "my server", true)

	err := svc.SetASCParams(context.Background(), "Wireguard2",
		json.RawMessage(`{"jc":3,"i1":"<b 0x01>"}`))
	if err != nil {
		t.Fatalf("SetASCParams: %v", err)
	}
	if got := poster.last(); strings.Contains(got, `"i1"`) {
		t.Fatalf("сигнатура перенятого сервера ушла в NDMS:\n%s", got)
	}
}

// Туннель-клиент — владелец собственной сигнатуры: i1 проходит, и слушающий
// порт (у поднятого клиента он тоже есть) этого не меняет.
func TestSetASCParams_KeepsSignatureForClientTunnel(t *testing.T) {
	svc, poster := newASCHarness(t, "Wireguard1", "awg20_vdsina", false)

	err := svc.SetASCParams(context.Background(), "Wireguard1",
		json.RawMessage(`{"jc":3,"s1":18,"i1":"<b 0x01>"}`))
	if err != nil {
		t.Fatalf("SetASCParams: %v", err)
	}
	// json.Marshal фейкового постера экранирует <> — сверяем по полезной части.
	if got := poster.last(); !strings.Contains(got, `"i1"`) || !strings.Contains(got, `0x01`) {
		t.Fatalf("сигнатура туннеля-клиента потеряна:\n%s", got)
	}
}
