package linkres

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

// LinkedTunnel — связанный AWG-туннель (связь: WdttClientID /
// FreeTurnClientID в записи туннеля).
type LinkedTunnel struct {
	ID       string
	Endpoint string
	// Running — туннель поднят (running либо starting): то же условие, по
	// которому старый мир пропускал старт и стоп.
	Running bool
	// Lifecycle — запись участвует в подъёме и остановке. У WDTT это все
	// связанные записи КРОМЕ raw-зеркала (паритет tunnelLinkedAwgOnly):
	// зеркало — не туннель роутера, его состояние ведёт сам raw-клиент.
	Lifecycle bool
}

// EndpointSync — контракт с туннельной подсистемой (закрывает открытый вопрос
// §13 спеки). Туннельная подсистема владеет записью; прокси — единственный
// писатель РОВНО одного поля связанных записей: Peer.Endpoint, и пишет туда
// ровно 127.0.0.1:<порт listen>. Sync применяет изменение и на живом
// интерфейсе, и в хранилище (методы хендлеров SyncLinkedTunnelEndpoints
// поверх хелпера linked_tunnels_lifecycle.go:190). Ручная правка endpoint'а —
// дрейф, который чинится: endpoint связанного туннеля по построению локальный.
//
// SetState поднимает и опускает связанные туннели. Здесь, а не в HTTP-ручке:
// старый мир держал подъём и остановку в четырёх ручках и поэтому терял их при
// автостарте на загрузке роутера, при восстановлении после падения и просто
// при закрытой странице фронта.
type EndpointSync interface {
	List(ctx context.Context, clientID string) ([]LinkedTunnel, error)
	Sync(ctx context.Context, clientID, listen string) (int, error)
	// SetState приводит связанные туннели клиента к желаемому состоянию и
	// возвращает число изменённых.
	SetState(ctx context.Context, clientID string, up bool) (int, error)
}

// LinkedEndpoint — ресурс linked_endpoint.
type LinkedEndpoint struct {
	id       proxyrt.ResourceID
	sync     EndpointSync
	clientID string
	listen   string
	// up — желаемое состояние связанных туннелей: намерение владельца
	// инстанса, а не наблюдение.
	up bool
}

func NewLinkedEndpoint(id proxyrt.ResourceID, sync EndpointSync) *LinkedEndpoint {
	return &LinkedEndpoint{id: id, sync: sync}
}

func (l *LinkedEndpoint) SetDesired(clientID, listen string, up bool) {
	l.clientID, l.listen, l.up = clientID, listen, up
}

func (l *LinkedEndpoint) ID() proxyrt.ResourceID { return l.id }

func (l *LinkedEndpoint) Observe(ctx context.Context) (proxyrt.Observation, error) {
	tunnels, err := l.sync.List(ctx, l.clientID)
	if err != nil {
		return proxyrt.Observation{}, err
	}
	drift := 0
	if l.up {
		// Endpoint доводится только у включённого клиента: у выключенного
		// listen ничего не значит, а правка записи была бы мутацией без нужды.
		want, werr := localPort(l.listen)
		if werr != nil {
			return proxyrt.Observation{}, werr
		}
		for _, t := range tunnels {
			if t.Endpoint != fmt.Sprintf("127.0.0.1:%d", want) {
				drift++
			}
		}
	}
	state := 0
	for _, t := range tunnels {
		if t.Lifecycle && t.Running != l.up {
			state++
		}
	}
	return proxyrt.Observation{Known: true, Exists: drift == 0 && state == 0,
		Attrs: map[string]string{
			"drift": strconv.Itoa(drift),
			"state": strconv.Itoa(state),
			"total": strconv.Itoa(len(tunnels)),
		}}, nil
}

func (l *LinkedEndpoint) Plan(obs proxyrt.Observation) []proxyrt.Step {
	var steps []proxyrt.Step
	if obs.Attrs["drift"] != "0" {
		steps = append(steps, proxyrt.Step{Resource: l.id, Op: "sync",
			Args: map[string]string{"listen": l.listen}, Reason: "endpoint связанных туннелей отстал от listen"})
	}
	// Порядок шагов значим: endpoint правится ДО подъёма, иначе туннель
	// поднимется на старый порт.
	if obs.Attrs["state"] != "0" {
		op, reason := "stop", "связанные туннели подняты при выключенном клиенте"
		if l.up {
			op, reason = "start", "связанные туннели не подняты при включённом клиенте"
		}
		steps = append(steps, proxyrt.Step{Resource: l.id, Op: op, Reason: reason})
	}
	return steps
}

func (l *LinkedEndpoint) Apply(ctx context.Context, s proxyrt.Step) error {
	switch s.Op {
	case "sync":
		_, err := l.sync.Sync(ctx, l.clientID, l.listen)
		return err
	case "start", "stop":
		// Желаемое берётся из шага, а не из поля ресурса: план — данные, и
		// применяться обязан именно тот шаг, который запланирован.
		_, err := l.sync.SetState(ctx, l.clientID, s.Op == "start")
		return err
	}
	return fmt.Errorf("неизвестный шаг %q", s.Op)
}

func (l *LinkedEndpoint) RecheckAfter() time.Duration { return 0 }
